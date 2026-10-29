package core_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
