package custom

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #626: `lmm serve` re-keys a live source when a user saves a key in Setup,
// so SetAPIKey runs while requests are in flight. The custom api and
// manifest sources keep their own key (they are not built on httpclient), so
// they are held to the same rule: under -race an unguarded key is a
// reported data race, and a request never sends a key it was not given.

// rekeyConcurrently flips the source's key between "first" and "second"
// from several goroutines while work runs in several others.
func rekeyConcurrently(t *testing.T, setKey func(string), authed func() bool, work func()) {
	t.Helper()
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := range 25 {
				if (g+i)%2 == 0 {
					setKey("first")
				} else {
					setKey("second")
				}
				assert.True(t, authed())
			}
		}()
		go func() {
			defer wg.Done()
			for range 25 {
				work()
			}
		}()
	}
	wg.Wait()
}

// sentKeyServer answers 200 with body, failing the test on any key that was
// never set.
func sentKeyServer(t *testing.T, sent func(*http.Request) string, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := sent(r); got != "first" && got != "second" {
			t.Errorf("server received a key that was never set: %q", got)
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAPI_RekeyConcurrentWithRequestsIsRaceFree(t *testing.T) {
	for _, in := range []string{"header", "query"} {
		t.Run(in, func(t *testing.T) {
			srv := sentKeyServer(t, func(r *http.Request) string {
				if in == "header" {
					return r.Header.Get("X-API-Key")
				}
				return r.URL.Query().Get("api_key")
			}, `{"url": "/dl/1.zip"}`)
			def := apiDef(srv.URL)
			def.API.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: in, Name: map[string]string{"header": "X-API-Key", "query": "api_key"}[in]}}
			a, err := NewAPI(def)
			require.NoError(t, err)
			a.SetAPIKey("first")

			rekeyConcurrently(t, a.SetAPIKey, a.IsAuthenticated, func() {
				_, err := a.getJSON(t.Context(), srv.URL+"/mods/1")
				assert.NoError(t, err)
				_ = a.DownloadHeaders(srv.URL + "/dl/1.zip")
				_, err = a.GetDownloadURL(t.Context(), &domain.Mod{ID: "1"}, "1")
				assert.NoError(t, err)
			})
		})
	}
}

func TestManifest_RekeyConcurrentWithRequestsIsRaceFree(t *testing.T) {
	for _, in := range []string{"header", "query"} {
		t.Run(in, func(t *testing.T) {
			srv := sentKeyServer(t, func(r *http.Request) string {
				if in == "header" {
					return r.Header.Get("X-API-Key")
				}
				return r.URL.Query().Get("api_key")
			}, testManifest)
			def := manifestDef(srv.URL + "/mods.yaml")
			def.AllowHTTP = true
			def.Manifest.Auth = &AuthConfig{APIKey: &APIKeyConfig{In: in, Name: map[string]string{"header": "X-API-Key", "query": "api_key"}[in]}}
			m, err := NewManifest(def)
			require.NoError(t, err)
			m.SetAPIKey("first")

			rekeyConcurrently(t, m.SetAPIKey, m.IsAuthenticated, func() {
				_, err := m.fetchRemote(t.Context())
				assert.NoError(t, err)
				_ = m.DownloadHeaders(srv.URL + "/dl/1.zip")
			})
		})
	}
}
