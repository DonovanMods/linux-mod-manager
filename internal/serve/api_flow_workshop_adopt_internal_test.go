package serve

// The workshop-adopt flow end to end through the entry points the SPA
// drives (issue 269): POST /api/v1/plans/workshop_adopt, then POST
// /api/v1/jobs - with the assertions on END STATE that matter most for
// this flow, namely that NOTHING was written under mod_path or into the
// cache. lmm tracks a Steam Workshop item; it does not manage it.

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const workshopFixtureSourceID = "steamworkshop"

// workshopFixtureSource is the fake Steam Workshop source: it answers the
// two optional capabilities core's adopt uses and nothing else. No network,
// no real Steam library.
type workshopFixtureSource struct {
	fixtureSource
	scan     source.WorkshopScan
	describe []source.ModDescription
}

func (s *workshopFixtureSource) ID() string   { return workshopFixtureSourceID }
func (s *workshopFixtureSource) Name() string { return "Steam Workshop" }

func (s *workshopFixtureSource) ScanWorkshopItems(context.Context, string) (source.WorkshopScan, error) {
	return s.scan, nil
}

func (s *workshopFixtureSource) DescribeMods(_ context.Context, _ string, ids []string, _ bool) ([]source.ModDescription, error) {
	out := make([]source.ModDescription, 0, len(ids))
	for _, id := range ids {
		hit := source.ModDescription{ModID: id, Unavailable: true, Note: "not described"}
		for _, d := range s.describe {
			if d.ModID == id {
				hit = d
				break
			}
		}
		out = append(out, hit)
	}
	return out, nil
}

// seedWorkshopFixture registers the fake workshop source against the
// fixture game and lays one subscribed item out on a temp "Steam" tree.
func seedWorkshopFixture(t *testing.T, svc *core.Service, game *domain.Game) string {
	t.Helper()
	steamDir := filepath.Join(t.TempDir(), "steamapps", "workshop", "content", "1133870", "3617086610")
	require.NoError(t, os.MkdirAll(steamDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(steamDir, "mod.pak"), []byte("steam owns this"), 0o644))

	svc.RegisterSource(&workshopFixtureSource{
		scan: source.WorkshopScan{
			Roots: []string{"/steam"},
			Items: []domain.WorkshopItem{{
				FileID: "3617086610", Path: steamDir, SizeOnDisk: 15,
				Manifest: "7987119735124793734", TimeUpdated: 1764767935,
			}},
		},
		describe: []source.ModDescription{{
			ModID: "3617086610",
			Mod: domain.Mod{
				ID: "3617086610", SourceID: workshopFixtureSourceID,
				Name: "Sample Workshop Item", Author: "76561198000000000",
			},
		}},
	})

	game.SourceIDs[workshopFixtureSourceID] = "1133870"
	require.NoError(t, svc.SaveGame(t.Context(), game))
	return steamDir
}

func TestAPIFlow_WorkshopAdopt_TracksTheItemAndWritesNoFiles(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	steamDir := seedWorkshopFixture(t, svc, game)

	modPathBefore := dirEntryNames(t, game.ModPath)

	planID, planBody := planFlow(t, s, game, "workshop_adopt", `{}`)

	var planned struct {
		Plan core.WorkshopAdoptPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(planBody, &planned))
	require.Len(t, planned.Plan.Entries, 1)
	assert.Equal(t, "3617086610", planned.Plan.Entries[0].FileID)
	assert.Equal(t, steamDir, planned.Plan.Entries[0].Path)
	assert.Equal(t, 1, planned.Plan.Scan.Untracked)
	assert.False(t, planned.Plan.NoChanges)

	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)

	result, ok := j.status().Result.(*core.WorkshopAdoptResult)
	require.True(t, ok)
	assert.Equal(t, 1, result.Adopted)
	assert.Zero(t, result.Failed)

	// End state: the tracking row, the profile ref, and NOTHING else.
	mod, err := svc.GetInstalledMod(t.Context(), workshopFixtureSourceID, "3617086610", game.ID, "default")
	require.NoError(t, err)
	assert.True(t, mod.External)
	assert.Equal(t, steamDir, mod.ExternalPath)
	assert.Equal(t, "7987119735124793734", mod.Version)
	assert.True(t, mod.Deployed)
	assert.Equal(t, domain.UpdateNotify, mod.UpdatePolicy)

	profile, err := svc.NewProfileManager().Get(t.Context(), game.ID, "default")
	require.NoError(t, err)
	assert.NotNil(t, profile.FindRef(workshopFixtureSourceID, "3617086610"))

	assert.Equal(t, modPathBefore, dirEntryNames(t, game.ModPath),
		"nothing may be written under the game's mod directory")
	assert.False(t,
		svc.GetGameCache(game).Exists(game.ID, workshopFixtureSourceID, "3617086610", "7987119735124793734"),
		"lmm downloaded nothing, so it caches nothing")
	assert.FileExists(t, filepath.Join(steamDir, "mod.pak"), "Steam's own file is untouched")
}

func TestAPIFlow_WorkshopAdopt_NothingUntrackedPlansNoChanges(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedWorkshopFixture(t, svc, game)

	planID, _ := planFlow(t, s, game, "workshop_adopt", `{}`)
	j := startFlowJob(t, s, planID, "")
	require.Equal(t, jobSucceeded, j.status().State)

	// A second plan sees the item already tracked.
	_, planBody := planFlow(t, s, game, "workshop_adopt", `{"refresh":true}`)
	var planned struct {
		Plan core.WorkshopAdoptPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(planBody, &planned))
	assert.True(t, planned.Plan.NoChanges)
	assert.Equal(t, 1, planned.Plan.Scan.Tracked)
	assert.Empty(t, planned.Plan.Entries)
}

// TestAPIFlow_WorkshopAdopt_NoWorkshopSourceIsBadInput pins that a game
// with no `steamworkshop` mapping answers 400, not 500: the caller's
// selection is wrong, and the remedy (map the source) is theirs.
func TestAPIFlow_WorkshopAdopt_NoWorkshopSourceIsBadInput(t *testing.T) {
	s, _, game := newFlowFixtureServer(t)

	rec := doAPI(s, http.MethodPost, scoped("/api/v1/plans/workshop_adopt", game), `{}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "Steam Workshop")
}

// dirEntryNames lists a directory's immediate entry names, sorted by the
// filesystem's own order - enough to prove "nothing was added".
func dirEntryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestUninstallPlanViewQuotesCoresExternalNote is the anti-drift ratchet for
// the one #269 sentence the SPA has to hold as its own literal: there is no
// bundler and no shared string table, so plan_uninstall.js spells out
// core.UninstallExternalNote. If the wording moves in core and not there,
// the CLI and the web UI would tell the user two different things about the
// same refusal - which is exactly what internal/core/external.go exists to
// prevent.
func TestUninstallPlanViewQuotesCoresExternalNote(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("spa", "app", "components", "plan_uninstall.js"))
	require.NoError(t, err)
	assert.Contains(t, string(src), core.UninstallExternalNote,
		"plan_uninstall.js must quote core.UninstallExternalNote verbatim")
}
