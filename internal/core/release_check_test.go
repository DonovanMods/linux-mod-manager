package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// releaseServer is a stand-in for GitHub's "latest release" endpoint that
// counts its requests and answers with body (or status, when non-zero).
type releaseServer struct {
	*httptest.Server
	hits   atomic.Int32
	status int
	body   string
	ua     atomic.Value
}

func newReleaseServer(t *testing.T, body string) *releaseServer {
	t.Helper()
	rs := &releaseServer{body: body}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.hits.Add(1)
		rs.ua.Store(r.Header.Get("User-Agent"))
		if r.URL.Path != "/repos/DonovanMods/linux-mod-manager/releases/latest" {
			http.NotFound(w, r)
			return
		}
		if rs.status != 0 {
			w.WriteHeader(rs.status)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		_, _ = w.Write([]byte(rs.body))
	}))
	t.Cleanup(rs.Close)
	return rs
}

const releaseV210 = `{"tag_name":"v2.1.0","html_url":"https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.1.0","draft":false,"prerelease":false}`

// fakeClock is a settable Now.
type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func newChecker(t *testing.T, rs *releaseServer, clock *fakeClock, mutate func(*core.ReleaseCheckOptions)) (*core.ReleaseChecker, core.ReleaseCheckOptions) {
	t.Helper()
	opts := core.ReleaseCheckOptions{
		ConfigDir: t.TempDir(),
		DataDir:   t.TempDir(),
		Version:   "2.0.1",
		APIBase:   rs.URL,
		Now:       clock.Now,
		Probe: &core.InstallProbe{
			Executable: func() (string, error) { return "/home/u/.local/bin/lmm", nil },
			LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
			Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
		},
	}
	if mutate != nil {
		mutate(&opts)
	}
	return core.NewReleaseChecker(opts), opts
}

func TestReleaseChecker_AtMostOneRequestPerDay(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, _ := newChecker(t, rs, clock, nil)
	ctx := context.Background()

	st, err := rc.Check(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, int32(1), rs.hits.Load())
	assert.Equal(t, "lmm/2.0.1", rs.ua.Load(), "the only identifying thing sent")
	assert.Equal(t, core.ReleaseStatus{
		Current:         "2.0.1",
		Latest:          "2.1.0",
		UpdateAvailable: true,
		ReleaseURL:      "https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.1.0",
		CheckedAt:       clock.now,
		CheckEnabled:    true,
		Install:         core.InstallMethod{Method: core.InstallSelfManaged},
	}, st)

	clock.now = clock.now.Add(23 * time.Hour)
	st, err = rc.Check(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, int32(1), rs.hits.Load(), "a check inside the day answers from the cache")
	assert.True(t, st.UpdateAvailable)

	assert.Equal(t, st, rc.Status(ctx), "Status is the cache, without a request")
	assert.Equal(t, int32(1), rs.hits.Load())

	clock.now = clock.now.Add(time.Hour)
	_, err = rc.Check(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, int32(2), rs.hits.Load(), "a day later it asks again")

	_, err = rc.Check(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, int32(3), rs.hits.Load(), "a forced check always asks")
}

// TestReleaseChecker_TheCacheOutlivesTheChecker: the 24h rule is held by
// the file under the data dir, not by one process's memory - the CLI makes
// a new checker on every run.
func TestReleaseChecker_TheCacheOutlivesTheChecker(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, opts := newChecker(t, rs, clock, nil)
	_, err := rc.Check(context.Background(), false)
	require.NoError(t, err)

	again := core.NewReleaseChecker(opts)
	st, err := again.Check(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, int32(1), rs.hits.Load())
	assert.Equal(t, "2.1.0", st.Latest)
	_, err = os.Stat(filepath.Join(opts.DataDir, "release-check.json"))
	require.NoError(t, err)
}

// TestReleaseChecker_AFailureIsCachedToo: a failed check is recorded, keeps
// what an earlier success learned, and is not retried inside the day.
func TestReleaseChecker_AFailureIsCachedToo(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, _ := newChecker(t, rs, clock, nil)
	ctx := context.Background()
	_, err := rc.Check(ctx, false)
	require.NoError(t, err)

	rs.status = http.StatusForbidden
	clock.now = clock.now.Add(25 * time.Hour)
	st, err := rc.Check(ctx, false)
	require.Error(t, err)
	assert.Equal(t, int32(2), rs.hits.Load())
	assert.Equal(t, "GitHub answered 403 Forbidden", st.CheckError)
	assert.Equal(t, clock.now, st.CheckedAt)
	assert.Equal(t, "2.1.0", st.Latest, "a failure does not forget a known release")
	assert.True(t, st.UpdateAvailable)

	clock.now = clock.now.Add(time.Hour)
	st, err = rc.Check(ctx, false)
	require.NoError(t, err, "inside the day the cached failure is the answer, not a new error")
	assert.Equal(t, int32(2), rs.hits.Load())
	assert.NotEmpty(t, st.CheckError)
}

// TestReleaseChecker_AnUnreachableServerIsAFailureNotAHang: a check against
// a closed port fails, inside ctx's deadline, and is recorded.
func TestReleaseChecker_AnUnreachableServerIsAFailureNotAHang(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	rs.Close()
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, _ := newChecker(t, rs, clock, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	st, err := rc.Check(ctx, false)
	require.Error(t, err)
	assert.NotEmpty(t, st.CheckError)
	assert.Equal(t, clock.now, st.CheckedAt)
}

func TestReleaseChecker_DisabledAsksNothing(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, opts := newChecker(t, rs, clock, nil)
	require.NoError(t, os.WriteFile(filepath.Join(opts.ConfigDir, "config.yaml"), []byte("update_check: false\n"), 0o644))

	assert.False(t, rc.Enabled())
	st, err := rc.Check(context.Background(), false)
	require.NoError(t, err)
	assert.Zero(t, rs.hits.Load())
	assert.False(t, st.CheckEnabled)
	assert.False(t, st.UpdateAvailable)

	_, err = rc.Check(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, int32(1), rs.hits.Load(), "an explicit `lmm version --check` still asks")
}

// TestReleaseChecker_DevBuildsNeverReport: a dev build asks nothing on its
// own, and even a forced check that finds a newer tag reports no update.
func TestReleaseChecker_DevBuildsNeverReport(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, _ := newChecker(t, rs, clock, func(o *core.ReleaseCheckOptions) { o.Dev = true })

	_, err := rc.Check(context.Background(), false)
	require.NoError(t, err)
	assert.Zero(t, rs.hits.Load())

	st, err := rc.Check(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, "2.1.0", st.Latest)
	assert.False(t, st.UpdateAvailable)
	assert.True(t, st.Dev)
	assert.Equal(t, core.InstallMethod{Method: core.InstallDev}, st.Install)
}

func TestReleaseChecker_Comparison(t *testing.T) {
	tests := []struct {
		name, current, body string
		wantLatest          string
		want                bool
	}{
		{"newer minor", "2.0.1", `{"tag_name":"v2.1.0","html_url":"https://github.com/x"}`, "2.1.0", true},
		{"same", "2.0.1", `{"tag_name":"v2.0.1","html_url":"https://github.com/x"}`, "2.0.1", false},
		{"older", "2.1.0", `{"tag_name":"v2.0.9","html_url":"https://github.com/x"}`, "2.0.9", false},
		{"numeric not lexical", "2.0.9", `{"tag_name":"v2.0.10","html_url":"https://github.com/x"}`, "2.0.10", true},
		{"a prerelease build is older than its release", "2.1.0-rc.1", `{"tag_name":"v2.1.0","html_url":"https://github.com/x"}`, "2.1.0", true},
		{"a prerelease flag is skipped", "2.0.1", `{"tag_name":"v2.1.0","html_url":"https://github.com/x","prerelease":true}`, "", false},
		{"a draft is skipped", "2.0.1", `{"tag_name":"v2.1.0","html_url":"https://github.com/x","draft":true}`, "", false},
		{"a prerelease tag is skipped", "2.0.1", `{"tag_name":"v2.1.0-rc.1","html_url":"https://github.com/x"}`, "", false},
		{"a tag that is not semver is skipped", "2.0.1", `{"tag_name":"nightly","html_url":"https://github.com/x"}`, "", false},
		{"an unparseable current version reports nothing", "unknown", `{"tag_name":"v2.1.0","html_url":"https://github.com/x"}`, "2.1.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs := newReleaseServer(t, tt.body)
			clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
			rc, _ := newChecker(t, rs, clock, func(o *core.ReleaseCheckOptions) { o.Version = tt.current })
			st, err := rc.Check(context.Background(), true)
			require.NoError(t, err)
			assert.Equal(t, tt.wantLatest, st.Latest)
			assert.Equal(t, tt.want, st.UpdateAvailable)
		})
	}
}

// TestReleaseChecker_NoReleaseYet: GitHub's 404 for a repository with no
// published release is an answer, not a failure.
func TestReleaseChecker_NoReleaseYet(t *testing.T) {
	rs := newReleaseServer(t, "")
	rs.status = http.StatusNotFound
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, _ := newChecker(t, rs, clock, nil)
	st, err := rc.Check(context.Background(), false)
	require.NoError(t, err)
	assert.Empty(t, st.Latest)
	assert.Empty(t, st.CheckError)
	assert.False(t, st.UpdateAvailable)
}

// TestReleaseChecker_UnsafeReleaseURLIsDropped: the URL becomes a link in
// the SPA and a line in the terminal, so only http(s) survives.
func TestReleaseChecker_UnsafeReleaseURLIsDropped(t *testing.T) {
	rs := newReleaseServer(t, `{"tag_name":"v2.1.0","html_url":"javascript:alert(1)"}`)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, _ := newChecker(t, rs, clock, nil)
	st, err := rc.Check(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, "2.1.0", st.Latest)
	assert.Empty(t, st.ReleaseURL)
}

// TestReleaseChecker_FirstRunNotice is shown once per data dir.
func TestReleaseChecker_FirstRunNotice(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, opts := newChecker(t, rs, clock, nil)
	assert.True(t, rc.FirstRunNoticeDue())
	require.NoError(t, rc.RecordFirstRunNotice())
	assert.False(t, rc.FirstRunNoticeDue())
	assert.False(t, core.NewReleaseChecker(opts).FirstRunNoticeDue())

	_, err := rc.Check(context.Background(), false)
	require.NoError(t, err)
	assert.False(t, rc.FirstRunNoticeDue(), "a check does not forget the notice was shown")
}

// TestReleaseChecker_CorruptCacheIsStale: a cache lmm cannot read is
// treated as no cache, never an error.
func TestReleaseChecker_CorruptCacheIsStale(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, opts := newChecker(t, rs, clock, nil)
	require.NoError(t, os.WriteFile(filepath.Join(opts.DataDir, "release-check.json"), []byte("{nope"), 0o644))
	st, err := rc.Check(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, int32(1), rs.hits.Load())
	assert.Equal(t, "2.1.0", st.Latest)
}

func TestNewReleaseChecker_RequiresAnAPIBase(t *testing.T) {
	assert.Panics(t, func() {
		core.NewReleaseChecker(core.ReleaseCheckOptions{DataDir: t.TempDir(), Version: "2.0.1"})
	}, "no default: a caller that forgets it must not reach GitHub by accident")
	assert.Equal(t, "https://api.github.com", core.GitHubAPIBase)
}

// TestReleaseChecker_RefreshAndPendingUpdateAskNoPackageManager: the CLI's
// after-command path refreshes in the background and reads PendingUpdate,
// and neither runs an owner query unless there is an update to word.
func TestReleaseChecker_RefreshAndPendingUpdateAskNoPackageManager(t *testing.T) {
	rs := newReleaseServer(t, `{"tag_name":"v2.0.1","html_url":"https://github.com/x"}`)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	var probed atomic.Int32
	rc, opts := newChecker(t, rs, clock, func(o *core.ReleaseCheckOptions) {
		o.Probe.Executable = func() (string, error) { probed.Add(1); return "/usr/bin/lmm", nil }
	})
	ctx := context.Background()

	require.NoError(t, rc.Refresh(ctx))
	require.NoError(t, rc.Refresh(ctx))
	assert.Equal(t, int32(1), rs.hits.Load(), "Refresh is the unforced check")
	_, ok := rc.PendingUpdate(ctx)
	assert.False(t, ok, "up to date")
	assert.Zero(t, probed.Load())

	rs.body = releaseV210
	clock.now = clock.now.Add(ReleaseDay)
	require.NoError(t, core.NewReleaseChecker(opts).Refresh(ctx))
	st, ok := rc.PendingUpdate(ctx)
	require.True(t, ok)
	assert.Equal(t, "2.1.0", st.Latest)
	assert.Equal(t, int32(1), probed.Load(), "the install method is asked once there is an update to word")
}

// ReleaseDay is one ReleaseCheckInterval, for readability above.
const ReleaseDay = core.ReleaseCheckInterval

// hangingReleaseServer answers nothing until the test ends, so a check
// against it is abandoned rather than answered.
func hangingReleaseServer(t *testing.T) *releaseServer {
	t.Helper()
	stop := make(chan struct{})
	rs := &releaseServer{}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.hits.Add(1)
		select {
		case <-stop:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(stop); rs.Close() })
	return rs
}

// TestReleaseChecker_AnAbandonedCheckHoldsOffOnlyBriefly: a check whose
// caller gave up before the answer (a CLI command that exited, Ctrl-C) is
// not a day's answer. It holds off another attempt for a few minutes, so
// a burst of commands does not each ask, but not for 24 hours, and it is
// not reported as a failure.
func TestReleaseChecker_AnAbandonedCheckHoldsOffOnlyBriefly(t *testing.T) {
	rs := hangingReleaseServer(t)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, opts := newChecker(t, rs, clock, nil)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		require.Eventually(t, func() bool { return rs.hits.Load() == 1 }, 5*time.Second, 5*time.Millisecond)
		cancel()
	}()
	require.Error(t, rc.Refresh(ctx))

	st := rc.Status(context.Background())
	assert.Empty(t, st.CheckError, "an abandoned check is not a failed one")
	assert.True(t, st.CheckedAt.IsZero(), "nor an answer")

	answered := newReleaseServer(t, releaseV210)
	rc2 := core.NewReleaseChecker(func() core.ReleaseCheckOptions { o := opts; o.APIBase = answered.URL; return o }())
	clock.now = clock.now.Add(time.Minute)
	require.NoError(t, rc2.Refresh(context.Background()))
	assert.Zero(t, answered.hits.Load(), "a minute later the attempt still holds off another")

	clock.now = clock.now.Add(15 * time.Minute)
	require.NoError(t, rc2.Refresh(context.Background()))
	assert.Equal(t, int32(1), answered.hits.Load(), "minutes later, not a day later, it asks again")
	assert.Equal(t, "2.1.0", rc2.Status(context.Background()).Latest)
}

// TestReleaseChecker_ASkippedLatestKeepsTheKnownRelease: a "latest" that
// is a prerelease or not a version this check compares says nothing about
// the stable release lmm already knows, so it does not erase it.
func TestReleaseChecker_ASkippedLatestKeepsTheKnownRelease(t *testing.T) {
	for _, body := range []string{
		`{"tag_name":"v2.2.0-rc.1","html_url":"https://github.com/x/rc"}`,
		`{"tag_name":"v2.2.0","html_url":"https://github.com/x/rc","prerelease":true}`,
		`{"tag_name":"nightly","html_url":"https://github.com/x/nightly"}`,
	} {
		t.Run(body, func(t *testing.T) {
			rs := newReleaseServer(t, releaseV210)
			clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
			rc, _ := newChecker(t, rs, clock, nil)
			_, err := rc.Check(context.Background(), true)
			require.NoError(t, err)

			rs.body = body
			clock.now = clock.now.Add(time.Hour)
			st, err := rc.Check(context.Background(), true)
			require.NoError(t, err)
			assert.Equal(t, "2.1.0", st.Latest)
			assert.Equal(t, "https://github.com/DonovanMods/linux-mod-manager/releases/tag/v2.1.0", st.ReleaseURL)
			assert.Equal(t, clock.now, st.CheckedAt, "it is still an answer")
			assert.Empty(t, st.CheckError)
		})
	}
}

// TestReleaseChecker_ASaveKeepsAnotherProcessesWrite: two processes share
// the cache file (a CLI run beside `lmm serve`), so a save merges the
// fields it owns into the file as it is now, never a copy read earlier.
func TestReleaseChecker_ASaveKeepsAnotherProcessesWrite(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	var other *core.ReleaseChecker
	var once sync.Once
	rc, opts := newChecker(t, rs, clock, func(o *core.ReleaseCheckOptions) {
		// The clock is read after the checker loads the cache and before
		// it writes it: the other process records its notice in between.
		o.Now = func() time.Time {
			once.Do(func() { require.NoError(t, other.RecordFirstRunNotice()) })
			return clock.now
		}
	})
	other = core.NewReleaseChecker(opts)

	require.NoError(t, rc.Refresh(context.Background()))
	assert.False(t, rc.FirstRunNoticeDue(), "the other process's notice survived this one's save")
	assert.Equal(t, "2.1.0", rc.Status(context.Background()).Latest)
}

// TestReleaseChecker_AnUnsafeCachedURLIsDropped: the cache file is the
// user's to edit (or damage); a URL in it is held to http(s) on the way out
// too, so it can never become a javascript: link in the web UI.
func TestReleaseChecker_AnUnsafeCachedURLIsDropped(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	rc, opts := newChecker(t, rs, clock, nil)
	require.NoError(t, os.WriteFile(filepath.Join(opts.DataDir, "release-check.json"),
		[]byte(`{"checked_at":"2026-10-01T11:00:00Z","latest":"2.1.0","url":"javascript:alert(1)"}`), 0o644))
	st := rc.Status(context.Background())
	assert.Equal(t, "2.1.0", st.Latest)
	assert.Empty(t, st.ReleaseURL)
}

// TestReleaseChecker_TheInstallMethodIsCachedPerBinary: detection runs
// package-manager subprocesses, so its answer is cached beside the release
// answer, keyed by the binary's path and modification time - a later
// process asks nothing, and a replaced binary is asked about afresh.
func TestReleaseChecker_TheInstallMethodIsCachedPerBinary(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	exe := filepath.Join(t.TempDir(), "lmm")
	require.NoError(t, os.WriteFile(exe, []byte("binary"), 0o755))
	var runs atomic.Int32
	_, opts := newChecker(t, rs, clock, func(o *core.ReleaseCheckOptions) {
		o.Probe = &core.InstallProbe{
			Executable: func() (string, error) { return exe, nil },
			LookPath:   func(name string) (string, error) { return "/usr/bin/" + name, nil },
			Run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
				runs.Add(1)
				if name == "pacman" {
					return []byte("lmm\n"), nil
				}
				return nil, os.ErrNotExist
			},
		}
	})
	want := core.InstallMethod{Method: core.InstallPackage, Manager: "pacman", Package: "lmm", UpgradeCommand: "sudo pacman -Syu"}

	assert.Equal(t, want, core.NewReleaseChecker(opts).Status(context.Background()).Install)
	asked := runs.Load()
	require.NotZero(t, asked)

	assert.Equal(t, want, core.NewReleaseChecker(opts).Status(context.Background()).Install)
	assert.Equal(t, asked, runs.Load(), "a later process reads the cached answer")

	later := time.Now().Add(time.Hour)
	require.NoError(t, os.Chtimes(exe, later, later))
	core.NewReleaseChecker(opts).Status(context.Background())
	assert.Greater(t, runs.Load(), asked, "a replaced binary is asked about again")
}

// TestReleaseChecker_AnUnfinishedDetectionFallsBack: detection that runs
// out of time answers self-managed - the release page, which is never the
// wrong place to send someone - and is neither remembered nor cached, so
// the next caller with time to spare asks properly.
func TestReleaseChecker_AnUnfinishedDetectionFallsBack(t *testing.T) {
	rs := newReleaseServer(t, releaseV210)
	clock := &fakeClock{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	var slow atomic.Bool
	slow.Store(true)
	rc, opts := newChecker(t, rs, clock, func(o *core.ReleaseCheckOptions) {
		o.Probe.LookPath = func(name string) (string, error) { return "/usr/bin/" + name, nil }
		o.Probe.Run = func(ctx context.Context, name string, _ ...string) ([]byte, error) {
			if slow.Load() {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(10 * time.Second):
				}
			}
			if name == "pacman" {
				return []byte("lmm\n"), nil
			}
			return nil, os.ErrNotExist
		}
	})
	require.NoError(t, os.WriteFile(filepath.Join(opts.DataDir, "release-check.json"),
		[]byte(`{"checked_at":"2026-10-01T11:00:00Z","latest":"2.1.0","url":"https://github.com/x"}`), 0o644))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	st, ok := rc.PendingUpdate(ctx)
	require.True(t, ok)
	assert.Less(t, time.Since(start), 2*time.Second, "bounded by the caller's context")
	assert.Equal(t, core.InstallMethod{Method: core.InstallSelfManaged}, st.Install)

	slow.Store(false)
	st, ok = rc.PendingUpdate(context.Background())
	require.True(t, ok)
	assert.Equal(t, "pacman", st.Install.Manager, "the fallback was not remembered")
}
