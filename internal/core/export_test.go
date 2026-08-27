package core

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// Test-only accessors for package core_test. This file is compiled only into
// the test binary, so none of these are part of the production API.

// EnabledMergeSourcesForTest exposes enabledMergeSources.
func (s *Service) EnabledMergeSourcesForTest(game *domain.Game, profileName string) ([]source.MergeSource, error) {
	return s.enabledMergeSources(game, profileName)
}

// ReconcilePakManifestsForTest exposes reconcilePakManifests.
func (s *Service) ReconcilePakManifestsForTest(ctx context.Context, game *domain.Game, profileName string, installer *Installer, failedByRef map[string]string) ([]string, error) {
	return s.reconcilePakManifests(ctx, game, profileName, installer, failedByRef)
}
