package main

// A stalled transfer over HTTP/2 is a FAILURE, not the user's Ctrl-C (T3
// review F1).
//
// thunderstore.io speaks HTTP/2, and Go's HTTP/2 transport reports the
// cancellation lmm's stall guard makes as a bare context.Canceled. Before
// the fix that put context.Canceled in the error chain, so every command
// that stalled printed "Cancelled.", exited 2, and under --json printed no
// document at all. These tests drive the real Thunderstore source and the
// real downloader against a local HTTP/2 server - nothing leaves the
// machine - and check the three things a caller sees: the exit code, the
// stderr text and the --json document.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stallArchive is the package the stand-in serves.
func stallArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	fw, err := w.Create("plugins/ShipLoot.dll")
	require.NoError(t, err)
	_, err = fw.Write(bytes.Repeat([]byte("assembly bytes "), 400))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	return buf.Bytes()
}

// stallListing is a one-package community document.
func stallListing(size int) []byte {
	return fmt.Appendf(nil, `[{"name":"ShipLoot","full_name":"tinyhoot-ShipLoot","owner":"tinyhoot",`+
		`"date_updated":"2026-09-05T10:00:00.000000Z","is_deprecated":false,"categories":["Mods"],`+
		`"versions":[{"name":"ShipLoot","full_name":"tinyhoot-ShipLoot-1.1.0","description":"Shows the scrap value.",`+
		`"version_number":"1.1.0","dependencies":["BepInEx-BepInExPack-5.4.2100"],`+
		`"date_created":"2026-09-05T10:00:00.000000Z","website_url":"","file_size":%d}]}]`, size)
}

// newHTTP2StandIn starts an HTTP/2 server over TLS - the protocol the real
// host speaks - and a Service whose Thunderstore source and downloader
// reach only it, each with a stall window of window.
func newHTTP2StandIn(t *testing.T, window time.Duration, handler http.Handler) (*core.Service, *domain.Game) {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	require.True(t, srv.Client().Transport.(*http.Transport).ForceAttemptHTTP2, "the stand-in must negotiate HTTP/2")

	configDir = t.TempDir()
	dataDir = t.TempDir()
	cacheDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: cacheDir,
		DownloadClient: &http.Client{Transport: &httpclient.IdleTimeout{Base: srv.Client().Transport, Timeout: window}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(thunderstore.New(thunderstore.Options{
		HTTPClient: srv.Client(), BaseURL: srv.URL, CacheDir: cacheDir, StallTimeout: window,
	}))
	app.RegisterAdapters(svc)

	root := t.TempDir()
	game := &domain.Game{
		ID: "lethal", Name: "Lethal Company", InstallPath: root, ModPath: root,
		LinkMethod: domain.LinkSymlink,
		Loader:     &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Version: "5.4.2100"},
		SourceIDs:  map[string]string{"thunderstore": "lethal-company"},
	}
	require.NoError(t, svc.SaveGame(t.Context(), game))
	return svc, game
}

// hold blocks a handler until the client goes away or the test ends.
func hold(t *testing.T, r *http.Request) {
	select {
	case <-r.Context().Done():
	case <-t.Context().Done():
	}
}

// runAsExecute runs fn the way Execute does under --json - the command,
// then reportError for its failure - and returns what reached stdout and
// stderr, and the error.
func runAsExecute(t *testing.T, fn func(ctx context.Context) error) (stdout, stderr string, err error) {
	t.Helper()
	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	stdout, stderr, _ = captureStdoutAndStderr(t, func() error {
		err = fn(withSourceNotices(t.Context()))
		if err != nil {
			reportError(err)
		}
		return nil
	})
	return stdout, stderr, err
}

// assertStallFailure is what every stalled command must look like.
func assertStallFailure(t *testing.T, stdout string, err error) {
	t.Helper()
	require.Error(t, err)
	assert.False(t, errors.Is(err, context.Canceled), "a stall is not a cancellation: %v", err)
	assert.Equal(t, exitError, exitCodeFor(err), "a stall exits 1, not 2 (\"Cancelled.\"): %v", err)
	assert.Equal(t, exitError, exitCodeIn(t.Context(), err))

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(stdout), &doc), "--json stdout is one error document: %q", stdout)
	msg, _ := doc["error"].(string)
	assert.Contains(t, msg, "stalled", "the document names the stall")
	assert.NotContains(t, msg, "context canceled")

	origJSON := jsonOutput
	jsonOutput = false
	defer func() { jsonOutput = origJSON }()
	plain := captureStderr(t, func() { reportError(err) })
	assert.Contains(t, plain, "Error: ")
	assert.Contains(t, plain, "stalled")
	assert.NotContains(t, plain, "Cancelled.")
}

func TestHTTP2Stall_SearchWhoseHeadersNeverArriveFails(t *testing.T) {
	svc, game := newHTTP2StandIn(t, 300*time.Millisecond, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		hold(t, r)
	}))
	withSearchFlags(t, "thunderstore", 10)

	stdout, stderr, err := runAsExecute(t, func(ctx context.Context) error {
		return doSearch(ctx, svc, game, []string{"shiploot"})
	})
	assertStallFailure(t, stdout, err)
	assert.Equal(t, 2, strings.Count(stderr, "The transfer from Thunderstore stalled; retrying in"),
		"each retry says the host was reached and went quiet: %q", stderr)
	assert.NotContains(t, stderr, "Could not reach")
}

func TestHTTP2Stall_SearchWhoseBodyStopsFails(t *testing.T) {
	svc, game := newHTTP2StandIn(t, 300*time.Millisecond, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(stallListing(1000)[:40])
		w.(http.Flusher).Flush()
		hold(t, r)
	}))
	withSearchFlags(t, "thunderstore", 10)

	stdout, stderr, err := runAsExecute(t, func(ctx context.Context) error {
		return doSearch(ctx, svc, game, []string{"shiploot"})
	})
	assertStallFailure(t, stdout, err)
	assert.Contains(t, stderr, "Building the Thunderstore index for lethal-company")
}

func TestHTTP2Stall_ARefreshWhoseBodyStopsFails(t *testing.T) {
	svc, game := newHTTP2StandIn(t, 300*time.Millisecond, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(stallListing(1000)[:40])
		w.(http.Flusher).Flush()
		hold(t, r)
	}))

	stdout, _, err := runAsExecute(t, func(ctx context.Context) error {
		return doSourceIndex(ctx, svc, game, "thunderstore", true)
	})
	assertStallFailure(t, stdout, err)
}

// TestHTTP2Stall_AGapUnderTheWindowCompletes: pauses shorter than the window,
// adding up to far more than it, are a slow transfer - never a stall.
func TestHTTP2Stall_AGapUnderTheWindowCompletes(t *testing.T) {
	const window, gap, chunks = time.Second, 200 * time.Millisecond, 8
	listing := stallListing(1000)
	svc, game := newHTTP2StandIn(t, window, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		step := len(listing)/chunks + 1
		for off := 0; off < len(listing); off += step {
			_, _ = w.Write(listing[off:min(off+step, len(listing))])
			w.(http.Flusher).Flush()
			time.Sleep(gap)
		}
	}))
	withSearchFlags(t, "thunderstore", 10)

	started := time.Now()
	stdout, _, err := runAsExecute(t, func(ctx context.Context) error {
		return doSearch(ctx, svc, game, []string{"shiploot"})
	})
	require.NoError(t, err)
	assert.Greater(t, time.Since(started), window, "the transfer outlasted the window it never exceeded")
	assert.Equal(t, exitOK, exitCodeIn(t.Context(), err))
	assert.Contains(t, stdout, "tinyhoot-ShipLoot")
}

func TestHTTP2Stall_ADownloadThatStopsFails(t *testing.T) {
	archive := stallArchive(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/c/lethal-company/api/v1/package/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(stallListing(len(archive)))
	})
	mux.HandleFunc("/package/download/tinyhoot/ShipLoot/1.1.0/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(len(archive)))
		_, _ = w.Write(archive[:100])
		w.(http.Flusher).Flush()
		hold(t, r)
	})
	svc, game := newHTTP2StandIn(t, 300*time.Millisecond, mux)

	setupInstallFlags(t)
	installSource, installModID = "thunderstore", "tinyhoot-ShipLoot"

	stdout, stderr, err := runAsExecute(t, func(ctx context.Context) error {
		return doInstall(ctx, svc, game, nil)
	})
	assertStallFailure(t, stdout, err)
	assert.Equal(t, 2, strings.Count(stderr, "stalled; retrying in"), "each retry says why: %q", stderr)
	assert.NotContains(t, stderr, "Could not reach")
}

// setupInstallFlags resets install's flags for one test.
func setupInstallFlags(t *testing.T) {
	t.Helper()
	oldSource, oldProfile, oldVersion, oldModID, oldFileID := installSource, installProfile, installVersion, installModID, installFileID
	oldYes, oldShowArchived, oldSkipVerify, oldForce, oldNoDeps := installYes, installShowArchived, skipVerify, installForce, installNoDeps
	t.Cleanup(func() {
		installSource, installProfile, installVersion, installModID, installFileID = oldSource, oldProfile, oldVersion, oldModID, oldFileID
		installYes, installShowArchived, skipVerify, installForce, installNoDeps = oldYes, oldShowArchived, oldSkipVerify, oldForce, oldNoDeps
	})
	installProfile, installVersion, installFileID = "", "", ""
	installYes, installShowArchived, skipVerify, installForce, installNoDeps = true, false, false, false, true
}
