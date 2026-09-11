package serve

// POST /api/v1/mods/{source}/{id}/convert - the Icarus pak-conversion
// toggle (#326, epic live review C-3: "the one I would not defer - it is
// the pak-conversion toggle on a first-class supported game"). Its CLI
// twin is `lmm mod convert <mod-id> <on|off>`.

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// compileFixtureSource is fixtureSource plus adapter.MergeCompiler, so the
// fixture game can be DeployCompile - core resolves the game's ONE
// compile-capable source to classify a mod's retained files, and a game
// that maps none has no convertible format at all (which is the 400 this
// route's guard reports).
//
// Only ClassifyMergeSource is exercised; the rest satisfy the interface.
// Nothing here compiles or reads a real pak - that is internal/source and
// internal/core's ground to cover, not this package's.
type compileFixtureSource struct{ fixtureSource }

func (*compileFixtureSource) ValidateSource(string) error { return nil }
func (*compileFixtureSource) MergeCompile(context.Context, string, []adapter.MergeSource, string) ([]string, []adapter.MergeFailure, error) {
	return nil, nil, nil
}
func (*compileFixtureSource) ResolveBaseArtifact(*domain.Game) (string, error) { return "", nil }
func (*compileFixtureSource) FingerprintBase(string) (string, error)           { return "", nil }
func (*compileFixtureSource) IsNativeMergeSource(name string) bool             { return name == "exmodz" }
func (*compileFixtureSource) IsConvertibleArtifact(name string) bool           { return name == "pak" }
func (*compileFixtureSource) ClassifyMergeSource(id string) (string, bool) {
	if id == "pak" {
		return "pak", true
	}
	return "exmodz", false
}
func (*compileFixtureSource) MergedArtifactName() string            { return "zzz_LMM_Merged_P.pak" }
func (*compileFixtureSource) MergedArtifactLabel() string           { return "Merged Pak" }
func (*compileFixtureSource) RestoredArtifactName(id string) string { return id + "_P.pak" }

var _ adapter.MergeCompiler = (*compileFixtureSource)(nil)

// convertFixtureAdapter presents compileFixtureSource's format vocabulary as
// the GAME's adapter (#412). Since U2 that is where core looks: a
// `deploy_mode: compile` game with no explicit `adapter:` key derives the
// name "icarus", so registering under it keeps the fixture game below
// exactly as it was written.
type convertFixtureAdapter struct{ compileFixtureSource }

func (convertFixtureAdapter) ID() string    { return "icarus" }
func (convertFixtureAdapter) Label() string { return "Convert fixture" }
func (convertFixtureAdapter) NormalizeArchive(adapter.NormalizeRequest) (adapter.Layout, error) {
	return adapter.Layout{}, nil
}

// compileFixtureSource's methods have POINTER receivers, so only
// *convertFixtureAdapter carries the compile capability - asserted here
// rather than discovered as a 400 from a route that quietly decided the
// game has no convertible format.
var (
	_ adapter.GameAdapter   = (*convertFixtureAdapter)(nil)
	_ adapter.MergeCompiler = (*convertFixtureAdapter)(nil)
)

// newConvertServer builds a DeployCompile game whose one installed mod has
// a pak-kind retained file - the only state in which pak conversion means
// anything.
func newConvertServer(t *testing.T, fileIDs []string) (*Server, *core.Service, *domain.Game) {
	t.Helper()
	sandboxEnv(t)

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir(),
		Logger: slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	svc.RegisterSource(&compileFixtureSource{})
	// #412: compilation is a property of the GAME since U2, so the fixture's
	// format vocabulary has to be registered as the game's adapter - the
	// `deploy_mode: compile` game below derives exactly this id.
	svc.RegisterAdapter(&convertFixtureAdapter{})

	ctx := t.Context()
	game := &domain.Game{
		ID:          "g1",
		Name:        "Compile Game",
		InstallPath: t.TempDir(),
		ModPath:     t.TempDir(),
		LinkMethod:  domain.LinkSymlink,
		DeployMode:  domain.DeployCompile,
		ConvertPaks: true,
		SourceIDs:   map[string]string{fixtureSourceID: ""},
	}
	require.NoError(t, svc.SaveGame(ctx, game))
	_, err = svc.NewProfileManager().Create(ctx, game.ID, "default")
	require.NoError(t, err)
	require.NoError(t, svc.SetDefaultGame(ctx, game.ID))
	require.NoError(t, svc.SaveInstalledMod(ctx, &domain.InstalledMod{
		Mod:          domain.Mod{ID: "m1", SourceID: fixtureSourceID, Name: "Mod One", Version: "1.0", GameID: game.ID},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		ConvertPaks:  true,
		FileIDs:      fileIDs,
	}))
	require.NoError(t, svc.NewProfileManager().AddMod(ctx, game.ID, "default",
		domain.ModReference{SourceID: fixtureSourceID, ModID: "m1", Version: "1.0"}))

	return New(ctx, svc, slog.New(slog.DiscardHandler), Options{Addr: internalTestAddr}), svc, game
}

// TestAPIModConvert_TurnsConversionOffAndAnswersTheCoreDocument is the
// happy path: the DB flag moves and the response is the frozen
// core.ModSettingResult `lmm mod convert --json` emits.
func TestAPIModConvert_TurnsConversionOffAndAnswersTheCoreDocument(t *testing.T) {
	s, svc, game := newConvertServer(t, []string{"pak"})

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/convert", game), `{"enabled":false}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	var result core.ModSettingResult
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result, json.RejectUnknownMembers(true)))
	require.NotNil(t, result.ConvertPaks)
	assert.False(t, *result.ConvertPaks)

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.False(t, mod.ConvertPaks, "the write must have landed in the database")
}

// TestAPIModConvert_TurnsConversionBackOn is the same route's other
// direction, so the toggle is pinned both ways.
func TestAPIModConvert_TurnsConversionBackOn(t *testing.T) {
	s, svc, game := newConvertServer(t, []string{"pak"})

	require.Equal(t, http.StatusOK,
		doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/convert", game), `{"enabled":false}`).Code)
	rec := doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/convert", game), `{"enabled":true}`)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.ConvertPaks)
}

// TestAPIModConvert_NoConvertibleFormatIs400 pins the guard `lmm mod
// convert` applies before its own write: an exmodz-only mod has no pak to
// convert or leave raw, so the request is refused rather than storing a
// flag nothing reads.
func TestAPIModConvert_NoConvertibleFormatIs400(t *testing.T) {
	s, svc, game := newConvertServer(t, []string{"exmodz"})

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/convert", game), `{"enabled":false}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	assert.Contains(t, rec.Body.String(), "pak merge source")

	mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.ConvertPaks, "a refused request must not write")
}

// TestAPIModConvert_UnknownModIs404 - the same not-found treatment its
// lock/policy siblings give.
func TestAPIModConvert_UnknownModIs404(t *testing.T) {
	s, _, game := newConvertServer(t, []string{"pak"})

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/nope/convert", game), `{"enabled":false}`)
	assert.Equal(t, http.StatusNotFound, rec.Code, "body: %s", rec.Body.String())
}

// TestAPIModConvert_MissingEnabledIs400 is I-1: the wire contract says
// "enabled" is required, "since a missing member decoding to false would
// silently mean 'off'" (this file's request doc comment) - but json/v2 has
// no "required" struct tag, and decodeAPIBody short-circuits an empty body
// before any decode runs, so all three ways of omitting the member must be
// refused explicitly rather than landing on false. Before the fix, all
// three answered 200 and flipped ConvertPaks to false regardless of the
// mod's prior setting.
func TestAPIModConvert_MissingEnabledIs400(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"empty body", ""},
		{"empty object", `{}`},
		{"enabled explicitly null", `{"enabled":null}`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, svc, game := newConvertServer(t, []string{"pak"})

			rec := doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/convert", game), tt.body)
			require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
			assert.Contains(t, rec.Body.String(), "enabled")

			mod, err := svc.GetInstalledMod(t.Context(), fixtureSourceID, "m1", game.ID, "default")
			require.NoError(t, err)
			assert.True(t, mod.ConvertPaks, "a refused request must not write")
		})
	}
}

// TestAPIModConvert_RejectsUnknownMembers pins the strict decode.
func TestAPIModConvert_RejectsUnknownMembers(t *testing.T) {
	s, _, game := newConvertServer(t, []string{"pak"})

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/convert", game), `{"enabld":false}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
}

// TestAPIModConvert_RequiresCSRF.
func TestAPIModConvert_RequiresCSRF(t *testing.T) {
	s, _, game := newConvertServer(t, []string{"pak"})

	rec := doAPIWithoutCSRF(s, http.MethodPost, scoped("/api/v1/mods/"+fixtureSourceID+"/m1/convert", game), `{"enabled":false}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}
