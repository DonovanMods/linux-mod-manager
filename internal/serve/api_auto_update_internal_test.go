package serve

// #586: `lmm serve` installs a newer lmm by itself when auto_update is on
// and the install is self-managed - on its refresh goroutine, through an
// AutoUpdater this test fakes, so nothing is downloaded or replaced - and
// Setup > Updates' switch writes auto_update through PUT
// /api/v1/settings/auto-update.

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAutoUpdater records the runs a server asks of it.
type fakeAutoUpdater struct {
	mu      sync.Mutex
	due     bool
	asked   []string
	runs    int
	outcome core.AutoUpdateOutcome
	err     error
	// run, when set, is the run itself, outside mu.
	run func(context.Context, core.AutoRunOptions) (core.AutoUpdateOutcome, error)
}

func (f *fakeAutoUpdater) AutoUpdateDue(latest string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, latest)
	return f.due
}

func (f *fakeAutoUpdater) RunAuto(ctx context.Context, opts core.AutoRunOptions) (core.AutoUpdateOutcome, error) {
	f.mu.Lock()
	f.runs++
	run, outcome, err := f.run, f.outcome, f.err
	f.mu.Unlock()
	if run != nil {
		return run(ctx, opts)
	}
	return outcome, err
}

func (f *fakeAutoUpdater) counts() (asked, runs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asked), f.runs
}

// selfManagedProbe owns nothing: a self-managed lmm.
func selfManagedProbe() *core.InstallProbe {
	return &core.InstallProbe{
		Executable: func() (string, error) { return "/home/u/.local/bin/lmm", nil },
		LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
		Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
	}
}

// releaseChecker answers latest from an httptest GitHub, running 2.0.1.
func releaseChecker(t *testing.T, latest string, probe *core.InstallProbe) *core.ReleaseChecker {
	t.Helper()
	return releaseCheckerIn(t, t.TempDir(), latest, probe)
}

// releaseCheckerIn is releaseChecker reading configDir's config.yaml, as
// production's checker reads the service's.
func releaseCheckerIn(t *testing.T, configDir, latest string, probe *core.InstallProbe) *core.ReleaseChecker {
	t.Helper()
	return releaseCheckerAt(t, configDir, t.TempDir(), latest, probe)
}

// releaseCheckerAt is releaseCheckerIn keeping its state in dataDir.
func releaseCheckerAt(t *testing.T, configDir, dataDir, latest string, probe *core.InstallProbe) *core.ReleaseChecker {
	t.Helper()
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v` + latest + `","html_url":"https://github.com/DonovanMods/linux-mod-manager/releases/tag/v` + latest + `"}`))
	}))
	t.Cleanup(gh.Close)
	return core.NewReleaseChecker(core.ReleaseCheckOptions{
		ConfigDir: configDir, DataDir: dataDir, Version: "2.0.1", APIBase: gh.URL, Probe: probe,
	})
}

// serveUntil runs a server with rc and au until cond holds, then stops it.
func serveUntil(t *testing.T, rc *core.ReleaseChecker, au AutoUpdater, notices io.Writer, cond func() bool) {
	t.Helper()
	svc, _ := newDeployFixtureService(t)
	ctx, cancel := context.WithCancel(t.Context())
	srv := New(ctx, svc, slog.New(slog.DiscardHandler), Options{Addr: "127.0.0.1:0", ReleaseChecker: rc, AutoUpdater: au, Notices: notices})
	_, err := srv.Listen()
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	require.Eventually(t, cond, 5*time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}

func TestServe_RunsADueAutomaticUpdateAfterTheRefresh(t *testing.T) {
	rc := releaseChecker(t, "2.1.0", selfManagedProbe())
	au := &fakeAutoUpdater{due: true, outcome: core.AutoUpdateOutcome{Result: core.SelfUpdateResult{
		Updated: true, Previous: "2.0.1", Version: "2.1.0", ServeRestarted: true,
	}}}
	var notices bytes.Buffer
	serveUntil(t, rc, au, &notices, func() bool { _, runs := au.counts(); return runs == 1 })
	assert.Equal(t, []string{"2.1.0"}, au.asked, "asked about the release the refresh found")
	assert.Contains(t, notices.String(), "lmm updated itself to v2.1.0.")
}

func TestServe_NoAutomaticUpdateWhenNotDueOrNotApplicable(t *testing.T) {
	t.Run("not due", func(t *testing.T) {
		rc := releaseChecker(t, "2.1.0", selfManagedProbe())
		au := &fakeAutoUpdater{}
		serveUntil(t, rc, au, nil, func() bool { asked, _ := au.counts(); return asked == 1 })
		_, runs := au.counts()
		assert.Zero(t, runs)
	})
	t.Run("up to date", func(t *testing.T) {
		rc := releaseChecker(t, "2.0.1", selfManagedProbe())
		au := &fakeAutoUpdater{due: true}
		serveUntil(t, rc, au, nil, func() bool { return !rc.Status(t.Context()).CheckedAt.IsZero() })
		asked, runs := au.counts()
		assert.Zero(t, asked)
		assert.Zero(t, runs)
	})
	t.Run("a package install", func(t *testing.T) {
		rc := releaseChecker(t, "2.1.0", &core.InstallProbe{
			Executable: func() (string, error) { return "/usr/bin/lmm", nil },
			LookPath:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
			Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
				if name == "pacman" {
					return []byte("lmm\n"), nil
				}
				return nil, os.ErrNotExist
			},
		})
		au := &fakeAutoUpdater{due: true}
		serveUntil(t, rc, au, nil, func() bool { return !rc.Status(t.Context()).CheckedAt.IsZero() })
		// The refresh and the update decision run on one goroutine, in that
		// order, and the status read has to wait on detection the update
		// decision shares - so the decision is made by now.
		asked, runs := au.counts()
		assert.Zero(t, asked)
		assert.Zero(t, runs)
	})
}

func TestAPIAutoUpdateSetting_WritesTheConfigAndAnswersTheStatus(t *testing.T) {
	svc, _ := newDeployFixtureService(t)
	rc := releaseCheckerIn(t, svc.ConfigDir(), "2.1.0", selfManagedProbe())
	srv := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr, ReleaseChecker: rc})

	rec := doAPI(srv, http.MethodGet, "/api/v1/updates/lmm", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var st core.ReleaseStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st, json.RejectUnknownMembers(true)))
	require.NotNil(t, st.AutoUpdate)
	assert.True(t, st.AutoUpdate.Enabled, "on by default")
	assert.True(t, st.AutoUpdate.Applicable)

	rec = doAPI(srv, http.MethodPut, "/api/v1/settings/auto-update", `{"enabled":false}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st, json.RejectUnknownMembers(true)))
	require.NotNil(t, st.AutoUpdate)
	assert.False(t, st.AutoUpdate.Enabled, "the answer is the status after the write")
	data, err := os.ReadFile(filepath.Join(svc.ConfigDir(), "config.yaml"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "auto_update: false")

	rec = doAPI(srv, http.MethodPut, "/api/v1/settings/auto-update", `{"enabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &st, json.RejectUnknownMembers(true)))
	assert.True(t, st.AutoUpdate.Enabled)
}

func TestAPIAutoUpdateSetting_RefusesABadBodyAndAMissingToken(t *testing.T) {
	svc, _ := newDeployFixtureService(t)
	srv := New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr})
	for _, body := range []string{`{}`, `{"enabled":"no"}`, `{"enabled":false,"extra":1}`, ``} {
		rec := doAPI(srv, http.MethodPut, "/api/v1/settings/auto-update", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "body %q: %s", body, rec.Body.String())
	}
	rec := doAPIWithoutCSRF(srv, http.MethodPut, "/api/v1/settings/auto-update", `{"enabled":false}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	_, err := os.Stat(filepath.Join(svc.ConfigDir(), "config.yaml"))
	if err == nil {
		data, _ := os.ReadFile(filepath.Join(svc.ConfigDir(), "config.yaml"))
		assert.NotContains(t, string(data), "auto_update")
	}
}
