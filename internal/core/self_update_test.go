package core_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/releasesig"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// self_update_test.go - `lmm self-update` (#569) against an httptest
// GitHub: the release API and the release downloads both, with a key pair
// each test generates. The real release key never signs anything here.

// tarEntry is one member of a test release tarball.
type tarEntry struct {
	name     string
	body     string
	typeflag byte
	linkname string
	mode     int64
}

// buildTarball gzips a tar of entries.
func buildTarball(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typeflag
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o755
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Linkname: e.linkname, Mode: mode}
		if typ == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		require.NoError(t, tw.WriteHeader(h))
		if typ == tar.TypeReg {
			_, err := tw.Write([]byte(e.body))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	return buf.Bytes()
}

// fakeRelease is a published release on the fake GitHub: its assets by
// name. A nil asset is a 404.
type fakeRelease struct {
	tag    string
	assets map[string][]byte
	// latestStatus, when set, is the latest-release document's HTTP status
	// instead of the document: GitHub failing (#586).
	latestStatus int
	// onAsset, when set, runs as an asset is served.
	onAsset func(name string)

	mu       sync.Mutex
	requests []string
	headers  []http.Header
}

// newSignedRelease returns release v<ver> holding tarball for this
// machine's GOARCH, its checksums.txt and a signature by privKey.
func newSignedRelease(t *testing.T, ver, privKey string, tarball []byte) *fakeRelease {
	t.Helper()
	asset := fmt.Sprintf("lmm_%s_linux_%s.tar.gz", ver, runtime.GOARCH)
	sum := sha256.Sum256(tarball)
	checksums := fmt.Sprintf("%s  lmm_%s_linux_other.tar.gz\n%s  %s\n",
		strings.Repeat("0", 64), ver, hex.EncodeToString(sum[:]), asset)
	sig, err := releasesig.Sign(privKey, []byte(checksums))
	require.NoError(t, err)
	return &fakeRelease{
		tag: "v" + ver,
		assets: map[string][]byte{
			asset:               tarball,
			"checksums.txt":     []byte(checksums),
			"checksums.txt.sig": []byte(sig + "\n"),
		},
	}
}

// serve stands the release up as GitHub: the API's latest-release document
// and the download URLs under /DonovanMods/linux-mod-manager/releases/download.
func (r *fakeRelease) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.requests = append(r.requests, req.URL.Path)
		r.headers = append(r.headers, req.Header.Clone())
		r.mu.Unlock()
		if req.URL.Path == "/repos/DonovanMods/linux-mod-manager/releases/latest" {
			if r.latestStatus != 0 {
				w.WriteHeader(r.latestStatus)
				return
			}
			_, _ = fmt.Fprintf(w, `{"tag_name":%q,"html_url":"https://github.com/DonovanMods/linux-mod-manager/releases/tag/%s","draft":false,"prerelease":false}`, r.tag, r.tag)
			return
		}
		name, ok := strings.CutPrefix(req.URL.Path, "/DonovanMods/linux-mod-manager/releases/download/"+r.tag+"/")
		if !ok {
			http.NotFound(w, req)
			return
		}
		body, ok := r.assets[name]
		if !ok || body == nil {
			http.NotFound(w, req)
			return
		}
		if r.onAsset != nil {
			r.onAsset(name)
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fakeRestarter records RestartServeIfActive calls.
type fakeRestarter struct {
	calls     int
	restarted bool
	err       error
	// onRestart, when set, runs as the restart is asked for.
	onRestart func()
}

func (f *fakeRestarter) RestartServeIfActive(context.Context) (bool, error) {
	f.calls++
	if f.onRestart != nil {
		f.onRestart()
	}
	return f.restarted, f.err
}

// selfUpdateFixture is a self-managed "installed" lmm in a temp dir and a
// key pair to sign its next release with.
type selfUpdateFixture struct {
	exe      string
	priv     string
	pub      string
	restarts *fakeRestarter
	opts     core.SelfUpdateOptions
}

func newSelfUpdateFixture(t *testing.T) *selfUpdateFixture {
	t.Helper()
	priv, pub, err := releasesig.Generate()
	require.NoError(t, err)
	exe := filepath.Join(t.TempDir(), "lmm")
	require.NoError(t, os.WriteFile(exe, []byte("old lmm"), 0o750))
	f := &selfUpdateFixture{exe: exe, priv: priv, pub: pub, restarts: &fakeRestarter{}}
	f.opts = core.SelfUpdateOptions{
		ConfigDir:   t.TempDir(),
		DataDir:     t.TempDir(),
		Version:     "2.1.0",
		TrustedKeys: []string{pub},
		Probe: &core.InstallProbe{
			Executable: func() (string, error) { return exe, nil },
			LookPath:   func(string) (string, error) { return "", os.ErrNotExist },
			Run:        func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist },
		},
		Restarter: f.restarts,
	}
	return f
}

// updater points the fixture at srv for both the API and the downloads.
func (f *selfUpdateFixture) updater(srv *httptest.Server) *core.SelfUpdater {
	opts := f.opts
	opts.APIBase, opts.DownloadBase = srv.URL, srv.URL
	return core.NewSelfUpdater(opts)
}

func goodTarball(t *testing.T) []byte {
	return buildTarball(t,
		tarEntry{name: "README.md", body: "readme", mode: 0o644},
		tarEntry{name: "lmm", body: "new lmm 2.2.0"},
	)
}

// Apply replaces the binary and stops there (#586 review): it cannot see
// lmm serve's jobs, so lmm-serve.service restarts itself
// (RestartServeWhenIdle) rather than being restarted under one.
func TestSelfUpdate_ReplacesTheBinaryAtomicallyAndLeavesServeAlone(t *testing.T) {
	f := newSelfUpdateFixture(t)
	f.restarts.restarted = true
	srv := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t)
	u := f.updater(srv)

	plan, err := u.Plan(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "2.1.0", plan.Current)
	assert.Equal(t, "2.2.0", plan.Latest)
	assert.False(t, plan.UpToDate)
	assert.Equal(t, f.exe, plan.Executable)
	assert.Equal(t, fmt.Sprintf("lmm_2.2.0_linux_%s.tar.gz", runtime.GOARCH), plan.Asset)

	res, err := u.Apply(context.Background(), plan)
	require.NoError(t, err)
	assert.True(t, res.Updated)
	assert.Equal(t, "2.1.0", res.Previous)
	assert.Equal(t, "2.2.0", res.Version)
	assert.False(t, res.ServeRestarted)
	assert.Zero(t, f.restarts.calls, "lmm serve restarts itself once it is idle")

	data, err := os.ReadFile(f.exe)
	require.NoError(t, err)
	assert.Equal(t, "new lmm 2.2.0", string(data))
	fi, err := os.Stat(f.exe)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o750), fi.Mode().Perm(), "the old binary's mode is kept")

	// Nothing is left beside the binary, nor in the staging dir.
	entries, err := os.ReadDir(filepath.Dir(f.exe))
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	staged, _ := os.ReadDir(filepath.Join(f.opts.DataDir, "downloads"))
	assert.Empty(t, staged)
}

func TestSelfUpdate_UpToDateIsANoOp(t *testing.T) {
	for _, current := range []string{"2.2.0", "2.3.0"} {
		t.Run(current, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			f.opts.Version = current
			rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
			srv := rel.serve(t)
			u := f.updater(srv)
			plan, err := u.Plan(context.Background())
			require.NoError(t, err)
			assert.True(t, plan.UpToDate)
			res, err := u.Apply(context.Background(), plan)
			require.NoError(t, err)
			assert.False(t, res.Updated)
			assert.Equal(t, 0, f.restarts.calls)
			data, _ := os.ReadFile(f.exe)
			assert.Equal(t, "old lmm", string(data))
			for _, p := range rel.requests {
				assert.NotContains(t, p, "/releases/download/", "an up-to-date lmm downloads nothing")
			}
		})
	}
}

func TestSelfUpdate_RefusesAPackageOwnedBinary(t *testing.T) {
	f := newSelfUpdateFixture(t)
	f.opts.Probe.LookPath = func(name string) (string, error) {
		if name == "pacman" {
			return "/usr/bin/pacman", nil
		}
		return "", os.ErrNotExist
	}
	f.opts.Probe.Run = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		return []byte("lmm-bin\n"), nil
	}
	srv := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t)
	_, err := f.updater(srv).Plan(context.Background())
	var pkgErr *core.PackageManagedError
	require.ErrorAs(t, err, &pkgErr)
	assert.Equal(t, "lmm-bin", pkgErr.Install.Package)
	assert.Contains(t, err.Error(), "sudo pacman -Syu", "the refusal names the package manager's upgrade command")
}

func TestSelfUpdate_RefusesADevBuild(t *testing.T) {
	f := newSelfUpdateFixture(t)
	f.opts.Dev = true
	srv := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t)
	_, err := f.updater(srv).Plan(context.Background())
	require.ErrorIs(t, err, core.ErrDevBuild)
}

// applyErr plans and applies against rel, returning Apply's error, and
// asserts the installed binary was left exactly as it was.
func (f *selfUpdateFixture) applyErr(t *testing.T, rel *fakeRelease) error {
	t.Helper()
	u := f.updater(rel.serve(t))
	plan, err := u.Plan(context.Background())
	require.NoError(t, err)
	_, err = u.Apply(context.Background(), plan)
	require.Error(t, err)
	data, rerr := os.ReadFile(f.exe)
	require.NoError(t, rerr)
	assert.Equal(t, "old lmm", string(data), "a refused update leaves the binary alone")
	entries, _ := os.ReadDir(filepath.Dir(f.exe))
	assert.Len(t, entries, 1, "and leaves nothing beside it")
	assert.Equal(t, 0, f.restarts.calls)
	return err
}

func TestSelfUpdate_RefusesAMissingSignature(t *testing.T) {
	f := newSelfUpdateFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	delete(rel.assets, "checksums.txt.sig")
	err := f.applyErr(t, rel)
	var sigErr *core.ReleaseSignatureError
	require.ErrorAs(t, err, &sigErr)
	assert.True(t, sigErr.Missing)
	assert.ErrorIs(t, err, core.ErrReleaseNotVerified)
}

func TestSelfUpdate_RefusesABadSignature(t *testing.T) {
	cases := map[string]func(t *testing.T, f *selfUpdateFixture, rel *fakeRelease){
		"another key signed it": func(t *testing.T, _ *selfUpdateFixture, rel *fakeRelease) {
			other, _, err := releasesig.Generate()
			require.NoError(t, err)
			sig, err := releasesig.Sign(other, rel.assets["checksums.txt"])
			require.NoError(t, err)
			rel.assets["checksums.txt.sig"] = []byte(sig)
		},
		"checksums.txt was changed after signing": func(_ *testing.T, _ *selfUpdateFixture, rel *fakeRelease) {
			rel.assets["checksums.txt"] = append(rel.assets["checksums.txt"], []byte("# tampered\n")...)
		},
		"the signature is not base64": func(_ *testing.T, _ *selfUpdateFixture, rel *fakeRelease) {
			rel.assets["checksums.txt.sig"] = []byte("not a signature!")
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
			mutate(t, f, rel)
			err := f.applyErr(t, rel)
			var sigErr *core.ReleaseSignatureError
			require.ErrorAs(t, err, &sigErr)
			assert.False(t, sigErr.Missing)
			assert.ErrorIs(t, err, core.ErrReleaseNotVerified)
		})
	}
}

func TestSelfUpdate_AcceptsAnyTrustedKey(t *testing.T) {
	// A key rotation overlaps two keys: a release signed by either verifies.
	f := newSelfUpdateFixture(t)
	_, other, err := releasesig.Generate()
	require.NoError(t, err)
	f.opts.TrustedKeys = []string{other, f.pub}
	u := f.updater(newSignedRelease(t, "2.2.0", f.priv, goodTarball(t)).serve(t))
	plan, err := u.Plan(context.Background())
	require.NoError(t, err)
	_, err = u.Apply(context.Background(), plan)
	require.NoError(t, err)
}

func TestSelfUpdate_RefusesAChecksumMismatch(t *testing.T) {
	f := newSelfUpdateFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	// The signed checksums.txt is genuine; the tarball beside it is not.
	rel.assets[fmt.Sprintf("lmm_2.2.0_linux_%s.tar.gz", runtime.GOARCH)] = buildTarball(t, tarEntry{name: "lmm", body: "evil"})
	err := f.applyErr(t, rel)
	var sumErr *core.ReleaseChecksumError
	require.ErrorAs(t, err, &sumErr)
	assert.ErrorIs(t, err, core.ErrReleaseNotVerified)
}

func TestSelfUpdate_RefusesATarballChecksumsTxtDoesNotList(t *testing.T) {
	// A replayed older checksums.txt (genuinely signed) names other files:
	// the asset name carries the version, so it binds the release.
	f := newSelfUpdateFixture(t)
	old := newSignedRelease(t, "2.1.5", f.priv, goodTarball(t))
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	rel.assets["checksums.txt"] = old.assets["checksums.txt"]
	rel.assets["checksums.txt.sig"] = old.assets["checksums.txt.sig"]
	err := f.applyErr(t, rel)
	var sumErr *core.ReleaseChecksumError
	require.ErrorAs(t, err, &sumErr)
	assert.True(t, sumErr.Unlisted)
}

func TestSelfUpdate_RefusesAMalformedTarball(t *testing.T) {
	cases := map[string][]byte{
		"not gzip":      []byte("this is not a tarball"),
		"no lmm in it":  buildTarball(t, tarEntry{name: "README.md", body: "x"}),
		"lmm a symlink": buildTarball(t, tarEntry{name: "lmm", typeflag: tar.TypeSymlink, linkname: "/bin/sh"}),
		"lmm a dir":     buildTarball(t, tarEntry{name: "lmm", typeflag: tar.TypeDir}),
		"path escape":   buildTarball(t, tarEntry{name: "../lmm", body: "x"}, tarEntry{name: "lmm", body: "x"}),
		"absolute path": buildTarball(t, tarEntry{name: "/usr/bin/lmm", body: "x"}, tarEntry{name: "lmm", body: "x"}),
		"empty lmm":     buildTarball(t, tarEntry{name: "lmm", body: ""}),
	}
	for name, tarball := range cases {
		t.Run(name, func(t *testing.T) {
			f := newSelfUpdateFixture(t)
			err := f.applyErr(t, newSignedRelease(t, "2.2.0", f.priv, tarball))
			var archErr *core.ReleaseArchiveError
			require.ErrorAs(t, err, &archErr)
		})
	}
}

func TestSelfUpdate_RefusesAnUnwritableDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	f := newSelfUpdateFixture(t)
	dir := filepath.Dir(f.exe)
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	u := f.updater(rel.serve(t))
	plan, err := u.Plan(context.Background())
	require.NoError(t, err)
	_, err = u.Apply(context.Background(), plan)
	var wErr *core.ExecutableNotWritableError
	require.ErrorAs(t, err, &wErr)
	assert.Equal(t, dir, wErr.Dir)
	assert.NotContains(t, err.Error(), "sudo", "the fix is never sudo")
	assert.Contains(t, err.Error(), "~/.local/bin")
	for _, p := range rel.requests {
		assert.NotContains(t, p, "/releases/download/", "an unwritable directory is found before anything is downloaded")
	}
	data, _ := os.ReadFile(f.exe)
	assert.Equal(t, "old lmm", string(data))
}

func TestSelfUpdate_RefusesAReleaseWithoutThisArchitecture(t *testing.T) {
	f := newSelfUpdateFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	delete(rel.assets, fmt.Sprintf("lmm_2.2.0_linux_%s.tar.gz", runtime.GOARCH))
	err := f.applyErr(t, rel)
	var assetErr *core.ReleaseAssetError
	require.ErrorAs(t, err, &assetErr)
}

func TestSelfUpdate_SendsTheLmmUserAgentAndNoCredential(t *testing.T) {
	f := newSelfUpdateFixture(t)
	rel := newSignedRelease(t, "2.2.0", f.priv, goodTarball(t))
	u := f.updater(rel.serve(t))
	plan, err := u.Plan(context.Background())
	require.NoError(t, err)
	_, err = u.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Len(t, rel.headers, 4, "the release document and three downloads")
	for _, h := range rel.headers {
		assert.Equal(t, "lmm/2.1.0", h.Get("User-Agent"))
		assert.Empty(t, h.Get("Authorization"))
	}
}

func withBases(o core.SelfUpdateOptions) core.SelfUpdateOptions {
	o.APIBase, o.DownloadBase = "http://127.0.0.1:1", "http://127.0.0.1:1"
	return o
}

func TestTrustedReleaseKeys_HoldTheProductionKey(t *testing.T) {
	keys := core.TrustedReleaseKeys()
	assert.Contains(t, keys, "NFjV1hUuRPm3P/3iQ+Ohyfqmzty2G27mLpaCdsdJGkg=")
	keys[0] = "mutated"
	assert.NotEqual(t, "mutated", core.TrustedReleaseKeys()[0], "callers get a copy")
}

func TestNewSelfUpdater_RequiresTheBases(t *testing.T) {
	f := newSelfUpdateFixture(t)
	assert.Panics(t, func() { core.NewSelfUpdater(f.opts) }, "no APIBase/DownloadBase: nothing reaches GitHub by default")
}
