// Package core: this file is profileRefBatch, the profile-ref writes of a
// flow that installs many mods in one loop - a profile import (a collection
// import included) and `lmm profile finish` - held in one in-memory
// document and saved a batch at a time, not once per item (#661).
//
// Each item's ref used to be one upsertModRef: load the whole profile,
// change one ref, save the whole profile. Over n items that is n loads and
// n saves of an n-entry document, quadratic in the collection's size. The
// batch loads the document once, applies each item's upsertRef - the very
// rule upsertModRef applies, so the document it ends with is the one the
// per-item writes would have left - and saves it every refBatchItems items
// or refBatchWindow, and once more when the flow's loop ends.
//
// Holding the document across the loop is safe because nothing else
// writes it meanwhile: the flow runs inside beginOp, the Service's one
// mutation slot and its cross-process lock file, so neither another
// mutation in this process (a web UI request) nor another lmm process
// can edit the profile under it. What the loop itself reads from the
// profile file while a batch is pending - the hand-off's load order and
// off markers (heirCandidates), the overrides (overriddenModFiles) - is
// nothing upsertRef changes in place; an upsert that APPENDS a ref (the
// document did not list the mod) changes the order, so it is saved at
// once.
//
// Ruling 16: an item's DB row is written before its ref, and the ref is
// recorded in the held document even under a cancelled ctx (the caller
// runs upsert through completeProfileWrite, as before). The flow then
// flushes when its loop ends - cancelled, failed or done - under no ctx at
// all (profileRefBatch.close), so no installed row ends a run without its
// ref on disk. Only a process that dies mid-batch can leave the profile
// behind its rows, by at most one batch (see refBatchItems).
package core

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// refBatchItems is how many ref upserts a batch holds before it saves:
// the most a process that dies mid-loop can leave the profile behind its
// DB rows by. At 25 a 600-item collection makes 24 saves instead of 600.
const refBatchItems = 25

// refBatchWindow is how long a batch holds its first pending upsert before
// the next upsert saves it, whatever the count: a slow loop (large mods
// deployed by copy) still writes its profile every few seconds.
const refBatchWindow = 2 * time.Second

// profileRefBatch is one flow's profile document, held across its install
// loop (see the file comment). It is not safe for concurrent use: a flow's
// commits run one at a time.
type profileRefBatch struct {
	pm          *ProfileManager
	gameID      string
	profileName string

	// doc is the profile as loaded at the first upsert, with every upsert
	// since applied; nil until then, and again after a load failed.
	doc *domain.Profile
	// pending counts the upserts doc holds that are not saved yet, and
	// since is when the oldest of them was applied.
	pending int
	since   time.Time

	items  int
	window time.Duration
	now    func() time.Time
}

// newRefBatch is a batch of gameID/profileName's ref upserts, sized by the
// Service's test seams when they are set.
func (s *Service) newRefBatch(pm *ProfileManager, gameID, profileName string) *profileRefBatch {
	b := &profileRefBatch{pm: pm, gameID: gameID, profileName: profileName,
		items: refBatchItems, window: refBatchWindow, now: time.Now}
	if s.refBatchItems > 0 {
		b.items = s.refBatchItems
	}
	return b
}

// upsert records mod's ref as upsertMod would: the same refusal (a locked
// ref at another version, the document left as it was), the same change.
// It returns ctx.Err() before touching anything, as every ProfileManager
// write does (Ruling 11); a caller completing a DB write runs it through
// completeProfileWrite. A save it makes on reaching the batch's bound can
// fail; the change stays in the document and the next save carries it.
func (b *profileRefBatch) upsert(ctx context.Context, mod domain.ModReference, rec fomodRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.doc == nil {
		doc, err := config.LoadProfile(b.pm.configDir, b.gameID, b.profileName)
		if err != nil {
			return err
		}
		b.doc = doc
	}
	// The per-item write saved at once, so nothing it was handed could
	// change under it; the held document must not share the caller's
	// slice either.
	mod.FileIDs = slices.Clone(mod.FileIDs)
	appended, err := upsertRef(b.doc, b.profileName, mod, rec, false)
	if err != nil {
		return err
	}
	if b.pending == 0 {
		b.since = b.now()
	}
	b.pending++
	if appended || b.pending >= b.items || b.now().Sub(b.since) >= b.window {
		return b.flush()
	}
	return nil
}

// close is the flow's last flush, once its loop has ended: it saves what
// is pending and then lets the document go, saved or not, so that a later
// flush - the flow's deferred one, there for a panicking commit - writes
// nothing over what the flow saves to the profile after its loop (a
// finish's to-dos). A failure is returned for the flow to report, as a
// failed per-item save was.
func (b *profileRefBatch) close() error {
	if b == nil {
		return nil
	}
	err := b.flush()
	b.doc, b.pending = nil, 0
	return err
}

// flush saves the document when it holds an upsert not saved yet. It takes
// no ctx: it completes writes whose DB halves are already made (Ruling 16),
// so it runs whatever the flow's ctx says. On failure the upserts stay
// pending, for the next flush to carry.
func (b *profileRefBatch) flush() error {
	if b == nil || b.pending == 0 {
		return nil
	}
	if err := b.pm.save(b.doc); err != nil {
		return err
	}
	b.pending = 0
	return nil
}

// readOverridesOnce makes i read each profile's overrides once (#661), for
// an install loop that deploys many mods under one profile: the per-mod
// read parsed the whole profile document for every mod it placed, the
// other quadratic half of a large import. The overrides cannot change
// while the loop runs - it holds beginOp, and its ref upserts touch no
// override - so the first answer is every later one.
func (i *Installer) readOverridesOnce() {
	read := i.overridden
	if read == nil {
		return
	}
	type key struct{ game, profile string }
	var mu sync.Mutex
	memo := map[key]overriddenFiles{}
	i.overridden = func(game *domain.Game, profileName string) overriddenFiles {
		k := key{game.ID, profileName}
		mu.Lock()
		defer mu.Unlock()
		if files, ok := memo[k]; ok {
			return files
		}
		files := read(game, profileName)
		memo[k] = files
		return files
	}
}
