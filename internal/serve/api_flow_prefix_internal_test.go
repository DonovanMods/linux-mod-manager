package serve

// #636 through the entry points the SPA drives: on a Baldur's Gate 3 game
// the deploy plan names a .pak's place in the prefix "<prefix>/...", the
// deploy job puts it in the Mods folder there (keeping the user's own pak
// of that name first), the mod's files and the Health reads name its
// root, the purge job puts the user's pak back, and a game whose prefix
// cannot be resolved has its deploy plan refused with 409.

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/app"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bg3ModsRel is Baldur's Gate 3's Mods folder under the prefix's user
// directory.
const bg3ModsRel = "AppData/Local/Larian Studios/Baldur's Gate 3/Mods"

// prefixFlowFixture is the deploy fixture made a Baldur's Gate 3 game on
// the baldurs-gate-3 adapter, with an explicit Proton-shaped prefix (or
// none, for withPrefix false). Its mod m1 ships Mods/one.pak, which the
// adapter places in the prefix's Mods folder.
func prefixFlowFixture(t *testing.T, withPrefix bool) (*Server, *core.Service, *domain.Game, string) {
	t.Helper()
	s, svc, game := newFlowFixtureServer(t)
	app.RegisterAdapters(svc)
	game.ModPath = filepath.Join(game.InstallPath, "Data")
	game.Adapter = "baldurs-gate-3"
	prefix := ""
	if withPrefix {
		prefix = filepath.Join(t.TempDir(), "pfx")
		require.NoError(t, os.MkdirAll(filepath.Join(prefix, "drive_c", "users", "steamuser", "AppData", "Local"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(prefix, "system.reg"), []byte("WINE REGISTRY Version 2\n"), 0o644))
		game.Prefix, game.PrefixUser = prefix, "steamuser"
	}
	require.NoError(t, os.MkdirAll(game.ModPath, 0o755))
	require.NoError(t, svc.SaveGame(context.Background(), game))
	return s, svc, game, prefix
}

func TestFlowPrefix_DeployHealthAndPurge(t *testing.T) {
	s, _, game, prefix := prefixFlowFixture(t, true)
	mods := filepath.Join(prefix, "drive_c", "users", "steamuser", filepath.FromSlash(bg3ModsRel))
	require.NoError(t, os.MkdirAll(mods, 0o755))
	pak := filepath.Join(mods, "one.pak")
	require.NoError(t, os.WriteFile(pak, []byte("the user's own"), 0o644))

	_, raw := planFlow(t, s, game, "deploy", `{}`)
	assert.Contains(t, string(raw), `"<prefix>/`+bg3ModsRel+`/one.pak"`)

	j := runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	got, err := os.ReadFile(pak)
	require.NoError(t, err)
	assert.NotEqual(t, "the user's own", string(got), "the mod's pak is in the Mods folder")
	_, err = os.Lstat(filepath.Join(game.ModPath, "Mods"))
	assert.True(t, os.IsNotExist(err), "not into Data/")
	res, ok := j.status().Result.(*core.DeployResult)
	require.True(t, ok, "%T", j.status().Result)
	assert.Contains(t, res.Warnings, "Baldur's Gate 3 loads a .pak mod from its Mods folder only once the game's mod list, modsettings.lsx (PlayerProfiles/Public, beside the Mods folder in the game's prefix), enables it; lmm doesn't write that file yet.",
		"the job's result carries the same advice the CLI prints")

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/files", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"path": "`+bg3ModsRel+`/one.pak"`)
	assert.Contains(t, rec.Body.String(), `"root": "prefix"`)

	// The user swaps lmm's pak for another: Health names its root.
	require.NoError(t, os.Remove(pak))
	require.NoError(t, os.WriteFile(pak, []byte("swapped"), 0o644))
	rec = doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&force=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"file_id": "`+bg3ModsRel+`/one.pak"`)
	assert.Contains(t, rec.Body.String(), `"root": "prefix"`)
	assert.Contains(t, rec.Body.String(), "<prefix>/"+bg3ModsRel+"/one.pak")

	// Back to lmm's, so the purge removes it and returns the user's own.
	require.NoError(t, os.Remove(pak))
	j = runFlow(t, s, game, "deploy", `{}`, "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	j = runFlow(t, s, game, "purge", "", "")
	require.Equal(t, jobSucceeded, j.status().State, "job failed: %+v", j.status().Error)
	info, err := os.Lstat(pak)
	require.NoError(t, err)
	assert.True(t, info.Mode().IsRegular())
	assert.Equal(t, "the user's own", readFile(t, pak), "purge puts the user's pak back")
}

// A prefix lmm cannot resolve refuses the deploy plan with 409 and the
// message naming what to set - the CLI's refusal, verbatim.
func TestFlowPrefix_UnresolvedPrefixRefusesThePlan(t *testing.T) {
	s, _, game, _ := prefixFlowFixture(t, false)
	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/deploy", game), `{}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "--prefix")
	_, err := os.Lstat(filepath.Join(game.ModPath, "Mods"))
	assert.True(t, os.IsNotExist(err), "nothing falls back to Data/")
}
