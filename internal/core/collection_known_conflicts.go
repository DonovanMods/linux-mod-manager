// Package core: this file is a collection's known conflicts (#668). A
// collection is built with intentional overwrites - its order exists so one
// of its mods replaces another's files - so every conflict between two of
// its own mods (from_collection) is marked known, origin collection, under
// the collection's order:
//
//   - an import marks them once its mods are installed, and a profile fill
//     or Finish collection marks the pairs the mods it installs bring;
//   - a collection update recomputes them against the new version, under
//     the new version's order, and keeps the user's own.
//
// The baseline (CollectionBaseline.KnownConflicts) records the pairs the
// collection last marked, so a pair the user unmarked stays unmarked: an
// update re-marks only a pair the collection did not mark before, and an
// install adds only a pair the collection never marked either way round.
// A pair the user marked (origin user) always wins over the collection's.
package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// collectionConflictPairs is every directed pair of the profile's
// installed collection mods that share a deploy path: the later of the two
// in order wins. A disabled mod counts, so disabling one and updating the
// collection does not forget its pairs; it deploys nothing meanwhile, so
// they mark nothing it shows. order is the collection's order (an update's new version);
// nil is the profile's own. The pairs come sorted by winner, then loser.
// It reads only the cache, never what is deployed, so it answers for a
// profile that is not the active one too.
func (s *Service) collectionConflictPairs(ctx context.Context, game *domain.Game, profile *domain.Profile, order []domain.ModReference) (domain.KnownConflicts, error) {
	ours := map[string]bool{}
	for _, ref := range profile.Mods {
		if ref.FromCollection {
			ours[domain.ModKey(ref.SourceID, ref.ModID)] = true
		}
	}
	if len(ours) < 2 {
		return nil, nil
	}
	installed, err := s.GetInstalledMods(ctx, game.ID, profile.Name)
	if err != nil {
		return nil, err
	}
	var mods []domain.InstalledMod
	for _, m := range installed {
		if !m.External && ours[domain.ModKey(m.SourceID, m.ID)] {
			mods = append(mods, m)
		}
	}
	if len(mods) < 2 {
		return nil, nil
	}
	providers, err := s.conflictProviders(ctx, game, mods)
	if err != nil {
		return nil, err
	}
	if order == nil {
		order = profile.Mods
	}
	index := make(map[string]int, len(mods))
	for i, m := range orderByProfile(&domain.Profile{Mods: order}, mods) {
		index[domain.ModKey(m.SourceID, m.ID)] = i
	}
	seen := map[domain.KnownConflict]bool{}
	var pairs domain.KnownConflicts
	for _, keys := range providers {
		if len(keys) < 2 {
			continue
		}
		winner := slices.MaxFunc(keys, func(a, b string) int { return cmp.Compare(index[a], index[b]) })
		for _, k := range keys {
			e := domain.KnownConflict{Winner: winner, Loser: k, Origin: domain.KnownConflictCollection}
			if k != winner && !seen[e] {
				seen[e] = true
				pairs = append(pairs, e)
			}
		}
	}
	slices.SortFunc(pairs, func(a, b domain.KnownConflict) int {
		return cmp.Or(cmp.Compare(a.Winner, b.Winner), cmp.Compare(a.Loser, b.Loser))
	})
	return pairs, nil
}

// markCollectionConflicts marks the collection's conflicts on profileName
// after a flow installed collection mods: with update, a collection
// update's recompute under order (its new version's); without, an
// install's additions under the profile's order. The caller holds the op.
// A profile with no collection mods is left as it is.
func (s *Service) markCollectionConflicts(ctx context.Context, game *domain.Game, profileName string, order []domain.ModReference, update bool) error {
	pm := s.NewProfileManager()
	profile, err := pm.Get(ctx, game.ID, profileName)
	if err != nil {
		return err
	}
	pairs, err := s.collectionConflictPairs(ctx, game, profile, order)
	if err != nil {
		return err
	}
	var changed bool
	if update {
		changed = updateCollectionKnown(profile, pairs)
	} else {
		changed = addCollectionKnown(profile, pairs)
	}
	if !changed {
		return nil
	}
	return pm.save(profile)
}

// addCollectionKnown adds the collection's pairs an install brought: each
// pair the baseline does not hold either way round is recorded there, and
// marked known unless the profile already has an entry for it (the
// user's, or the collection's). A pair the baseline holds is one the
// collection marked before, so whatever the user did to it since stands -
// a reorder that flipped it included. It reports whether it changed
// anything.
func addCollectionKnown(profile *domain.Profile, pairs domain.KnownConflicts) bool {
	// Indexed once each, as pathKnown's lookups are: an import's pairs
	// against the profile's and the baseline's lists, a map read apiece.
	var base domain.KnownConflictSet
	if profile.CollectionBaseline != nil {
		base = profile.CollectionBaseline.KnownConflicts.Set()
	}
	have := profile.KnownConflicts.Set()
	changed := false
	for _, e := range pairs {
		if base.HasPair(e.Winner, e.Loser) {
			continue
		}
		if profile.CollectionBaseline != nil {
			profile.CollectionBaseline.KnownConflicts = append(profile.CollectionBaseline.KnownConflicts, e)
			base.Add(e.Winner, e.Loser)
			changed = true
		}
		if !have.HasPair(e.Winner, e.Loser) {
			profile.KnownConflicts = append(profile.KnownConflicts, e)
			have.Add(e.Winner, e.Loser)
			changed = true
		}
	}
	return changed
}

// updateCollectionKnown is a collection update's three-way merge of the
// profile's collection-origin pairs P with the new version's pairs N
// against the baseline B: a pair of N is marked when P has it (kept) or B
// does not (the collection's new mark), so a pair the user unmarked stays
// unmarked and a pair the collection no longer makes goes. The user's own
// entries are kept as they are, and a pair they hold either way round is
// never marked by the collection. The baseline becomes N. It reports
// whether it changed anything.
func updateCollectionKnown(profile *domain.Profile, pairs domain.KnownConflicts) bool {
	var base domain.KnownConflicts
	if profile.CollectionBaseline != nil {
		base = profile.CollectionBaseline.KnownConflicts
	}
	var user, ours domain.KnownConflicts
	for _, e := range profile.KnownConflicts {
		if e.Origin == domain.KnownConflictCollection {
			ours = append(ours, e)
		} else {
			user = append(user, e)
		}
	}
	userSet, oursSet, baseSet := user.Set(), ours.Set(), base.Set()
	result := user.Clone()
	for _, e := range pairs {
		if userSet.HasPair(e.Winner, e.Loser) {
			continue
		}
		if oursSet.Has(e.Winner, e.Loser) || !baseSet.Has(e.Winner, e.Loser) {
			result = append(result, e)
		}
	}
	changed := !result.Equal(profile.KnownConflicts)
	profile.KnownConflicts = result
	if profile.CollectionBaseline != nil && !pairs.Equal(base) {
		profile.CollectionBaseline.KnownConflicts = pairs.Clone()
		changed = true
	}
	return changed
}

// noteCollectionConflicts is markCollectionConflicts for the end of a
// flow: a failure is the flow's warning, never its error - the mods are
// installed, and `lmm conflicts ack` marks what it missed - and a
// cancelled flow marks nothing.
func (s *Service) noteCollectionConflicts(ctx context.Context, game *domain.Game, profileName string, order []domain.ModReference, update bool, warnings *[]string) {
	if ctx.Err() != nil {
		return
	}
	if err := s.markCollectionConflicts(ctx, game, profileName, order, update); err != nil && ctx.Err() == nil {
		*warnings = append(*warnings, fmt.Sprintf("marking the collection's own conflicts as known: %v", err))
	}
}
