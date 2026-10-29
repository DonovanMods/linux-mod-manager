package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/spf13/cobra"
)

// newUserUnits builds the systemd seam `lmm service` works through. A test
// swaps it for one over a temp config home and a fake systemctl, so no test
// touches the real user manager.
var newUserUnits = core.NewUserUnits

var serviceAddr string

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

Examples:
  lmm service install
  lmm service install --addr 127.0.0.1:8080`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		return doServiceInstall(cmd.Context(), cmd, serviceAddr)
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop, disable and remove the lmm-serve.service that 'lmm service install' wrote",
	Long: `Stop and disable lmm-serve.service, remove the unit file 'lmm service
install' wrote, and reload the user manager. A unit a package installed is
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
	serviceCmd.AddCommand(serviceInstallCmd, serviceUninstallCmd, serviceStatusCmd)
	rootCmd.AddCommand(serviceCmd)
}

// doServiceInstall installs the unit for this binary and prints where it
// went, the address it serves and, without lingering, the boot-time hint.
func doServiceInstall(ctx context.Context, cmd *cobra.Command, addr string) error {
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
	opts := core.ServiceInstallOptions{Addr: addr}
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
		if u.Active {
			fmt.Printf("  The web UI is running at %s\n", colorBold("http://"+addr+"/"))
		} else {
			fmt.Printf("  %s it started but is not running (%s); see: journalctl --user -u %s\n", colorYellow("!"), u.ActiveState, strings.TrimSuffix(u.Name, ".service"))
		}
	}
	printLingerHint(report.Linger, units.User)
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
		return nil
	}
	fmt.Println("lingering:    off - your user services start when you log in")
	printLingerHint(false, units.User)
	return nil
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
