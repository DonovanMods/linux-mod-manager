package main

import (
	"context"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockVersionTypeSource stands in for CurseForge's version-type list (#563)
// without the network: a source.GameVersionTypeLister registered as
// "curseforge".
type mockVersionTypeSource struct {
	mockGameAddSource
	types   []source.GameVersionType
	listErr error
	calls   int
}

func (m *mockVersionTypeSource) ListGameVersionTypes(context.Context, string) ([]source.GameVersionType, error) {
	m.calls++
	return m.types, m.listErr
}

var _ source.GameVersionTypeLister = (*mockVersionTypeSource)(nil)

// setupVersionTypeTest is setupGameEditTest plus a CurseForge double and a
// "wow-classic" game mapping it to game 1, accepting Classic (67408) and an
// id CurseForge no longer lists (12345).
func setupVersionTypeTest(t *testing.T, listErr error) (*core.Service, *mockVersionTypeSource) {
	t.Helper()
	svc := setupGameEditTest(t)
	cf := &mockVersionTypeSource{
		mockGameAddSource: mockGameAddSource{id: "curseforge", name: "CurseForge"},
		types: []source.GameVersionType{
			{ID: 79434, Name: "WoW Mists of Pandaria Classic", Slug: "wow-mists-of-pandaria-classic"},
			{ID: 67408, Name: "WoW Classic", Slug: "wow-classic"},
			{ID: 517, Name: "WoW Retail", Slug: "game"},
		},
		listErr: listErr,
	}
	svc.RegisterSource(cf)
	require.NoError(t, svc.SaveGame(context.Background(), &domain.Game{
		ID: "wow-classic", Name: "World of Warcraft Classic",
		InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs:  map[string]string{"curseforge": "1"},
		CurseForge: &domain.GameCurseForge{GameVersionTypes: []int{67408, 12345}},
	}))
	return svc, cf
}

// The listing names every type by name, slug and id, marks the ones the
// game accepts, and still shows a stored id CurseForge no longer lists.
func TestDoGameVersionTypes_Lists(t *testing.T) {
	svc, _ := setupVersionTypeTest(t, nil)

	out := captureStdout(t, func() error {
		return doGameVersionTypes(context.Background(), svc, "wow-classic")
	})
	for _, want := range []string{"WoW Classic", "wow-classic", "67408", "WoW Retail", "game", "517", "12345"} {
		assert.Contains(t, out, want)
	}
	assert.Regexp(t, `✓\s+WoW Classic`, out)
	assert.NotRegexp(t, `✓\s+WoW Retail`, out)
}

func TestJSONGolden_GameVersionTypes(t *testing.T) {
	svc, _ := setupVersionTypeTest(t, nil)
	withJSONOutput(t)

	out := captureStdout(t, func() error {
		return doGameVersionTypes(context.Background(), svc, "wow-classic")
	})
	assertJSONCLIGolden(t, "game_version_types", out)
}

// No key is the usual "run lmm auth login" prompt.
func TestDoGameVersionTypes_AuthRequired(t *testing.T) {
	svc, _ := setupVersionTypeTest(t, domain.ErrAuthRequired)

	err := doGameVersionTypes(context.Background(), svc, "wow-classic")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lmm auth login curseforge")
}

// #563: --curseforge-version-type takes CurseForge's slug as well as an id;
// games.yaml still stores the id.
func TestDoGameEdit_CurseForgeVersionTypeSlug(t *testing.T) {
	svc, _ := setupVersionTypeTest(t, nil)
	setCurseForgeTypeFlags(t, "wow-mists-of-pandaria-classic", "517")

	captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "wow-classic", false)
	})
	game, err := svc.GetGame("wow-classic")
	require.NoError(t, err)
	require.NotNil(t, game.CurseForge)
	assert.Equal(t, []int{79434, 517}, game.CurseForge.GameVersionTypes)
}

// Ids alone never ask CurseForge, so they work offline.
func TestDoGameEdit_CurseForgeVersionTypeIDsNeedNoLookup(t *testing.T) {
	svc, cf := setupVersionTypeTest(t, domain.ErrAuthRequired)
	setCurseForgeTypeFlags(t, "517")

	captureStdout(t, func() error {
		return doGameEdit(context.Background(), svc, "wow-classic", false)
	})
	assert.Zero(t, cf.calls)
	game, err := svc.GetGame("wow-classic")
	require.NoError(t, err)
	assert.Equal(t, []int{517}, game.CurseForgeVersionTypes())
}

// A wrong guess fails with the answer, and nothing is written.
func TestDoGameEdit_CurseForgeVersionTypeUnknownListsTypes(t *testing.T) {
	svc, _ := setupVersionTypeTest(t, nil)
	setCurseForgeTypeFlags(t, "classic")

	err := doGameEdit(context.Background(), svc, "wow-classic", false)
	var typeErr *core.GameVersionTypeError
	require.ErrorAs(t, err, &typeErr)
	for _, want := range []string{`"classic"`, "WoW Classic", "wow-classic", "67408", "WoW Retail", "game", "517"} {
		assert.Contains(t, err.Error(), want)
	}
	game, err := svc.GetGame("wow-classic")
	require.NoError(t, err)
	assert.Equal(t, []int{67408, 12345}, game.CurseForgeVersionTypes())
}

// A slug needs the list; without a key that is the auth prompt.
func TestDoGameEdit_CurseForgeVersionTypeSlugNeedsAuth(t *testing.T) {
	svc, _ := setupVersionTypeTest(t, domain.ErrAuthRequired)
	setCurseForgeTypeFlags(t, "wow-classic")

	err := doGameEdit(context.Background(), svc, "wow-classic", false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "lmm auth login curseforge")
}

func TestReportError_JSON_GameVersionTypeError(t *testing.T) {
	svc, _ := setupVersionTypeTest(t, nil)
	_, err := svc.ResolveGameVersionTypes(context.Background(), "wow-classic", "curseforge", []string{"classic"})
	require.Error(t, err)
	withJSONOutput(t)

	out := captureStdout(t, func() error { reportError(err); return nil })
	assert.Equal(t, "{\n"+
		"  \"error\": \"\\\"classic\\\" is not a CurseForge game version type for wow-classic; give one of these by slug or id:\\n"+
		"  WoW Classic (slug wow-classic, id 67408)\\n"+
		"  WoW Mists of Pandaria Classic (slug wow-mists-of-pandaria-classic, id 79434)\\n"+
		"  WoW Retail (slug game, id 517)\",\n"+
		"  \"details\": {\n"+
		"    \"game_id\": \"wow-classic\",\n"+
		"    \"source_id\": \"curseforge\",\n"+
		"    \"value\": \"classic\",\n"+
		"    \"reason\": \"unknown\",\n"+
		"    \"types\": [\n"+
		"      {\n"+
		"        \"id\": 67408,\n"+
		"        \"name\": \"WoW Classic\",\n"+
		"        \"slug\": \"wow-classic\"\n"+
		"      },\n"+
		"      {\n"+
		"        \"id\": 79434,\n"+
		"        \"name\": \"WoW Mists of Pandaria Classic\",\n"+
		"        \"slug\": \"wow-mists-of-pandaria-classic\"\n"+
		"      },\n"+
		"      {\n"+
		"        \"id\": 517,\n"+
		"        \"name\": \"WoW Retail\",\n"+
		"        \"slug\": \"game\"\n"+
		"      }\n"+
		"    ]\n"+
		"  }\n"+
		"}\n", out)
}
