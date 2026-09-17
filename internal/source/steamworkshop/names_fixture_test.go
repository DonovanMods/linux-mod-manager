package steamworkshop_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
)

// answerNameLookup answers the author-name lookups (#420) a GetMod or a
// Search now makes beside its metadata request, so a fixture server that
// counts or scripts its METADATA requests is unaffected by them: a keyed
// GetPlayerSummaries or a keyless community profile read gets a 404, which
// the resolver treats as "no name" and the row keeps its id. It reports
// whether it answered.
func answerNameLookup(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasPrefix(r.URL.Path, "/profiles/") || strings.HasPrefix(r.URL.Path, "/ISteamUser/") {
		w.WriteHeader(http.StatusNotFound)
		return true
	}
	return false
}

// newNamedSource is newTestSource with the community host pointed at its
// own server, for the name-resolver tests.
func newNamedSource(t *testing.T, baseURL, communityURL, cacheDir string, now func() time.Time) *steamworkshop.Source {
	t.Helper()
	return steamworkshop.New(steamworkshop.Options{
		BaseURL:      baseURL,
		CommunityURL: communityURL,
		CacheDir:     cacheDir,
		Now:          now,
		SteamRoots:   []string{t.TempDir()},
	})
}
