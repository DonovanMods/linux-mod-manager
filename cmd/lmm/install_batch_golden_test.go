package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- v2 Phase 2 Unit H Task 6: pre-lift characterization ---
//
// These goldens freeze batchInstallMods's CURRENT console output (stdout AND
// stderr, separately) AND its end state (installed rows + profile refs) for
// the scenarios that matter to Task 7's PlanInstallMany/ApplyInstall lift.
// Recorded ONCE, on the pre-lift tree, then frozen: Task 7 must reproduce
// them byte-for-byte through installMultipleMods's replacement call, or the
// lift isn't done. See docs/plans/2026-08-28-v2-phase2-impl.md Unit H.

var updateInstallBatchGoldens = flag.Bool("update-install-batch", false, "rewrite batchInstallMods characterization goldens from current output (pre-lift tree ONLY)")

// perModFetchFailureSource wraps fakeInstallSource, failing GetModFiles for
// exactly one mod ID while every other mod (and every other method)
// behaves normally. fakeInstallSource's own getModFilesErr field is
// source-wide (every mod fails), so a scenario needing ONE mod's fetch to
// fail mid-batch - while an earlier mod in the SAME batch call already
// succeeded - needs this instead.
type perModFetchFailureSource struct {
	*fakeInstallSource
	failModID string
	err       error
}

func (s *perModFetchFailureSource) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod.ID == s.failModID {
		return nil, s.err
	}
	return s.fakeInstallSource.GetModFiles(ctx, mod)
}

// installBatchStateRow/Ref/State capture exactly the fields the task brief
// specifies for the golden's "## state" section - the lift must preserve
// these, not just the console text. Field order is the encoding order
// (Go's encoding/json marshals struct fields in declaration order), so the
// JSON is deterministic without any explicit sort.
type installBatchStateRow struct {
	SourceID       string   `json:"source_id"`
	ID             string   `json:"id"`
	Version        string   `json:"version"`
	Enabled        bool     `json:"enabled"`
	Deployed       bool     `json:"deployed"`
	FileIDs        []string `json:"file_ids"`
	ManualDownload bool     `json:"manual_download"`
}

type installBatchStateRef struct {
	SourceID string   `json:"source_id"`
	ModID    string   `json:"mod_id"`
	Version  string   `json:"version"`
	FileIDs  []string `json:"file_ids"`
	Locked   bool     `json:"locked"`
}

type installBatchState struct {
	InstalledMods []installBatchStateRow `json:"installed_mods"`
	ProfileRefs   []installBatchStateRef `json:"profile_refs"`
}

// dumpInstallBatchState renders GetInstalledMods (installed_at order - the
// order batchInstallMods actually wrote them in) and the profile's ModRefs
// (load order - append/update-in-place order, never sorted) as deterministic
// JSON. Nil FileIDs slices are normalized to [] so a mod with no files
// selected doesn't flip the encoding between null and [] depending on how it
// got there.
func dumpInstallBatchState(t *testing.T, svc *core.Service, gameID, profileName string) string {
	t.Helper()

	installed, err := svc.GetInstalledMods(context.Background(), gameID, profileName)
	require.NoError(t, err)
	rows := make([]installBatchStateRow, 0, len(installed))
	for _, m := range installed {
		fileIDs := m.FileIDs
		if fileIDs == nil {
			fileIDs = []string{}
		}
		rows = append(rows, installBatchStateRow{
			SourceID: m.SourceID, ID: m.ID, Version: m.Version,
			Enabled: m.Enabled, Deployed: m.Deployed,
			FileIDs: fileIDs, ManualDownload: m.ManualDownload,
		})
	}

	pm := getProfileManager(svc)
	profile, err := pm.Get(context.Background(), gameID, profileName)
	require.NoError(t, err)
	refs := make([]installBatchStateRef, 0, len(profile.Mods))
	for _, r := range profile.Mods {
		fileIDs := r.FileIDs
		if fileIDs == nil {
			fileIDs = []string{}
		}
		refs = append(refs, installBatchStateRef{
			SourceID: r.SourceID, ModID: r.ModID, Version: r.Version,
			FileIDs: fileIDs, Locked: r.Locked,
		})
	}

	data, err := json.MarshalIndent(installBatchState{InstalledMods: rows, ProfileRefs: refs}, "", "  ")
	require.NoError(t, err)
	return string(data)
}

// captureStdoutStderrErr redirects os.Stdout and os.Stderr to SEPARATE
// pipes for the duration of fn, returning both streams independently plus
// fn's own error. Unlike captureStdout/captureStdoutErr (stdout only,
// install_test.go) or captureCombined (both streams merged into one,
// deploy_test.go), the golden format here needs stdout and stderr as
// distinct sections - and, unlike captureStdoutErr, never requires fn to
// succeed.
func captureStdoutStderrErr(t *testing.T, fn func() error) (stdout, stderr string, fnErr error) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	require.NoError(t, err)
	rErr, wErr, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout, os.Stderr = wOut, wErr
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()

	fnErr = fn()
	require.NoError(t, wOut.Close(), "closing write end of the stdout pipe")
	require.NoError(t, wErr.Close(), "closing write end of the stderr pipe")
	outBytes, err := io.ReadAll(rOut)
	require.NoError(t, err)
	errBytes, err := io.ReadAll(rErr)
	require.NoError(t, err)
	require.NoError(t, rOut.Close())
	require.NoError(t, rErr.Close())
	return string(outBytes), string(errBytes), fnErr
}

// installBatchFixture is what each golden scenario hands to
// runInstallBatchGolden: an already-seeded *core.Service/*domain.Game plus
// the mods to drive through installMultipleMods. redact holds volatile
// substrings (e.g. a hook script's t.TempDir()-rooted absolute path) to
// normalize out of the captured stdout/stderr/error before the golden is
// compared or recorded, so the golden stays byte-stable across machines and
// re-runs instead of embedding a path that only exists on the recording box.
type installBatchFixture struct {
	svc         *core.Service
	game        *domain.Game
	mods        []*domain.Mod
	profileName string
	redact      map[string]string
}

// runInstallBatchGolden drives installMultipleMods - the multi-select
// install path these goldens exist to freeze, which delegated to
// batchInstallMods before Task 7 and goes through core.PlanInstallMany +
// core.ApplyInstall after it - for one scenario, and compares (or, with -update-install-batch, records) a
// four-section transcript in a fixed order: stdout, stderr, the returned
// error's text (or "<nil>"), and a JSON dump of end state. Nothing here is
// sorted beyond what the real code already orders - the order IS the
// contract.
func runInstallBatchGolden(t *testing.T, name string, setup func(t *testing.T) installBatchFixture) {
	t.Helper()
	fx := setup(t)

	stdout, stderr, runErr := captureStdoutStderrErr(t, func() error {
		return installMultipleMods(context.Background(), fx.svc, fx.game, fx.mods, fx.profileName)
	})

	errText := "<nil>"
	if runErr != nil {
		errText = runErr.Error()
	}

	for from, to := range fx.redact {
		if from == "" {
			continue
		}
		stdout = strings.ReplaceAll(stdout, from, to)
		stderr = strings.ReplaceAll(stderr, from, to)
		errText = strings.ReplaceAll(errText, from, to)
	}

	state := dumpInstallBatchState(t, fx.svc, fx.game.ID, fx.profileName)

	var buf bytes.Buffer
	buf.WriteString("## stdout\n")
	buf.WriteString(stdout)
	buf.WriteString("## stderr\n")
	buf.WriteString(stderr)
	buf.WriteString("## error\n")
	buf.WriteString(errText)
	buf.WriteString("\n## state\n")
	buf.WriteString(state)
	buf.WriteString("\n")

	path := filepath.Join("testdata", "install_batch_golden", name+".golden")
	if *updateInstallBatchGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden missing - run with -update-install-batch on the PRE-lift tree only")
	require.Equal(t, string(want), buf.String(), "batchInstallMods output/state must match the pre-lift golden")
}

// --- scenario fixtures ---

func twoFreshModsFixture(t *testing.T) installBatchFixture {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	modA := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", Author: "Alice", GameID: "g1"}
	modB := &domain.Mod{ID: "mod-b", SourceID: "test-src", Name: "Mod B", Version: "2.0", Author: "Bob", GameID: "g1"}
	src.AddMod(modA, []domain.DownloadableFile{{ID: "a-file", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})
	src.AddDownload("a-file", []byte("mod a content"))
	src.AddMod(modB, []domain.DownloadableFile{{ID: "b-file", Name: "Main", FileName: "modB.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"}})
	src.AddDownload("b-file", []byte("mod b content"))
	return installBatchFixture{svc: svc, game: game, mods: []*domain.Mod{modA, modB}, profileName: "default"}
}

// two_fresh_mods_verbose pins that -v is a NO-OP for a two-fresh-mods batch:
// batchInstallMods only gates the reinstall/cache-delete-failure and
// UpsertMod-failure warnings behind verbose, none of which this scenario
// hits, so the golden is expected to be byte-identical to the plain variant
// - itself the characterization worth pinning before the lift.
func twoFreshModsVerboseFixture(t *testing.T) installBatchFixture {
	t.Helper()
	fx := twoFreshModsFixture(t)
	verbose = true
	return fx
}

// reinstallSameVersionPlusFreshFixture pre-installs mod-a at v1.0 (via a
// direct, uncaptured installMultipleMods call), then the CAPTURED batch
// reinstalls mod-a at the SAME effective version alongside a fresh mod-b -
// the batch path has no same-version dedup: it always uninstalls + clears
// cache + redownloads + redeploys, unconditionally.
func reinstallSameVersionPlusFreshFixture(t *testing.T) installBatchFixture {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	modA := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", Author: "Alice", GameID: "g1"}
	src.AddMod(modA, []domain.DownloadableFile{{ID: "a-file", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})
	src.AddDownload("a-file", []byte("mod a v1 content"))
	require.NoError(t, installMultipleMods(context.Background(), svc, game, []*domain.Mod{modA}, "default"))

	modB := &domain.Mod{ID: "mod-b", SourceID: "test-src", Name: "Mod B", Version: "2.0", Author: "Bob", GameID: "g1"}
	src.AddMod(modB, []domain.DownloadableFile{{ID: "b-file", Name: "Main", FileName: "modB.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"}})
	src.AddDownload("b-file", []byte("mod b content"))

	return installBatchFixture{svc: svc, game: game, mods: []*domain.Mod{modA, modB}, profileName: "default"}
}

func reinstallSameVersionPlusFreshVerboseFixture(t *testing.T) installBatchFixture {
	t.Helper()
	fx := reinstallSameVersionPlusFreshFixture(t)
	verbose = true
	return fx
}

// lockedRefDifferentVersionFixture mirrors
// TestBatchInstallMods_LockedRefDifferentVersion_SkippedBeforeUninstall:
// mod-a installed and locked at v1.0, the source now serving v2.0 as
// latest - the batch must skip mod-a BEFORE its remove-previous-installation
// step, never touching the deployed lock target.
func lockedRefDifferentVersionFixture(t *testing.T) installBatchFixture {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	mod := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", Author: "Alice", GameID: "g1"}
	src.AddMod(mod, []domain.DownloadableFile{{ID: "f1", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})
	src.AddDownload("f1", []byte("v1 content"))
	require.NoError(t, installMultipleMods(context.Background(), svc, game, []*domain.Mod{mod}, "default"))

	pm := getProfileManager(svc)
	require.NoError(t, pm.SetModLock(context.Background(), game.ID, "default", "test-src", "mod-a", ""))

	latest := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "2.0", Author: "Alice", GameID: "g1"}
	src.AddMod(latest, []domain.DownloadableFile{{ID: "f2", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"}})
	src.AddDownload("f2", []byte("v2 content"))

	return installBatchFixture{svc: svc, game: game, mods: []*domain.Mod{latest}, profileName: "default"}
}

// fetchFailureSecondModFixture installs mod-a normally, then batches mod-a's
// (already-installed) neighbor mod-b whose GetModFiles fails - proving a
// mid-batch fetch failure skips only the failing mod, leaving the earlier
// success in the same call untouched.
func fetchFailureSecondModFixture(t *testing.T) installBatchFixture {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	modA := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", Author: "Alice", GameID: "g1"}
	modB := &domain.Mod{ID: "mod-b", SourceID: "test-src", Name: "Mod B", Version: "2.0", Author: "Bob", GameID: "g1"}
	src.AddMod(modA, []domain.DownloadableFile{{ID: "a-file", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})
	src.AddDownload("a-file", []byte("mod a content"))

	wrapped := &perModFetchFailureSource{fakeInstallSource: src, failModID: "mod-b", err: errBoomInstall}
	svc.RegisterSource(wrapped)

	return installBatchFixture{svc: svc, game: game, mods: []*domain.Mod{modA, modB}, profileName: "default"}
}

// fileConflictFixture gives mod-a and mod-b the SAME deploy-relative
// filename ("shared.esp"): mod-a installs and deploys it first (no
// conflict, nothing owns it yet); mod-b's own GetConflicts check then finds
// mod-a already owns that path - a warning, never a block - and proceeds to
// overwrite it.
func fileConflictFixture(t *testing.T) installBatchFixture {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	modA := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", Author: "Alice", GameID: "g1"}
	modB := &domain.Mod{ID: "mod-b", SourceID: "test-src", Name: "Mod B", Version: "1.0", Author: "Bob", GameID: "g1"}
	src.AddMod(modA, []domain.DownloadableFile{{ID: "a-file", Name: "Shared", FileName: "shared.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})
	src.AddDownload("a-file", []byte("mod a content"))
	src.AddMod(modB, []domain.DownloadableFile{{ID: "b-file", Name: "Shared", FileName: "shared.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})
	src.AddDownload("b-file", []byte("mod b content"))
	return installBatchFixture{svc: svc, game: game, mods: []*domain.Mod{modA, modB}, profileName: "default"}
}

// forceWithFailingBeforeAllFixture: --force downgrades a failing
// install.before_all hook to a stderr warning instead of aborting - the
// batch still installs every mod. The hook script lives under t.TempDir(),
// so its absolute path is redacted from the golden (see installBatchFixture
// doc comment).
func forceWithFailingBeforeAllFixture(t *testing.T) installBatchFixture {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	installForce = true
	modA := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", Author: "Alice", GameID: "g1"}
	modB := &domain.Mod{ID: "mod-b", SourceID: "test-src", Name: "Mod B", Version: "2.0", Author: "Bob", GameID: "g1"}
	src.AddMod(modA, []domain.DownloadableFile{{ID: "a-file", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})
	src.AddDownload("a-file", []byte("mod a content"))
	src.AddMod(modB, []domain.DownloadableFile{{ID: "b-file", Name: "Main", FileName: "modB.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"}})
	src.AddDownload("b-file", []byte("mod b content"))

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "before_all.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0o755))
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{BeforeAll: failScript}}

	return installBatchFixture{
		svc: svc, game: game, mods: []*domain.Mod{modA, modB}, profileName: "default",
		redact: map[string]string{failScript: "<hook-script>"},
	}
}

// failingAfterEachFixture: a failing install.after_each hook accumulates a
// non-fatal warning PER MOD but never removes a mod from the batch's
// installed count. The hook script's t.TempDir() path is redacted, as above.
func failingAfterEachFixture(t *testing.T) installBatchFixture {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	modA := &domain.Mod{ID: "mod-a", SourceID: "test-src", Name: "Mod A", Version: "1.0", Author: "Alice", GameID: "g1"}
	modB := &domain.Mod{ID: "mod-b", SourceID: "test-src", Name: "Mod B", Version: "2.0", Author: "Bob", GameID: "g1"}
	src.AddMod(modA, []domain.DownloadableFile{{ID: "a-file", Name: "Main", FileName: "modA.esp", IsPrimary: true, Category: "MAIN", Version: "1.0"}})
	src.AddDownload("a-file", []byte("mod a content"))
	src.AddMod(modB, []domain.DownloadableFile{{ID: "b-file", Name: "Main", FileName: "modB.esp", IsPrimary: true, Category: "MAIN", Version: "2.0"}})
	src.AddDownload("b-file", []byte("mod b content"))

	scriptsDir := t.TempDir()
	failScript := filepath.Join(scriptsDir, "after_each.sh")
	require.NoError(t, os.WriteFile(failScript, []byte("#!/bin/bash\necho boom >&2\nexit 1\n"), 0o755))
	game.Hooks = domain.GameHooks{Install: domain.HookConfig{AfterEach: failScript}}

	return installBatchFixture{
		svc: svc, game: game, mods: []*domain.Mod{modA, modB}, profileName: "default",
		redact: map[string]string{failScript: "<hook-script>"},
	}
}

// deployCompileMergedPakFixture mirrors
// TestBatchInstallMods_DeployCompile_DeploysMergedPak: two ".exmodz" mods
// (each deploying zero files of their own) whose content only reaches disk
// via the batch's unconditional post-loop SyncMergedPak call.
func deployCompileMergedPakFixture(t *testing.T) installBatchFixture {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: src}
	svc.RegisterSource(compiler)
	require.NoError(t, svc.SaveGame(context.Background(), game))

	bearMod := &domain.Mod{ID: "bear-mount", SourceID: "test-src", Name: "Bear Mount", Version: "1.0", GameID: "g1"}
	wolfMod := &domain.Mod{ID: "wolf-mount", SourceID: "test-src", Name: "Wolf Mount", Version: "1.0", GameID: "g1"}
	src.AddMod(bearMod, []domain.DownloadableFile{{ID: "bear-exmodz", Name: "Bear Mount", FileName: "Bear_Mount.exmodz", IsPrimary: true, Category: "MAIN"}})
	src.AddMod(wolfMod, []domain.DownloadableFile{{ID: "wolf-exmodz", Name: "Wolf Mount", FileName: "Wolf_Mount.exmodz", IsPrimary: true, Category: "MAIN"}})
	src.AddDownload("bear-exmodz", []byte("bear-bytes"))
	src.AddDownload("wolf-exmodz", []byte("wolf-bytes"))

	return installBatchFixture{svc: svc, game: game, mods: []*domain.Mod{bearMod, wolfMod}, profileName: "default"}
}

// deployCompileMergedPakSyncFailureFixture mirrors
// TestBatchInstallMods_DeployCompile_SyncFailure_LinesDontClaimSuccess: the
// same two-exmodz-mod batch, but the merge itself fails - both per-mod
// completion lines must say the sync FAILED (deferred until the sync's
// outcome is known), never the success wording, and the failure prints a
// stderr Warning unconditionally.
func deployCompileMergedPakSyncFailureFixture(t *testing.T) installBatchFixture {
	t.Helper()
	svc, game, src := setupDoInstallTest(t)
	game.DeployMode = domain.DeployCompile
	game.InstallPath = t.TempDir()

	basePak := filepath.Join(game.InstallPath, "Icarus", "Content", "Data", "data.pak")
	require.NoError(t, os.MkdirAll(filepath.Dir(basePak), 0o755))
	writeFakeBasePak(t, basePak)

	compiler := &compilerInstallSource{fakeInstallSource: src, mergeErr: assert.AnError}
	svc.RegisterSource(compiler)
	require.NoError(t, svc.SaveGame(context.Background(), game))

	bearMod := &domain.Mod{ID: "bear-mount", SourceID: "test-src", Name: "Bear Mount", Version: "1.0", GameID: "g1"}
	wolfMod := &domain.Mod{ID: "wolf-mount", SourceID: "test-src", Name: "Wolf Mount", Version: "1.0", GameID: "g1"}
	src.AddMod(bearMod, []domain.DownloadableFile{{ID: "bear-exmodz", Name: "Bear Mount", FileName: "Bear_Mount.exmodz", IsPrimary: true, Category: "MAIN"}})
	src.AddMod(wolfMod, []domain.DownloadableFile{{ID: "wolf-exmodz", Name: "Wolf Mount", FileName: "Wolf_Mount.exmodz", IsPrimary: true, Category: "MAIN"}})
	src.AddDownload("bear-exmodz", []byte("bear-bytes"))
	src.AddDownload("wolf-exmodz", []byte("wolf-bytes"))

	return installBatchFixture{svc: svc, game: game, mods: []*domain.Mod{bearMod, wolfMod}, profileName: "default"}
}

func TestInstallBatchGolden(t *testing.T) {
	scenarios := []struct {
		name    string
		fixture func(t *testing.T) installBatchFixture
	}{
		{"two_fresh_mods", twoFreshModsFixture},
		{"two_fresh_mods_verbose", twoFreshModsVerboseFixture},
		{"reinstall_same_version_plus_fresh", reinstallSameVersionPlusFreshFixture},
		{"reinstall_same_version_plus_fresh_verbose", reinstallSameVersionPlusFreshVerboseFixture},
		{"locked_ref_different_version_skipped", lockedRefDifferentVersionFixture},
		{"fetch_failure_second_mod", fetchFailureSecondModFixture},
		{"file_conflict_warns_continues", fileConflictFixture},
		{"force_with_failing_before_all", forceWithFailingBeforeAllFixture},
		{"failing_after_each", failingAfterEachFixture},
		{"deploy_compile_merged_pak", deployCompileMergedPakFixture},
		{"deploy_compile_merged_pak_sync_failure", deployCompileMergedPakSyncFailureFixture},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			runInstallBatchGolden(t, sc.name, sc.fixture)
		})
	}
}
