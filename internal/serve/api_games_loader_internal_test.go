package serve

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// addLoaderGame adds a game through POST /api/v1/games with an install
// directory carrying the on-disk markers of a Proton Unity Mono build, so the
// detail document has something real to detect. Returns the install path.
func addLoaderGame(t *testing.T, s *Server, id, loaderBody string) string {
	t.Helper()
	install := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(install, "UnityPlayer.dll"), []byte("pe"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(install, "Game_Data", "Managed"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(install, "Game_Data", "Managed", "Assembly-CSharp.dll"), []byte("x"), 0o644))

	body := `{"source_id":"nexusmods","identifier":"` + id + `","name":"` + id + `","game_id":"` + id +
		`","install_path":` + jsonString(install) + `,"mod_path":` + jsonString(install)
	if loaderBody != "" {
		body += `,"loader":` + loaderBody
	}
	body += "}"

	rec := doAPI(s, http.MethodPost, "/api/v1/games", body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return install
}

// TestAPIGameAdd_CarriesTheLoaderDeclaration: #359's additive wire member on
// the add body, so the web form configures a BepInEx game in one step just as
// `lmm game add --loader` does.
func TestAPIGameAdd_CarriesTheLoaderDeclaration(t *testing.T) {
	s := newGamesServer(t)
	addLoaderGame(t, s, "valheim", `{"kind":"bepinex","version":"5.4.23.5","bootstrap":"proton"}`)

	game, err := s.svc.GetGame("valheim")
	require.NoError(t, err)
	require.NotNil(t, game.Loader)
	assert.Equal(t, domain.LoaderKindBepInEx, game.Loader.Kind)
	assert.Equal(t, domain.LoaderBootstrapProton, game.Loader.Bootstrap)
}

// A rejected loader value is 400 carrying core.GameSpecError's {field, value,
// reason} with the field naming the offending SELECT, not the whole block -
// which is what lets the form mark it rather than parsing a sentence.
func TestAPIGameAdd_RejectsAnUnknownLoaderValueNamingTheField(t *testing.T) {
	s := newGamesServer(t)
	install := t.TempDir()
	rec := doAPI(s, http.MethodPost, "/api/v1/games",
		`{"source_id":"nexusmods","identifier":"g","name":"G","install_path":`+jsonString(install)+
			`,"loader":{"kind":"bepinex","runtime":"coreclr"}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	var envelope struct {
		Details struct {
			Field  string `json:"field"`
			Value  string `json:"value"`
			Reason string `json:"reason"`
		} `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	assert.Equal(t, "loader.runtime", envelope.Details.Field)
	assert.Equal(t, "coreclr", envelope.Details.Value)
	assert.Contains(t, envelope.Details.Reason, "mono, il2cpp")
}

// TestAPIGameDetail_CarriesTheLaunchOptionToPaste is the web game page's
// hydrate, and the reason the route exists: lmm hands the exact Steam launch
// option over as DATA because it will not write it into Steam's own
// configuration.
func TestAPIGameDetail_CarriesTheLaunchOptionToPaste(t *testing.T) {
	s := newGamesServer(t)
	addLoaderGame(t, s, "valheim", `{"kind":"bepinex","version":"5.4.23.5"}`)

	rec := doAPI(s, http.MethodGet, "/api/v1/games/valheim", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var detail core.GameDetail
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &detail))
	assert.Equal(t, "valheim", detail.ID)
	require.NotNil(t, detail.Loader)
	// Detected from the markers, not declared - the declaration answered
	// neither question.
	assert.Equal(t, domain.LoaderRuntimeMono, detail.Loader.DetectedRuntime)
	assert.Equal(t, domain.LoaderBootstrapProton, detail.Loader.DetectedBootstrap)
	assert.Equal(t, core.BepInExLaunchOptionProton, detail.Loader.LaunchOption)
	assert.False(t, detail.Loader.Installed)
}

// TestAPIGameSources_PUTEditsTheLoader: the same route edits the loader when
// the body carries one, additively - the SPA sends the block it changed.
func TestAPIGameSources_PUTEditsTheLoader(t *testing.T) {
	s := newGamesServer(t)
	addLoaderGame(t, s, "valheim", "")

	rec := doAPI(s, http.MethodPut, "/api/v1/games/valheim",
		`{"loader":{"kind":"bepinex","version":"5.4.23.5","bootstrap":"native"}}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var entry core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &entry))
	require.NotNil(t, entry.Loader)
	assert.Equal(t, domain.LoaderBootstrapNative, entry.Loader.Bootstrap)

	// "loader_set" with no loader is how the form says "remove it" - the
	// difference between an absent member and a null one.
	rec = doAPI(s, http.MethodPut, "/api/v1/games/valheim", `{"loader_set":true}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	// A FRESH value: decoding into the one above would leave the previous
	// response's pointer in place, since the cleared document carries no
	// "loader" member at all.
	var cleared core.GameListEntry
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &cleared))
	assert.Nil(t, cleared.Loader)
}

// #527: the loader is one more member of the same edit, so a body carrying
// it beside the source map applies both - in one write.
func TestAPIGameSources_PUTAppliesALoaderWithTheOtherFields(t *testing.T) {
	s := newGamesServer(t)
	addLoaderGame(t, s, "valheim", "")

	rec := doAPI(s, http.MethodPut, "/api/v1/games/valheim",
		`{"name":"Valheim (modded)","sources":{"nexusmods":"valheim"},"loader":{"kind":"bepinex"}}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	game, err := s.svc.GetGame("valheim")
	require.NoError(t, err)
	assert.Equal(t, "Valheim (modded)", game.Name)
	assert.Equal(t, map[string]string{"nexusmods": "valheim"}, game.SourceIDs)
	require.NotNil(t, game.Loader)
	assert.Equal(t, "bepinex", game.Loader.Kind)
}

// #527: and a body whose loader is refused writes none of the others -
// the 400 names the loader field, which is where the Setup editor marks it.
func TestAPIGameSources_PUTARefusedLoaderWritesNothing(t *testing.T) {
	s := newGamesServer(t)
	addLoaderGame(t, s, "valheim", "")

	rec := doAPI(s, http.MethodPut, "/api/v1/games/valheim",
		`{"name":"Renamed","adapter":"generic-files","loader":{"kind":"bepinex","runtime":"dotnet"}}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	var env struct {
		Details core.GameSpecError `json:"details"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &env))
	assert.Equal(t, "loader.runtime", env.Details.Field)

	game, err := s.svc.GetGame("valheim")
	require.NoError(t, err)
	assert.Nil(t, game.Loader, "nothing is written when the request is refused")
	assert.Empty(t, game.Adapter)
	assert.NotEqual(t, "Renamed", game.Name)
}
