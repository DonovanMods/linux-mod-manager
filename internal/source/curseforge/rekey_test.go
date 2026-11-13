package curseforge

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestClient_RekeyConcurrentWithRequestsIsRaceFree (#626): `lmm serve`
// re-keys a live CurseForge source when a user saves a key in Setup, so
// SetAPIKey runs while requests - and the 403 mapping, which asks whether a
// key is configured - are in flight. Under -race an unguarded copy of the
// key is a reported data race.
func TestClient_RekeyConcurrentWithRequestsIsRaceFree(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error": "denied"}`))
	}))
	defer server.Close()

	client := NewClient(server.Client(), "first")
	client.SetBaseURL(server.URL)

	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 25 {
				if (g+i)%2 == 0 {
					client.SetAPIKey("first")
				} else {
					client.SetAPIKey("second")
				}
				assert.True(t, client.IsAuthenticated())
			}
		}()
		go func() {
			defer wg.Done()
			for range 25 {
				_, err := client.GetMod(t.Context(), 1)
				assert.Error(t, err)
			}
		}()
	}
	wg.Wait()
}
