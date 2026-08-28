// Package core: this file holds the profile-reorder flow -
// ReorderProfileMods, moved verbatim out of flows.go by v2 Phase 2 Unit ...
// (Task 13), per the phase plan's "flows.go shrinks every unit" constraint.
// The move commit changes nothing but the file the code lives in;
// ResolveReorder (the identifier-resolution policy lifted out of cmd/lmm's
// doProfileReorder) is added in its own follow-up commit.
package core

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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
