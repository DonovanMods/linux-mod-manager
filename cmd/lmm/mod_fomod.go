package main

// mod_fomod.go is `lmm mod fomod <mod-id>` (#615): change an installed
// FOMOD mod's installer choices. Core's PlanFomodReconfigure /
// ApplyFomodReconfigure ask the installer again, preselected from the
// recorded choices; the answers (a walk at a terminal, --fomod-choices or
// --fomod-defaults) install a new variant and redeploy it.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

var (
	modFomodForce     bool
	modFomodSkipCheck bool
	modFomodFile      string
)

var modFomodCmd = &cobra.Command{
	Use:   "fomod <mod-id>",
	Short: "Change an installed mod's FOMOD installer choices",
	Long: `Run an installed mod's FOMOD installer again, with the choices you made
last time preselected, and install what the new choices select.

The mod keeps its version: lmm fetches the same file again (or reuses one
it kept), asks the installer's questions one step at a time, and replaces
the deployed files with the new selection. The new choices are recorded on
the profile.

A mod installed before lmm ran FOMOD installers went in as the whole
archive; this runs its installer and replaces that install.

A mod whose files each carry their own installer (a collection that pins
a mod's main file and a patch file, each a FOMOD) is asked per file: pick
one with --file <file-id>, or, at a terminal, from a list; without one,
every installer is asked. The others keep their choices.

Without a terminal, answer with --fomod-choices <file.json> or
--fomod-defaults; with neither, the questions are printed (the full
document under --json) and lmm exits non-zero.

Examples:
  lmm mod fomod 12604 --game skyrim-se
  lmm mod fomod 12604 --game skyrim-se --fomod-choices choices.json
  lmm mod fomod 43532 --game skyrim-se --file 101`,
	Args: cobra.ExactArgs(1),
	RunE: runModFomod,
}

func init() {
	modFomodCmd.Flags().BoolVarP(&modFomodForce, "force", "f", false, "overwrite other mods' files without asking, and continue past failing hooks")
	modFomodCmd.Flags().BoolVar(&modFomodSkipCheck, "skip-verify", false, "skip checksum verification of the download")
	modFomodCmd.Flags().StringVar(&modFomodFile, "file", "", "for a mod whose files each carry their own installer, the file id whose installer to change (default: every one, or a list to pick from at a terminal)")
	addFomodFlags(modFomodCmd)
	modCmd.AddCommand(modFomodCmd)
}

func runModFomod(cmd *cobra.Command, args []string) error {
	return withGameService(cmd, func(ctx context.Context, service *core.Service, game *domain.Game) error {
		return doModFomod(ctx, service, game, args[0])
	})
}

func doModFomod(ctx context.Context, service *core.Service, game *domain.Game, modID string) error {
	profileName, err := resolveModTarget(ctx, service, game, modID)
	if err != nil {
		return err
	}
	fa, err := newFomodAnswerer(service)
	if err != nil {
		return err
	}
	plan, err := service.PlanFomodReconfigure(ctx, game, profileName, modSource, modID)
	if err != nil {
		return err
	}
	if !jsonOutput {
		fmt.Printf("Reconfiguring %s %s (profile %s)\n", domain.SanitizeLine(plan.Mod.Name), domain.SanitizeLine(plan.Mod.Version), profileName)
	}
	opts := core.FomodReconfigureOptions{Force: modFomodForce, SkipVerify: modFomodSkipCheck, SkipHooks: noHooks, FileID: modFomodFile}
	if opts.FileID == "" && len(plan.Installers) > 1 && fa.interactive {
		if opts.FileID, err = fa.pickInstaller(plan); err != nil {
			return err
		}
	}
	progress := func(e core.Event) {
		p, ok := lineOf(e)
		if !ok {
			return
		}
		switch p.Phase {
		case core.InstallDownloadStarted:
			fmt.Printf("\nDownloading %s...\n", displayFileLabel(*p.File))
		case core.InstallDownloading:
			if p.TotalBytes > 0 {
				printProgressLine("\r  [%s] %.1f%% (%s / %s)", progressBar(p.Percent, 30), p.Percent, formatSize(p.Downloaded), formatSize(p.TotalBytes))
			} else {
				printProgressLine("\r  Downloaded %s", formatSize(p.Downloaded))
			}
		case core.InstallDownloadDone:
			finishProgressLine()
		case core.InstallDeploying:
			fmt.Println("Deploying to game directory...")
		case core.InstallBeforeAllForced, core.InstallBeforeEachForced, core.InstallWarning:
			fmt.Fprintf(os.Stderr, "Warning: %s\n", p.Detail)
		}
	}
	apply := func(answers map[string]domain.FomodChoices) (*core.InstallResult, error) {
		opts.Fomod = answers
		return service.ApplyFomodReconfigure(ctx, game, plan, opts, quietSink(progress))
	}
	result, err := applyAnsweringFomod(fa, apply, nil)
	var conflictErr *core.ConflictError
	if errors.As(err, &conflictErr) && !jsonOutput {
		proceed, readErr := confirmInstallConflicts(ctx, service, game, profileName, conflictErr.Conflicts)
		if readErr != nil {
			return readErr
		}
		if !proceed {
			return ErrCancelled
		}
		opts.AcceptConflicts = true
		result, err = applyAnsweringFomod(fa, apply, nil)
	}
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(result)
	}
	fmt.Printf("\n✓ Reconfigured: %s %s\n", domain.SanitizeLine(plan.Mod.Name), domain.SanitizeLine(plan.Mod.Version))
	fmt.Printf("  Files deployed: %d\n", result.FilesDeployed)
	if result.ProfileWriteFailed {
		fmt.Printf("  NOT recorded on profile: %s\n", profileName)
	}
	return nil
}

// pickInstaller asks which installer of a mod whose files each carry one
// to change (#634): one of plan.Installers' file ids, or "" for every one.
func (f *fomodAnswerer) pickInstaller(plan *core.FomodReconfigurePlan) (string, error) {
	_, _ = fmt.Fprintf(f.out, "%s has %d installers, one per file:\n", domain.SanitizeLine(plan.Mod.Name), len(plan.Installers))
	for i, inst := range plan.Installers {
		name := inst.Name
		if name == "" {
			name = "file " + inst.FileID
		}
		_, _ = fmt.Fprintf(f.out, "  %d) %s (file %s)\n", i+1, domain.SanitizeLine(name), domain.SanitizeLine(inst.FileID))
	}
	for {
		_, _ = fmt.Fprintf(f.out, "Change which? [1-%d, Enter for all, q cancels]: ", len(plan.Installers))
		line, err := f.in.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || strings.TrimSpace(line) == "") {
			return "", promptReadErrorAs(err, ErrCancelled)
		}
		switch line = strings.TrimSpace(line); strings.ToLower(line) {
		case "":
			return "", nil
		case "q":
			return "", ErrCancelled
		}
		n, perr := strconv.Atoi(line)
		if perr != nil || n < 1 || n > len(plan.Installers) {
			_, _ = fmt.Fprintf(f.out, "  Choose a number from 1 to %d.\n", len(plan.Installers))
			continue
		}
		return plan.Installers[n-1].FileID, nil
	}
}
