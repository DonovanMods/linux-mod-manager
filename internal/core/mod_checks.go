// Package core: this file is the per-mod check record (#672) - what lmm
// last learned about each installed mod from its source, so an update check
// or a verify asks for a mod's file list only when the mod has changed.
//
// A source that can say which mods changed since a given time
// (source.ChangeTracker - NexusMods, from one updated-mods listing per
// game) is asked about the mods lmm has a record for; each it vouches for
// is answered from the record, and the rest are checked in full and their
// answers recorded. A source without a change signal is checked in full,
// as it always was.
//
// The record is a cache (db.ModCheck, migrateV29). It is used only for the
// installed version and file IDs it was made about, a source that cannot
// answer costs only the full check, and an explicit refresh (`lmm update
// --refresh`, `lmm verify --refresh`, the web UI's Check for updates and
// Re-verify) never reads it.
package core

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// modCheckStore is the record's storage: *db.DB.
type modCheckStore interface {
	ModChecks(ctx context.Context, kind db.ModCheckKind, gameID, profileName string) (map[string]db.ModCheck, error)
	SaveModCheck(ctx context.Context, kind db.ModCheckKind, gameID, profileName string, rec db.ModCheck) error
	TouchModChecks(ctx context.Context, kind db.ModCheckKind, gameID, profileName, sourceID string, at map[string]time.Time) error
	DeleteModCheck(ctx context.Context, kind db.ModCheckKind, gameID, profileName, sourceID, modID string) error
}

// modChecks reads and writes the record for one game. A nil *modChecks
// records and answers nothing.
type modChecks struct {
	store  modCheckStore
	gameID string
}

// recordedUpdate is the part of an update the record keeps: the rest is
// the installed mod, which is read fresh each time.
type recordedUpdate struct {
	NewVersion         string            `json:"new_version"`
	Changelog          string            `json:"changelog,omitempty"`
	FileIDReplacements map[string]string `json:"file_id_replacements,omitempty"`
}

// recordAbout reports whether rec was made about mod as it is installed now:
// the same version and the same file IDs.
func recordAbout(rec db.ModCheck, mod domain.InstalledMod) bool {
	return rec.Version == mod.Version && slices.Equal(rec.FileIDs, mod.FileIDs)
}

// unchanged asks tracker which of mods - one source's, with their source
// game IDs - have not changed since their kind's record, and moves each
// such record to the time the source vouched for it. It returns those mods'
// records by mod ID; a mod with no usable record, or one the source does
// not vouch for, is absent.
func (c *modChecks) unchanged(ctx context.Context, kind db.ModCheckKind, tracker source.ChangeTracker, sourceID string, mods []domain.InstalledMod) map[string]db.ModCheck {
	if c == nil || len(mods) == 0 {
		return nil
	}
	out := make(map[string]db.ModCheck)
	// One batch is one profile's, in practice; grouped anyway, since a
	// record is a profile's.
	byProfile := make(map[string][]domain.InstalledMod)
	for _, mod := range mods {
		byProfile[mod.ProfileName] = append(byProfile[mod.ProfileName], mod)
	}
	for profile, group := range byProfile {
		recs, err := c.store.ModChecks(ctx, kind, c.gameID, profile)
		if err != nil {
			continue
		}
		since := make(map[string]time.Time)
		usable := make(map[string]db.ModCheck)
		for _, mod := range group {
			rec, ok := recs[domain.ModKey(sourceID, mod.ID)]
			if !ok || !recordAbout(rec, mod) {
				continue
			}
			since[mod.ID], usable[mod.ID] = rec.CheckedAt, rec
		}
		if len(since) == 0 {
			continue
		}
		vouched, err := tracker.UnchangedSince(ctx, group[0].GameID, since)
		if err != nil {
			continue
		}
		touch := make(map[string]time.Time)
		for id, at := range vouched {
			rec, ok := usable[id]
			if !ok {
				continue
			}
			out[id] = rec
			if at.After(rec.CheckedAt) {
				touch[id] = at
			}
		}
		_ = c.store.TouchModChecks(ctx, kind, c.gameID, profile, sourceID, touch) // a cache: a failed write costs a fetch later
	}
	return out
}

// answerUpdates splits one source's update-check batch into the mods its
// change signal answers from the record - returning the updates recorded
// for them - and the rest, which the source checks in full. It answers
// nothing for a source without a change signal.
func (c *modChecks) answerUpdates(ctx context.Context, src source.ModSource, sourceID string, mods []domain.InstalledMod) (answered []domain.Update, unchanged, rest []domain.InstalledMod) {
	tracker, ok := src.(source.ChangeTracker)
	if c == nil || !ok {
		return nil, nil, mods
	}
	recs := c.unchanged(ctx, db.ModCheckUpdate, tracker, sourceID, mods)
	for _, mod := range mods {
		rec, ok := recs[mod.ID]
		if !ok {
			rest = append(rest, mod)
			continue
		}
		unchanged = append(unchanged, mod)
		if rec.Result == "" {
			continue // up to date
		}
		var upd recordedUpdate
		if json.Unmarshal([]byte(rec.Result), &upd) != nil {
			// A record lmm cannot read is one it does not have.
			unchanged = unchanged[:len(unchanged)-1]
			rest = append(rest, mod)
			continue
		}
		answered = append(answered, domain.Update{
			InstalledMod: mod, NewVersion: upd.NewVersion, Changelog: upd.Changelog, FileIDReplacements: upd.FileIDReplacements,
		})
	}
	return answered, unchanged, rest
}

// checkedMods collects the mods a source reports it checked in full
// (source.ReportModChecked).
type checkedMods struct {
	mu  sync.Mutex
	ids map[string]bool
}

// watch returns ctx reporting into c.
func (c *checkedMods) watch(ctx context.Context) context.Context {
	return source.WithModCheckedObserver(ctx, func(id string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.ids == nil {
			c.ids = make(map[string]bool)
		}
		c.ids[id] = true
	})
}

func (c *checkedMods) has(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ids[id]
}

// recordUpdates records the answer for each of mods its source reported
// checking in full: the update found for it in updates, or up to date. at
// is when the check began, so a change during it is after the record.
func (c *modChecks) recordUpdates(ctx context.Context, sourceID string, mods []domain.InstalledMod, checked *checkedMods, updates []domain.Update, at time.Time) {
	if c == nil {
		return
	}
	found := make(map[string]domain.Update)
	for _, u := range updates {
		found[u.InstalledMod.ID] = u
	}
	for _, mod := range mods {
		if !checked.has(mod.ID) {
			continue
		}
		result := ""
		if u, ok := found[mod.ID]; ok {
			data, err := json.Marshal(recordedUpdate{NewVersion: u.NewVersion, Changelog: u.Changelog, FileIDReplacements: u.FileIDReplacements})
			if err != nil {
				continue
			}
			result = string(data)
		}
		_ = c.store.SaveModCheck(ctx, db.ModCheckUpdate, c.gameID, mod.ProfileName, db.ModCheck{
			SourceID: sourceID, ModID: mod.ID, Version: mod.Version, FileIDs: mod.FileIDs, CheckedAt: at, Result: result,
		})
	}
}

// modChecksFor is the record for game, or nil - recording nothing - when
// there is no game or no database to keep it in.
func (s *Service) modChecksFor(game *domain.Game) *modChecks {
	if game == nil || s.db == nil {
		return nil
	}
	return &modChecks{store: s.db, gameID: game.ID}
}

// unchangedVersions is verify's half (#672): the installed mods whose
// recorded version verify already confirmed against their source, and that
// their source vouches have not changed since, keyed by domain.ModKey. The
// version pass skips them. Each source is asked once.
func (s *Service) unchangedVersions(ctx context.Context, game *domain.Game, mods []domain.InstalledMod) map[string]bool {
	checks := s.modChecksFor(game)
	if checks == nil {
		return nil
	}
	bySource := make(map[string][]domain.InstalledMod)
	for _, mod := range mods {
		mapped := mod
		mapped.Mod = *SourceMappedMod(game, &mod.Mod)
		bySource[mod.SourceID] = append(bySource[mod.SourceID], mapped)
	}
	out := make(map[string]bool)
	for sourceID, group := range bySource {
		src, err := s.sourceByID(sourceID)
		if err != nil {
			continue
		}
		tracker, ok := src.(source.ChangeTracker)
		if !ok {
			continue
		}
		for id := range checks.unchanged(source.WithGame(ctx, game), db.ModCheckVersion, tracker, sourceID, group) {
			out[domain.ModKey(sourceID, id)] = true
		}
	}
	return out
}

// recordVersion records that verify confirmed mod's recorded version
// against its source at at - or, with confirmed false, forgets that it
// ever did.
func (s *Service) recordVersion(ctx context.Context, game *domain.Game, mod domain.InstalledMod, confirmed bool, at time.Time) {
	checks := s.modChecksFor(game)
	if checks == nil {
		return
	}
	if !confirmed {
		_ = checks.store.DeleteModCheck(ctx, db.ModCheckVersion, game.ID, mod.ProfileName, mod.SourceID, mod.ID)
		return
	}
	_ = checks.store.SaveModCheck(ctx, db.ModCheckVersion, game.ID, mod.ProfileName, db.ModCheck{
		SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, FileIDs: mod.FileIDs, CheckedAt: at,
	})
}
