package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// `lmm update <mod-id> --from-file <archive>` (#530): update an installed
// mod from an archive downloaded by hand, for a source that will not serve
// the file through its API. Identity comes from the installed mod; core
// works out which file the archive is (PlanUpdateFromArchive) and applies it
// as an update (ApplyUpdateFromArchive).

var (
	// updateFromFile is --from-file: the archive to update from.
	updateFromFile string
	// updateVersion is --version: the version to record for it.
	updateVersion string
	// updateAcceptMismatch is --accept-mismatch: update from an archive
	// that is not the file the update check advertised.
	updateAcceptMismatch bool
)

func init() {
	updateCmd.Flags().StringVar(&updateFromFile, "from-file", "", "update the mod from an archive you downloaded (for a source that refuses API downloads)")
	updateCmd.Flags().StringVar(&updateVersion, "version", "", "with --from-file: the version to record (default: the matched source file's, else the archive name's)")
	updateCmd.Flags().BoolVar(&updateAcceptMismatch, "accept-mismatch", false, "with --from-file: update even when the archive is not the file the update check advertised")
}

// checkUpdateFromFileFlags refuses flag combinations --from-file cannot
// honour, before anything is read.
func checkUpdateFromFileFlags(args []string) error {
	if updateFromFile == "" {
		if updateVersion != "" || updateAcceptMismatch {
			return errors.New("--version and --accept-mismatch only apply with --from-file")
		}
		return nil
	}
	if len(args) != 1 {
		return errors.New("--from-file updates one mod: give its mod ID")
	}
	if updateAll {
		return errors.New("--from-file cannot be combined with --all")
	}
	return nil
}

// applyUpdateFromFile renders `lmm update <mod-id> --from-file`: the plan
// (the plan document itself under --dry-run --json), then the apply.
func applyUpdateFromFile(ctx context.Context, service *core.Service, game *domain.Game, mod *domain.InstalledMod, profileName string) error {
	opts := core.UpdateFromArchiveOptions{
		Version:        updateVersion,
		AcceptMismatch: updateAcceptMismatch,
		Force:          updateForce,
		SkipHooks:      noHooks,
	}
	plan, err := service.PlanUpdateFromArchive(ctx, game, profileName, mod.SourceID, mod.ID, updateFromFile, opts)
	if err != nil {
		return err
	}
	if jsonOutput && updateDryRun {
		return emitJSON(plan)
	}

	if !jsonOutput {
		fmt.Printf("Updating %s %s → %s from %s\n", plan.Mod.Name, plan.FromVersion, plan.ToVersion, plan.ArchiveName)
		if line := archiveMatchLine(plan); line != "" {
			fmt.Printf("  %s\n", line)
		}
		for _, w := range plan.Warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", w)
		}
	}
	if plan.Locked {
		if jsonOutput {
			return emitJSON(fromFileSkipped(plan))
		}
		fmt.Println(plan.Refusal)
		return nil
	}
	if updateDryRun {
		fmt.Println("(dry-run: no changes applied)")
		return nil
	}

	result, err := service.ApplyUpdateFromArchive(ctx, game, plan, opts, quietSink(updateProgress))
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(result)
	}
	fmt.Printf("\n%s Updated: %s %s → %s\n", colorGreen("✓"), result.Name, result.FromVersion, result.ToVersion)
	fmt.Println("  Previous version preserved for rollback")
	return nil
}

// archiveMatchLine says which source file the archive was recognised as,
// or nothing when it is none (the plan's warnings then say what that means).
func archiveMatchLine(plan *core.UpdateFromArchivePlan) string {
	f := plan.MatchedFile
	if f == nil || plan.Match == core.ArchiveMatchMismatch {
		return ""
	}
	what := "Matches"
	if plan.Match == core.ArchiveMatchAdvertised {
		what = "Matches the advertised update"
	}
	if plan.MatchNormalized {
		return fmt.Sprintf("%s: matched %s (file %s)", what, f.FileName, f.ID)
	}
	return fmt.Sprintf("%s: %s (file %s)", what, f.FileName, f.ID)
}

// fromFileSkipped is the --json document for a locked mod: nothing was
// applied, so the profile ref is reported as it stands.
func fromFileSkipped(plan *core.UpdateFromArchivePlan) *core.UpdateApplyResult {
	return &core.UpdateApplyResult{
		Mod:         domain.ModReference{SourceID: plan.Mod.SourceID, ModID: plan.Mod.ID, Version: plan.LockedVersion, Locked: true},
		Name:        plan.Mod.Name,
		FromVersion: plan.FromVersion,
		ToVersion:   plan.ToVersion,
		Status:      core.UpdateSkipped,
		Reason:      "locked",
	}
}
