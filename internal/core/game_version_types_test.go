package core_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// versionTypeSource adds source.GameVersionTypeLister to the catalog-less
// double (#563), recording what it was asked so a test can prove a pure-id
// resolution never calls it.
type versionTypeSource struct {
	catalogLessSource
	types   []source.GameVersionType
	listErr error

	calls     int
	askedGame string
	ctxGameID string
}

func (m *versionTypeSource) ListGameVersionTypes(ctx context.Context, sourceGameID string) ([]source.GameVersionType, error) {
	m.calls++
	m.askedGame = sourceGameID
	if g := source.GameFromContext(ctx); g != nil {
		m.ctxGameID = g.ID
	}
	return m.types, m.listErr
}

var _ source.GameVersionTypeLister = (*versionTypeSource)(nil)

// newVersionTypeService registers a lister as "versioned" and a plain source
// as "plain", and one game "wow-classic" mapping both, plus "unmapped" that
// maps neither.
func newVersionTypeService(t *testing.T, lister *versionTypeSource) *core.Service {
	t.Helper()
	svc := newGameAddService(t)
	lister.id, lister.name = "versioned", "Versioned"
	svc.RegisterSource(lister)
	svc.RegisterSource(&catalogLessSource{id: "plain", name: "Plain"})
	ctx := context.Background()
	require.NoError(t, svc.SaveGame(ctx, &domain.Game{
		ID: "wow-classic", Name: "WoW Classic", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"versioned": "1", "plain": "x"},
	}))
	require.NoError(t, svc.SaveGame(ctx, &domain.Game{
		ID: "unmapped", Name: "Unmapped", InstallPath: t.TempDir(), ModPath: t.TempDir(),
		SourceIDs: map[string]string{"nexusmods": "y"},
	}))
	return svc
}

func wowTypes() []source.GameVersionType {
	return []source.GameVersionType{
		{ID: 79434, Name: "WoW Mists of Pandaria Classic", Slug: "wow-mists-of-pandaria-classic"},
		{ID: 67408, Name: "WoW Classic", Slug: "wow-classic"},
		{ID: 517, Name: "WoW Retail", Slug: "game"},
	}
}

// The listing asks the source with the game's own identifier for it, with
// the game on the context, and answers the rows sorted by name.
func TestGameVersionTypes_ListsByName(t *testing.T) {
	lister := &versionTypeSource{types: wowTypes()}
	svc := newVersionTypeService(t, lister)

	report, err := svc.GameVersionTypes(context.Background(), "wow-classic", "versioned")
	require.NoError(t, err)
	assert.Equal(t, "1", lister.askedGame)
	assert.Equal(t, "wow-classic", lister.ctxGameID)
	assert.Equal(t, &core.GameVersionTypesReport{
		GameID: "wow-classic", SourceID: "versioned",
		Types: []core.GameVersionType{
			{ID: 67408, Name: "WoW Classic", Slug: "wow-classic"},
			{ID: 79434, Name: "WoW Mists of Pandaria Classic", Slug: "wow-mists-of-pandaria-classic"},
			{ID: 517, Name: "WoW Retail", Slug: "game"},
		},
	}, report)
}

// A source that publishes none is a list of none, never null.
func TestGameVersionTypes_EmptyIsAnEmptyList(t *testing.T) {
	svc := newVersionTypeService(t, &versionTypeSource{})
	report, err := svc.GameVersionTypes(context.Background(), "wow-classic", "versioned")
	require.NoError(t, err)
	assert.NotNil(t, report.Types)
	assert.Empty(t, report.Types)
}

func TestGameVersionTypes_SourceWithoutThemIsTyped(t *testing.T) {
	svc := newVersionTypeService(t, &versionTypeSource{})
	_, err := svc.GameVersionTypes(context.Background(), "wow-classic", "plain")
	assert.ErrorIs(t, err, core.ErrNoGameVersionTypes)
}

func TestGameVersionTypes_UnknownGame(t *testing.T) {
	svc := newVersionTypeService(t, &versionTypeSource{})
	_, err := svc.GameVersionTypes(context.Background(), "nope", "versioned")
	assert.ErrorIs(t, err, domain.ErrGameNotFound)
}

func TestGameVersionTypes_UnknownSource(t *testing.T) {
	svc := newVersionTypeService(t, &versionTypeSource{})
	_, err := svc.GameVersionTypes(context.Background(), "wow-classic", "nope")
	require.Error(t, err)
	assert.NotErrorIs(t, err, core.ErrNoGameVersionTypes)
}

// A game that does not use the source has no identifier to ask with.
func TestGameVersionTypes_GameNotMappingTheSource(t *testing.T) {
	lister := &versionTypeSource{types: wowTypes()}
	svc := newVersionTypeService(t, lister)
	_, err := svc.GameVersionTypes(context.Background(), "unmapped", "versioned")
	var idErr *core.GameIdentifierError
	require.ErrorAs(t, err, &idErr)
	assert.Equal(t, "versioned", idErr.Source)
	assert.Zero(t, lister.calls)
}

// The source's own failure - here a missing key - survives the wrapping.
func TestGameVersionTypes_SourceErrorPropagates(t *testing.T) {
	svc := newVersionTypeService(t, &versionTypeSource{listErr: domain.ErrAuthRequired})
	_, err := svc.GameVersionTypes(context.Background(), "wow-classic", "versioned")
	assert.ErrorIs(t, err, domain.ErrAuthRequired)
}

// Numbers are ids already: resolving them never asks the source, so it works
// offline and for a game that does not map the source.
func TestResolveGameVersionTypes_NumbersNeedNoLookup(t *testing.T) {
	lister := &versionTypeSource{listErr: errors.New("offline")}
	svc := newVersionTypeService(t, lister)
	ids, err := svc.ResolveGameVersionTypes(context.Background(), "unmapped", "versioned", nil, []string{"67408", " 517 "})
	require.NoError(t, err)
	assert.Equal(t, []int{67408, 517}, ids)
	assert.Zero(t, lister.calls)
}

// A slug is matched exactly, case aside, and asks the source once however
// many slugs there are.
func TestResolveGameVersionTypes_Slugs(t *testing.T) {
	lister := &versionTypeSource{types: wowTypes()}
	svc := newVersionTypeService(t, lister)
	ids, err := svc.ResolveGameVersionTypes(context.Background(), "wow-classic", "versioned", nil,
		[]string{"wow-classic", "517", "WoW-Mists-Of-Pandaria-Classic"})
	require.NoError(t, err)
	assert.Equal(t, []int{67408, 517, 79434}, ids)
	assert.Equal(t, 1, lister.calls)
}

// A wrong guess fails with the answer: every valid type's name, slug and id.
func TestResolveGameVersionTypes_UnknownListsTheValidTypes(t *testing.T) {
	svc := newVersionTypeService(t, &versionTypeSource{types: wowTypes()})
	_, err := svc.ResolveGameVersionTypes(context.Background(), "wow-classic", "versioned", nil, []string{"classic"})
	var typeErr *core.GameVersionTypeError
	require.ErrorAs(t, err, &typeErr)
	assert.Equal(t, "classic", typeErr.Value)
	assert.Equal(t, core.GameVersionTypeUnknown, typeErr.Reason)
	assert.Len(t, typeErr.Types, 3)
	assert.Same(t, typeErr, typeErr.Details())
	for _, want := range []string{`"classic"`, "WoW Classic", "wow-classic", "67408", "WoW Retail", "game", "517"} {
		assert.Contains(t, err.Error(), want)
	}
}

// A partial slug is not a match: "wow" names no type.
func TestResolveGameVersionTypes_PrefixIsNotAMatch(t *testing.T) {
	svc := newVersionTypeService(t, &versionTypeSource{types: wowTypes()})
	_, err := svc.ResolveGameVersionTypes(context.Background(), "wow-classic", "versioned", nil, []string{"wow"})
	var typeErr *core.GameVersionTypeError
	require.ErrorAs(t, err, &typeErr)
	assert.Equal(t, core.GameVersionTypeUnknown, typeErr.Reason)
}

// Two types sharing a slug cannot be told apart by it, so neither is picked.
func TestResolveGameVersionTypes_AmbiguousSlugFails(t *testing.T) {
	types := append(wowTypes(), source.GameVersionType{ID: 999, Name: "Another Classic", Slug: "wow-classic"})
	svc := newVersionTypeService(t, &versionTypeSource{types: types})
	_, err := svc.ResolveGameVersionTypes(context.Background(), "wow-classic", "versioned", nil, []string{"wow-classic"})
	var typeErr *core.GameVersionTypeError
	require.ErrorAs(t, err, &typeErr)
	assert.Equal(t, core.GameVersionTypeAmbiguous, typeErr.Reason)
	assert.ElementsMatch(t, []int{67408, 999}, []int{typeErr.Types[0].ID, typeErr.Types[1].ID})
	assert.Contains(t, err.Error(), "by id")
}

// A slug needs the list, so the lookup's own failure is the answer.
func TestResolveGameVersionTypes_SlugLookupFailure(t *testing.T) {
	svc := newVersionTypeService(t, &versionTypeSource{listErr: domain.ErrAuthRequired})
	_, err := svc.ResolveGameVersionTypes(context.Background(), "wow-classic", "versioned", nil, []string{"wow-classic"})
	assert.ErrorIs(t, err, domain.ErrAuthRequired)
	assert.Contains(t, err.Error(), `"wow-classic"`)
}

// An edit that re-points the source resolves its slugs against the mapping
// it will save, not the one games.yaml holds now (#563 review).
func TestResolveGameVersionTypes_UsesTheEditsMapping(t *testing.T) {
	lister := &versionTypeSource{types: wowTypes()}
	svc := newVersionTypeService(t, lister)
	ids, err := svc.ResolveGameVersionTypes(context.Background(), "wow-classic", "versioned",
		map[string]string{"versioned": "432"}, []string{"wow-classic"})
	require.NoError(t, err)
	assert.Equal(t, []int{67408}, ids)
	assert.Equal(t, "432", lister.askedGame)
}

// An edit that adds the mapping can name slugs in the same run.
func TestResolveGameVersionTypes_EditAddsTheMapping(t *testing.T) {
	lister := &versionTypeSource{types: wowTypes()}
	svc := newVersionTypeService(t, lister)
	ids, err := svc.ResolveGameVersionTypes(context.Background(), "unmapped", "versioned",
		map[string]string{"nexusmods": "y", "versioned": "1"}, []string{"wow-classic"})
	require.NoError(t, err)
	assert.Equal(t, []int{67408}, ids)
	assert.Equal(t, "1", lister.askedGame)
}

// An edit that drops the mapping has nothing to look a slug up in: refused,
// saying so, without asking the source.
func TestResolveGameVersionTypes_EditRemovesTheMapping(t *testing.T) {
	lister := &versionTypeSource{types: wowTypes()}
	svc := newVersionTypeService(t, lister)
	_, err := svc.ResolveGameVersionTypes(context.Background(), "wow-classic", "versioned",
		map[string]string{"plain": "x"}, []string{"wow-classic"})
	var idErr *core.GameIdentifierError
	require.ErrorAs(t, err, &idErr)
	assert.Contains(t, err.Error(), "removes")
	assert.Zero(t, lister.calls)

	// Ids still need no mapping.
	ids, err := svc.ResolveGameVersionTypes(context.Background(), "wow-classic", "versioned",
		map[string]string{"plain": "x"}, []string{"517"})
	require.NoError(t, err)
	assert.Equal(t, []int{517}, ids)
}
