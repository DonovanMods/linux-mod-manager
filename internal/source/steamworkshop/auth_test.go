package steamworkshop_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steamworkshop"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// routedFixture serves the Tier-2 GET endpoints (QueryFiles,
// GetCollectionDetails) off recorded bodies, recording every request URL so
// a test can assert the parameter mapping without leaving the process. It
// is the GET sibling of client_test.go's form-POST serveFixture.
type routedFixture struct {
	srv      *httptest.Server
	requests []url.Values
	paths    []string
	// status/file are consulted per call index; the last entry repeats.
	replies []reply
	calls   int
}

type reply struct {
	status int
	file   string
}

func serveRoutes(t *testing.T, replies ...reply) *routedFixture {
	t.Helper()
	fx := &routedFixture{replies: replies}
	fx.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fx.paths = append(fx.paths, r.URL.Path)
		fx.requests = append(fx.requests, r.URL.Query())
		idx := min(fx.calls, len(fx.replies)-1)
		fx.calls++
		rep := fx.replies[idx]
		data, err := os.ReadFile(filepath.Join("testdata", "api", rep.file))
		require.NoError(t, err)
		w.Header().Set("Content-Type", "application/json")
		if rep.status != 0 && rep.status != http.StatusOK {
			w.WriteHeader(rep.status)
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(fx.srv.Close)
	return fx
}

func TestEnvKeyIsTheNameEveryOtherSteamToolUses(t *testing.T) {
	src := steamworkshop.New(steamworkshop.Options{SteamRoots: []string{t.TempDir()}})
	var provider source.EnvKeyProvider = src
	assert.Equal(t, "STEAM_WEB_API_KEY", provider.EnvKey())
}

func TestAuthInstructionsNameTheFreeKeyAndItsConfidentiality(t *testing.T) {
	src := steamworkshop.New(steamworkshop.Options{SteamRoots: []string{t.TempDir()}})
	var provider source.AuthInstructionsProvider = src
	text := provider.AuthInstructions()
	assert.Contains(t, text, "/dev/apikey")
	assert.Contains(t, text, "never share it")
}

func TestCapabilities_Tier2DeclaresSearchAndAuth(t *testing.T) {
	src := steamworkshop.New(steamworkshop.Options{SteamRoots: []string{t.TempDir()}})
	assert.Equal(t,
		source.Capabilities{Search: true, Dependencies: false, Updates: true, Auth: true, Versions: false},
		src.Capabilities())
}

func TestValidateKey_ProbesQueryFilesWithTheKeyAndAcceptsZeroResults(t *testing.T) {
	fx := serveRoutes(t, reply{file: "queryfiles_empty.json"})
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	var validator source.KeyValidator = src
	require.NoError(t, validator.ValidateKey(context.Background(), "abc123"))

	require.Len(t, fx.requests, 1)
	assert.Equal(t, "/IPublishedFileService/QueryFiles/v1/", fx.paths[0])
	q := fx.requests[0]
	assert.Equal(t, "abc123", q.Get("key"), "the probe must be the KEYED call - a keyless one validates nothing")
	assert.Equal(t, "1", q.Get("query_type"))
	assert.Equal(t, "1", q.Get("numperpage"))
	assert.Equal(t, "480", q.Get("appid"), "Spacewar is present for every account")
}

func TestValidateKey_403IsAnInvalidKey(t *testing.T) {
	fx := serveRoutes(t, reply{status: http.StatusForbidden, file: "queryfiles_403.json"})
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	err := src.ValidateKey(context.Background(), "wrong")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid Steam Web API key")
	assert.NotContains(t, err.Error(), "wrong", "a validator must never echo the key")
}

func TestValidateKey_LeavesTheRegisteredKeyAlone(t *testing.T) {
	fx := serveRoutes(t, reply{file: "queryfiles_empty.json"}, reply{file: "queryfiles_page1.json"})
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)
	src.SetAPIKey("registered")

	require.NoError(t, src.ValidateKey(context.Background(), "candidate"))
	_, err := src.Search(context.Background(), source.SearchQuery{GameID: "1133870", Query: "ship"})
	require.NoError(t, err)

	assert.Equal(t, "candidate", fx.requests[0].Get("key"))
	assert.Equal(t, "registered", fx.requests[1].Get("key"),
		"validating a candidate key must not overwrite the one the source was registered with")
}

func TestSearch_WithoutAKeyIsAuthRequired(t *testing.T) {
	fx := serveRoutes(t, reply{status: http.StatusForbidden, file: "queryfiles_403.json"})
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)

	_, err := src.Search(context.Background(), source.SearchQuery{GameID: "1133870", Query: "ship"})
	require.ErrorIs(t, err, domain.ErrAuthRequired)
}

func TestSearch_AStoredKeyValveRefusesIsAlsoAuthRequired(t *testing.T) {
	fx := serveRoutes(t, reply{status: http.StatusForbidden, file: "queryfiles_403.json"})
	src := newTestSource(t, fx.srv.URL, t.TempDir(), nil)
	src.SetAPIKey("stale")

	_, err := src.Search(context.Background(), source.SearchQuery{GameID: "1133870", Query: "ship"})
	require.ErrorIs(t, err, domain.ErrAuthRequired)
	assert.False(t, strings.Contains(err.Error(), "stale"), "the 403 body must not carry the key back")
}
