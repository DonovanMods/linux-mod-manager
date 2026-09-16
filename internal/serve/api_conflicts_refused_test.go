package serve_test

// #455: GET /api/v1/conflicts on a game whose adapter is refused answered
// 500 for a known, handled state. It is the refusal now - a 409 carrying
// the game and the adapter - which the conflicts card already renders as
// "Couldn't check for conflicts: <the refusal>".

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestServer_APIConflicts_ARefusedGameIsTheRefusalNot500(t *testing.T) {
	svc, game := twinConflictFixture(t)
	game.Adapter = "no-such-adapter"
	require.NoError(t, svc.SaveGame(t.Context(), game))

	srv := serve.New(t.Context(), svc, slog.New(slog.DiscardHandler), serve.Options{Addr: testAddr})
	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/conflicts", nil)
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code, "body: %s", rec.Body.String())
	var envelope struct {
		Error   string `json:"error"`
		Details struct {
			GameID  string `json:"game_id"`
			Adapter string `json:"adapter"`
		} `json:"details"`
	}
	decodeStrict(t, rec.Body.Bytes(), &envelope)
	assert.Contains(t, envelope.Error, `unknown adapter "no-such-adapter"`)
	assert.Equal(t, game.ID, envelope.Details.GameID)
	assert.Equal(t, "no-such-adapter", envelope.Details.Adapter)
}
