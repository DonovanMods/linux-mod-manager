package serve

// httptest coverage for GET /api/v1/fs/dirs (#529): the folder chooser's
// read-only listing. core owns what is listed (internal/core/
// list_directories_test.go); these pin the wire - the query, the status
// codes, the shared error envelope - and that the route is no more
// reachable than the rest of the API.

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fsDirsURL(path string, extra ...string) string {
	q := url.Values{"path": {path}}
	for i := 0; i+1 < len(extra); i += 2 {
		q.Set(extra[i], extra[i+1])
	}
	return "/api/v1/fs/dirs?" + q.Encode()
}

func getListing(t *testing.T, s *Server, target string) core.DirectoryListing {
	t.Helper()
	rec := doAPI(s, http.MethodGet, target, "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var l core.DirectoryListing
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &l, json.RejectUnknownMembers(true)))
	return l
}

func TestAPIFsDirs_ListsSubfoldersAndNeverFiles(t *testing.T) {
	s := newGamesServer(t)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "beta"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "Alpha"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".hidden"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "file.txt"), nil, 0o644))

	l := getListing(t, s, fsDirsURL(root))
	assert.Equal(t, root, l.Path)
	assert.Equal(t, filepath.Dir(root), l.Parent)
	require.Len(t, l.Entries, 2)
	assert.Equal(t, "Alpha", l.Entries[0].Name)
	assert.Equal(t, "beta", l.Entries[1].Name)

	l = getListing(t, s, fsDirsURL(root, "hidden", "1"))
	require.Len(t, l.Entries, 3)
	assert.Equal(t, ".hidden", l.Entries[0].Name)
	assert.True(t, l.Entries[0].Hidden)
}

func TestAPIFsDirs_NearestAnswersForTheClosestFolder(t *testing.T) {
	s := newGamesServer(t)
	root := t.TempDir()
	missing := filepath.Join(root, "not", "here")

	rec := doAPI(s, http.MethodGet, fsDirsURL(missing), "")
	assert.Equal(t, http.StatusNotFound, rec.Code)

	l := getListing(t, s, fsDirsURL(missing, "nearest", "1"))
	assert.Equal(t, root, l.Path)
	assert.Equal(t, missing, l.Requested)

	// No path at all, asked for the nearest, is the home folder.
	home := t.TempDir()
	t.Setenv("HOME", home)
	l = getListing(t, s, "/api/v1/fs/dirs?nearest=1")
	assert.Equal(t, home, l.Path)
}

func TestAPIFsDirs_Refusals(t *testing.T) {
	s := newGamesServer(t)
	root := t.TempDir()
	file := filepath.Join(root, "f.txt")
	require.NoError(t, os.WriteFile(file, nil, 0o644))

	cases := []struct {
		name   string
		target string
		status int
		reason core.DirectoryErrorReason
	}{
		{"relative path", fsDirsURL("relative/dir"), http.StatusBadRequest, core.DirectoryNotAbsolute},
		{"no path", "/api/v1/fs/dirs", http.StatusBadRequest, core.DirectoryNotAbsolute},
		{"missing", fsDirsURL(filepath.Join(root, "nope")), http.StatusNotFound, core.DirectoryNotFound},
		{"a file", fsDirsURL(file), http.StatusBadRequest, core.DirectoryNotADirectory},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := doAPI(s, http.MethodGet, tc.target, "")
			require.Equal(t, tc.status, rec.Code, "body: %s", rec.Body.String())
			var env struct {
				Error   string                  `json:"error"`
				Details core.DirectoryListError `json:"details"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
			assert.NotEmpty(t, env.Error)
			assert.Equal(t, tc.reason, env.Details.Reason)
		})
	}

	t.Run("permission denied is 403", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads everything")
		}
		locked := filepath.Join(root, "locked")
		require.NoError(t, os.Mkdir(locked, 0o755))
		require.NoError(t, os.Chmod(locked, 0o000))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
		rec := doAPI(s, http.MethodGet, fsDirsURL(locked), "")
		require.Equal(t, http.StatusForbidden, rec.Code, "body: %s", rec.Body.String())
		var env struct {
			Details core.DirectoryListError `json:"details"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
		assert.Equal(t, core.DirectoryPermissionDenied, env.Details.Reason)
	})
}

func TestAPIFsDirs_IsReadOnly(t *testing.T) {
	s := newGamesServer(t)
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := doAPI(s, method, fsDirsURL(t.TempDir()), "")
		assert.Contains(t, []int{http.StatusMethodNotAllowed, http.StatusNotFound}, rec.Code, "method %s", method)
	}
}

// TestAPIFsDirs_GivesACrossOriginRequestNothingExtra: the listing is for the
// SPA's own origin. A foreign Origin gets the same answer as any other
// request and no Access-Control-* header that would let that page READ it;
// a foreign Host is refused before the handler is reached (the DNS-rebinding
// guard every route shares).
func TestAPIFsDirs_GivesACrossOriginRequestNothingExtra(t *testing.T) {
	s := newGamesServer(t)
	root := t.TempDir()

	req := apiRequest(s, http.MethodGet, fsDirsURL(root), "")
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	for name := range rec.Header() {
		assert.NotContains(t, name, "Access-Control-", "no CORS header on a cross-origin read")
	}
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))

	// A preflight is not answered with permission either.
	pre := apiRequest(s, http.MethodOptions, fsDirsURL(root), "")
	pre.Header.Set("Origin", "http://evil.example")
	pre.Header.Set("Access-Control-Request-Method", "GET")
	prec := httptest.NewRecorder()
	s.Handler().ServeHTTP(prec, pre)
	assert.Empty(t, prec.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, prec.Header().Get("Access-Control-Allow-Methods"))

	bad := apiRequest(s, http.MethodGet, fsDirsURL(root), "")
	bad.Host = "attacker.example"
	brec := httptest.NewRecorder()
	s.Handler().ServeHTTP(brec, bad)
	assert.Equal(t, http.StatusForbidden, brec.Code)
}
