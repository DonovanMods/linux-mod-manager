package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/testutil"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cliReleaseServer stands in for GitHub's latest-release endpoint (#568)
// and points the CLI's checker at it, with a fixed running version and an
// install probe that owns nothing, for the length of the test.
func cliReleaseServer(t *testing.T, tag string) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://github.com/DonovanMods/linux-mod-manager/releases/tag/` + tag + `"}`))
	}))
	t.Cleanup(srv.Close)

	oldBase, oldProbe, oldVersion, oldDescribe := releaseAPIBase, releaseInstallProbe, version, buildDescribe
	releaseAPIBase = srv.URL
	releaseInstallProbe = &core.InstallProbe{
		Executable: func() (string, error) { return "/home/u/.local/bin/lmm", nil },
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
	}
	version, buildDescribe = "2.0.0", ""
	t.Cleanup(func() {
		releaseAPIBase, releaseInstallProbe, version, buildDescribe = oldBase, oldProbe, oldVersion, oldDescribe
	})

	cfg, data := configDir, dataDir
	configDir, dataDir = t.TempDir(), t.TempDir()
	oldJSON := jsonOutput
	t.Cleanup(func() {
		configDir, dataDir, jsonOutput = cfg, data, oldJSON
		rootCmd.SetArgs(nil)
		resetChangedFlags(rootCmd)
	})
	return &hits
}

// armReleaseNotice makes this test run what Execute arms in production: the
// after-command check, with stderr reported as a terminal.
func armReleaseNotice(t *testing.T, tty bool) {
	t.Helper()
	oldArmed, oldTTY := releaseNoticeArmed, stderrIsTerminal
	releaseNoticeArmed = true
	stderrIsTerminal = func() bool { return tty }
	t.Cleanup(func() {
		releaseNoticeArmed, stderrIsTerminal = oldArmed, oldTTY
		pendingRelease = nil
	})
}

// afterCommand runs one command line as Execute would and returns what the
// release notice wrote after it.
func afterCommand(t *testing.T, args ...string) string {
	t.Helper()
	runLMM(t, args...)
	var notice bytes.Buffer
	finishReleaseCheck(&notice)
	return notice.String()
}

func TestVersionCommand_CheckReportsANewerRelease(t *testing.T) {
	hits := cliReleaseServer(t, "v2.1.0")

	out, _ := runLMM(t, "version")
	assert.Contains(t, out, "lmm 2.0.0\n")
	assert.Contains(t, out, "Latest release: unknown (run 'lmm version --check')")
	assert.Zero(t, hits.Load(), "plain `lmm version` reads the cache only")

	out, _ = runLMM(t, "version", "--check")
	assert.Equal(t, int32(1), hits.Load())
	assert.Contains(t, out, "Latest release: 2.1.0 (checked ")
	assert.Contains(t, out, "Update available: lmm 2.1.0, from https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.1.0")
	assert.Contains(t, out, "Installed: self-managed")
	assert.Contains(t, out, "Automatic checks: on")
}

func TestVersionCommand_UpToDateAndOff(t *testing.T) {
	hits := cliReleaseServer(t, "v2.0.0")
	require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("update_check: false\n"), 0o644))

	out, _ := runLMM(t, "version", "--check")
	assert.Equal(t, int32(1), hits.Load(), "an explicit --check asks even with the automatic check off")
	assert.Contains(t, out, "lmm is up to date.")
	assert.Contains(t, out, "Automatic checks: off (update_check: false)")
}

func TestJSONGolden_Version(t *testing.T) {
	cliReleaseServer(t, "v2.1.0")
	out, _ := runLMM(t, "version", "--check", "--json")
	assertJSONCLIGolden(t, "version_check", out)
}

func TestReleaseNotice_FirstRunThenTheUpdateLine(t *testing.T) {
	hits := cliReleaseServer(t, "v2.1.0")
	armReleaseNotice(t, true)

	notice := afterCommand(t, "game", "list")
	assert.Equal(t, int32(1), hits.Load())
	lines := strings.Split(strings.TrimSpace(notice), "\n")
	require.Len(t, lines, 2, notice)
	assert.Equal(t, "lmm checks GitHub for a new release once a day; to turn this off, set update_check: false in "+
		filepath.Join(configDir, "config.yaml"), lines[0])
	assert.Equal(t, "lmm 2.1.0 is available (you have 2.0.0): https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.1.0", lines[1])

	notice = afterCommand(t, "game", "list")
	assert.Equal(t, int32(1), hits.Load(), "inside the day the cache answers")
	assert.Equal(t, "lmm 2.1.0 is available (you have 2.0.0): https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.1.0\n", notice,
		"the first-run line is shown once; the update line after every interactive command")
}

func TestReleaseNotice_PackageInstallNamesTheUpgradeCommand(t *testing.T) {
	cliReleaseServer(t, "v2.1.0")
	releaseInstallProbe = &core.InstallProbe{
		Executable: func() (string, error) { return "/usr/bin/lmm", nil },
		LookPath:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
		Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			if name == "pacman" {
				return []byte("lmm\n"), nil
			}
			return nil, os.ErrNotExist
		},
	}
	armReleaseNotice(t, true)
	notice := afterCommand(t, "game", "list")
	assert.Contains(t, notice, "lmm 2.1.0 is available (you have 2.0.0); upgrade with: sudo pacman -Syu\n")
}

// TestReleaseNotice_Silent covers every case that must neither print nor
// ask GitHub.
func TestReleaseNotice_Silent(t *testing.T) {
	tests := []struct {
		name  string
		tty   bool
		armed bool
		setup func(t *testing.T)
		args  []string
	}{
		{name: "not armed (every other test in this package)", tty: true, args: []string{"game", "list"}},
		{name: "stderr is not a terminal", armed: true, args: []string{"game", "list"}},
		{name: "--json", tty: true, armed: true, args: []string{"game", "list", "--json"}},
		{name: "lmm version reports for itself", tty: true, armed: true, args: []string{"version"}},
		{name: "update_check: false", tty: true, armed: true, args: []string{"game", "list"}, setup: func(t *testing.T) {
			require.NoError(t, os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte("update_check: false\n"), 0o644))
		}},
		{name: "a dev build", tty: true, armed: true, args: []string{"game", "list"}, setup: func(*testing.T) {
			buildDescribe = "v2.0.0-3-gabc1234"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits := cliReleaseServer(t, "v2.1.0")
			armReleaseNotice(t, tt.tty)
			releaseNoticeArmed = tt.armed
			if tt.setup != nil {
				tt.setup(t)
			}
			assert.Empty(t, afterCommand(t, tt.args...))
			assert.Zero(t, hits.Load())
		})
	}
}

// TestReleaseNotice_NeverForServe: `lmm serve` keeps its own output stream
// clean - its server refreshes the cache and the SPA shows the banner.
func TestReleaseNotice_NeverForServe(t *testing.T) {
	for _, c := range []string{"serve", "version", "completion", "help"} {
		cmd, _, err := rootCmd.Find([]string{c})
		require.NoError(t, err)
		assert.False(t, releaseNoticeWanted(cmd), c)
	}
	cmd, _, err := rootCmd.Find([]string{"game", "list"})
	require.NoError(t, err)
	assert.True(t, releaseNoticeWanted(cmd))
}

// TestReleaseCheck_TheTestBinaryCannotReachGitHub is #568's network rule,
// verified: TestMain guards http.DefaultTransport, so a checker built with
// the PRODUCTION base URL - what the CLI uses whenever a test forgets to
// point it elsewhere - fails before anything is sent. The test installs
// its own guard on top so this deliberate attempt is not the one TestMain
// fails the run on.
func TestReleaseCheck_TheTestBinaryCannotReachGitHub(t *testing.T) {
	assert.Equal(t, core.GitHubAPIBase, releaseAPIBase, "production default")

	prev := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = prev })
	attempts := testutil.GuardGitHub()

	rc := core.NewReleaseChecker(core.ReleaseCheckOptions{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), Version: "2.0.0", APIBase: core.GitHubAPIBase,
		Probe: &core.InstallProbe{
			Executable: func() (string, error) { return "", os.ErrNotExist },
		},
	})
	_, err := rc.Check(context.Background(), true)
	require.ErrorIs(t, err, testutil.ErrGitHubRefused)
	assert.Equal(t, []string{"GET https://api.github.com/repos/DonovanMods/linux-mod-manager/releases/latest"}, attempts())
}
