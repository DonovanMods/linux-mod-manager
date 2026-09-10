package thunderstore_test

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/thunderstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file is the package-wide guard on the one thing no test here may do:
// reach the real Thunderstore. Every test drives an httptest server over a
// hand-built fixture, and the two ratchets below make that structural
// rather than a convention someone remembers.

// TestNoTestReachesTheProductionAPI refuses a test file that names the
// production host as a BASE URL - the one form that would send a request
// there. A test asserting a derived page or icon URL (which are longer
// strings) is fine and expected: those are pure string construction and
// never fetched.
func TestNoTestReachesTheProductionAPI(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	// Split so this guard's own text is not a match.
	base := `"https://` + "thunderstore" + `.io"`
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		require.NoError(t, err)
		assert.NotContains(t, string(data), base,
			"%s names the live host as a base URL: tests must use an httptest server", e.Name())
	}
}

// TestEveryTestBuildsTheSourceWithABaseURL extends the guard past a string
// match to the shape that actually matters: an Options with no BaseURL
// falls back to the production host, so every construction of this source
// in any test in the module must name one - qualified from another
// package, or bare from one of this package's own internal tests.
func TestEveryTestBuildsTheSourceWithABaseURL(t *testing.T) {
	root := moduleRoot(t)
	var offenders []string
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(data)
		// Two spellings, because this package has INTERNAL tests too: a
		// test in `package thunderstore` constructs with a bare New(, which
		// the qualified form below would never see. That second token is
		// only looked for inside this package's own directory, where it can
		// mean nothing else.
		tokens := []string{"thunderstore.New("}
		if filepath.Base(filepath.Dir(path)) == "thunderstore" {
			tokens = append(tokens, "New(Options{")
		}
		for _, token := range tokens {
			for i := 0; ; {
				at := strings.Index(text[i:], token)
				if at < 0 {
					break
				}
				at += i
				window := text[at:min(at+400, len(text))]
				if !strings.Contains(window, "BaseURL") {
					offenders = append(offenders, path)
				}
				i = at + 1
			}
		}
		return nil
	}))
	assert.Empty(t, offenders, "these tests construct the source without a BaseURL, which points it at the live host")
}

// TestNoRequestCarriesACredential pins Auth: false end to end. This is the
// first built-in that needs no key at all, and the only way that stays true
// is if nothing on the wire ever carries one.
func TestNoRequestCarriesACredential(t *testing.T) {
	sandboxEnv(t)
	var seen http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.Header().Set("Last-Modified", "Wed, 10 Sep 2026 12:00:00 GMT")
		_, _ = w.Write(fixtureDocument(t))
	}))
	defer srv.Close()

	src := thunderstore.New(thunderstore.Options{CacheDir: t.TempDir(), BaseURL: srv.URL})
	_, err := src.Search(t.Context(), source.SearchQuery{GameID: testCommunity, Query: "skinwalkers"})
	require.NoError(t, err)

	require.NotNil(t, seen)
	assert.Empty(t, seen.Get("Authorization"))
	assert.Empty(t, seen.Get("apikey"))
	assert.Empty(t, seen.Get("x-api-key"))

	// And the capability set says so, so no frontend offers a login for it.
	caps := source.CapabilitiesOf(src)
	assert.False(t, caps.Auth, "Thunderstore needs no credential of any kind")
	assert.True(t, caps.Search)
	assert.Equal(t, "", src.AuthURL())
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no go.mod above %s", dir)
		dir = parent
	}
}
