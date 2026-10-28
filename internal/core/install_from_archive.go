// Package core: this file holds the install-from-file half of the archive
// import (#535) - `lmm install --id <id> -s <source> --from-file <archive>`,
// and the web UI's "Install from file..." beside a failed install whose
// source refused the download.
//
// It is the install twin of #530's update from a file, and it is the
// archive import (PlanImportArchive/ApplyImportArchive) with two things
// added rather than a flow of its own: the identity is the failed install's
// (source, mod ID), and the archive is expected to be the file that install
// would have downloaded. Which file and version the archive is decides
// whether future update checks - file-ID based on CurseForge (#504) - stay
// correct, so the plan matches the archive's name against that file with
// #530's matcher (classifyArchive) and adopts its version and file ID; a name
// that is not that file is a decision the user makes (ArchiveMismatchError,
// answered by ImportArchiveOptions.AcceptMismatch). Everything else - the
// ingest, hooks, deploy, the archive's checksum (#514), the profile ref - is
// the import's own.
package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ErrArchiveModInstalled is PlanImportArchive's refusal of an install from a
// file for a mod already installed in the profile: that is an update from a
// file (PlanUpdateFromArchive), which keeps the row's lock, policy and
// rollback history. Callers branch with errors.Is.
var ErrArchiveModInstalled = errors.New("the mod is already installed")

// UnmetDependency is one dependency of an install from a file that is not
// installed (ImportArchivePlan.UnmetDependencies). Name is empty for a
// dependency the source could not resolve.
type UnmetDependency struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name,omitempty"`
}

// checkInstallFromFile refuses, before anything is read from the archive or
// the source, an install from a file that cannot be one: no identity, a
// source the game does not map, an item Steam already loads (Q2, #269), and
// a mod already installed in the profile.
func (s *Service) checkInstallFromFile(ctx context.Context, game *domain.Game, profileName, archivePath string, opts ImportArchiveOptions) error {
	if !importPinsRealSource(opts) {
		return errors.New("installing from a file needs the mod's source and ID (--source and --id)")
	}
	if _, ok := game.SourceIDs[opts.SourceID]; !ok {
		return fmt.Errorf("source %s is not configured for this game", opts.SourceID)
	}
	if err := s.CheckExternalInstallExclusivity(ctx, game.ID, profileName, opts.SourceID, opts.ModID); err != nil {
		return err
	}
	existing, err := s.GetInstalledMod(ctx, opts.SourceID, opts.ModID, game.ID, profileName)
	switch {
	case err == nil:
		return fmt.Errorf("%w: %s is installed in profile %s - update it from the file with `lmm update %s --from-file %s`",
			ErrArchiveModInstalled, existing.Name, profileName, opts.ModID, filepath.Base(archivePath))
	case errors.Is(err, domain.ErrModNotFound):
		return nil
	default:
		return fmt.Errorf("checking existing installed mod: %w", err)
	}
}

// identifyInstallArchive fills an install from a file's Expected,
// MatchedFile, Match, version and file identity from the source - one
// listing of the mod's files (srcMod is the mod enrichImportedMod fetched,
// nil when that failed) - and names its unmet dependencies. A source
// failure is a warning: the plan then knows less, and says so. A version
// nothing names is *ArchiveVersionRequiredError.
func (s *Service) identifyInstallArchive(ctx context.Context, game *domain.Game, profileName string, plan *ImportArchivePlan, srcMod *domain.Mod, opts ImportArchiveOptions, warn func(string, ...any)) error {
	plan.Match = ArchiveMatchNone
	name := filepath.Base(plan.Archive)

	var matched *domain.DownloadableFile
	if srcMod != nil {
		files, err := s.GetModFiles(ctx, opts.SourceID, srcMod)
		if err != nil {
			warn("could not fetch %s's files from %s: %v", plan.Mod.Name, opts.SourceID, err)
		} else {
			if expected := expectedInstallFile(opts.SourceID, files, opts, warn); expected != nil {
				plan.Expected = fileRef(expected)
				if plan.Expected.Version == "" {
					// The version an install of this file records.
					plan.Expected.Version = domain.EffectiveInstalledVersion(srcMod.Version, []*domain.DownloadableFile{expected})
				}
			}
			c := classifyArchive(files, name, plan.Expected)
			plan.Match, plan.MatchedFile, plan.MatchNormalized, matched = c.match, c.ref, c.normalized, c.file
		}
	}

	version, fileID, err := resolveArchiveVersion(plan.MatchedFile, name, opts.Version, opts.SourceID, opts.ModID)
	if err != nil {
		return err
	}
	plan.Mod.Version = version
	if fileID != "" {
		plan.resolvedFile = matched
	}
	// A mismatch is not repeated here: Match/Expected/MatchedFile state it,
	// and each frontend words it beside its own way to proceed.
	if plan.Match != ArchiveMatchMismatch && fileID == "" {
		warn("%s", noFileIDWarning(name))
	}
	// The ingest caches straight under the resolved version, so there is no
	// enrichment rename to make - and whether that entry already exists is
	// asked of the version it will actually be written at.
	plan.ident.version = version
	plan.EntryPreExists = s.GetGameCache(game).Exists(game.ID, plan.ident.sourceID, plan.ident.modID, version)

	if srcMod != nil {
		plan.UnmetDependencies = s.unmetDependencies(ctx, game, profileName, opts.SourceID, srcMod, plan.Mod.Name, warn)
	}
	return nil
}

// expectedInstallFile is the file an install of this mod would download:
// opts.ExpectedFileID's file (the one a failed install tried) when given,
// else the install's own pick - PlanInstall's selection over the same
// candidate pool (installCandidatePool, selectInstallTargetFiles), at
// opts.Version when set. nil, with a warning, when it cannot be told.
func expectedInstallFile(sourceID string, files []domain.DownloadableFile, opts ImportArchiveOptions, warn func(string, ...any)) *domain.DownloadableFile {
	if opts.ExpectedFileID != "" {
		for i := range files {
			if files[i].ID == opts.ExpectedFileID {
				return &files[i]
			}
		}
		warn("%s no longer lists file %s, the file the install tried", sourceID, opts.ExpectedFileID)
		return nil
	}
	pool, err := installCandidatePool(sourceID, files, opts.ShowArchived, opts.Version)
	if err == nil && len(pool) == 0 {
		err = errors.New("no downloadable files available for this mod")
	}
	var selected []domain.DownloadableFile
	if err == nil {
		selected, err = selectInstallTargetFiles(pool, nil)
	}
	if err != nil || len(selected) != 1 {
		if err != nil {
			warn("could not tell which file the install would download: %v", err)
		}
		return nil
	}
	return &selected[0]
}

// unmetDependencies resolves srcMod's dependencies the way PlanInstall does
// and returns the ones not installed in profileName, with one warning naming
// them: an install from a file installs this one mod.
func (s *Service) unmetDependencies(ctx context.Context, game *domain.Game, profileName, sourceID string, srcMod *domain.Mod, modName string, warn func(string, ...any)) []UnmetDependency {
	// An unreadable installed set reads as empty, as PlanInstall's does.
	installedMods, _ := s.GetInstalledMods(ctx, game.ID, profileName)
	installedIDs := make(map[string]bool, len(installedMods))
	for _, im := range installedMods {
		installedIDs[domain.ModKey(im.SourceID, im.ID)] = true
	}
	deps, missing, _, failures := s.resolveInstallDependencies(ctx, sourceID, game.ID, srcMod, installedIDs)
	for _, f := range failures {
		warn("could not check %s's dependencies: %s", domain.ModKey(f.SourceID, f.ModID), f.Message)
	}

	var unmet []UnmetDependency
	var names []string
	for _, d := range deps {
		unmet = append(unmet, UnmetDependency{SourceID: d.SourceID, ModID: d.ID, Name: d.Name})
		names = append(names, d.Name)
	}
	for _, m := range missing {
		unmet = append(unmet, UnmetDependency{SourceID: m.SourceID, ModID: m.ModID})
		names = append(names, domain.ModKey(m.SourceID, m.ModID))
	}
	switch len(unmet) {
	case 0:
	case 1:
		warn("%s depends on a mod that is not installed, and installing from a file does not install it: %s", modName, names[0])
	default:
		warn("%s depends on %d mods that are not installed, and installing from a file does not install them: %s", modName, len(unmet), strings.Join(names, ", "))
	}
	return unmet
}
