package main

import (
	"context"
	"regexp"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Tests for #543's CLI surfaces: a mod its source will not serve through
// the API is marked manual in `lmm update`, `lmm list` and `lmm mod show`,
// and the bulk update never attempts it - it names the page and the
// from-file command instead.

// setupManualOnlyUpdate is setupDoUpdateTest with two installed mods that
// both have a 2.0: Mod A, whose source refuses every download (and which
// core has therefore recorded manual-only), and Mod B, an ordinary one.
func setupManualOnlyUpdate(t *testing.T) (*core.Service, *domain.Game, *fakeUpdateSource) {
	t.Helper()
	svc, game, src := setupDoUpdateTest(t)
	ctx := context.Background()
	modA := seedInstalledForUpdate(t, svc, game, "test-src", "modA", "Mod A", "1.0", []string{"a-old"}, map[string][]byte{"a-old.esp": []byte("old")})
	modA.SourceURL = "https://example.test/mods/mod-a"
	require.NoError(t, svc.SaveInstalledMod(ctx, modA))
	src.AddMod(&domain.Mod{ID: "modA", SourceID: "test-src", Name: "Mod A", Version: "2.0", GameID: "g1", SourceURL: "https://example.test/mods/mod-a"},
		[]domain.DownloadableFile{{ID: "a-new", FileName: "a-new.esp", IsPrimary: true}})
	seedInstalledForUpdate(t, svc, game, "test-src", "modB", "Mod B", "1.0", []string{"b-old"}, map[string][]byte{"b-old.esp": []byte("old")})
	src.AddMod(&domain.Mod{ID: "modB", SourceID: "test-src", Name: "Mod B", Version: "2.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "b-new", FileName: "b-new.esp", IsPrimary: true}})
	src.AddDownload("b-new", []byte("new"))

	// The refusal an explicit update met is what records the fact.
	src.manualOnly = map[string]bool{"modA": true}
	row, err := svc.GetInstalledMod(ctx, "test-src", "modA", "g1", "default")
	require.NoError(t, err)
	plan, err := svc.PlanUpdateFrom(ctx, game, "default", domain.Update{InstalledMod: *row, NewVersion: "2.0"})
	require.NoError(t, err)
	_, err = svc.ApplyUpdate(ctx, game, plan, core.UpdateOptions{}, nil)
	require.ErrorIs(t, err, source.ErrManualDownload)
	src.refusedCalls = 0
	return svc, game, src
}

// policyCell is the POLICY cell of name's row in an update table.
func policyCell(t *testing.T, out, name string) string {
	t.Helper()
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\s+\S+\s+\S+\s+(.*)$`).FindStringSubmatch(out)
	require.NotNil(t, m, "no row for %s in:\n%s", name, out)
	return m[1]
}

func TestDoUpdate_All_SkipsAManualOnlyModAndSaysHowToFinish(t *testing.T) {
	svc, game, src := setupManualOnlyUpdate(t)
	_, err := svc.SetModUpdatePolicy(context.Background(), "test-src", "modA", "g1", "default", domain.UpdateAuto)
	require.NoError(t, err)
	updateAll = true

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })

	assert.Equal(t, "auto [manual]", policyCell(t, out, "Mod A"),
		"marked manual, and no ✓: an auto policy will not apply it")
	assert.Contains(t, out, "1 mod(s) not applied — their source will not serve the file to lmm:")
	assert.Contains(t, out, "  Mod A\n    Download it manually from: https://example.test/mods/mod-a\n")
	assert.Contains(t, out, "    Then update from the file: lmm update modA -s test-src --from-file <downloaded-file>\n")
	assert.Zero(t, src.refusedCalls, "the batch must not ask the source for a download")

	a, err := svc.GetInstalledMod(context.Background(), "test-src", "modA", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "1.0", a.Version)
	b, err := svc.GetInstalledMod(context.Background(), "test-src", "modB", "g1", "default")
	require.NoError(t, err)
	assert.Equal(t, "2.0", b.Version, "the rest of the batch is unaffected")
}

// The check alone (no --all) still reports the update, marked.
func TestDoUpdate_Check_MarksAManualOnlyMod(t *testing.T) {
	svc, game, _ := setupManualOnlyUpdate(t)

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })

	assert.Equal(t, "notify [manual]", policyCell(t, out, "Mod A"))
	assert.Equal(t, "notify", policyCell(t, out, "Mod B"))
	assert.Contains(t, out, "2 update(s) available.")
}

func TestJSONGolden_UpdateBulkAllManualOnly(t *testing.T) {
	withJSONOutput(t)
	svc, game, _ := setupManualOnlyUpdate(t)
	updateAll = true

	out := captureStdout(t, func() error { return doUpdate(context.Background(), svc, game, nil) })
	assertJSONCLIGolden(t, "update_bulk_all_manual_only", out)
}

func TestList_MarksAManualOnlyMod(t *testing.T) {
	svc, game, _ := setupManualOnlyUpdate(t)

	out := listNonVerbose(t, svc, game)

	assert.Contains(t, out, "MANUAL")
	assert.Contains(t, out, "1 manual download only")
	assert.Regexp(t, `(?m)^modA\s.*\sMANUAL$`, out)
	assert.Regexp(t, `(?m)^modB\s.*\s-$`, out)
}

func TestList_NoManualOnlyModKeepsItsShape(t *testing.T) {
	svc, game, _ := setupDoUpdateTest(t)
	seedInstalledForUpdate(t, svc, game, "test-src", "modB", "Mod B", "1.0", []string{"b-old"}, map[string][]byte{"b-old.esp": []byte("old")})

	out := listNonVerbose(t, svc, game)

	assert.NotContains(t, out, "MANUAL")
	assert.NotContains(t, out, "manual")
}

func TestModShow_SaysAManualOnlyModIsUpdatedFromAFile(t *testing.T) {
	svc, game, _ := setupManualOnlyUpdate(t)
	oldProfile, oldSource := modProfile, modSource
	modProfile, modSource = "default", "test-src"
	t.Cleanup(func() { modProfile, modSource = oldProfile, oldSource })

	out := captureStdout(t, func() error { return doModShow(context.Background(), svc, game, "modA") })
	assert.Contains(t, out, "Download: manual only — the source will not serve this mod's files to lmm\n")
	assert.Contains(t, out, "  Update from a file you download: lmm update modA -s test-src -p default --from-file <downloaded-file>\n")

	out = captureStdout(t, func() error { return doModShow(context.Background(), svc, game, "modB") })
	assert.NotContains(t, out, "manual")
}
