package serve

// httptest + unit coverage for the archive upload surface (uploads.go,
// api_uploads.go).

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// multipartUpload builds a multipart body with one file part.
func multipartUpload(t *testing.T, field, filename string, content []byte) (body string, contentType string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, filename)
	require.NoError(t, err)
	_, err = part.Write(content)
	require.NoError(t, err)
	require.NoError(t, mw.Close())
	return buf.String(), mw.FormDataContentType()
}

// postUpload sends a multipart upload through the full middleware chain.
func postUpload(t *testing.T, s *Server, filename string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := multipartUpload(t, uploadFormField, filename, content)
	req := apiRequest(s, http.MethodPost, "/api/v1/uploads", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// newUploadCapFixtureServer builds a Server with the upload cap shrunk to
// capBytes via Options.MaxUploadBytes (#333 Important #2's seam - the
// production maxUploadBytes constant, 2 GiB, is impractical for a test to
// exceed), and returns the staging root so a test can assert nothing was
// left under it.
func newUploadCapFixtureServer(t *testing.T, capBytes int64) (s *Server, stagingRoot string) {
	t.Helper()
	sandboxEnv(t)
	dataDir := t.TempDir()
	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	s = New(t.Context(), svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr, MaxUploadBytes: capBytes})
	return s, filepath.Join(dataDir, "downloads")
}

// TestAPIUploadCreate_RefusesOverTheConfiguredCap drives the REAL 413 path
// end to end - unlike TestAPIUploadCreate_RefusesAnOverLongBody, which only
// unit-tests the status classifier - by shrinking the cap instead of trying
// to build a 2 GiB body: an over-cap part is refused with 413, no entry
// survives in the store, and nothing is left under the staging root.
//
// #333 Important #2: this is the test the review found missing. Proof it
// pins the real mechanism, not just the classifier: replacing
// handleAPIUploadCreate's `r.Body = http.MaxBytesReader(...)` line with
// `_ = w` (a no-op) turns this test RED - reported alongside this fix.
func TestAPIUploadCreate_RefusesOverTheConfiguredCap(t *testing.T) {
	s, stagingRoot := newUploadCapFixtureServer(t, 4096)

	rec := postUpload(t, s, "SomeMod.zip", bytes.Repeat([]byte("x"), 64*1024))
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code, "body: %s", rec.Body.String())
	assert.Equal(t, 0, s.uploads.len(), "no entry survives a refused upload")

	entries, err := os.ReadDir(stagingRoot)
	if !os.IsNotExist(err) {
		require.NoError(t, err)
	}
	assert.Empty(t, entries, "nothing left under the staging root")
}

// decodeUpload decodes an upload receipt.
func decodeUpload(t *testing.T, body []byte) uploadResponse {
	t.Helper()
	var res uploadResponse
	require.NoError(t, json.Unmarshal(body, &res, json.RejectUnknownMembers(true)))
	return res
}

func TestAPIUploadCreate_StagesTheArchiveUnderTheDataDir(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	content := []byte("PK\x03\x04 not really a zip, but the bytes are what get staged")

	rec := postUpload(t, s, "SomeMod-1.2.zip", content)
	require.Equal(t, http.StatusOK, rec.Code)

	res := decodeUpload(t, rec.Body.Bytes())
	assert.Equal(t, "SomeMod-1.2.zip", res.Filename)
	assert.Equal(t, int64(len(content)), res.Size)
	assert.Len(t, res.UploadID, 32, "an opaque random handle, not a path")

	staged, ok := s.uploads.Get(res.UploadID)
	require.True(t, ok)
	onDisk, err := os.ReadFile(staged.Path)
	require.NoError(t, err)
	assert.Equal(t, content, onDisk)
	assert.Equal(t, "downloads", filepath.Base(filepath.Dir(staged.Dir)),
		"staged under core's staging root, never a new tmp root")

	info, err := os.Stat(staged.Path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}

func TestAPIUploadCreate_RefusesWhatItCannotImport(t *testing.T) {
	tests := []struct {
		name     string
		filename string
	}{
		{"an extension the extractor does not handle", "notes.txt"},
		{"no extension at all", "archive"},
		{"a traversal attempt", "../../../etc/passwd"},
		{"a dotfile", ".bashrc.zip"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newDeployFixtureServer(t)
			rec := postUpload(t, s, tc.filename, []byte("x"))
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, 0, s.uploads.len(), "nothing was staged")
		})
	}
}

func TestAPIUploadCreate_RefusesANonMultipartBody(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	rec := doAPI(s, http.MethodPost, "/api/v1/uploads", `{"file":"x"}`)
	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)
}

func TestAPIUploadCreate_RefusesABodyWithNoFilePart(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	require.NoError(t, mw.WriteField("note", "no file here"))
	require.NoError(t, mw.Close())

	req := apiRequest(s, http.MethodPost, "/api/v1/uploads", buf.String())
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "no file part")
}

func TestAPIUploadCreate_RefusesASecondFile(t *testing.T) {
	s, _ := newDeployFixtureServer(t)

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, name := range []string{"one.zip", "two.zip"} {
		part, err := mw.CreateFormFile(uploadFormField, name)
		require.NoError(t, err)
		_, err = part.Write([]byte("x"))
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())

	req := apiRequest(s, http.MethodPost, "/api/v1/uploads", buf.String())
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "exactly one file")
	assert.Equal(t, 0, s.uploads.len(), "the staged first file is reclaimed, not silently imported")
}

func TestAPIUploadCreate_RefusesAnOverLongBody(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	// Shrink the cap for the length of the test by driving the handler's
	// own limit: a body larger than maxUploadBytes is impractical to build,
	// so this asserts the classification helper instead, with the real
	// error type net/http produces.
	assert.Equal(t, http.StatusRequestEntityTooLarge,
		uploadRequestStatus(&http.MaxBytesError{Limit: maxUploadBytes}))
	assert.Equal(t, http.StatusBadRequest, uploadRequestStatus(errNoFilePart))
	assert.Equal(t, 0, s.uploads.len())
}

func TestAPIUploadCreate_RequiresTheCSRFToken(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	body, contentType := multipartUpload(t, uploadFormField, "x.zip", []byte("x"))
	req := apiRequest(s, http.MethodPost, "/api/v1/uploads", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Del(csrfHeaderName)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Equal(t, 0, s.uploads.len())
}

func TestAPIUploadDelete(t *testing.T) {
	s, _ := newDeployFixtureServer(t)
	rec := postUpload(t, s, "SomeMod.zip", []byte("x"))
	require.Equal(t, http.StatusOK, rec.Code)
	res := decodeUpload(t, rec.Body.Bytes())
	staged, ok := s.uploads.Get(res.UploadID)
	require.True(t, ok)

	del := doAPI(s, http.MethodDelete, "/api/v1/uploads/"+string(res.UploadID), "")
	assert.Equal(t, http.StatusNoContent, del.Code)
	assert.Empty(t, del.Body.String())
	assert.NoDirExists(t, staged.Dir, "cancelling reclaims the disk, not just the index entry")

	assert.Equal(t, http.StatusNotFound,
		doAPI(s, http.MethodDelete, "/api/v1/uploads/"+string(res.UploadID), "").Code)
}

// TestUploadStore_ExpiresAndReclaimsTheDisk drives the TTL through the
// store's clock seam rather than sleeping.
func TestUploadStore_ExpiresAndReclaimsTheDisk(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newUploadStore(30*time.Minute, defaultUploadStoreCap, func() time.Time { return now })

	dir := t.TempDir()
	id := store.Put(&stagedUpload{Filename: "a.zip", Dir: dir, Path: filepath.Join(dir, "a.zip")})
	_, ok := store.Get(id)
	assert.True(t, ok)

	now = now.Add(30 * time.Minute)
	_, ok = store.Get(id)
	assert.False(t, ok, "exactly ttl old counts as expired")
	assert.NoDirExists(t, dir, "the sweep reclaims the staging directory too")
}

// TestUploadStore_EvictsTheOldestAtCapacity pins that a store at capacity
// drops the oldest entry, files included, rather than growing.
func TestUploadStore_EvictsTheOldestAtCapacity(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newUploadStore(time.Hour, 2, func() time.Time { return now })

	first := t.TempDir()
	firstID := store.Put(&stagedUpload{Filename: "a.zip", Dir: first})
	now = now.Add(time.Minute)
	store.Put(&stagedUpload{Filename: "b.zip", Dir: t.TempDir()})
	now = now.Add(time.Minute)
	store.Put(&stagedUpload{Filename: "c.zip", Dir: t.TempDir()})

	assert.Equal(t, 2, store.len())
	_, ok := store.Get(firstID)
	assert.False(t, ok, "the oldest went")
	assert.NoDirExists(t, first)
}

// TestUploadStore_MarkInUseSurvivesASweep pins #333 Minor #2: an entry
// flagged in-use is skipped by a sweep even once its TTL has passed - the
// window an in-flight import (kind_import_archive.go) needs to survive
// traffic to OTHER uploads that would otherwise reclaim its file mid-read.
// Once ClearInUse lifts the flag, the next sweep reclaims it exactly as
// before.
func TestUploadStore_MarkInUseSurvivesASweep(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	store := newUploadStore(30*time.Minute, defaultUploadStoreCap, func() time.Time { return now })

	dir := t.TempDir()
	id := store.Put(&stagedUpload{Filename: "a.zip", Dir: dir, Path: filepath.Join(dir, "a.zip")})
	store.MarkInUse(id)

	now = now.Add(time.Hour) // well past the 30-minute TTL
	// Put's own sweep runs first, over an unrelated upload - the traffic
	// that could otherwise reclaim "a.zip" mid-import.
	store.Put(&stagedUpload{Filename: "b.zip", Dir: t.TempDir()})

	_, ok := store.Get(id)
	assert.True(t, ok, "an in-use entry survives a sweep no matter how expired it is")
	assert.DirExists(t, dir)

	store.ClearInUse(id)
	_, ok = store.Get(id)
	assert.False(t, ok, "once cleared, the next sweep reclaims it like any other expired entry")
	assert.NoDirExists(t, dir)
}

// TestUploadStore_PurgeAllReclaimsEverything pins the shutdown path.
func TestUploadStore_PurgeAllReclaimsEverything(t *testing.T) {
	store := newUploadStore(time.Hour, defaultUploadStoreCap, time.Now)
	dir := t.TempDir()
	store.Put(&stagedUpload{Filename: "a.zip", Dir: dir})

	store.PurgeAll()
	assert.Equal(t, 0, store.len())
	assert.NoDirExists(t, dir)
}

// TestSafeUploadName is the unit form of the name rules the handler
// applies, including the ones a multipart writer will not let a test send.
func TestSafeUploadName(t *testing.T) {
	for _, ok := range []string{"Mod.zip", "Mod-1.2.7z", "Mod.RAR", "a b c.zip"} {
		name, err := safeUploadName(ok)
		require.NoError(t, err, ok)
		assert.Equal(t, ok, name)
	}
	for _, bad := range []string{"", ".", "..", "/", "notes.txt", "archive", ".hidden.zip", "a/../.hidden.zip"} {
		_, err := safeUploadName(bad)
		assert.Error(t, err, bad)
	}
	// A path is reduced to its basename rather than refused outright: a
	// browser on Windows sends a backslash path, and the file either names
	// is still a perfectly good archive.
	for path, want := range map[string]string{
		"/home/someone/Downloads/Mod.zip": "Mod.zip",
		`C:\Users\me\Mod.zip`:             "Mod.zip",
		"../../../etc/Mod.zip":            "Mod.zip",
	} {
		name, err := safeUploadName(path)
		require.NoError(t, err, path)
		assert.Equal(t, want, name)
	}
}

// TestStageUploadedFile_StreamsWithoutBuffering is a shape assertion: the
// staging helper takes an io.Reader and writes what it reads, so a large
// archive never has to fit in memory.
func TestStageUploadedFile_StreamsWithoutBuffering(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.zip")
	content := strings.Repeat("mod bytes ", 4096)

	size, err := stageUploadedFile(path, io.LimitReader(strings.NewReader(content), int64(len(content))))
	require.NoError(t, err)
	assert.Equal(t, int64(len(content)), size)

	onDisk, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, content, string(onDisk))

	_, err = stageUploadedFile(path, strings.NewReader("x"))
	require.Error(t, err, "an existing staged file is never silently overwritten")
}
