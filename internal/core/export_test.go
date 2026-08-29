package core

import (
	"context"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
)

// Test-only accessors for package core_test. This file is compiled only into
// the test binary, so none of these are part of the production API.

// EnabledMergeSourcesForTest exposes enabledMergeSources.
func (s *Service) EnabledMergeSourcesForTest(ctx context.Context, game *domain.Game, profileName string) ([]source.MergeSource, error) {
	return s.enabledMergeSources(ctx, game, profileName)
}

// ReconcilePakManifestsForTest exposes reconcilePakManifests.
func (s *Service) ReconcilePakManifestsForTest(ctx context.Context, game *domain.Game, profileName string, installer *Installer, failedByRef map[string]string) ([]string, error) {
	return s.reconcilePakManifests(ctx, game, profileName, installer, failedByRef)
}

// ClassifyCompileDeployModsForTest exposes classifyCompileDeployMods.
func (s *Service) ClassifyCompileDeployModsForTest(ctx context.Context, game *domain.Game, profileName string, mods []*domain.InstalledMod) map[string]DeployModClass {
	return s.classifyCompileDeployMods(ctx, game, profileName, mods)
}

// SetBeforeSaveInstalledForTest arms the install flow's pre-SaveInstalledMod
// hook so a test can make that DB write fail (e.g. by cancelling the ctx)
// after the deploy has already succeeded.
func (s *Service) SetBeforeSaveInstalledForTest(fn func()) {
	s.beforeSaveInstalled = fn
}

// SetDownloadClientForTest replaces the Service's download HTTP client. A
// cancellation test uses it to install a transport that IGNORES ctx, so the
// only thing that can stop a per-file download loop is the loop's own guard
// - with the stock ctx-aware transport, a cancelled ctx aborts the next
// request by itself and the test cannot tell the two apart (final-review
// Important 2).
func (s *Service) SetDownloadClientForTest(c *http.Client) {
	s.downloader = NewDownloader(c)
}

// SetModEnabledForTest exposes the enabled-flag write behind the same
// beginOp gate the production flows take. The exported SetModEnabled it
// replaced was ratcheted away with doProfileApply's lift (#290) - core's own
// tests are its only remaining callers, and they are setting up state, not
// exercising a frontend API.
func (s *Service) SetModEnabledForTest(ctx context.Context, sourceID, modID, gameID, profileName string, enabled bool) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.setModEnabled(ctx, sourceID, modID, gameID, profileName, enabled)
}

// GetInstallerForProfileForTest exposes getInstallerForProfile, which the
// same lift unexported: an Installer is a core primitive no frontend
// touches, but core's own tests deploy through one to build fixtures.
func (s *Service) GetInstallerForProfileForTest(ctx context.Context, game *domain.Game, profileName string) (*Installer, error) {
	return s.getInstallerForProfile(ctx, game, profileName)
}

// ScanModPathForTest exposes scanModPath, which the scan-import lift (#291)
// unexported: ScanLocal is production's only caller, but the scan's own
// classification rules (copy vs extract mode, symlink detection, tilde
// expansion) are pinned directly by importer_test.go.
func (i *Importer) ScanModPathForTest(ctx context.Context, game *domain.Game, installedMods []domain.InstalledMod, opts ScanOptions) ([]ScanResult, error) {
	return i.scanModPath(ctx, game, installedMods, opts)
}

// FindDuplicateModForTest exposes findDuplicateMod, unexported by the same
// lift; its name-normalization table is pinned by importer_test.go.
func (i *Importer) FindDuplicateModForTest(modName string, installedMods []domain.InstalledMod) *domain.InstalledMod {
	return i.findDuplicateMod(modName, installedMods)
}

// NewImporterForTest exposes newImporter, which the archive-import lift
// (#291) unexported: the scan/adopt/import flows are production's only
// callers, but core's own tests drive a real Import round-trip through the
// service-backed importer (the standalone core.NewImporter cannot resolve a
// DeployCompile game's merge compiler).
func (s *Service) NewImporterForTest(game *domain.Game) *Importer {
	return s.newImporter(game)
}

// MarkImportedFileCompleteForTest exposes markImportedFileComplete behind the
// same beginOp gate the exported wrapper the lift removed used to take.
func (s *Service) MarkImportedFileCompleteForTest(ctx context.Context, game *domain.Game, mod *domain.Mod, fileID string) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.markImportedFileComplete(ctx, game, mod, fileID)
}

// NewInstallerWithLinkerForTest exposes newInstallerWithLinker + getLinker,
// both unexported by the same lift, as the one call core's tests actually
// make: an Installer that deploys with an explicitly chosen link method.
func (s *Service) NewInstallerWithLinkerForTest(game *domain.Game, method domain.LinkMethod) *Installer {
	return s.newInstallerWithLinker(game, s.getLinker(method))
}

// FreshSwitchPlanForTest stamps plan.From's current installed-mod snapshot
// (Ruling 5, phase-2-close review Important #1) onto a hand-built SwitchPlan
// and returns it, for tests that construct one directly - bypassing
// PlanProfileSwitch to isolate ApplyProfileSwitch's own execution logic -
// so ApplyProfileSwitch's checkPlanFresh call doesn't refuse it as stale.
// Call AFTER seeding whatever installed-mod state the test wants
// ApplyProfileSwitch to see.
func (s *Service) FreshSwitchPlanForTest(ctx context.Context, plan *SwitchPlan) *SwitchPlan {
	plan.snapshot, _ = s.currentInstalledSnapshot(ctx, plan.GameID, plan.From)
	return plan
}

// UpdateModVersionForTest exposes updateModVersion behind the same beginOp
// gate the exported wrapper (phase-2-close review Important #3) removed:
// zero production callers, and core's own rollback-fixture tests are its
// only remaining callers.
func (s *Service) UpdateModVersionForTest(ctx context.Context, sourceID, modID, gameID, profileName, newVersion string) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.updateModVersion(ctx, sourceID, modID, gameID, profileName, newVersion)
}

// ApplyModUpdateForTest is UpdateModVersionForTest's ApplyModUpdate twin.
func (s *Service) ApplyModUpdateForTest(ctx context.Context, sourceID, modID, gameID, profileName, newVersion string, fileIDs []string) error {
	release, err := s.beginOp(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.applyModUpdate(ctx, sourceID, modID, gameID, profileName, newVersion, fileIDs)
}

// ConvergeDeployedFilesForTest exposes convergeDeployedFiles behind the same
// beginOp gate the exported wrapper the deploy lift (#293) removed used to
// take: verify's --fix pass is production's only caller, but convergence's
// own row/sweep rules are pinned directly by converge_test.go.
func (s *Service) ConvergeDeployedFilesForTest(ctx context.Context, game *domain.Game, profileName string, dryRun bool) (*ConvergeResult, error) {
	if !dryRun {
		release, err := s.beginOp(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	return s.convergeDeployedFiles(ctx, game, profileName, dryRun)
}
