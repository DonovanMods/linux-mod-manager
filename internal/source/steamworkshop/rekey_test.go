package steamworkshop_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSetAPIKey_ConcurrentWithSearchIsRaceFree (#626): `lmm serve` re-keys
// a live source when a user saves a key in Setup, so SetAPIKey runs while a
// search is in flight. Beyond the keys httpclient now guards, the source
// keeps the key's fingerprint for its search cache; under -race an
// unguarded fingerprint is a reported data race.
func TestSetAPIKey_ConcurrentWithSearchIsRaceFree(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "api", "queryfiles_page1.json"))
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if answerNameLookup(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	src := keyedSource(t, srv.URL, nil)

	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 25 {
				src.SetAPIKey(fmt.Sprintf("key-%d", (g+i)%2))
				assert.True(t, src.IsAuthenticated())
			}
		}()
		go func() {
			defer wg.Done()
			for i := range 25 {
				_, err := src.Search(t.Context(), source.SearchQuery{GameID: "1133870", Query: fmt.Sprintf("q%d-%d", g, i), PageSize: 20})
				assert.NoError(t, err)
			}
		}()
	}
	wg.Wait()
}
