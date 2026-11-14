package serve

// The profile_fill flow (#608) end to end through the entry points the SPA
// drives: POST /api/v1/uploads for each dropped archive, POST
// /api/v1/plans/profile_fill over the uploads, POST /api/v1/jobs - with
// the assertions on END STATE. The collection's recipe is in its cache
// entry, as a free account's import leaves it, so no 7z and no network.

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedFillProfile saves "Coll", a free account's collection profile
// pinning 10 and 11 with neither installed, the recipe recording each
// file's md5 and size, and returns each mod's archive.
func seedFillProfile(t *testing.T, svc *core.Service, game *domain.Game) map[string][]byte {
	t.Helper()
	return seedFillProfileWith(t, svc, game, zipBytes(t, map[string]string{"ten.esp": "ten"}))
}

// seedFillProfileWith is seedFillProfile with Ten's archive given.
func seedFillProfileWith(t *testing.T, svc *core.Service, game *domain.Game, ten []byte) map[string][]byte {
	t.Helper()
	seedNexusCollection(t, svc, game, source.TierFree)
	archives := map[string][]byte{
		"10": ten,
		"11": zipBytes(t, map[string]string{"eleven.esp": "eleven" + strings.Repeat(".", 64)}),
	}
	type recipeMod struct {
		Name, Version, SourceType, ModID, FileID, UpdatePolicy, MD5 string
		SizeBytes                                                   int64
	}
	var mods []recipeMod
	for _, m := range []struct{ id, name, policy string }{{"10", "Ten", "exact"}, {"11", "Eleven", "latest"}} {
		sum := md5.Sum(archives[m.id])
		mods = append(mods, recipeMod{Name: m.name, Version: "1.0", SourceType: "nexus", ModID: m.id, FileID: m.id + "0",
			UpdatePolicy: m.policy, MD5: hex.EncodeToString(sum[:]), SizeBytes: int64(len(archives[m.id]))})
	}
	recipe, err := json.Marshal(map[string]any{"Name": "Three Mods", "Mods": mods})
	require.NoError(t, err)
	entry := filepath.Join(svc.GlobalCacheDir(), game.ID, "collections", "nexusmods-abcdef", "3", "collection.json")
	require.NoError(t, os.WriteFile(entry, recipe, 0o644))
	require.NoError(t, config.SaveProfile(svc.ConfigDir(), &domain.Profile{
		Name: "Coll", GameID: game.ID,
		Mods: []domain.ModReference{
			{SourceID: "nexusmods", ModID: "10", Version: "1.0", FileIDs: []string{"100"}, Locked: true},
			{SourceID: "nexusmods", ModID: "11", Version: "1.0", FileIDs: []string{"110"}},
		},
		Collection: &domain.CollectionInfo{ID: "nexusmods:abcdef", Origin: &domain.CollectionOrigin{SourceID: "nexusmods", Slug: "abcdef", Revision: 3}},
	}))
	return archives
}

// planFill drives POST /api/v1/plans/profile_fill for profile "Coll".
func planFill(t *testing.T, s *Server, game *domain.Game, body string) (planID, core.ProfileFillPlan) {
	t.Helper()
	rec := doAPI(s, http.MethodPost, scopedTo("/api/v1/plans/profile_fill", game, "Coll"), body)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		PlanID planID               `json:"plan_id"`
		Plan   core.ProfileFillPlan `json:"plan"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return resp.PlanID, resp.Plan
}

func stage(t *testing.T, s *Server, name string, content []byte) uploadID {
	t.Helper()
	rec := postUpload(t, s, name, content)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return decodeUpload(t, rec.Body.Bytes()).UploadID
}

func TestAPIFlow_ProfileFill_PendingSizesWithNoUploads(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	archives := seedFillProfile(t, svc, game)

	_, plan := planFill(t, s, game, `{}`)
	assert.Empty(t, plan.Matched)
	require.Len(t, plan.Pending, 2)
	sizes := map[int64]bool{}
	for _, p := range plan.Pending {
		for _, f := range p.Files {
			sizes[f.Size] = true
		}
	}
	assert.Equal(t, map[int64]bool{int64(len(archives["10"])): true, int64(len(archives["11"])): true}, sizes,
		"the SPA uploads only a file of one of these sizes")
}

func TestAPIFlow_ProfileFill_InstallsTheMatchedUploads(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	archives := seedFillProfile(t, svc, game)
	ten := stage(t, s, "Ten-10-1-0.zip", archives["10"])
	other := stage(t, s, "Other.zip", zipBytes(t, map[string]string{"x.esp": "x"}))

	id, plan := planFill(t, s, game, `{"upload_ids":["`+string(ten)+`","`+string(other)+`"]}`)
	require.Len(t, plan.Matched, 1)
	assert.Equal(t, "10", plan.Matched[0].ModID)
	assert.Equal(t, "Ten-10-1-0.zip", plan.Matched[0].Files[0].Archive, "the upload's own name, never its staged path")
	assert.Empty(t, plan.Ignored, "a file of no pending size is counted, not named")
	assert.Equal(t, []core.FillIgnoredCount{{Reason: core.FillIgnoredNoMatch, Count: 1}}, plan.IgnoredCounts)
	assert.True(t, plan.RecordedOnly, "Coll is not the active profile")

	j := startFlowJob(t, s, id, `{}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	result, ok := j.status().Result.(*core.ProfileFillResult)
	require.True(t, ok)
	assert.Equal(t, 1, result.Recorded)
	assert.Equal(t, 1, result.Remaining)
	assert.Equal(t, &jobTally{Kind: "profile_fill", Installed: 1, Skipped: 1, Warnings: len(result.Warnings)}, j.status().Tally)

	row, err := svc.GetInstalledMod(t.Context(), "nexusmods", "10", game.ID, "Coll")
	require.NoError(t, err)
	assert.Equal(t, []string{"100"}, row.FileIDs)
	_, staged := s.uploads.Get(ten)
	assert.False(t, staged, "a successful fill removes its uploads")
}

func TestAPIFlow_ProfileFill_BadRequests(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	seedFillProfile(t, svc, game)
	tooMany := `{"upload_ids":["a","b","c","d","e"]}`
	for name, tc := range map[string]struct{ profile, body string }{
		"unknown upload":         {"Coll", `{"upload_ids":["0123456789abcdef0123456789abcdef"]}`},
		"more uploads than kept": {"Coll", tooMany},
		"not a collection":       {"default", `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			rec := doAPI(s, http.MethodPost, scopedTo("/api/v1/plans/profile_fill", game, tc.profile), tc.body)
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		})
	}
}

// The folder mode (#608 review): the plan names a folder on this machine,
// which the server scans as `lmm profile fill <dir>` does - nothing is
// uploaded.
func TestAPIFlow_ProfileFill_FromAFolder(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	archives := seedFillProfile(t, svc, game)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Eleven-11.zip"), archives["11"], 0o644))

	body, err := json.Marshal(map[string]string{"folder": dir})
	require.NoError(t, err)
	id, plan := planFill(t, s, game, string(body))
	require.Len(t, plan.Matched, 1)
	assert.Equal(t, "11", plan.Matched[0].ModID)
	j := startFlowJob(t, s, id, `{}`)
	require.Equal(t, jobSucceeded, j.status().State, "%v", j.status().Error)
	row, err := svc.GetInstalledMod(t.Context(), "nexusmods", "11", game.ID, "Coll")
	require.NoError(t, err)
	assert.Equal(t, domain.UpdateAuto, row.UpdatePolicy)
	_, err = os.Stat(filepath.Join(dir, "Eleven-11.zip"))
	assert.NoError(t, err, "the user's file stays")

	for name, body := range map[string]string{
		"relative":          `{"folder":"Downloads"}`,
		"a file":            `{"folder":` + string(must(json.Marshal(filepath.Join(dir, "Eleven-11.zip")))) + `}`,
		"folder and upload": `{"folder":"/tmp","upload_ids":["0123456789abcdef0123456789abcdef"]}`,
	} {
		rec := doAPI(s, http.MethodPost, scopedTo("/api/v1/plans/profile_fill", game, "Coll"), body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "%s: %s", name, rec.Body.String())
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// A matched mod whose installer needs an answer fails the job with the
// question in its details; re-planning over the same uploads with the
// answer installs it (#608 review).
func TestAPIFlow_ProfileFill_AnInstallerQuestionRoundTrip(t *testing.T) {
	s, svc, game := newFlowFixtureServer(t)
	archives := seedFillProfileWith(t, svc, game, fomodTestArchive(t))
	ten := stage(t, s, "Ten.zip", archives["10"])
	planBody := `{"upload_ids":["` + string(ten) + `"]}`

	id, plan := planFill(t, s, game, planBody)
	require.Len(t, plan.Matched, 1)
	j := startFlowJob(t, s, id, `{}`)
	st := j.status()
	require.Equal(t, jobFailed, st.State)
	require.NotNil(t, st.Error)
	// The failure's details are the whole fill result, refused, with the
	// question under fomod_choices_required - as a refused profile import's
	// are (fomod.js#fomodQuestionFor reads both).
	raw, err := json.Marshal(st.Error.Details)
	require.NoError(t, err)
	var details core.ProfileFillResult
	require.NoError(t, json.Unmarshal(raw, &details))
	assert.True(t, details.Refused)
	require.NotNil(t, details.FomodChoicesRequired, "%s", raw)
	q := details.FomodChoicesRequired
	require.Len(t, q.Mods, 1)
	assert.Equal(t, "nexusmods", q.Mods[0].SourceID)
	assert.Equal(t, "10", q.Mods[0].ModID)
	_, staged := s.uploads.Get(ten)
	require.True(t, staged, "a refused fill keeps its uploads for the re-plan")
	_, err = svc.GetInstalledMod(t.Context(), "nexusmods", "10", game.ID, "Coll")
	require.Error(t, err, "nothing was written")

	answer, err := json.Marshal(map[string]any{"fomod": map[string]domain.FomodChoices{
		"nexusmods:10": {Steps: []domain.FomodStepChoice{sizeStep("2K", 0)}},
	}})
	require.NoError(t, err)
	id, _ = planFill(t, s, game, planBody)
	j = startFlowJob(t, s, id, string(answer))
	require.Equal(t, jobSucceeded, j.status().State, "%+v", j.status().Error)
	row, err := svc.GetInstalledMod(t.Context(), "nexusmods", "10", game.ID, "Coll")
	require.NoError(t, err)
	assert.NotEmpty(t, row.Variant, "installed as the answered variant")
}
