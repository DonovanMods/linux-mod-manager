package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// `lmm install --id <id> -s <source> --from-file <archive>` (#535): install a
// mod from an archive downloaded by hand, for a source that will not serve
// the file through its API - the install twin of `lmm update --from-file`.
// It is core's archive import with the install's identity
// (ImportArchiveOptions.InstallFromFile): core works out which file the
// archive is against the file the install would download, and installs it.

var (
	// installFromFile is --from-file: the archive to install from.
	installFromFile string
	// installAcceptMismatch is --accept-mismatch: install from an archive
	// that is not the file the install would download.
	installAcceptMismatch bool
)

func init() {
	installCmd.Flags().StringVar(&installFromFile, "from-file", "", "install the mod named by --id from an archive you downloaded (for a source that refuses API downloads); --file/--version name the file it should be")
	installCmd.Flags().BoolVar(&installAcceptMismatch, "accept-mismatch", false, "with --from-file: install even when the archive is not the file the install would download")
}

// checkInstallFromFileFlags refuses flag combinations --from-file cannot
// honour, before anything is read.
func checkInstallFromFileFlags(args []string) error {
	if installFromFile == "" {
		if installAcceptMismatch {
			return errors.New("--accept-mismatch only applies with --from-file")
		}
		return nil
	}
	switch {
	case len(args) > 0:
		return errors.New("--from-file installs the mod named by --id; it takes no search query")
	case installModID == "":
		return errors.New("--from-file needs the mod's --id")
	case len(installFileIDList()) > 1:
		return errors.New("--from-file installs one file: give a single --file ID")
	}
	return nil
}

// doInstallFromFile renders `lmm install --from-file`: the plan's readout,
// how the archive matched, the dependencies it leaves to install, the
// conflict question, then the apply.
func doInstallFromFile(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	if err := checkInstallFromFileFlags(args); err != nil {
		return err
	}
	profileName, err := resolveProfile(ctx, service, game.ID, installProfile)
	if err != nil {
		return err
	}
	if err := service.CheckDeployTarget(ctx, game.ID, profileName, core.VerbInstall); err != nil {
		return err
	}
	installSource, err = resolveSource(service, game, installSource, installYes)
	if err != nil {
		return err
	}
	if _, err := os.Stat(installFromFile); err != nil {
		return fmt.Errorf("archive not found: %s", installFromFile)
	}

	var expectedFileID string
	if ids := installFileIDList(); len(ids) == 1 {
		expectedFileID = ids[0]
	}
	opts := core.ImportArchiveOptions{
		SourceID:        installSource,
		ModID:           installModID,
		Force:           installForce,
		SkipHooks:       noHooks,
		InstallFromFile: true,
		ExpectedFileID:  expectedFileID,
		Version:         installVersion,
		ShowArchived:    installShowArchived,
		AcceptMismatch:  installAcceptMismatch,
	}

	if !jsonOutput {
		fmt.Printf("Installing from file: %s\n", installFromFile)
	}
	sink := quietSink(importArchiveProgress)
	if sink != nil && core.ImportEnrichmentRuns(game, opts) {
		sink(core.StepEvent{
			Scope: core.Scope{Op: core.OpImport}, Phase: core.ImportArchiveFetching,
			Detail: "Fetching metadata from " + opts.SourceID + "...",
		})
	}

	plan, err := service.PlanImportArchive(ctx, game, profileName, installFromFile, opts)
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return authPromptError(installSource)
		}
		return err
	}
	core.EmitImportArchiveReadout(plan, sink)
	if !jsonOutput {
		renderInstallArchiveMatch(plan)
	}

	accept, err := answerImportConflicts(ctx, service, game, profileName, plan, installForce)
	if err != nil {
		return err
	}
	opts.AcceptConflicts = accept

	result, err := service.ApplyImportArchive(ctx, game, profileName, plan, opts, sink)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(result)
	}
	for _, w := range result.HookWarnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
	}
	fmt.Printf("\n%s Installed: %s %s\n", colorGreen("✓"), result.Mod.Name, result.Mod.Version)
	if game.DeployMode == domain.DeployCompile && result.Deployed == 0 {
		fmt.Println("  Installed (merged pak updated)")
	} else {
		fmt.Printf("  Files deployed: %d\n", result.Deployed)
	}
	fmt.Printf("  Added to profile: %s\n", profileName)
	return nil
}

// renderInstallArchiveMatch says which source file the archive was
// recognised as (or, on stderr, that it is not the file the install would
// download), and prints the command that installs each dependency an
// install from a file leaves out.
func renderInstallArchiveMatch(plan *core.ImportArchivePlan) {
	if f := plan.MatchedFile; f != nil && plan.Match != core.ArchiveMatchMismatch {
		what := "Matches"
		if plan.Match == core.ArchiveMatchAdvertised {
			what = "Matches the file lmm would install"
		}
		if plan.MatchNormalized {
			fmt.Printf("  %s: matched %s (file %s)\n", what, f.FileName, f.ID)
		} else {
			fmt.Printf("  %s: %s (file %s)\n", what, f.FileName, f.ID)
		}
	}
	if plan.Match == core.ArchiveMatchMismatch {
		mismatch := &core.ArchiveMismatchError{ArchiveName: filepath.Base(plan.Archive), Advertised: *plan.Expected, Matched: plan.MatchedFile, Install: true}
		fmt.Fprintf(os.Stderr, "Warning: %s\n", mismatch.Sentence())
	}
	if len(plan.UnmetDependencies) == 0 {
		return
	}
	fmt.Println("\nInstall its dependencies with:")
	for _, d := range plan.UnmetDependencies {
		line := "  " + installCommandFor(d.ModID, d.SourceID)
		if d.Name != "" {
			line += "   # " + d.Name
		}
		fmt.Println(line)
	}
}

// installCommandFor is `lmm install` for one mod, carrying the -g/-p this
// run was given so the command acts where this one did.
func installCommandFor(modID, sourceID string, extra ...string) string {
	parts := []string{"lmm install --id", modID, "-s", sourceID}
	if gameID != "" {
		parts = append(parts, "-g", gameID)
	}
	if installProfile != "" {
		parts = append(parts, "-p", installProfile)
	}
	return strings.Join(append(parts, extra...), " ")
}

// installFromFileRemedy is the line a failed install's manual download is
// finished by (#535): `lmm install ... --from-file`, every ID filled in.
func installFromFileRemedy(dl *core.DownloadError) string {
	var extra []string
	if dl.FileID != "" {
		extra = append(extra, "--file", dl.FileID)
	}
	extra = append(extra, "--from-file", "<downloaded-file>")
	return "Then install it from the file: " + installCommandFor(dl.ModID, dl.SourceID, extra...)
}
