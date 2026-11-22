// Package core: this file is known (intended) conflicts (#668): marking a
// mod pair's file conflict as intended, unmarking it, and accepting every
// present conflict as it stands - `lmm conflicts ack/unack`, and the web
// UI's "Mark as intended"/"Unmark". Each is one beginOp-gated profile
// write. The marks themselves live on the profile (domain.KnownConflicts);
// GetProfileConflicts reports a path known when its load-order winner is
// marked over every other provider (pathKnown).
//
// What a collection marks (origin collection) is computed apart, in
// collection_known_conflicts.go.
package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ConflictPair is one directed mod pair a mark or unmark names: Winner's
// files override Loser's. Each is a mod identifier as a reorder takes one
// - "source:modid", or a bare mod ID naming one mod of the profile.
type ConflictPair struct {
	Winner string `json:"winner"`
	Loser  string `json:"loser"`
}

// KnownConflictsResult is what a mark or unmark did to the profile's
// known conflicts: the entries it added and removed, and the list it left.
// Every list is [] on the wire when empty, never null.
type KnownConflictsResult struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Added are the entries the request added; Removed the entries it took
	// out - an unmarked pair, or the old direction of a pair marked anew.
	Added   domain.KnownConflicts `json:"added"`
	Removed domain.KnownConflicts `json:"removed"`
	// KnownConflicts is the profile's whole list afterwards.
	KnownConflicts domain.KnownConflicts `json:"known_conflicts"`
}

// ErrSameModPair refuses a pair naming one mod twice: a mod never
// conflicts with itself.
var ErrSameModPair = errors.New("a conflict pair names two different mods")

// ErrNoConflictPairs refuses a mark or unmark that names no pair.
var ErrNoConflictPairs = errors.New("no conflict pair given")

// MarkConflictsKnown marks each pair known (origin user): its winner's
// overriding the loser's files is intended, on every path the two share.
// A pair already marked in that direction is left as it is, whatever its
// origin; one marked the other way round is replaced, since a pair has one
// intended winner. Every pair is resolved first, so a bad one
// (ErrModNotInProfile, ErrAmbiguousModID, ErrSameModPair) writes nothing.
func (s *Service) MarkConflictsKnown(ctx context.Context, game *domain.Game, profileName string, pairs []ConflictPair) (*KnownConflictsResult, error) {
	if len(pairs) == 0 {
		return nil, ErrNoConflictPairs
	}
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.editKnownConflicts(ctx, game, profileName, pairs, markPair)
}

// MarkCurrentConflictsKnown marks every conflict GetProfileConflicts
// reports and does not already call known as it stands: each path's
// load-order winner over each other provider (origin user). A profile with
// no conflicts is left as it is.
func (s *Service) MarkCurrentConflictsKnown(ctx context.Context, game *domain.Game, profileName string) (*KnownConflictsResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	conflicts, err := s.GetProfileConflicts(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	profile, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	// A conflict's owner comes from the deployed-files record, which can
	// name a mod the profile no longer lists (removed since the last
	// deploy): such a mod is skipped - a mark naming it would be dropped on
	// the save anyway - rather than refusing the whole batch.
	listed := make(map[string]bool, len(profile.Mods))
	for _, ref := range profile.Mods {
		listed[domain.ModKey(ref.SourceID, ref.ModID)] = true
	}
	// One pair per mod pair, however many paths it shares.
	seen := domain.KnownConflictSet{}
	var pairs []ConflictPair
	for _, c := range conflicts {
		winner := c.LoadOrderWinner.Key
		if c.Known || !listed[winner] {
			continue
		}
		for _, m := range append([]ConflictModRef{c.Owner}, c.AlsoIn...) {
			if m.Key == winner || !listed[m.Key] || seen.Has(winner, m.Key) {
				continue
			}
			seen.Add(winner, m.Key)
			pairs = append(pairs, ConflictPair{Winner: winner, Loser: m.Key})
		}
	}
	return s.editKnownConflicts(ctx, game, profileName, pairs, markPair)
}

// UnmarkConflictsKnown removes each pair's entry, whichever way round it
// points and whatever its origin: the pair's conflict is reported again. A
// pair with no entry removes nothing. A collection update leaves an
// unmarked collection pair unmarked (mergeCollectionKnown).
func (s *Service) UnmarkConflictsKnown(ctx context.Context, game *domain.Game, profileName string, pairs []ConflictPair) (*KnownConflictsResult, error) {
	if len(pairs) == 0 {
		return nil, ErrNoConflictPairs
	}
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.editKnownConflicts(ctx, game, profileName, pairs, unmarkPair)
}

// editKnownConflicts loads the profile, resolves the pairs against it,
// applies edit to each, and saves the profile when anything changed. The
// caller holds the op.
func (s *Service) editKnownConflicts(ctx context.Context, game *domain.Game, profileName string, pairs []ConflictPair,
	edit func(list domain.KnownConflicts, winner, loser string, result *KnownConflictsResult) domain.KnownConflicts) (*KnownConflictsResult, error) {
	pm := s.NewProfileManager()
	profile, err := pm.Get(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	resolved, err := resolveConflictPairs(profile, pairs)
	if err != nil {
		return nil, err
	}
	result := &KnownConflictsResult{GameID: game.ID, Profile: profileName, Added: domain.KnownConflicts{}, Removed: domain.KnownConflicts{}}
	list := profile.KnownConflicts.Clone()
	for _, p := range resolved {
		list = edit(list, p.Winner, p.Loser, result)
	}
	if len(result.Added) > 0 || len(result.Removed) > 0 {
		profile.KnownConflicts = list
		if err := pm.save(profile); err != nil {
			return nil, fmt.Errorf("saving profile: %w", err)
		}
	}
	result.KnownConflicts = profile.KnownConflicts.Clone()
	if result.KnownConflicts == nil {
		result.KnownConflicts = domain.KnownConflicts{}
	}
	return result, nil
}

// resolveConflictPairs is pairs with each identifier resolved to its
// domain.ModKey among the profile's mods (first copy of each), refusing an
// unknown or ambiguous one and a pair naming one mod twice.
func resolveConflictPairs(profile *domain.Profile, pairs []ConflictPair) ([]ConflictPair, error) {
	byKey := make(map[string]domain.ModReference, len(profile.Mods))
	for _, ref := range profile.Mods {
		key := domain.ModKey(ref.SourceID, ref.ModID)
		if _, dup := byKey[key]; !dup {
			byKey[key] = ref
		}
	}
	out := make([]ConflictPair, 0, len(pairs))
	for _, p := range pairs {
		winner, err := resolveModIdentifier(byKey, p.Winner)
		if err != nil {
			return nil, err
		}
		loser, err := resolveModIdentifier(byKey, p.Loser)
		if err != nil {
			return nil, err
		}
		if winner == loser {
			return nil, fmt.Errorf("%w: %s and %s are the same mod", ErrSameModPair, p.Winner, p.Loser)
		}
		out = append(out, ConflictPair{Winner: winner, Loser: loser})
	}
	return out, nil
}

// markPair is MarkConflictsKnown's edit for one resolved pair.
func markPair(list domain.KnownConflicts, winner, loser string, result *KnownConflictsResult) domain.KnownConflicts {
	if list.Has(winner, loser) {
		return list
	}
	if i := list.Index(winner, loser); i >= 0 {
		result.Removed = append(result.Removed, list[i])
		list = append(list[:i:i], list[i+1:]...)
	}
	entry := domain.KnownConflict{Winner: winner, Loser: loser, Origin: domain.KnownConflictUser}
	result.Added = append(result.Added, entry)
	return append(list, entry)
}

// unmarkPair is UnmarkConflictsKnown's edit for one resolved pair.
func unmarkPair(list domain.KnownConflicts, winner, loser string, result *KnownConflictsResult) domain.KnownConflicts {
	i := list.Index(winner, loser)
	if i < 0 {
		return list
	}
	result.Removed = append(result.Removed, list[i])
	return append(list[:i:i], list[i+1:]...)
}
