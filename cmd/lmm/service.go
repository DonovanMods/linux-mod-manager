package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/spf13/cobra"
)

// newUserUnits builds the systemd seam `lmm service` works through. A test
// swaps it for one over a temp config home and a fake systemctl, so no test
// touches the real user manager.
var newUserUnits = core.NewUserUnits

var (
	serviceAddr       string
	serviceAutoUpdate bool
)

// serviceCmd groups the systemd user-service commands (#567). None of them
// opens the mod database: a unit file and the user manager are all they
// touch.
var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Run the web UI in the background as a systemd user service",
	Long: `Run 'lmm serve' in the background as the systemd user service
lmm-serve.service, so the web UI is there whenever you are logged in.

If you installed lmm from a package (deb, rpm, apk or the AUR), the unit is
already installed, turned off; turn it on with:
  systemctl --user enable --now lmm-serve

'lmm service install' is for a tarball, 'go install' or 'make install':
it writes the unit to ~/.config/systemd/user, pointing at this lmm binary.

The service starts when you log in. To start it at boot without a login,
run 'loginctl enable-linger $USER' once; lmm never runs it for you.`,
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install, enable and start lmm-serve.service for this lmm binary",
	Long: `Write lmm-serve.service to the directory the systemd user manager loads
units from ($XDG_CONFIG_HOME/systemd/user as the user manager sees it,
default ~/.config/systemd/user) with ExecStart pointing at this lmm binary,
then reload the user manager and enable and start the service.

The service reads the same config and data as this command: --config and
--data are passed on to it, and so are XDG_CONFIG_HOME and XDG_DATA_HOME
when they differ from the user manager's. It runs with the user manager's
PATH, not your shell's (see: systemctl --user show-environment), so 7z,
unrar and steamcmd must be on that one.

Running it again rewrites the unit (say, with a new --addr) and restarts the
service. If a package already installed the unit, nothing is written: lmm
tells you the systemctl command that turns it on. A unit file lmm did not
write is left alone.

--auto-update also installs lmm-update.timer, which runs 'lmm self-update
--auto' once a day. That installs a new lmm release only while config.yaml
says 'auto_update: true', so the setting turns updates on and off without
touching the units. Later installs keep the timer; 'lmm service uninstall'
removes it. It is refused for an lmm a package manager installed, which
'lmm self-update' never replaces.

Examples:
  lmm service install
  lmm service install --addr 127.0.0.1:8080
  lmm service install --auto-update`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return doServiceInstallWith(cmd.Context(), cmd, serviceAddr, serviceAutoUpdate)
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop, disable and remove the units that 'lmm service install' wrote",
	Long: `Stop and disable lmm-serve.service, and lmm-update.timer and
lmm-update.service when --auto-update installed them, remove the unit files
'lmm service install' wrote, and reload the user manager. A unit a package installed is
left alone; lmm tells you the systemctl command that turns it off.

Disabling removes the enablement link in ~/.config/systemd/user, which a
packaged lmm-serve.service shares: if your lmm package also installed the
unit, it is turned off too, and lmm prints the command that turns it on.

Examples:
  lmm service uninstall`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return doServiceUninstall(cmd.Context())
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether lmm-serve.service is installed, enabled and running",
	Long: `Show whether lmm-serve.service is installed (and by whom), enabled and
running, the unit file systemd uses, and whether lingering is on - that is,
whether your user services start at boot rather than at login.

Examples:
  lmm service status
  lmm service status --json`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return doServiceStatus(cmd.Context())
	},
}

func init() {
	serviceInstallCmd.Flags().StringVar(&serviceAddr, "addr", "", "address 'lmm serve' listens on (host:port; default "+defaultServeAddr+")")
	serviceInstallCmd.Flags().BoolVar(&serviceAutoUpdate, "auto-update", false, "also install lmm-update.timer, which runs 'lmm self-update' daily while auto_update is true in config.yaml")
	serviceCmd.AddCommand(serviceInstallCmd, serviceUninstallCmd, serviceStatusCmd)
	rootCmd.AddCommand(serviceCmd)
}

// serviceEnvOnlyCredentials reads which sources' keys this shell holds only
// in an environment variable (#582), for the warning install and status
// print. It is a variable so a test never opens a real installation.
var serviceEnvOnlyCredentials = realServiceEnvOnlyCredentials

// realServiceEnvOnlyCredentials is serviceEnvOnlyCredentials' real
// implementation. A failure to read them is not the command's failure -
// the units are what it is about - so it reports none.
func realServiceEnvOnlyCredentials(ctx context.Context) []core.EnvOnlyCredential {
	svc, err := initServiceWith(ctx, app.Options{WarnWriter: io.Discard})
	if err != nil {
		return nil
	}
	defer closeService(svc)
	report, err := app.AuthStatus(ctx, svc)
	if err != nil {
		return nil
	}
	return app.EnvOnlyCredentials(report)
}

// serviceMissingCredentials is every source whose key this shell has only
// in an environment variable that the user manager does not hand
// lmm-serve.service either (#582).
func serviceMissingCredentials(ctx context.Context, units *core.UserUnits) []core.EnvOnlyCredential {
	return units.WithoutManagerEnvironment(ctx, serviceEnvOnlyCredentials(ctx))
}

// printEnvOnlyCredentials warns once per source whose key lmm-serve will
// not see (#582): the unit does not inherit this shell's exports, and lmm
// never writes a key into the unit file.
func printEnvOnlyCredentials(creds []core.EnvOnlyCredential) {
	for _, c := range creds {
		fmt.Printf("%s %s's key comes only from $%s, which lmm-serve.service will not see.\n", colorYellow("!"), c.Name, c.EnvVar)
		fmt.Printf("  Store it with: lmm auth login %s (or the web UI's Setup > Authentication), then: systemctl --user restart lmm-serve\n", c.SourceID)
	}
}

// doServiceInstall installs lmm-serve.service alone, as before
// --auto-update existed.
func doServiceInstall(ctx context.Context, cmd *cobra.Command, addr string) error {
	return doServiceInstallWith(ctx, cmd, addr, false)
}

// doServiceInstallWith installs the units for this binary and prints where
// they went, the address it serves and, without lingering, the boot-time
// hint. autoUpdate adds the self-update timer (#569), refused first for a
// binary `lmm self-update` cannot replace.
func doServiceInstallWith(ctx context.Context, cmd *cobra.Command, addr string, autoUpdate bool) error {
	if addr != "" {
		if err := core.ValidateServeAddr(addr); err != nil {
			return err
		}
		// Under --json stderr stays empty (the JSON contract), so the
		// warning is printed only for a person at a terminal.
		if !jsonOutput {
			if err := warnIfNonLoopback(cmd, addr); err != nil {
				return err
			}
		}
	}
	// The service does not start in this directory, so a relative
	// --config or --data is made absolute here.
	if autoUpdate {
		probe := core.DefaultInstallProbe()
		if releaseInstallProbe != nil {
			probe = *releaseInstallProbe
		}
		if err := core.CheckSelfUpdateInstall(ctx, probe, isDevBuild()); err != nil {
			return err
		}
	}
	opts := core.ServiceInstallOptions{Addr: addr, AutoUpdate: autoUpdate}
	for _, d := range []struct {
		from string
		to   *string
	}{{configDir, &opts.ConfigDir}, {dataDir, &opts.DataDir}} {
		if d.from == "" {
			continue
		}
		abs, err := filepath.Abs(d.from)
		if err != nil {
			return err
		}
		*d.to = abs
	}
	units, err := newUserUnits()
	if err != nil {
		return err
	}
	report, err := units.Install(ctx, opts)
	if err != nil {
		return err
	}
	report.EnvOnlyCredentials = serviceMissingCredentials(ctx, units)
	if jsonOutput {
		return emitJSON(report)
	}
	if addr == "" {
		addr = defaultServeAddr
	}
	for _, u := range report.Units {
		verb := "Installed"
		if !report.Changed {
			verb = "Already installed"
		}
		fmt.Printf("%s %s at %s\n", verb, colorBold(u.Name), u.Path)
		switch u.Name {
		case core.UpdateServiceName:
			continue
		case core.UpdateTimerName:
			printAutoUpdateState()
			continue
		}
		if u.Active {
			fmt.Printf("  The web UI is running at %s\n", colorBold("http://"+addr+"/"))
		} else {
			fmt.Printf("  %s it started but is not running (%s); see: journalctl --user -u %s\n", colorYellow("!"), u.ActiveState, strings.TrimSuffix(u.Name, ".service"))
		}
	}
	printLingerHint(report.Linger, units.User)
	printEnvOnlyCredentials(report.EnvOnlyCredentials)
	return nil
}

// doServiceUninstall removes the unit `lmm service install` wrote.
func doServiceUninstall(ctx context.Context) error {
	units, err := newUserUnits()
	if err != nil {
		return err
	}
	report, err := units.Uninstall(ctx)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitJSON(report)
	}
	if !report.Changed {
		fmt.Println("lmm-serve.service is not installed; nothing to remove.")
		return nil
	}
	for _, u := range report.Units {
		fmt.Printf("Removed %s; it is stopped and will not start again.\n", colorBold(u.Name))
		// Disabling removed the enablement link the packaged unit shares.
		if u.Origin == core.UnitOriginPackage {
			fmt.Printf("  Your lmm package's %s (%s) is disabled too; turn it on with: systemctl --user enable --now %s\n",
				u.Name, u.Path, strings.TrimSuffix(u.Name, ".service"))
		}
	}
	return nil
}

// doServiceStatus prints each unit's state and whether lingering is on.
func doServiceStatus(ctx context.Context) error {
	units, err := newUserUnits()
	if err != nil {
		return err
	}
	report, err := units.Status(ctx)
	if err != nil {
		return err
	}
	report.EnvOnlyCredentials = serviceMissingCredentials(ctx, units)
	if jsonOutput {
		return emitJSON(report)
	}
	for _, u := range report.Units {
		fmt.Println(colorBold(u.Name))
		switch u.Origin {
		case core.UnitOriginUser:
			fmt.Printf("  installed:  yes, by lmm service install (%s)\n", u.Path)
		case core.UnitOriginPackage:
			fmt.Printf("  installed:  yes, by your lmm package (%s)\n", u.Path)
		default:
			fmt.Println("  installed:  no - not installed")
		}
		fmt.Printf("  enabled:    %s (%s)\n", yesNo(u.Enabled), u.EnabledState)
		fmt.Printf("  running:    %s (%s)\n", yesNo(u.Active), u.ActiveState)
		switch {
		case !u.Installed:
			fmt.Println(colorDim("  Install it with: lmm service install"))
		case !u.Enabled || !u.Active:
			fmt.Println(colorDim("  Turn it on with: systemctl --user enable --now " + u.Name))
		}
	}
	if report.Linger {
		fmt.Println("lingering:    on - your user services start at boot")
	} else {
		fmt.Println("lingering:    off - your user services start when you log in")
		printLingerHint(false, units.User)
	}
	printEnvOnlyCredentials(report.EnvOnlyCredentials)
	return nil
}

// printAutoUpdateState says whether the timer will install anything: it
// does only while config.yaml says auto_update: true.
func printAutoUpdateState() {
	cfgDir := configDir
	if cfg, err := getServiceConfig(); err == nil {
		cfgDir = cfg.ConfigDir
	}
	u, err := newSelfUpdater()
	if err == nil && u.AutoUpdateEnabled() {
		fmt.Println("  lmm updates itself daily: auto_update is on.")
		return
	}
	fmt.Printf("  It checks daily, but installs nothing until you add 'auto_update: true' to %s\n", filepath.Join(cfgDir, "config.yaml"))
}

// printLingerHint tells a user without lingering how to start at boot. It
// is a system setting, so lmm prints the command and never runs it.
func printLingerHint(linger bool, user string) {
	if linger {
		return
	}
	if user == "" {
		user = "$USER"
	}
	fmt.Println(colorDim("  To start it at boot without logging in, run once: loginctl enable-linger " + user))
}

// yesNo renders a bool for the status table.
func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
