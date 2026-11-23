// Package core: this file is the file-list store (#676) - a mod's file list
// re-used across flows while its source vouches the mod has not changed.
//
// Every flow that needs a mod's files - deploy, switch, profile apply, an
// install or update plan, a collection's finish or fill, verify and its
// repairs - asks Service.GetModFiles, so the store sits there, behind one
// choke point. For a source with a change signal (source.ChangeTracker -
// NexusMods' updated-mods listing, #672) the list it last gave is kept in
// the database (db.ModFileList, migrateV30) and answered again while the
// source vouches the mod unchanged since it was fetched; otherwise the
// list is fetched, and the stored one replaced.
//
// The rules that keep a stale list from installing the wrong file:
//
//   - A flow that knows which file IDs it needs (a row's or a profile's
//     recorded files) names them, and a stored list that lacks one is not
//     used: the list is fetched once more (modFilesWanting).
//   - A source answering domain.ErrModNotFound drops the stored list, and
//     so does a download the source refuses for a file the list offered
//     (acquireFileRaw), so the next attempt asks again.
//   - An explicit refresh never reads the store (withFreshFileLists:
//     `lmm verify --refresh`, the web UI's Re-verify).
//
// A source without a change signal keeps today's behaviour, a fetch every
// time: nothing could vouch for its stored list, and a fixed time-to-live
// would be a guess that a long-running `lmm serve` holds onto. Steam
// Workshop and Thunderstore keep their own caches (metacache, the index).
//
// The store is a cache: a missing, unreadable or unvouched list only costs
// a fetch. It is bounded: PruneModFileLists, at most every
// fileListPruneEvery, drops lists for mods no profile has, lists no source
// would vouch for any more, and the oldest past fileListLimit - 5,000 rows,
// each a mod's whole list as the source gave it, descriptions included, so
// a few megabytes at the outside.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

const (
	// fileListFormat is the stored JSON's shape: []domain.DownloadableFile
	// as this core marshals it. A core that changes it bumps this, and
	// every older row is fetched again.
	fileListFormat = 1
	// fileListFresh is how long a list just fetched (or just vouched for)
	// is answered without asking the source again: one flow asking for
	// the same mod twice - a plan, then its apply - asks once.
	fileListFresh = time.Minute
	// fileListVouchReuse is how long one change-signal answer for a game
	// is re-used: the hundreds of lists a switch asks for in a row cost
	// the source one request, not one apiece.
	fileListVouchReuse = time.Minute
	// fileListOrphanGrace is how long a list for a mod no profile has is
	// kept: long enough for an install plan's list to serve its install.
	fileListOrphanGrace = time.Hour
	// fileListStaleAfter drops a list older than any change signal
	// vouches for (#672: NexusMods' longest listing reaches 28 days).
	fileListStaleAfter = 30 * 24 * time.Hour
	// fileListLimit bounds the store's rows.
	fileListLimit = 5000
	// fileListPruneEvery is how often a store write also prunes.
	fileListPruneEvery = 10 * time.Minute
)

// fileListState is the Service's in-memory half of the store: the change
// signal's most recent answer per source and game, and when it last
// pruned. The zero value is ready.
type fileListState struct {
	mu        sync.Mutex
	vouches   map[string]*fileListVouch
	lastPrune time.Time
}

// fileListVouch is one change-signal answer: the stored lists it was asked
// about, those it vouched for (and up to when), and when it was asked.
// turn is a one-slot semaphore: whoever holds it asks the source, and the
// rest wait for that answer - or give up with their own context (#676
// review), so a closed browser tab is never held behind another caller's
// request.
type fileListVouch struct {
	turn    chan struct{}
	askedAt time.Time
	asked   map[string]bool
	vouched map[string]time.Time
}

// freshFileListsKey marks a context whose file-list reads must ask the
// source (withFreshFileLists).
type freshFileListsKey struct{}

// withFreshFileLists returns ctx asking every file-list read under it to
// fetch, never to answer from the store: an explicit refresh. What it
// fetches is still stored.
func withFreshFileLists(ctx context.Context) context.Context {
	return context.WithValue(ctx, freshFileListsKey{}, true)
}

func freshFileLists(ctx context.Context) bool {
	fresh, _ := ctx.Value(freshFileListsKey{}).(bool)
	return fresh
}

// modFilesWanting is GetModFiles for a flow that knows which file IDs it
// needs: a stored list that lacks any of want is not answered, and the
// list is fetched once more. want may be empty.
func (s *Service) modFilesWanting(ctx context.Context, game *domain.Game, sourceID string, mod *domain.Mod, want []string) ([]domain.DownloadableFile, error) {
	files, _, err := s.modFilesFrom(ctx, game, sourceID, mod, want)
	return files, err
}

// modFilesFrom is modFilesWanting, also reporting whether the answer came
// from the store - the one case a caller's failed selection is worth
// asking the source again for (reselectFresh).
func (s *Service) modFilesFrom(ctx context.Context, game *domain.Game, sourceID string, mod *domain.Mod, want []string) ([]domain.DownloadableFile, bool, error) {
	src, err := s.sourceByID(sourceID)
	if err != nil {
		return nil, false, err
	}
	tracker, tracked := src.(source.ChangeTracker)
	storable := tracked && game != nil && s.db != nil && mod != nil && mod.ID != ""
	if storable && !freshFileLists(ctx) {
		if files, ok := s.storedModFiles(ctx, tracker, game, sourceID, mod.ID, want); ok {
			return sanitizeSourceFiles(files), true, nil
		}
	}

	fetchedAt := time.Now()
	files, err := src.GetModFiles(source.WithGame(ctx, game), mod)
	if storable {
		switch {
		case errors.Is(err, domain.ErrModNotFound):
			s.forgetModFiles(ctx, game, sourceID, mod.ID)
		case err == nil:
			s.storeModFiles(ctx, game, sourceID, mod.ID, files, fetchedAt)
		}
	}
	return sanitizeSourceFiles(files), false, err // #594
}

// reselectFresh runs pick over files and, when it fails because a version
// is not there (ErrVersionNotFound, ErrStoredFilesUnavailable) and files
// came from the store, asks the source once for its own list and picks
// again (#676 review): a version uploaded inside the change signal's lag
// is not a refusal. It returns the files the pick was made from. A list
// that was already the source's own is not asked for again.
func reselectFresh[T any](ctx context.Context, s *Service, game *domain.Game, sourceID string, mod *domain.Mod, want []string, files []domain.DownloadableFile, stored bool, pick func([]domain.DownloadableFile) (T, error)) ([]domain.DownloadableFile, T, error) {
	picked, err := pick(files)
	if err == nil || !stored || !(errors.Is(err, ErrVersionNotFound) || errors.Is(err, ErrStoredFilesUnavailable)) {
		return files, picked, err
	}
	fresh, ferr := s.modFilesWanting(withFreshFileLists(ctx), game, sourceID, mod, want)
	if ferr != nil {
		return files, picked, err // the stored answer's own refusal stands
	}
	picked, err = pick(fresh)
	return fresh, picked, err
}

// storedModFiles answers from the store: the stored list, when it holds
// every wanted file ID and is fresh or vouched for by the source.
func (s *Service) storedModFiles(ctx context.Context, tracker source.ChangeTracker, game *domain.Game, sourceID, modID string, want []string) ([]domain.DownloadableFile, bool) {
	rec, ok, err := s.db.ModFileList(ctx, sourceID, game.ID, modID)
	if err != nil || !ok || rec.Format != fileListFormat {
		return nil, false
	}
	var files []domain.DownloadableFile
	if json.Unmarshal([]byte(rec.Files), &files) != nil {
		return nil, false
	}
	for _, id := range want {
		if !slices.ContainsFunc(files, func(f domain.DownloadableFile) bool { return f.ID == id }) {
			return nil, false // a stale list: fetch it once more
		}
	}
	if time.Since(rec.CheckedAt) < fileListFresh {
		return files, true
	}
	_, vouched := s.vouchedFileLists(ctx, tracker, game, sourceID, modID)[modID]
	return files, vouched
}

// vouchedFileLists is the change signal's answer for sourceID's stored
// lists of game: the mods it vouches unchanged since their lists were
// stored. One answer serves every list of the game for
// fileListVouchReuse; it is asked again sooner only for a list it was not
// asked about. Each list it vouches for is moved forward to that time.
func (s *Service) vouchedFileLists(ctx context.Context, tracker source.ChangeTracker, game *domain.Game, sourceID, modID string) map[string]time.Time {
	key := sourceID + "\x00" + game.ID
	s.fileLists.mu.Lock()
	if s.fileLists.vouches == nil {
		s.fileLists.vouches = make(map[string]*fileListVouch)
	}
	v := s.fileLists.vouches[key]
	if v == nil {
		v = &fileListVouch{turn: make(chan struct{}, 1)}
		s.fileLists.vouches[key] = v
	}
	s.fileLists.mu.Unlock()

	// Held across the source's answer: a switch's concurrent reads of one
	// game wait for the one request rather than each sending their own.
	select {
	case v.turn <- struct{}{}:
	case <-ctx.Done():
		return nil
	}
	defer func() { <-v.turn }()
	if v.asked[modID] && time.Since(v.askedAt) < fileListVouchReuse {
		return v.vouched
	}
	stored, err := s.db.ModFileLists(ctx, sourceID, game.ID)
	if err != nil {
		return nil
	}
	since := make(map[string]time.Time, len(stored))
	for id, rec := range stored {
		if rec.Format == fileListFormat {
			since[id] = rec.CheckedAt
		}
	}
	askedAt := time.Now()
	vouched, err := tracker.UnchangedSince(source.WithGame(ctx, game), SourceMappedMod(game, &domain.Mod{SourceID: sourceID, GameID: game.ID}).GameID, since)
	if err != nil {
		return nil
	}
	v.askedAt, v.vouched = askedAt, vouched
	v.asked = make(map[string]bool, len(since))
	for id := range since {
		v.asked[id] = true
	}
	_ = s.db.TouchModFileLists(ctx, sourceID, game.ID, vouched) // a cache: a failed write costs a fetch later
	return vouched
}

// storeModFiles records files as modID's list, fetched at fetchedAt, and
// prunes the store when it is due.
func (s *Service) storeModFiles(ctx context.Context, game *domain.Game, sourceID, modID string, files []domain.DownloadableFile, fetchedAt time.Time) {
	data, err := json.Marshal(files)
	if err != nil {
		return
	}
	_ = s.db.SaveModFileList(ctx, db.ModFileList{
		SourceID: sourceID, GameID: game.ID, ModID: modID, Files: string(data),
		FetchedAt: fetchedAt, CheckedAt: fetchedAt, Format: fileListFormat,
	})
	s.fileLists.mu.Lock()
	due := time.Since(s.fileLists.lastPrune) >= fileListPruneEvery
	if due {
		s.fileLists.lastPrune = time.Now()
	}
	s.fileLists.mu.Unlock()
	if due {
		now := time.Now()
		_, _ = s.db.PruneModFileLists(ctx, now.Add(-fileListOrphanGrace), now.Add(-fileListStaleAfter), fileListLimit)
	}
}

// forgetModFiles drops modID's stored list: the source no longer has the
// mod, or refused a file the list offered.
func (s *Service) forgetModFiles(ctx context.Context, game *domain.Game, sourceID, modID string) {
	if game == nil || s.db == nil {
		return
	}
	_ = s.db.ForgetModFileList(ctx, sourceID, game.ID, modID)
}

// fileGone reports whether a download failed because the file is gone: the
// source says so (source.ErrFileGone, or the mod itself is gone), or the
// host answered HTTP 404 or 410. A throttle, a refusal of the account or a
// network failure is not: it says nothing about the list (#676 review).
func fileGone(err error) bool {
	if errors.Is(err, source.ErrFileGone) || errors.Is(err, domain.ErrModNotFound) {
		return true
	}
	var status *httpStatusError
	return errors.As(err, &status) && (status.code == http.StatusNotFound || status.code == http.StatusGone)
}

// storesFileLists reports whether sourceID's lists for game can come from
// the store - the only case a gone download is worth a fresh list: any
// other source's list was already the source's own.
func (s *Service) storesFileLists(sourceID string, game *domain.Game) bool {
	if game == nil || s.db == nil {
		return false
	}
	src, err := s.sourceByID(sourceID)
	if err != nil {
		return false
	}
	_, tracked := src.(source.ChangeTracker)
	return tracked
}
