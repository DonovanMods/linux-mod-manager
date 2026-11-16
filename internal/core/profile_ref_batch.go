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
//
// A read-only query between two saves - a web UI GET, which takes no
// beginOp - can see the profile behind the rows the loop has installed,
// by up to one batch, until the next save; the rows and the document are
// two stores read separately either way, and the per-item writes left the
// same gap for the length of one item.
//
// The loop's Installer reads the profile's overrides once, too
// (readOverridesOnce), rather than parsing the document for every mod it
// deploys.
package core

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// refBatchItems is how many ref upserts a batch holds before it saves:
// the most a process that dies mid-loop can leave the profile behind its
// DB rows by. At 25 a 600-item collection makes 25 saves instead of 600
// (24 full batches and the close).
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
	// keys are the mods whose pending upserts doc holds (domain.ModKey),
	// each once, in order: what close names when it cannot save them.
	keys []string
	// saveDoc, when set, replaces pm.save (a test seam for a save that
	// fails once).
	saveDoc func(*domain.Profile) error

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
	if key := domain.ModKey(mod.SourceID, mod.ModID); !slices.Contains(b.keys, key) {
		b.keys = append(b.keys, key)
	}
	if appended || b.pending >= b.items || b.now().Sub(b.since) >= b.window {
		return b.flush()
	}
	return nil
}

// close is the flow's last flush, once its loop has ended: it saves what
// is pending - trying twice, since one failure here can leave a whole
// batch unrecorded where a failed per-item save lost one ref - and then
// lets the document go, saved or not, so that a later flush (the flow's
// deferred one, there for a panicking commit) writes nothing over what the
// flow saves to the profile after its loop (a finish's to-dos). A failure
// is returned for the flow to report, as a failed per-item save was, and
// says which installed mods the profile does not record.
func (b *profileRefBatch) close() error {
	if b == nil {
		return nil
	}
	err := b.flush()
	if err != nil {
		if err = b.flush(); err != nil {
			err = fmt.Errorf("%w; %s", err, unrecordedRefs(b.keys))
		}
	}
	b.doc, b.pending, b.keys = nil, 0, nil
	return err
}

// unrecordedRefs is close's account of the mods keys names: how many, and
// the first few.
func unrecordedRefs(keys []string) string {
	const named = 5
	list := strings.Join(keys[:min(len(keys), named)], ", ")
	if len(keys) > named {
		list += ", ..."
	}
	if len(keys) == 1 {
		return fmt.Sprintf("1 installed mod is not recorded in it (%s)", list)
	}
	return fmt.Sprintf("%d installed mods are not recorded in it (%s)", len(keys), list)
}

// flush saves the document when it holds an upsert not saved yet. It takes
// no ctx: it completes writes whose DB halves are already made (Ruling 16),
// so it runs whatever the flow's ctx says. On failure the upserts stay
// pending, for the next flush to carry.
func (b *profileRefBatch) flush() error {
	if b == nil || b.pending == 0 {
		return nil
	}
	save := b.pm.save
	if b.saveDoc != nil {
		save = b.saveDoc
	}
	if err := save(b.doc); err != nil {
		return err
	}
	b.pending, b.keys = 0, nil
	return nil
}

// readOverridesOnce makes i read each profile's overrides once (#661), for
// an install loop that deploys many mods under one profile: the per-mod
// read parsed the whole profile document for every mod it placed, the
// other quadratic half of a large import. The overrides cannot change
// while the loop runs - it holds beginOp, and its ref upserts touch no
// override - so the first answer is every later one. A read that failed
// (the profile could not be loaded) is not kept: it placed no override,
// as before, and the next deploy reads again, as the per-mod read did. A
// read that found no overrides is kept.
func (i *Installer) readOverridesOnce() {
	read := i.overridden
	if read == nil {
		return
	}
	type key struct{ game, profile string }
	var mu sync.Mutex
	memo := map[key]overriddenFiles{}
	i.overridden = func(game *domain.Game, profileName string) (overriddenFiles, error) {
		k := key{game.ID, profileName}
		mu.Lock()
		defer mu.Unlock()
		if files, ok := memo[k]; ok {
			return files, nil
		}
		files, err := read(game, profileName)
		if err == nil {
			memo[k] = files
		}
		return files, err
	}
}
