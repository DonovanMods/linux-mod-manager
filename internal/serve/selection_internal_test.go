package serve

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveSelection_BuildsNoGameRows (#413 re-review L5): a game row
// resolves its adapter, which for a game with no adapter key can stat the
// install directory - so building every game's row on every scoped request
// made each request touch every game's disk, and one game on a hung network
// share hung them all. The rows are only the 404's list of valid choices,
// so they are built there and nowhere else.
func TestResolveSelection_BuildsNoGameRows(t *testing.T) {
	s, game := newLiveFixtureServer(t)
	var built atomic.Int32
	s.gameRows = func(ctx context.Context) ([]core.GameListEntry, error) {
		built.Add(1)
		return s.svc.ListGameEntries(ctx)
	}

	get := func(query string) int {
		return doAPI(s, http.MethodGet, "/api/v1/mods"+query, "").Code
	}

	require.Equal(t, http.StatusOK, get("?game="+game.ID))
	assert.Zero(t, built.Load(), "a resolved selection needs no game rows")

	require.Equal(t, http.StatusNotFound, get("?game=nope"))
	assert.Equal(t, int32(1), built.Load(), "the 404 lists the valid games")
}
