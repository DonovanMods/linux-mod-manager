// Package core: this file holds the profile-reorder flow -
// ReorderProfileMods, moved verbatim out of flows.go by v2 Phase 2 Unit J
// (#290), per the phase plan's "flows.go shrinks every unit" constraint.
// The move commit changes nothing but the file the code lives in;
// ResolveReorder (the identifier-resolution policy lifted out of cmd/lmm's
// doProfileReorder) is added in its own follow-up commit.
package core

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ErrModNotInProfile and ErrAmbiguousModID are ResolveReorder's two error
// sentinels, wrapped to reproduce cmd/lmm's pre-lift doProfileReorder text
// byte-for-byte ("mod %s not in profile" and "ambiguous mod id %s (use
// source:modid): %s") so errors.Is keeps working
// for callers while the printed text stays frozen.
var (
	ErrModNotInProfile = errors.New("not in profile")
	ErrAmbiguousModID  = errors.New("ambiguous mod id")
)

// ReorderProfileMods persists mods as gameID/profileName's new load order
// (via ProfileManager.ReorderMods) and syncs the merged pak (#197: a
// load-order change is a documented regeneration trigger, since profile
// load order IS merge-application order - see enabledMergeSources). The
// single seam cmd/lmm calls, replacing its previous direct
// pm.ReorderMods(...) call.
//
// A sync failure is non-fatal and returned as part of the SAME error only
// if the reorder itself also failed; a reorder that succeeded but whose
// merged-pak sync failed still returns nil - the reorder took effect, and
// `lmm update`/`lmm verify` are the safety net for a merged pak that
// didn't catch up. Callers wanting to surface a sync warning distinctly
// can call Service.syncMergedPak's own exported test seam directly in a
// follow-up if this proves too quiet in practice; kept simple here to
// match ReorderMods' own existing bare-error signature rather than
// inventing a new result type for one warning slice.
func (s *Service) ReorderProfileMods(ctx context.Context, gameID, profileName string, mods []domain.ModReference) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.reorderProfileMods(ctx, gameID, profileName, mods)
}

func (s *Service) reorderProfileMods(ctx context.Context, gameID, profileName string, mods []domain.ModReference) error {
	pm := NewProfileManager(s.configDir, s.db)
	if err := pm.ReorderMods(gameID, profileName, mods); err != nil {
		return err
	}
	game, ok := s.game(gameID)
	if !ok {
		return nil // an unknown game has no merged pak to sync either
	}
	// recovery must not inherit the caller's cancellation (v2 Phase 1 Task 3 C1 class)
	if _, err := s.syncMergedPak(context.WithoutCancel(ctx), game, profileName); err != nil {
		s.logger().Warn("merged pak sync after reorder failed", "game_id", gameID, "profile", profileName, "err", err)
	}
	return nil
}

// ResolveReorder turns user-supplied mod identifiers ("source:modid" or a bare mod ID) into the new
// load order: mentioned mods first in the given order (deduplicated), then every unmentioned profile
// mod in its existing relative order. Errors: ErrAmbiguousModID and
// ErrModNotInProfile, both worded exactly as doProfileReorder printed them.
//
// Lifted verbatim (Task 13) from cmd/lmm's pre-extraction doProfileReorder,
// which built this same byKey/newRefs resolution inline before calling
// ReorderProfileMods. It does not itself mutate anything - the caller still
// calls ReorderProfileMods with the returned order.
func (s *Service) ResolveReorder(ctx context.Context, game *domain.Game, profileName string, ids []string) ([]domain.ModReference, error) {
	profile, err := s.NewProfileManager().Get(game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("loading profile: %w", err)
	}

	// Key by sourceID:modID so mods from different sources with the same ModID are not overwritten.
	byKey := make(map[string]domain.ModReference)
	for _, ref := range profile.Mods {
		key := ref.SourceID + ":" + ref.ModID
		byKey[key] = ref
	}

	var newRefs []domain.ModReference
	seen := make(map[string]bool)
	for _, id := range ids {
		var ref domain.ModReference
		var key string
		if strings.Contains(id, ":") {
			key = id
			var ok bool
			ref, ok = byKey[key]
			if !ok {
				return nil, fmt.Errorf("mod %s %w", id, ErrModNotInProfile)
			}
		} else {
			// Look up by ModID only; ambiguous if multiple sources have this ModID
			var matches []string
			for k, r := range byKey {
				if r.ModID == id {
					matches = append(matches, k)
				}
			}
			switch len(matches) {
			case 0:
				return nil, fmt.Errorf("mod %s %w", id, ErrModNotInProfile)
			case 1:
				key = matches[0]
				ref = byKey[key]
			default:
				// Ruling 4 (#298): matches are "sourceID:modID" keys sharing
				// the same modID, so sorting the keys sorts by source ID.
				sort.Strings(matches)
				return nil, fmt.Errorf("%w %s (use source:modid): %s", ErrAmbiguousModID, id, strings.Join(matches, ", "))
			}
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		newRefs = append(newRefs, ref)
	}
	// Append mods not mentioned in ids (unchanged relative order)
	for _, ref := range profile.Mods {
		key := ref.SourceID + ":" + ref.ModID
		if !seen[key] {
			newRefs = append(newRefs, ref)
		}
	}

	return newRefs, nil
}
