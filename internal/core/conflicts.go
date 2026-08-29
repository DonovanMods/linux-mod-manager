package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ConflictModRef identifies one mod participating in a file conflict. Key is
// domain.ModKey ("sourceID:modID"); Name is the mod's display name, falling
// back to Key when no installed record supplies one.
type ConflictModRef struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// ProfileConflict describes one game-directory path that more than one
// enabled mod in the profile provides. Owner is the mod whose copy of the
// file is currently deployed per the deployed_files DB table; AlsoIn lists
// every other provider, in profile load order (unlisted providers first,
// mirroring orderByProfile). LoadOrderWinner is the provider that comes LAST
// in that ordering - the one whose copy a fresh deploy would leave on disk,
// since later-ordered mods deploy later and overwrite earlier ones. Stale
// flags a conflict whose DB owner disagrees with the load-order winner (the
// profile was reordered - or historically, deploy order was nondeterministic
// - since the last deploy), meaning a redeploy would change which file wins.
type ProfileConflict struct {
	Path            string           `json:"path"`
	Owner           ConflictModRef   `json:"owner"`
	AlsoIn          []ConflictModRef `json:"also_in"`
	LoadOrderWinner ConflictModRef   `json:"load_order_winner"`
	Stale           bool             `json:"stale"`
}

// ConflictReport is the document `lmm conflicts` (and serve) renders:
// GetProfileConflicts' result paired with the game/profile it describes, so
// the identity travels with the data instead of each frontend re-stamping
// its own. Conflicts is sorted by Path (the query's own order) and is empty
// - never null on the wire - both when the profile has no conflicts and when
// it has no installed mods at all; telling those two apart is a
// presentation concern the frontend already owns.
type ConflictReport struct {
	GameID    string            `json:"game_id"`
	Profile   string            `json:"profile"`
	Conflicts []ProfileConflict `json:"conflicts"`
}

// GetProfileConflicts is a pure read-only query returning every file path in
// the named profile that more than one mod provides, sorted by Path. Only
// ENABLED installed mods are considered: they are the set that participates
// in deployment, so a disabled mod's files can never contend for a game
// path. Each mod's provided files come from its cache manifest (the same
// source install-time conflict checking uses) - NOT from the deployed_files
// table, whose single-owner-per-path schema can only ever name one provider
// per file; a mod whose cache entry is missing (e.g. manually deleted)
// simply contributes no files rather than failing the query, while any
// other cache read failure (permissions, corruption) aborts with an error
// rather than silently under-reporting conflicts. Ownership per
// path still comes from deployed_files (GetFileOwner); a path with no
// recorded owner is skipped, matching the pre-extraction CLI's behavior of
// only reporting conflicts on tracked deployments.
//
// The profile's load order is read via the ProfileManager; a profile that
// fails to load is treated as empty (nil), so every provider counts as
// unlisted and ordering stays deterministic (sorted by key) rather than the
// query aborting - mirroring orderByProfile's nil handling. The query reads
// the DB (GetInstalledMods, GetFileOwner) and walks each enabled mod's cache
// directory (ListFiles); ctx is checked between per-mod cache walks so a
// caller cancelling mid-query gets ctx.Err() promptly instead of paying for
// the remaining walks.
func (s *Service) GetProfileConflicts(ctx context.Context, game *domain.Game, profileName string) ([]ProfileConflict, error) {
	mods, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	var enabled []domain.InstalledMod
	for _, m := range mods {
		if m.Enabled {
			enabled = append(enabled, m)
		}
	}
	if len(enabled) == 0 {
		return nil, nil
	}

	// Display names by key, falling back to the key itself when empty. Built
	// from ALL installed mods (not just enabled ones) so an owner that has
	// since been disabled still renders by name, matching the pre-extraction
	// CLI's name map.
	modNames := make(map[string]string, len(mods))
	for _, m := range mods {
		modNames[domain.ModKey(m.SourceID, m.ID)] = m.Name
	}
	ref := func(key string) ConflictModRef {
		name := modNames[key]
		if name == "" {
			name = key
		}
		return ConflictModRef{Key: key, Name: name}
	}

	// Collect every path each enabled mod PROVIDES (its cache manifest) and
	// which mods provide it. A mod with no cache entry contributes nothing.
	gameCache := s.GetGameCache(game)
	fileToKeys := make(map[string][]string)
	for _, m := range enabled {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		files, err := deployableFiles(gameCache, game.ID, m.SourceID, m.ID, m.Version)
		if err != nil {
			// A missing cache entry (e.g. manually deleted) just means the mod
			// provides nothing; any OTHER read failure (permissions, corruption)
			// would silently under-report conflicts if skipped, so it aborts.
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("listing cache files for %s: %w", domain.ModKey(m.SourceID, m.ID), err)
		}
		key := domain.ModKey(m.SourceID, m.ID)
		for _, f := range files {
			fileToKeys[f] = append(fileToKeys[f], key)
		}
	}

	// Load order: position in orderByProfile's ordering (unlisted providers
	// first sorted by key, then profile.Mods order; last = winner). A
	// load-failed profile degrades to nil, per the doc comment above.
	profile, err := s.NewProfileManager().Get(game.ID, profileName)
	if err != nil {
		profile = nil
	}
	orderIndex := make(map[string]int, len(enabled))
	for i, m := range orderByProfile(profile, enabled) {
		orderIndex[domain.ModKey(m.SourceID, m.ID)] = i
	}

	var conflicts []ProfileConflict
	for path, keys := range fileToKeys {
		if len(keys) <= 1 {
			continue
		}

		ownerSourceID, ownerModID, found, err := s.GetFileOwner(ctx, game.ID, profileName, path)
		if err != nil {
			// GetFileOwner errs only on storage failure; conflating that with
			// "no owner recorded" would silently under-report conflicts.
			return nil, fmt.Errorf("looking up file owner for %s: %w", path, err)
		}
		if !found {
			continue
		}
		ownerKey := domain.ModKey(ownerSourceID, ownerModID)

		sort.Slice(keys, func(i, j int) bool { return orderIndex[keys[i]] < orderIndex[keys[j]] })
		winnerKey := keys[len(keys)-1]

		var alsoIn []ConflictModRef
		for _, k := range keys {
			if k != ownerKey {
				alsoIn = append(alsoIn, ref(k))
			}
		}
		if len(alsoIn) == 0 {
			continue
		}

		conflicts = append(conflicts, ProfileConflict{
			Path:            path,
			Owner:           ref(ownerKey),
			AlsoIn:          alsoIn,
			LoadOrderWinner: ref(winnerKey),
			Stale:           ownerKey != winnerKey,
		})
	}

	sort.Slice(conflicts, func(i, j int) bool { return conflicts[i].Path < conflicts[j].Path })
	return conflicts, nil
}
