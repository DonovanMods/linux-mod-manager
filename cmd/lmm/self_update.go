package main

import (
	"context"
	"fmt"
	"os"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
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

self-update never restarts lmm-serve.service (see 'lmm service'): it cannot
see the web UI's running jobs. With automatic updates on, lmm-serve.service
notices the new binary within a minute and restarts itself once no job is
running. Otherwise, or for an 'lmm serve' you started yourself, it keeps
running the old version until you restart it; its web UI says so.

Without --yes, lmm asks before it replaces the binary. --json never asks:
without --yes it prints the plan and changes nothing.

--auto is the automatic update: what lmm starts in the background after a
command once it knows of a newer release, and what lmm-update.timer runs
('lmm service install --auto-update'). It needs no --yes, and does nothing
when config.yaml says 'auto_update: false', when another self-update is
running, or for a release whose automatic update failed in the last day.
Automatic updates are on by default for a self-managed lmm; a package's lmm
is never updated by lmm itself.

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
	selfUpdateCmd.Flags().BoolVar(&selfUpdate.auto, "auto", false, "run as an automatic update: no --yes, nothing when auto_update is false in config.yaml")
	rootCmd.AddCommand(selfUpdateCmd)
}

// newSelfUpdater builds the updater for the --config/--data the flags
// select. It restarts nothing: neither `lmm self-update` nor an automatic
// update run from the CLI or lmm-update.timer can see `lmm serve`'s jobs,
// so lmm serve restarts itself (serveAutoUpdater, #586).
func newSelfUpdater() (*core.SelfUpdater, error) {
	opts, err := selfUpdateOptions()
	if err != nil {
		return nil, err
	}
	return core.NewSelfUpdater(opts), nil
}

// selfUpdateOptions is newSelfUpdater's options.
func selfUpdateOptions() (core.SelfUpdateOptions, error) {
	cfg, err := getServiceConfig()
	if err != nil {
		return core.SelfUpdateOptions{}, err
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
	// The mutation lock every lmm command and `lmm serve` takes: an
	// automatic update does not install while it is held (#586).
	if p, err := app.ResolvePaths(app.Options{ConfigDir: configDir, DataDir: dataDir}); err == nil {
		opts.OpLockPath = app.OpLockPath(p)
	}
	return opts, nil
}

// runSelfUpdate is `lmm self-update`.
func runSelfUpdate(ctx context.Context) error {
	u, err := newSelfUpdater()
	if err != nil {
		return err
	}
	if selfUpdate.auto && !selfUpdate.check {
		return runAutoUpdate(ctx, u)
	}
	// One run at a time, held from the plan through the install. --check
	// only reads, so it takes no lock.
	if !selfUpdate.check {
		unlock, err := u.Lock()
		if err != nil {
			return err
		}
		defer unlock()
	}
	plan, err := u.Plan(ctx)
	if err != nil {
		return err
	}
	yes := selfUpdate.yes
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
	printSelfUpdateResult(res, plan.ReleaseURL, u.AutoUpdateEnabled())
	return nil
}

// runAutoUpdate is --auto: core's RunAuto, which the detached run a CLI
// command starts and lmm-update.timer's both are (#586). A run that steps
// aside - turned off, a package's binary, another run under way, a
// release backed off after a failure - says why and succeeds, so systemd
// does not retry it.
func runAutoUpdate(ctx context.Context, u *core.SelfUpdater) error {
	out, err := u.RunAuto(ctx, core.AutoRunOptions{})
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(out.Result)
	}
	if out.Skipped != "" {
		fmt.Println(out.Skipped + "; not updating.")
		return nil
	}
	printSelfUpdateResult(out.Result, "", true)
	return nil
}

// printSelfUpdateResult is the text after an install, saying what becomes
// of a running lmm serve: with auto_update on, lmm-serve.service restarts
// itself once idle; otherwise it waits for the person.
func printSelfUpdateResult(res core.SelfUpdateResult, releaseURL string, autoUpdate bool) {
	fmt.Printf("Updated lmm %s to %s (%s).\n", res.Previous, colorBold(res.Version), res.Executable)
	if res.Updated {
		if autoUpdate {
			fmt.Println(colorDim("If lmm-serve.service is running, lmm serve restarts itself onto " + res.Version + " once no job is running."))
		} else {
			fmt.Println(colorDim("A running lmm serve keeps " + res.Previous + " until you restart it: systemctl --user restart lmm-serve"))
		}
	}
	if releaseURL != "" {
		fmt.Println(colorDim("Release notes: " + releaseURL))
	}
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
