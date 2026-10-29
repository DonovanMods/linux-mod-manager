package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/spf13/cobra"
)

// version_cmd.go - `lmm version` (#568): the running version, the newest
// release lmm knows of, and how this binary was installed. `--version`
// stays the bare version line it always was.

// releaseAPIBase is the GitHub API root the release check asks. A var for
// the tests' httptest servers; TestMain additionally guards the default
// transport, so a test that forgets to set it fails without a request.
var releaseAPIBase = core.GitHubAPIBase

// releaseInstallProbe, when non-nil, replaces the real package-manager
// queries - a test must not ask the host's package database about the test
// binary, whose answer would differ on every machine.
var releaseInstallProbe *core.InstallProbe

// versionCheckTimeout bounds `lmm version --check`'s request. The user
// asked for it, so it may wait longer than the after-command check does.
const versionCheckTimeout = 15 * time.Second

var versionCheck bool

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show lmm's version and whether a newer release exists",
	Long: `Show the running lmm version, the newest lmm release it knows of, and how
this binary was installed - which is how to upgrade it.

lmm checks GitHub for a new release at most once a day, after an interactive
command (and while 'lmm serve' runs), and remembers the answer. 'lmm version'
reports that remembered answer; --check asks GitHub now, even when the
automatic check is turned off with 'update_check: false' in config.yaml.

Examples:
  lmm version
  lmm version --check
  lmm version --check --json`,
	Args: cobra.NoArgs,
	RunE: runVersion,
}

func init() {
	versionCmd.Flags().BoolVar(&versionCheck, "check", false, "ask GitHub for the latest release now")
	rootCmd.AddCommand(versionCmd)
}

// isDevBuild reports a build not made from a release tag - the one whose
// display version carries a "(dev: ...)" suffix.
func isDevBuild() bool {
	return computeDisplayVersion(version, buildDescribe) != version
}

// newReleaseChecker builds the checker for the --config/--data the flags
// select, and returns config.yaml's path for the messages that name it.
func newReleaseChecker() (*core.ReleaseChecker, string, error) {
	cfg, err := getServiceConfig()
	if err != nil {
		return nil, "", err
	}
	return core.NewReleaseChecker(core.ReleaseCheckOptions{
		ConfigDir: cfg.ConfigDir,
		DataDir:   cfg.DataDir,
		Version:   version,
		Dev:       isDevBuild(),
		APIBase:   releaseAPIBase,
		Probe:     releaseInstallProbe,
	}), filepath.Join(cfg.ConfigDir, "config.yaml"), nil
}

func runVersion(cmd *cobra.Command, _ []string) error {
	rc, _, err := newReleaseChecker()
	if err != nil {
		return err
	}
	var st core.ReleaseStatus
	if versionCheck {
		ctx, cancel := context.WithTimeout(cmd.Context(), versionCheckTimeout)
		defer cancel()
		if st, err = rc.Check(ctx, true); err != nil {
			return fmt.Errorf("checking GitHub for a new lmm release: %w", err)
		}
	} else {
		st = rc.Status(cmd.Context())
	}
	if jsonOutput {
		return emitJSON(st)
	}
	return printReleaseStatus(cmd.OutOrStdout(), st)
}

// printReleaseStatus is `lmm version`'s text.
func printReleaseStatus(out io.Writer, st core.ReleaseStatus) error {
	w := &errWriter{w: out}
	w.printf("lmm %s\n", computeDisplayVersion(version, buildDescribe))
	switch {
	case st.Latest != "":
		w.printf("Latest release: %s (checked %s)\n", st.Latest, st.CheckedAt.Local().Format("2006-01-02 15:04"))
	case !st.CheckedAt.IsZero() && st.CheckError == "":
		w.printf("Latest release: none published\n")
	default:
		w.printf("Latest release: unknown (run 'lmm version --check')\n")
	}
	if st.CheckError != "" {
		w.printf("Last check failed: %s\n", st.CheckError)
	}
	switch {
	case st.Dev:
		w.printf("This is a development build; it never reports an update.\n")
	case st.UpdateAvailable:
		w.printf("Update available: %s\n", upgradeHint(st))
		if st.ReleaseURL != "" && st.Install.UpgradeCommand != "" {
			w.printf("Release notes: %s\n", st.ReleaseURL)
		}
	case st.Latest != "":
		w.printf("lmm is up to date.\n")
	}
	w.printf("Installed: %s\n", installText(st.Install))
	if st.CheckEnabled {
		w.printf("Automatic checks: on\n")
	} else {
		w.printf("Automatic checks: off (update_check: false)\n")
	}
	return w.err
}

// upgradeHint words how to get st's newer release: the package manager's
// command, or the release page for a binary the user manages themselves.
func upgradeHint(st core.ReleaseStatus) string {
	if st.Install.UpgradeCommand != "" {
		return fmt.Sprintf("lmm %s; upgrade with: %s", st.Latest, st.Install.UpgradeCommand)
	}
	if st.ReleaseURL != "" {
		return fmt.Sprintf("lmm %s, from %s", st.Latest, st.ReleaseURL)
	}
	return "lmm " + st.Latest
}

// installText is the "Installed:" line's value.
func installText(m core.InstallMethod) string {
	switch m.Method {
	case core.InstallPackage:
		return fmt.Sprintf("package %s (%s)", m.Package, m.Manager)
	case core.InstallDev:
		return "development build"
	default:
		return "self-managed"
	}
}

// errWriter is a writer that remembers its first error, so a run of
// printf calls is checked once.
type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err == nil {
		_, e.err = fmt.Fprintf(e.w, format, args...)
	}
}
