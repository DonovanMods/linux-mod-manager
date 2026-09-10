package core_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// collectionTestSource adds source.CollectionResolver to the workshop fake,
// so core's collection-import rules are exercised without the concrete
// steamworkshop package (the boundary the design pins) and without any
// possibility of a call to Valve.
type collectionTestSource struct {
	*workshopTestSource
	collection source.Collection
	collErr    error
	collRef    string
}

func newCollectionTestSource() *collectionTestSource {
	return &collectionTestSource{workshopTestSource: newWorkshopTestSource()}
}

func (s *collectionTestSource) ResolveCollection(ctx context.Context, ref string) (source.Collection, error) {
	s.collRef = ref
	if s.collErr != nil {
		return source.Collection{}, s.collErr
	}
	return s.collection, nil
}

func newCollectionService(t *testing.T) (*core.Service, *domain.Game, *collectionTestSource) {
	t.Helper()
	svc := newFlowsTestService(t)
	src := newCollectionTestSource()
	svc.RegisterSource(src)
	game := &domain.Game{
		ID: "space-engineers-2", Name: "Space Engineers 2", ModPath: t.TempDir(),
		LinkMethod: domain.LinkSymlink,
		SourceIDs:  map[string]string{"steamworkshop": "1133870"},
	}
	_, err := svc.NewProfileManager().Create(context.Background(), game.ID, "default")
	require.NoError(t, err)
	return svc, game, src
}

func TestPlanWorkshopCollectionImport_BuildsAProfileOfWorkshopRefs(t *testing.T) {
	svc, game, src := newCollectionService(t)
	src.collection = source.Collection{
		ID: "2500900001", Name: "Cargo Ships", URL: "https://example.invalid/2500900001",
		ItemIDs: []string{"3617086610", "3512001122"},
	}
	src.describe = []source.ModDescription{
		{ModID: "3617086610", Mod: domain.Mod{ID: "3617086610", SourceID: "steamworkshop", Name: "Sample Workshop Item"}},
		{ModID: "3512001122", Mod: domain.Mod{ID: "3512001122", SourceID: "steamworkshop", Name: "Second Workshop Item"}},
	}

	plan, err := svc.PlanWorkshopCollectionImport(context.Background(), game, "",
		"https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001")
	require.NoError(t, err)

	assert.Equal(t, "https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001", src.collRef,
		"core passes what the user typed through verbatim - the SOURCE owns what it recognises")

	require.NotNil(t, plan.Profile)
	assert.Equal(t, "cargo-ships", plan.Profile.Name, "the collection's own name, as a profile name")
	assert.Equal(t, game.ID, plan.Profile.GameID)
	require.Len(t, plan.Profile.Mods, 2)
	assert.Equal(t, domain.ModReference{SourceID: "steamworkshop", ModID: "3617086610", External: true}, plan.Profile.Mods[0])
	assert.Equal(t, "3512001122", plan.Profile.Mods[1].ModID)

	require.NotNil(t, plan.WorkshopCollection)
	c := plan.WorkshopCollection
	assert.Equal(t, "steamworkshop", c.SourceID)
	assert.Equal(t, "2500900001", c.CollectionID)
	assert.Equal(t, "Cargo Ships", c.Name)
	assert.Equal(t, game.ID, c.GameID)
	require.Len(t, c.Items, 2)
	assert.Equal(t, "Sample Workshop Item", c.Items[0].Name)
	assert.False(t, c.Items[0].Tracked)
	assert.Equal(t, 0, c.Tracked)
	assert.Equal(t, 2, c.NotSubscribed)
}

func TestPlanWorkshopCollectionImport_AlreadySubscribedItemsAreTracked(t *testing.T) {
	svc, game, src := newCollectionService(t)
	src.collection = source.Collection{ID: "2500900001", Name: "Cargo Ships", ItemIDs: []string{"3617086610", "3512001122"}}

	// One item has already been adopted by `lmm import --workshop`.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "3617086610", SourceID: "steamworkshop", Name: "Sample Workshop Item", GameID: game.ID},
		ProfileName:  "default",
		Enabled:      true,
		External:     true,
		ExternalPath: "/steam/workshop/content/1133870/3617086610",
	}))

	plan, err := svc.PlanWorkshopCollectionImport(context.Background(), game, "", "2500900001")
	require.NoError(t, err)

	c := plan.WorkshopCollection
	require.Len(t, c.Items, 2)
	assert.True(t, c.Items[0].Tracked, "an item Tier 1 already adopted needs nothing")
	assert.Empty(t, c.Items[0].Note)
	assert.False(t, c.Items[1].Tracked)
	assert.Contains(t, c.Items[1].Note, "subscribe in Steam")
	assert.Contains(t, c.Items[1].Note, "lmm import --workshop")
	assert.Equal(t, 1, c.Tracked)
	assert.Equal(t, 1, c.NotSubscribed)
}

func TestPlanWorkshopCollectionImport_AnExplicitProfileNameWins(t *testing.T) {
	svc, game, src := newCollectionService(t)
	src.collection = source.Collection{ID: "2500900001", Name: "Cargo Ships", ItemIDs: []string{"3617086610"}}

	plan, err := svc.PlanWorkshopCollectionImport(context.Background(), game, "my-ships", "2500900001")
	require.NoError(t, err)
	assert.Equal(t, "my-ships", plan.Profile.Name)
}

func TestPlanWorkshopCollectionImport_AnUnnamedCollectionFallsBackToItsID(t *testing.T) {
	svc, game, src := newCollectionService(t)
	src.collection = source.Collection{ID: "2500900001", ItemIDs: []string{"3617086610"}}

	plan, err := svc.PlanWorkshopCollectionImport(context.Background(), game, "", "2500900001")
	require.NoError(t, err)
	assert.Equal(t, "workshop-collection-2500900001", plan.Profile.Name)
}

// TestPlanWorkshopCollectionImport_AHostileTitleCannotShapeTheProfileName
// is the adversarial half of the naming rule (W2 review, Minor 10). A
// collection title is chosen by a stranger on the internet and becomes a
// FILE NAME under $XDG_CONFIG_HOME, so every one of these has to come out
// the far side as a bounded, single-segment, dash-and-alphanumeric slug —
// or as the id fallback when nothing usable survives.
func TestPlanWorkshopCollectionImport_AHostileTitleCannotShapeTheProfileName(t *testing.T) {
	for name, tc := range map[string]struct{ title, want string }{
		"traversal":           {"../../etc/passwd", "etc-passwd"},
		"windows traversal":   {`..\..\windows\system32`, "windows-system32"},
		"absolute path":       {"/etc/shadow", "etc-shadow"},
		"dot segments only":   {"../..", "workshop-collection-2500900001"},
		"nul and control":     {"ships\x00\x1b[31m", "ships-31m"},
		"leading dot":         {".hidden", "hidden"},
		"yaml-ish":            {"a: b\n- c", "a-b-c"},
		"non-latin only":      {"貨物船", "workshop-collection-2500900001"},
		"punctuation only":    {"!!! ???", "workshop-collection-2500900001"},
		"very long":           {strings.Repeat("cargo ", 200), strings.Repeat("cargo-", 9) + "cargo"},
		"long unbroken":       {strings.Repeat("x", 500), strings.Repeat("x", 64)},
		"cap lands on a dash": {strings.Repeat("ab-", 40), strings.TrimSuffix(strings.Repeat("ab-", 21), "-")},
	} {
		t.Run(name, func(t *testing.T) {
			svc, game, src := newCollectionService(t)
			src.collection = source.Collection{
				ID: "2500900001", Name: tc.title, ItemIDs: []string{"3617086610"},
			}

			plan, err := svc.PlanWorkshopCollectionImport(context.Background(), game, "", "2500900001")
			require.NoError(t, err)

			got := plan.Profile.Name
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len(got), 64, "a profile name is bounded")
			assert.NotContains(t, got, "..")
			assert.NotContains(t, got, "/")
			assert.NotContains(t, got, `\`)
			assert.Regexp(t, `^[a-z0-9][a-z0-9-]*$`, got, "traversal-safe by construction")

			// The name is not merely well-formed: the profile it produces
			// must actually save, which is what proves it is a legal path
			// segment rather than one that fails at the filesystem.
			result, err := svc.ApplyWorkshopCollectionImport(context.Background(), game, plan,
				core.ProfileImportOptions{}, nil)
			require.NoError(t, err)
			assert.Equal(t, got, result.ProfileName)
		})
	}
}

func TestPlanWorkshopCollectionImport_NoWorkshopSourceForTheGame(t *testing.T) {
	svc := newFlowsTestService(t)
	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	_, err := svc.PlanWorkshopCollectionImport(context.Background(), game, "", "2500900001")
	require.Error(t, err)
	assert.True(t, core.IsNoWorkshopSource(err), "a game with no workshop mapping is bad input, not a server fault")
}

func TestPlanWorkshopCollectionImport_ResolveFailurePropagates(t *testing.T) {
	svc, game, src := newCollectionService(t)
	src.collErr = errors.New("not a Steam Workshop collection id or URL")

	_, err := svc.PlanWorkshopCollectionImport(context.Background(), game, "", "nonsense")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a Steam Workshop collection")
}

func TestApplyWorkshopCollectionImport_SavesTheProfileAndInstallsNothing(t *testing.T) {
	svc, game, src := newCollectionService(t)
	src.collection = source.Collection{ID: "2500900001", Name: "Cargo Ships", ItemIDs: []string{"3617086610", "3512001122"}}

	plan, err := svc.PlanWorkshopCollectionImport(context.Background(), game, "", "2500900001")
	require.NoError(t, err)

	result, err := svc.ApplyWorkshopCollectionImport(context.Background(), game, plan,
		core.ProfileImportOptions{Install: true}, nil)
	require.NoError(t, err)

	assert.Equal(t, "cargo-ships", result.ProfileName)
	assert.Equal(t, 0, result.Installed, "Tier 3 lands the download path; W2 never fetches an item")
	assert.Equal(t, 2, result.Skipped)
	require.NotEmpty(t, result.Notes)

	saved, err := svc.NewProfileManager().Get(context.Background(), game.ID, "cargo-ships")
	require.NoError(t, err)
	require.Len(t, saved.Mods, 2)
	assert.Equal(t, "steamworkshop", saved.Mods[0].SourceID)

	installed, err := svc.GetInstalledMods(context.Background(), game.ID, "cargo-ships")
	require.NoError(t, err)
	assert.Empty(t, installed, "nothing is downloaded, so nothing is installed")
}

// TestPlanWorkshopCollectionImport_RefusesASourceThatCannotResolveOne pins
// the optional-capability gate: a game mapped to a workshop-capable source
// that is not ALSO a collection resolver has nothing to import from.
func TestPlanWorkshopCollectionImport_RefusesASourceThatCannotResolveOne(t *testing.T) {
	svc := newFlowsTestService(t)
	svc.RegisterSource(newWorkshopTestSource())
	game := &domain.Game{
		ID: "space-engineers-2", Name: "Space Engineers 2", ModPath: t.TempDir(),
		SourceIDs: map[string]string{"steamworkshop": "1133870"},
	}
	_, err := svc.PlanWorkshopCollectionImport(context.Background(), game, "", "2500900001")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collection")
}
