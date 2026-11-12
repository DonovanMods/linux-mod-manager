package serve

// #605: PUT /api/v1/games/{id}'s `prefix` and `prefix_user` members set
// and clear a game's Wine/Proton prefix - asserted against games.yaml, the
// end state - and GET /api/v1/games/{id} carries the resolved prefix.

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAPIGameSources_PrefixSetAndClear(t *testing.T) {
	s := newGameSourcesServer(t)
	pfx := filepath.Join(t.TempDir(), "pfx")
	require.NoError(t, os.MkdirAll(filepath.Join(pfx, "drive_c", "users", "dyoung"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pfx, "system.reg"), nil, 0o644))

	body, err := json.Marshal(map[string]string{"prefix": pfx, "prefix_user": "dyoung"})
	require.NoError(t, err)
	entry := putGame(t, s, string(body))
	assert.Equal(t, pfx, entry.Prefix)
	assert.Equal(t, "dyoung", entry.PrefixUser)
	assert.Contains(t, gamesYAML(t, s), "prefix: "+pfx)

	rec := doAPI(s, http.MethodGet, "/api/v1/games/skyrim-se", "")
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	var detail core.GameDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	require.NotNil(t, detail.Prefix)
	assert.True(t, detail.Prefix.Resolved, "%+v", detail.Prefix)
	assert.Equal(t, core.PrefixOriginExplicit, detail.Prefix.Origin)
	assert.Equal(t, "dyoung", detail.Prefix.User)

	// A body without the members leaves them alone.
	entry = putGame(t, s, `{"name":"Renamed"}`)
	assert.Equal(t, pfx, entry.Prefix)

	entry = putGame(t, s, `{"prefix":"","prefix_user":""}`)
	assert.Empty(t, entry.Prefix)
	assert.Empty(t, entry.PrefixUser)
	assert.NotContains(t, gamesYAML(t, s), "prefix")
}

// A relative prefix is a 400 naming the field, so the editor marks it.
func TestAPIGameSources_PrefixMustBeAbsolute(t *testing.T) {
	s := newGameSourcesServer(t)

	rec := doAPI(s, http.MethodPut, "/api/v1/games/skyrim-se", `{"prefix":"relative/pfx"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	var envelope struct {
		Details struct {
			Field string `json:"field"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "prefix", envelope.Details.Field)
	assert.NotContains(t, gamesYAML(t, s), "prefix")
}
