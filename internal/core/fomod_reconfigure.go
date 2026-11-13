// Package core: this file is the reconfigure flow (#615): `lmm mod fomod
// <mod>` and the web UI's "Reconfigure installer…", which change an
// installed FOMOD mod's choices.
//
// It is an install of the version and files already installed, through the
// strict install path, with one difference: the resolve step ASKS even
// when the recorded choices still fit (fomodInput.ask), with them as the
// preselection. The first Apply therefore always refuses with
// *FomodChoicesRequiredError (reason "reconfigure"), keeping the download;
// the frontend answers and re-runs it with Options.Fomod, and that run
// installs the new choices' tree as a new cache variant, redeploys the mod
// from it and records the choices on the profile - the same Replace an
// update does, so the old variant's files leave the game directory and its
// cache entry is dropped once nothing else uses it.
package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
)

// ErrNotFomodInstalled refuses a reconfigure of a mod that was not
// installed through a FOMOD installer: there are no choices to change.
var ErrNotFomodInstalled = errors.New("not installed through a FOMOD installer")

// FomodReconfigurePlan is PlanFomodReconfigure's result: the installed mod
// whose installer is asked again.
type FomodReconfigurePlan struct {
	// GameID and Profile name where the mod is installed.
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Mod is the installed row.
	Mod domain.InstalledMod `json:"mod"`
	// Choices are the choices the profile records for it: the question's
	// preselection.
	Choices domain.FomodChoices `json:"choices"`

	install *InstallPlan
}

// FomodReconfigureOptions configures ApplyFomodReconfigure.
type FomodReconfigureOptions struct {
	// Fomod answers the question, keyed by domain.ModKey, as
	// InstallOptions.Fomod does. Without an answer for the mod the Apply
	// refuses with *FomodChoicesRequiredError, nothing written.
	Fomod map[string]domain.FomodChoices
	// AcceptConflicts, Force, SkipHooks and SkipVerify are InstallOptions'.
	AcceptConflicts bool
	Force           bool
	SkipHooks       bool
	SkipVerify      bool
}

// PlanFomodReconfigure plans a reconfigure of sourceID/modID in
// profileName (see this file's doc comment). It refuses a mod that is not
// installed, not a FOMOD install (ErrNotFomodInstalled), disabled, external,
// or imported from a local archive (which has no source to fetch the
// installer from again).
func (s *Service) PlanFomodReconfigure(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) (*FomodReconfigurePlan, error) {
	// The adapter's precondition (and a moved mod path) refuse first, as
	// for every install-shaped plan.
	if _, err := s.currentInstalledSnapshot(ctx, game.ID, profileName); err != nil {
		return nil, err
	}
	row, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	if err := refuseExternal("reconfigure", row, ReasonExternalNoUpdate); err != nil {
		return nil, err
	}
	ref := s.recordedRef(ctx, game.ID, profileName, sourceID, modID)
	var choices domain.FomodChoices
	if ref != nil {
		choices = ref.Fomod
	}
	if row.Variant == "" && choices.IsZero() && !s.cachedFomodInstaller(game, row) {
		return nil, fmt.Errorf("%s: %w", row.Name, ErrNotFomodInstalled)
	}
	if row.SourceID == domain.SourceLocal {
		return nil, fmt.Errorf("%s was imported from a local archive, so lmm cannot fetch its installer again; import the archive again (lmm import <archive>) to answer its installer", row.Name)
	}
	if !row.Enabled {
		return nil, fmt.Errorf("%s is disabled; enable it (lmm mod enable) before changing its installer choices", row.Name)
	}
	install, err := s.PlanInstall(ctx, game, profileName, sourceID, modID, true)
	if err != nil {
		return nil, err
	}
	install.SkipDependencies()
	// The files and version installed, not the source's latest: a
	// reconfigure changes the choices and nothing else.
	if len(row.FileIDs) > 0 {
		pool, err := s.resolveInstallCandidatePool(ctx, game, sourceID, &install.Mod, true, "")
		if err != nil {
			return nil, err
		}
		files, err := selectInstallTargetFiles(pool, row.FileIDs)
		if err != nil {
			return nil, fmt.Errorf("%s: the installed files are no longer offered by %s: %w", row.Name, sourceID, err)
		}
		install.Files = files
	} else if domain.EffectiveInstalledVersion(install.Mod.Version, filePtrs(install.Files)) != row.Version {
		// No file ids recorded, and the source's default pick is another
		// version: ask the source for the installed version's files rather
		// than install the latest under the installed version's label.
		pool, err := s.resolveInstallCandidatePool(ctx, game, sourceID, &install.Mod, true, row.Version)
		if err != nil {
			return nil, fmt.Errorf("%s: lmm did not record which files of version %s are installed, and %s cannot select them by version (%w); reinstall it with lmm install --version %s, or update it, to run its installer", row.Name, row.Version, sourceID, err, row.Version)
		}
		files, err := selectInstallTargetFiles(pool, nil)
		if err != nil {
			return nil, fmt.Errorf("%s: version %s's files are no longer offered by %s: %w", row.Name, row.Version, sourceID, err)
		}
		install.Files = files
	}
	install.Mod.Version = row.Version
	return &FomodReconfigurePlan{GameID: game.ID, Profile: profileName, Mod: *row, Choices: choices, install: install}, nil
}

// ApplyFomodReconfigure runs a reconfigure (see this file's doc comment):
// without an answer for the mod it refuses with *FomodChoicesRequiredError
// (reason FomodReasonReconfigure) and writes nothing; with one it installs
// and deploys the answered tree and records the choices. Its result is the
// install's.
func (s *Service) ApplyFomodReconfigure(ctx context.Context, game *domain.Game, plan *FomodReconfigurePlan, opts FomodReconfigureOptions, sink EventSink) (*InstallResult, error) {
	if plan == nil || plan.install == nil {
		return &InstallResult{}, errors.New("reconfigure: no plan")
	}
	var fileIDs []string
	if len(plan.Mod.FileIDs) > 0 {
		fileIDs = plan.Mod.FileIDs
	}
	return s.ApplyInstall(ctx, game, plan.install, InstallOptions{
		TargetFileIDs:    fileIDs,
		Fomod:            opts.Fomod,
		AcceptConflicts:  opts.AcceptConflicts,
		Force:            opts.Force,
		SkipHooks:        opts.SkipHooks,
		SkipVerify:       opts.SkipVerify,
		fomodReconfigure: domain.ModKey(plan.Mod.SourceID, plan.Mod.ID),
	}, sink)
}

// cachedFomodInstaller reports that row's cache entry holds a FOMOD
// installer: a mod installed before lmm ran FOMOD installers (#614)
// deployed the archive as it was, installer included, and records no
// choices and no variant. Reconfiguring it is how its installer is run
// (#615). An installer Locate refuses (scripted, ambiguous) counts too, so
// the reconfigure reports that refusal rather than "not an installer".
func (s *Service) cachedFomodInstaller(game *domain.Game, row *domain.InstalledMod) bool {
	files, err := s.GetGameCache(game).ListFiles(modCacheKey(game.ID, row.Mod))
	if err != nil || len(files) == 0 {
		return false
	}
	inst, err := fomod.Locate("", slashMembers(files))
	return err != nil || inst != nil
}

// filePtrs is files as the pointers EffectiveInstalledVersion takes.
func filePtrs(files []domain.DownloadableFile) []*domain.DownloadableFile {
	out := make([]*domain.DownloadableFile, len(files))
	for i := range files {
		out[i] = &files[i]
	}
	return out
}
