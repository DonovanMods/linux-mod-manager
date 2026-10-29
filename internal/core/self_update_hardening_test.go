package core_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// self_update_hardening_test.go - the security review's findings on #569's
// self-update: an install method lmm could not settle, a release tag that
// is not a plain vX.Y.Z, two runs at once, and a download redirected off
// GitHub.

// A package database that is locked or timed out says nothing about who
// owns the binary. Reading that as self-managed would let `sudo lmm
// self-update`, run mid-upgrade, overwrite a package's /usr/bin/lmm: the
// update is refused instead, before anything is downloaded.
func TestSelfUpdate_RefusesAnInstallMethodItCouldNotDetermine(t *testing.T) {
	f := newSelfUpdateFixture(t)
	f.opts.Probe.LookPath = func(name string) (string, error) {
		if name == "dpkg" {
			return "/usr/bin/dpkg", nil
		}
		return "", os.ErrNotExist
	}
	f.opts.Probe.Run = func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("dpkg: database is locked")
	}
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	_, err := f.updater(rel.serve(t)).Plan(context.Background())
	var unknown *core.InstallMethodUnknownError
	require.ErrorAs(t, err, &unknown)
	assert.Equal(t, f.exe, unknown.Executable)
	assert.Contains(t, err.Error(), "could not determine whether a package manager owns lmm")
	assert.Contains(t, err.Error(), "try again")
	assert.Empty(t, rel.requests, "nothing is asked of GitHub")
}

// CheckSelfUpdateInstall is the one gate Plan and `lmm service install
// --auto-update` both ask.
func TestCheckSelfUpdateInstall(t *testing.T) {
	exe := "/usr/bin/lmm"
	probe := func(run func(context.Context, string, ...string) ([]byte, error)) core.InstallProbe {
		return core.InstallProbe{
			Executable: func() (string, error) { return exe, nil },
			LookPath: func(name string) (string, error) {
				if name == "pacman" {
					return "/usr/bin/pacman", nil
				}
				return "", os.ErrNotExist
			},
			Run: run,
		}
	}
	ctx := context.Background()

	err := core.CheckSelfUpdateInstall(ctx, probe(func(context.Context, string, ...string) ([]byte, error) {
		return nil, exitCode(1)
	}), false)
	require.NoError(t, err, "every manager said it does not own the file: self-managed")

	err = core.CheckSelfUpdateInstall(ctx, probe(func(context.Context, string, ...string) ([]byte, error) {
		return []byte("lmm-bin\n"), nil
	}), false)
	var pkg *core.PackageManagedError
	require.ErrorAs(t, err, &pkg)

	err = core.CheckSelfUpdateInstall(ctx, probe(func(context.Context, string, ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	}), false)
	var unknown *core.InstallMethodUnknownError
	require.ErrorAs(t, err, &unknown, "a timed-out query")

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err = core.CheckSelfUpdateInstall(cancelled, probe(func(context.Context, string, ...string) ([]byte, error) {
		return nil, exitCode(1)
	}), false)
	require.ErrorAs(t, err, &unknown, "an answer the caller's context cut short")

	err = core.CheckSelfUpdateInstall(ctx, probe(nil), true)
	require.ErrorIs(t, err, core.ErrDevBuild, "a dev build asks nothing")
}

// exitCode is an error carrying a process exit status, as *exec.ExitError
// does.
type exitCode int

func (e exitCode) Error() string { return "exit status" }
func (e exitCode) ExitCode() int { return int(e) }

// latestTagServer answers the latest-release document with tag, and
// records every other request.
func latestTagServer(t *testing.T, tag string) (*httptest.Server, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var other []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path == "/repos/DonovanMods/linux-mod-manager/releases/latest" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tag_name": tag,
				"html_url": "https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.2.0",
			})
			return
		}
		mu.Lock()
		other = append(other, req.URL.Path)
		mu.Unlock()
		http.NotFound(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv, &other
}

// The release's tag_name reaches a download URL, a staging path and the
// terminal, so anything but a plain vX.Y.Z is refused before it is used:
// build metadata, a prerelease, a missing "v", a path, a control byte.
func TestSelfUpdate_RefusesATagThatIsNotAPlainRelease(t *testing.T) {
	for _, tag := range []string{
		"v2.2.0+build.1",
		"v2.2.0-rc.1",
		"2.2.0",
		"V2.2.0",
		"v2.2",
		"v2.2.0.1",
		"v2.2.0/../../evil",
		"v2.2.0\x1b[2J",
		" v2.2.0",
		"v２.2.0",
	} {
		t.Run(tag, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			srv, other := latestTagServer(t, tag)
			_, err := f.updater(srv).Plan(context.Background())
			var tagErr *core.ReleaseTagError
			require.ErrorAs(t, err, &tagErr)
			assert.Equal(t, tag, tagErr.Tag)
			assert.NotContains(t, err.Error(), "\x1b", "the tag is quoted, never printed raw")
			assert.Empty(t, *other, "nothing is downloaded")
		})
	}
}

// Apply checks the version again: a plan is a value a caller can build.
func TestSelfUpdate_ApplyRefusesAPlanWithABadVersion(t *testing.T) {
	f := newSelfUpdateFixture(t)
	srv, other := latestTagServer(t, "v2.2.0")
	u := f.updater(srv)
	_, err := u.Apply(context.Background(), core.SelfUpdatePlan{
		Current:    "2.1.0",
		Latest:     "2.2.0/../../x",
		Executable: f.exe,
		Asset:      "lmm_2.2.0_linux_amd64.tar.gz",
	})
	var tagErr *core.ReleaseTagError
	require.ErrorAs(t, err, &tagErr)
	assert.Empty(t, *other)
}

// Two self-updates on one data directory - the timer and a person, say -
// never run at once: the second is told the first is running.
func TestSelfUpdate_LockIsExclusive(t *testing.T) {
	f := newSelfUpdateFixture(t)
	first := core.NewSelfUpdater(withBases(f.opts))
	second := core.NewSelfUpdater(withBases(f.opts))

	unlock, err := first.Lock()
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(f.opts.DataDir, "self-update.lock"))

	_, err = second.Lock()
	require.ErrorIs(t, err, core.ErrSelfUpdateRunning)
	assert.Contains(t, err.Error(), "another self-update is running")

	unlock()
	unlock() // idempotent
	unlock2, err := second.Lock()
	require.NoError(t, err, "free once the first lets go")
	unlock2()
}

// A download GitHub redirects anywhere but GitHub over https - here an
// http host - is refused, and the binary is left alone.
func TestSelfUpdate_RefusesARedirectOffGitHub(t *testing.T) {
	f := newSelfUpdateFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	elsewhere := rel.serve(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/repos/") {
			elsewhere.Config.Handler.ServeHTTP(w, req)
			return
		}
		http.Redirect(w, req, elsewhere.URL+req.URL.Path, http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	u := f.updater(srv)
	plan, err := u.Plan(context.Background())
	require.NoError(t, err)
	_, err = u.Apply(context.Background(), plan)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "redirect")
	data, rerr := os.ReadFile(f.exe)
	require.NoError(t, rerr)
	assert.Equal(t, "old lmm", string(data))
	for _, p := range rel.requests {
		assert.NotContains(t, p, "/releases/download/", "the redirect was never followed")
	}
}
