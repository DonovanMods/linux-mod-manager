package core_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
	"github.com/stretchr/testify/require"
)

// findOutcome looks up modID's entry in a MergedPakOutcomes result -
// TestPakConvertEndToEnd's uniform fingerprint-membership check at every
// lifecycle step, alongside the rec.lastSources merge-input proxy.
func findOutcome(outcomes []core.MergedFingerprintEntry, modID string) (core.MergedFingerprintEntry, bool) {
	for _, o := range outcomes {
		if o.ModID == modID {
			return o, true
		}
	}
	return core.MergedFingerprintEntry{}, false
}

// recordingMergeCompilerSource wraps fakeCompilerSource (service_icarus_
// compile_test.go), additionally recording the exact []source.MergeSource
// slice passed to the most recent MergeCompile call. TestPakConvertEndToEnd
// needs to assert not just THAT a merge happened but WHICH sources, in what
// order, and with what Kind actually reached the compiler - fakeCompilerSource
// itself only counts calls.
type recordingMergeCompilerSource struct {
	*fakeCompilerSource
	lastSources []source.MergeSource
}

func (s *recordingMergeCompilerSource) MergeCompile(ctx context.Context, basePakPath string, sources []source.MergeSource, outputPath string) ([]string, []source.MergeFailure, error) {
	s.lastSources = append([]source.MergeSource(nil), sources...)
	return s.fakeCompilerSource.MergeCompile(ctx, basePakPath, sources, outputPath)
}

var _ source.MergeCompiler = (*recordingMergeCompilerSource)(nil)

// TestPakConvertEndToEnd is the #221 lifecycle sweep: a profile carrying
// both an exmodz mod and a convert-eligible pak mod goes through convert ->
// toggle-off -> toggle-on -> disable, asserting deployed-state, manifest
// members, and merge-input membership at every step - not just "no error".
func TestPakConvertEndToEnd(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)
	game.ConvertPaks = true

	rec := &recordingMergeCompilerSource{fakeCompilerSource: &fakeCompilerSource{}}
	svc.RegisterSource(rec)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "exmod", "1.0", "exmodz-file", []byte("exmod-bytes"))
	seedEnabledPakMod(t, svc, game, "fake-compiler", "pakmod", "1.0", "pak", []byte("pak-bytes"))

	// Deploy pakmod's raw pak BEFORE the first sync, mirroring the real
	// ingest-then-first-sync window (TestPakInstallThenSyncNeverDoubleApplies)
	// - so "raw link gone" below is a genuine transition, not "never existed".
	installer, err := svc.GetInstallerForProfile(game, "default")
	require.NoError(t, err)
	pakMod := &domain.Mod{ID: "pakmod", SourceID: "fake-compiler", GameID: game.ID, Version: "1.0"}
	require.NoError(t, installer.Install(context.Background(), game, pakMod, "default"))

	rawPath := filepath.Join(game.ModPath, "pakmod.pak")
	_, err = os.Stat(rawPath)
	require.NoError(t, err, "precondition: raw pak deployed before the first sync")

	mergedPath := filepath.Join(game.ModPath, "zzz_LMM_Merged_P.pak")
	gameCache := svc.GetGameCache(game)

	// --- Step 1: convert ---
	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	require.Empty(t, warnings)

	require.Len(t, rec.lastSources, 2, "MergeCompile must receive both mods")
	require.Equal(t, "fake-compiler:exmod", rec.lastSources[0].ModRef, "profile load order: exmod first")
	require.Equal(t, "exmodz", rec.lastSources[0].Kind)
	require.Equal(t, "fake-compiler:pakmod", rec.lastSources[1].ModRef, "profile load order: pakmod second")
	require.Equal(t, "pak", rec.lastSources[1].Kind)

	manifests, err := gameCache.FileManifests(game.ID, "fake-compiler", "pakmod", "1.0")
	require.NoError(t, err)
	require.True(t, manifests["pak"].Recorded)
	require.Empty(t, manifests["pak"].Members, "converted: pak manifest flips to nil (merged pak claims it)")

	_, err = os.Stat(rawPath)
	require.True(t, os.IsNotExist(err), "raw link gone after conversion")
	_, err = os.Stat(mergedPath)
	require.NoError(t, err, "merged pak deployed")

	outcomes, ok := svc.MergedPakOutcomes(game, "default")
	require.True(t, ok)
	exmodOutcome, found := findOutcome(outcomes, "exmod")
	require.True(t, found)
	require.True(t, exmodOutcome.Converted)
	pakOutcome, found := findOutcome(outcomes, "pakmod")
	require.True(t, found, "converted: pakmod must appear in the merge fingerprint")
	require.True(t, pakOutcome.Converted)

	// --- Step 2: toggle pakmod's conversion off ---
	require.NoError(t, svc.SetModConvertPaks("fake-compiler", "pakmod", game.ID, "default", false))
	rec.lastSources = nil
	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	require.Len(t, rec.lastSources, 1, "MergeCompile must receive ONLY the exmodz source")
	require.Equal(t, "fake-compiler:exmod", rec.lastSources[0].ModRef)

	manifests, err = gameCache.FileManifests(game.ID, "fake-compiler", "pakmod", "1.0")
	require.NoError(t, err)
	require.Equal(t, []string{"pakmod.pak"}, manifests["pak"].Members, "opted-out: pak manifest lists the raw pak again")

	_, err = os.Stat(rawPath)
	require.NoError(t, err, "raw deployed again after opt-out")
	_, err = os.Stat(mergedPath)
	require.NoError(t, err, "merged pak still exists (exmodz-only merge must still produce one)")

	outcomes, ok = svc.MergedPakOutcomes(game, "default")
	require.True(t, ok)
	_, found = findOutcome(outcomes, "pakmod")
	require.False(t, found, "opted-out: pakmod must not appear in the merge fingerprint (membership changed)")

	// --- Step 3: toggle back on ---
	require.NoError(t, svc.SetModConvertPaks("fake-compiler", "pakmod", game.ID, "default", true))
	rec.lastSources = nil
	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	require.Len(t, rec.lastSources, 2, "re-opted-in: MergeCompile receives both sources again")

	manifests, err = gameCache.FileManifests(game.ID, "fake-compiler", "pakmod", "1.0")
	require.NoError(t, err)
	require.Empty(t, manifests["pak"].Members, "converted state again")

	_, err = os.Stat(rawPath)
	require.True(t, os.IsNotExist(err), "raw link gone again")
	_, err = os.Stat(mergedPath)
	require.NoError(t, err, "merged pak still deployed")

	outcomes, ok = svc.MergedPakOutcomes(game, "default")
	require.True(t, ok)
	pakOutcome, found = findOutcome(outcomes, "pakmod")
	require.True(t, found, "re-opted-in: pakmod must appear in the merge fingerprint again")
	require.True(t, pakOutcome.Converted)

	// --- Step 4: disable pakmod entirely ---
	require.NoError(t, svc.SetModEnabled("fake-compiler", "pakmod", game.ID, "default", false))
	rec.lastSources = nil
	_, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)

	require.Len(t, rec.lastSources, 1, "a disabled mod must contribute nothing to the merge")
	require.Equal(t, "fake-compiler:exmod", rec.lastSources[0].ModRef)

	outcomes, ok = svc.MergedPakOutcomes(game, "default")
	require.True(t, ok)
	_, found = findOutcome(outcomes, "pakmod")
	require.False(t, found, "a disabled mod must not appear in the merge fingerprint")

	// Disabled != opted-out: reconcilePakManifests' `!mod.Enabled` guard
	// skips disabled mods entirely (internal/core/merged_pak.go), so the
	// manifest must STAY in its pre-disable converted state (members=nil) -
	// NOT revert to the raw member set the way step 2's opt-out did. A
	// regression that conflated "disabled" with "opted out" (e.g. routing
	// disabled mods through the raw-fallback branch) would flip this back to
	// ["pakmod.pak"] and this assertion would catch it.
	manifests, err = gameCache.FileManifests(game.ID, "fake-compiler", "pakmod", "1.0")
	require.NoError(t, err)
	require.Empty(t, manifests["pak"].Members, "disabled mods are skipped by reconcile, not reverted to raw - members stays at its pre-disable converted state")

	_, err = os.Stat(rawPath)
	require.True(t, os.IsNotExist(err), "disabled mods are not deployed at all - reconcile skips them entirely")
	_, err = os.Stat(mergedPath)
	require.NoError(t, err, "merged pak remains deployed for the still-enabled exmodz mod")
}

// TestNoPakModsByteIdentical pins the #221 fingerprint-marker compat
// guarantee at flow level (Task 8's TestReadOldFingerprintMarkerCompat
// pins it at the unit level): a profile with only exmodz mods - the only
// kind that existed pre-#221 - produces a marker whose entries carry Kind
// set explicitly, MergeCompile receives sources with Kind set, and a marker
// downgraded to the exact pre-#221 shape (no Kind/Converted/FailReason keys
// at all, as pre-#221 code would have written) still fast-paths on the next
// sync - no spurious regen just because the marker predates those fields.
func TestNoPakModsByteIdentical(t *testing.T) {
	svc, game, _ := newMergedPakTestGame(t)

	rec := &recordingMergeCompilerSource{fakeCompilerSource: &fakeCompilerSource{}}
	svc.RegisterSource(rec)

	seedEnabledExmodzMod(t, svc, game, "fake-compiler", "exmod", "1.0", "exmodz-file", []byte("exmod-bytes"))

	warnings, err := svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, 1, rec.compileCalls)

	require.Len(t, rec.lastSources, 1)
	require.Equal(t, "exmodz", rec.lastSources[0].Kind, "MergeCompile must receive Kind set even for a pure-exmodz profile (#221 regression net)")

	outcomes, ok := svc.MergedPakOutcomes(game, "default")
	require.True(t, ok)
	require.Len(t, outcomes, 1)
	require.Equal(t, "exmodz", outcomes[0].Kind)
	require.True(t, outcomes[0].Converted)
	require.Empty(t, outcomes[0].FailReason)

	// Downgrade the stored marker on disk to the exact pre-#221 shape: strip
	// Kind/Converted/FailReason from every entry (the fields #221 added).
	// "merged-pak"/"merged" mirror core's private mergedPakModID/
	// mergedPakVersion constants (internal/core/merged_pak.go) - this test
	// lives in the external core_test package, so it names them literally
	// rather than reaching into core's unexported identifiers.
	gameCache := svc.GetGameCache(game)
	cachePath := gameCache.ModPath(game.ID, domain.SourceMerged, "merged-pak", "merged")
	markerPath := cache.MergeFingerprintPath(cachePath)

	raw, err := os.ReadFile(markerPath)
	require.NoError(t, err)
	var marker map[string]any
	require.NoError(t, json.Unmarshal(raw, &marker))
	mods, ok := marker["Mods"].([]any)
	require.True(t, ok)
	for _, m := range mods {
		entry, ok := m.(map[string]any)
		require.True(t, ok)
		delete(entry, "Kind")
		delete(entry, "Converted")
		delete(entry, "FailReason")
	}
	downgraded, err := json.Marshal(marker)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(markerPath, downgraded, 0o644))

	// A fresh SyncMergedPak against this pre-#221-shaped marker must still
	// fast-path: no spurious regen just because the marker predates Kind.
	warnings, err = svc.SyncMergedPak(context.Background(), game, "default")
	require.NoError(t, err)
	require.Empty(t, warnings)
	require.Equal(t, 1, rec.compileCalls, "a pre-#221 marker for unchanged inputs must not trigger a regen")

	// C1 regression (final whole-branch review of #221): a pre-#221 marker
	// entry unmarshals as Kind:"", Converted:false - the outcome fields
	// didn't exist yet, and equality (fingerprintInputs) never regenerates
	// them for unchanged inputs. Every consumer of MergedPakOutcomes
	// (verify's conversion_failed rows, status's conversion-failure counts)
	// must NOT treat that legacy shape as a conversion failure - it predates
	// pak conversion entirely (this profile has zero pak mods).
	outcomes, ok = svc.MergedPakOutcomes(game, "default")
	require.True(t, ok)
	require.Len(t, outcomes, 1)
	require.True(t, outcomes[0].Converted, "a pre-#221 legacy marker (Kind:\"\", Converted:false) must never report as a conversion failure")
	require.Empty(t, outcomes[0].FailReason)
}
