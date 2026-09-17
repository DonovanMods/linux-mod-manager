package core

// #436 on the download path: a Thunderstore package (or any other file)
// that is throttled or stalls mid-transfer. Package-INTERNAL for the same
// reason as thunderstore's: the retry sleep and the stall timer are clocks
// production never changes, so they are fields rather than options.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stallWindow struct {
	mu     sync.Mutex
	f      func()
	armed  bool
	resets chan struct{}
}

func (w *stallWindow) Reset(time.Duration) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	was := w.armed
	w.armed = true
	select {
	case w.resets <- struct{}{}:
	default:
	}
	return was
}

func (w *stallWindow) Stop() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	was := w.armed
	w.armed = false
	return was
}

func (w *stallWindow) fire() {
	w.mu.Lock()
	armed, f := w.armed, w.f
	w.armed = false
	w.mu.Unlock()
	if armed {
		f()
	}
}

// testDownloader is NewDownloader(nil) with its sleeps recorded and its
// stall windows handed to the test.
func testDownloader(t *testing.T) (*Downloader, *[]time.Duration, chan *stallWindow) {
	t.Helper()
	d := NewDownloader(nil)
	var waits []time.Duration
	d.sleep = func(ctx context.Context, w time.Duration) error {
		waits = append(waits, w)
		return ctx.Err()
	}
	windows := make(chan *stallWindow, 16)
	idle, ok := d.httpClient.Transport.(*httpclient.IdleTimeout)
	require.True(t, ok, "the default download client carries the stall guard: %T", d.httpClient.Transport)
	assert.Equal(t, downloadStallTimeout, idle.Timeout)
	idle.AfterFunc = func(_ time.Duration, f func()) httpclient.Timer {
		w := &stallWindow{f: f, armed: true, resets: make(chan struct{}, 256)}
		windows <- w
		return w
	}
	return d, &waits, windows
}

func noticesInto(t *testing.T) (context.Context, func() []source.Notice) {
	t.Helper()
	var mu sync.Mutex
	var got []source.Notice
	ctx := source.WithNotices(t.Context(), func(n source.Notice) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, n)
	})
	return ctx, func() []source.Notice {
		mu.Lock()
		defer mu.Unlock()
		return append([]source.Notice(nil), got...)
	}
}

// TestDownloader_AThrottledDownloadWaitsWhatItWasToldAndSaysSo: the
// server's Retry-After is the floor of the wait, and the wait is announced
// before it is spent.
func TestDownloader_AThrottledDownloadWaitsWhatItWasToldAndSaysSo(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("zipbytes"))
	}))
	defer srv.Close()

	d, waits, _ := testDownloader(t)
	ctx, notices := noticesInto(t)
	dest := filepath.Join(t.TempDir(), "mod.zip")
	_, err := d.Download(ctx, srv.URL+"/package/download/A/B/1.0.0/", dest, nil)
	require.NoError(t, err)

	require.Len(t, *waits, 1)
	assert.GreaterOrEqual(t, (*waits)[0], 7*time.Second)
	got := notices()
	require.Len(t, got, 1)
	host, _ := url.Parse(srv.URL)
	assert.Equal(t, host.Host, got[0].Source)
	assert.True(t, got[0].Download)
	assert.Equal(t, source.RetryRateLimited, got[0].Reason)
	assert.Equal(t, 2, got[0].Attempt)
	assert.Equal(t, (*waits)[0], got[0].Wait)
}

// TestRetryAfterOf_ClampsBeforeItOverflows: an absurd delay is a very long
// wait, never a wrapped negative one that reads as no wait at all.
func TestRetryAfterOf_ClampsBeforeItOverflows(t *testing.T) {
	// Exact values (T3 review P4 B6/B11): "positive and large" also held
	// for the wrapped value this clamp exists to prevent.
	now := time.Now()
	for _, v := range []string{"99999999999999", "99999999999999999999", "9223372036854775807", "Mon, 01 Jan 2300 00:00:00 GMT"} {
		assert.Equal(t, downloadRetryAfterCeiling, retryAfterOf(v, now), "Retry-After: %s", v)
	}
	assert.Equal(t, 90*time.Second, retryAfterOf("90", now))
}

// TestDownloader_ARetryAfterPastTheCeilingFailsNow: a download is not held
// silent for ten minutes because a CDN asked for it.
func TestDownloader_ARetryAfterPastTheCeilingFailsNow(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	d, waits, _ := testDownloader(t)
	_, err := d.Download(t.Context(), srv.URL, filepath.Join(t.TempDir(), "f"), nil)
	require.Error(t, err)
	assert.Empty(t, *waits)
	assert.Equal(t, 1, calls)
	assert.Contains(t, err.Error(), "10m0s")
}

// TestDownloader_AStalledBodyIsRetried: a body that stops arriving fails its
// attempt once the stall window passes, and - being transient - is tried
// again, with a notice saying so.
func TestDownloader_AStalledBodyIsRetried(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Content-Length", "100")
			_, _ = w.Write([]byte("partial"))
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
			case <-release:
			}
			return
		}
		_, _ = w.Write([]byte("complete"))
	}))
	defer srv.Close()

	d, waits, windows := testDownloader(t)
	ctx, notices := noticesInto(t)
	dest := filepath.Join(t.TempDir(), "mod.zip")

	done := make(chan error, 1)
	go func() {
		_, err := d.Download(ctx, srv.URL, dest, nil)
		done <- err
	}()
	first := <-windows
	<-first.resets // headers
	<-first.resets // the partial body
	first.fire()

	require.NoError(t, <-done)
	data, err := os.ReadFile(dest)
	require.NoError(t, err)
	assert.Equal(t, "complete", string(data))
	assert.Len(t, *waits, 1)
	got := notices()
	require.Len(t, got, 1)
	assert.Equal(t, source.RetryStalled, got[0].Reason, "a stall reached the host: it is not a network failure")
	assert.ErrorIs(t, got[0].Err, httpclient.ErrStalled)
}

// TestDownloader_AStatusIsNamedOnce: "HTTP error: 429 429 Too Many
// Requests" printed the code twice, because resp.Status already carries it
// (T3 review F12).
func TestDownloader_AStatusIsNamedOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	d, _, _ := testDownloader(t)
	_, err := d.Download(t.Context(), srv.URL, filepath.Join(t.TempDir(), "f"), nil)
	require.Error(t, err)
	assert.Equal(t, "HTTP error: 404 Not Found", err.Error())
}
