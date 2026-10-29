package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// self_update.go - `lmm self-update` (#569): the CLI over core.SelfUpdater.
// It parses the flags, asks before replacing the binary, and prints what
// happened; the release lookup, verification and replacement are core's.

// releaseDownloadBase is the host release assets download from. A var for
// the tests' httptest servers, like releaseAPIBase.
var releaseDownloadBase = core.GitHubDownloadBase

// releaseTrustedKeys, when non-nil, replaces the release keys compiled
// into lmm: a test signs its releases with a key it generated, never the
// real one.
var releaseTrustedKeys []string

// selfUpdateStdinIsTerminal reports whether a person can answer the
// confirmation prompt.
var selfUpdateStdinIsTerminal = func() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// selfUpdateFlags are `lmm self-update`'s flags.
type selfUpdateFlags struct {
	check, yes, auto bool
}

var selfUpdate selfUpdateFlags

var selfUpdateCmd = &cobra.Command{
	Use:   "self-update",
	Short: "Update lmm itself to the latest release, verified",
	Long: `Download the latest lmm release from GitHub and replace this lmm binary
with it.

The release is verified before anything is replaced: its checksums.txt must
carry a signature by the release key built into lmm, and the downloaded
archive must match its line there. A release that fails either check is
refused, and nothing is installed.

self-update only replaces a copy of lmm you manage yourself: the release
tarball, 'go install' or 'make install'. An lmm a package manager installed
(deb, rpm, apk, the AUR) is upgraded with that package manager, and lmm tells
you the command. You need write access to the directory lmm is in; lmm never
uses sudo.

If lmm-serve.service (see 'lmm service') is running, it is restarted onto the
new version. An 'lmm serve' you started yourself keeps running the old one
until you restart it; its web UI says so.

Without --yes, lmm asks before it replaces the binary. --json never asks:
without --yes it prints the plan and changes nothing.

--auto is what lmm-update.timer runs ('lmm service install --auto-update'):
it does nothing unless config.yaml says 'auto_update: true', and needs no
--yes.

Examples:
  lmm self-update --check
  lmm self-update
  lmm self-update --yes`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runSelfUpdate(cmd.Context())
	},
}

func init() {
	selfUpdateCmd.Flags().BoolVar(&selfUpdate.check, "check", false, "only report whether a newer release exists")
	selfUpdateCmd.Flags().BoolVarP(&selfUpdate.yes, "yes", "y", false, "install without asking")
	selfUpdateCmd.Flags().BoolVar(&selfUpdate.auto, "auto", false, "run as the unattended timer does: update only when auto_update is true in config.yaml")
	rootCmd.AddCommand(selfUpdateCmd)
}

// newSelfUpdater builds the updater for the --config/--data the flags
// select, restarting lmm-serve.service through the same systemd seam
// `lmm service` uses.
func newSelfUpdater() (*core.SelfUpdater, error) {
	cfg, err := getServiceConfig()
	if err != nil {
		return nil, err
	}
	opts := core.SelfUpdateOptions{
		ConfigDir:    cfg.ConfigDir,
		DataDir:      cfg.DataDir,
		Version:      version,
		Dev:          isDevBuild(),
		APIBase:      releaseAPIBase,
		DownloadBase: releaseDownloadBase,
		Probe:        releaseInstallProbe,
		TrustedKeys:  releaseTrustedKeys,
	}
	if units, err := newUserUnits(); err == nil {
		opts.Restarter = units
	}
	return core.NewSelfUpdater(opts), nil
}

// runSelfUpdate is `lmm self-update`.
func runSelfUpdate(ctx context.Context) error {
	u, err := newSelfUpdater()
	if err != nil {
		return err
	}
	if selfUpdate.auto && !u.AutoUpdateEnabled() {
		if jsonOutput {
			return emitJSON(core.SelfUpdateResult{Previous: version, Version: version})
		}
		fmt.Println("auto_update is off in config.yaml; not updating.")
		return nil
	}
	// One run at a time, held from the plan through the install. --check
	// only reads, so it takes no lock. The timer's run steps aside for one
	// already under way: that is not a failure for systemd to retry.
	if !selfUpdate.check {
		unlock, err := u.Lock()
		if errors.Is(err, core.ErrSelfUpdateRunning) && selfUpdate.auto {
			if jsonOutput {
				return emitJSON(core.SelfUpdateResult{Previous: version, Version: version})
			}
			fmt.Println(err.Error() + "; not updating.")
			return nil
		}
		if err != nil {
			return err
		}
		defer unlock()
	}
	plan, err := u.Plan(ctx)
	if err != nil {
		return err
	}
	yes := selfUpdate.yes || selfUpdate.auto
	if plan.UpToDate || selfUpdate.check || (jsonOutput && !yes) {
		if jsonOutput {
			return emitJSON(plan)
		}
		printSelfUpdatePlan(plan)
		return nil
	}
	if !yes {
		if !selfUpdateStdinIsTerminal() {
			return core.ErrConfirmationRequired
		}
		fmt.Printf("Update lmm %s to %s? This replaces %s. [y/N] ", plan.Current, plan.Latest, plan.Executable)
		response, err := readPromptLine()
		if err != nil {
			return err
		}
		if response != "y" && response != "yes" {
			return ErrCancelled
		}
	}
	res, err := u.Apply(ctx, plan)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(res)
	}
	fmt.Printf("Updated lmm %s to %s (%s).\n", res.Previous, colorBold(res.Version), res.Executable)
	switch {
	case res.ServeRestarted:
		fmt.Println("Restarted lmm-serve.service, which now runs " + res.Version + ".")
	case res.ServeRestartError != "":
		fmt.Fprintf(os.Stderr, "%s lmm-serve.service still runs %s: restarting it failed (%s); restart it with: systemctl --user restart lmm-serve\n",
			colorYellow("!"), res.Previous, res.ServeRestartError)
	}
	if plan.ReleaseURL != "" {
		fmt.Println(colorDim("Release notes: " + plan.ReleaseURL))
	}
	return nil
}

// printSelfUpdatePlan is --check's text, and the up-to-date answer.
func printSelfUpdatePlan(plan core.SelfUpdatePlan) {
	switch {
	case plan.Latest == "":
		fmt.Printf("lmm %s: no lmm release is published.\n", plan.Current)
	case plan.UpToDate:
		fmt.Printf("lmm %s is up to date.\n", plan.Current)
	default:
		fmt.Printf("lmm %s is available (you have %s).\n", colorBold(plan.Latest), plan.Current)
		fmt.Printf("Run 'lmm self-update' to replace %s with it.\n", plan.Executable)
		if plan.ReleaseURL != "" {
			fmt.Println(colorDim("Release notes: " + plan.ReleaseURL))
		}
	}
}
