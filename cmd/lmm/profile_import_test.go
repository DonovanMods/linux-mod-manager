package main

import (
	"context"
	"io"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- doProfileImport (Phase 6b Task 8 CLI refit) ---
//
// These tests pin cmd/lmm/profile.go's doProfileImport (git blame around
// :407-636 as of this task's start) BEFORE its PlanImport/ApplyImport
// refactor, across every path the task brief requires: all-installed,
// needs-redownload, missing-with-install (accepted), prompt-declined,
// --no-install, --force overwrite, and existing-profile-without-force. Once
// doProfileImport is rewritten onto core.PlanImport/core.ApplyImport, these
// same assertions must still pass byte-identically - see the task report for
// the before/after comparison.
//
// fakeInstallSource (install_test.go, same package) is reused rather than
// building a second httptest-backed fake source - it already provides
// GetMod/GetModFiles/GetDownloadURL/AddMod/AddDownload, everything
// doProfileImport's install loop needs, over a real (localhost-only, no
// network egress) httptest.Server.

// setupDoProfileImportTest builds a *core.Service, a game wired to a fresh
// fakeInstallSource, and resets the profileImport* package-level flag globals
// to their non-interactive defaults, mirroring setupDoInstallTest's pattern.
func setupDoProfileImportTest(t *testing.T) (*core.Service, *domain.Game, *fakeInstallSource) {
	t.Helper()

	configDir = t.TempDir()
	dataDir = t.TempDir()
	gameDir := t.TempDir()

	svc, err := core.NewService(core.ServiceConfig{
		ConfigDir: configDir, DataDir: dataDir, CacheDir: t.TempDir(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	src := newFakeInstallSource("test-src")
	t.Cleanup(src.Close)
	svc.RegisterSource(src)

	game := &domain.Game{
		ID: "g1", Name: "Game", ModPath: gameDir, LinkMethod: domain.LinkSymlink,
		SourceIDs: map[string]string{"test-src": "g1"},
	}

	oldForce, oldNoInstall, oldVerbose := profileImportForce, profileImportNoInstall, verbose
	profileImportForce = false
	profileImportNoInstall = false
	verbose = false
	t.Cleanup(func() {
		profileImportForce, profileImportNoInstall, verbose = oldForce, oldNoInstall, oldVerbose
	})

	return svc, game, src
}

// buildImportProfileData serializes a hand-built profile (name/gameID/mods)
// to the same portable YAML format ProfileManager.Export produces, ready to
// hand to doProfileImport - config.ExportProfile is a pure serializer, so
// this avoids duplicating YAML by hand.
func buildImportProfileData(t *testing.T, gameID, name string, mods []domain.ModReference) []byte {
	t.Helper()
	data, err := config.ExportProfile(&domain.Profile{Name: name, GameID: gameID, Mods: mods})
	require.NoError(t, err)
	return data
}

// downloadProgressRe matches doProfileImport's carriage-returned download
// progress readouts ("\r    Downloading: 47.3%") - the ONLY
// non-deterministic part of the download paths' output (how many ticks fire,
// and at which percentages, depends on chunk timing). stripDownloadProgress
// removes them so those paths can be pinned with exact full-output equality
// like every other path; everything else, including the ImportDownloadDone
// newline that terminates the progress line, is deterministic and kept.
var downloadProgressRe = regexp.MustCompile(`\r    Downloading: [0-9.]+%`)

func stripDownloadProgress(out string) string {
	return downloadProgressRe.ReplaceAllString(out, "")
}

// TestDoProfileImport_AllInstalled_PrintsSummaryAndSkipsInstallStep pins the
// "all-installed" path: every mod in the profile is already installed AND
// cached, so toDownload is empty - doProfileImport must never prompt, never
// print the download-skip message (guarded by `if len(toDownload) > 0`), and
// return nil after just the summary + save lines.
func TestDoProfileImport_AllInstalled_PrintsSummaryAndSkipsInstallStep(t *testing.T) {
	svc, game, src := setupDoProfileImportTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})

	// The cross-profile scan (:428-438) lists SAVED profile YAML files via
	// pm.List, not bare DB rows - the "default" profile must actually exist
	// on disk for an installed-under-"default" mod to be found while
	// importing into a different profile ("target").
	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	require.NoError(t, svc.GetGameCache(game).Store(game.ID, "test-src", "mod1", "1.0", "mod1.esp", []byte("cached")))
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
	}))

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "mod1", Version: "1.0"}})

	out := captureStdout(t, func() error {
		return doProfileImport(context.Background(), svc, game, data)
	})

	assert.Equal(t, "Importing profile: target\n\nFound 1 mod(s) in profile.\n  ✓ 1 already installed\n\n✓ Imported profile: target\n", out)
}

// TestDoProfileImport_NeedsRedownload_ReinstallsUsingStoredFileIDs pins the
// "needs-redownload" path (a mod's DB record exists but its cache entry is
// gone) all the way through an accepted install: the summary must list it
// under "cache missing, need re-download", and the redownload must use the
// DB-stored FileIDs (:541-552's rule), not the profile YAML's (which this
// fixture leaves empty), proving the CLI-level redownload selection rule
// survives the refactor end to end (complementing
// TestApplyImportRedownloadUsesStoredFileIDs at the core level).
func TestDoProfileImport_NeedsRedownload_ReinstallsUsingStoredFileIDs(t *testing.T) {
	svc, game, src := setupDoProfileImportTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{
			{ID: "main", FileName: "mod1.esp", IsPrimary: true},
			{ID: "extra", FileName: "mod1-extra.esp"},
		})
	src.AddDownload("extra", []byte("extra content"))

	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "default")
	require.NoError(t, err)

	// DB record exists (with FileIDs ["extra"], NOT the primary) but nothing
	// is in cache - a cache-miss redownload.
	require.NoError(t, svc.SaveInstalledMod(context.Background(), &domain.InstalledMod{
		Mod:          domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		ProfileName:  "default",
		UpdatePolicy: domain.UpdateNotify,
		Enabled:      true,
		FileIDs:      []string{"extra"},
	}))

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "mod1", Version: "1.0"}})

	var out string
	withStdin(t, "y\n", func() {
		out = captureStdout(t, func() error {
			return doProfileImport(context.Background(), svc, game, data)
		})
	})

	assert.Equal(t, "Importing profile: target\n"+
		"\n"+
		"Found 1 mod(s) in profile.\n"+
		"  ⚠ 1 cache missing, need re-download:\n"+
		"    - test-src:mod1 v1.0\n"+
		"\n"+
		"Download and install mods? [Y/n]: \n"+
		"✓ Imported profile: target\n"+
		"\n"+
		"Downloading and installing mods...\n"+
		"  Installing test-src:mod1...\n"+
		"\n"+
		"    ✓ Installed: Mod One\n"+
		"\n"+
		"--- Summary ---\n"+
		"Installed: 1\n", stripDownloadProgress(out))

	installed, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "target")
	require.NoError(t, err)
	assert.Equal(t, []string{"extra"}, installed.FileIDs, "the redownload must use the DB-stored FileIDs, not the (empty) profile YAML ones")
}

// TestDoProfileImport_MissingWithInstall_AcceptedInstallsMod pins the
// "missing-with-install" path: a mod absent from the DB entirely, prompt
// accepted with "y".
func TestDoProfileImport_MissingWithInstall_AcceptedInstallsMod(t *testing.T) {
	svc, game, src := setupDoProfileImportTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
	src.AddDownload("main", []byte("mod1 content"))

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "mod1", Version: "1.0"}})

	var out string
	withStdin(t, "y\n", func() {
		out = captureStdout(t, func() error {
			return doProfileImport(context.Background(), svc, game, data)
		})
	})

	assert.Equal(t, "Importing profile: target\n"+
		"\n"+
		"Found 1 mod(s) in profile.\n"+
		"  ↓ 1 need to be downloaded:\n"+
		"    - test-src:mod1 v1.0\n"+
		"\n"+
		"Download and install mods? [Y/n]: \n"+
		"✓ Imported profile: target\n"+
		"\n"+
		"Downloading and installing mods...\n"+
		"  Installing test-src:mod1...\n"+
		"\n"+
		"    ✓ Installed: Mod One\n"+
		"\n"+
		"--- Summary ---\n"+
		"Installed: 1\n", stripDownloadProgress(out))

	_, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "target")
	require.NoError(t, err)
}

// TestDoProfileImport_PromptDeclined_NoInstallHappens pins the decline path:
// answering "n" prints the short (count-less) "Skipped." message and leaves
// zero install mutations, while the profile itself is still saved. Ruling 7:
// the prompt (and its "Skipped." answer) now precede "✓ Imported profile"
// - the profile is no longer saved before the question is asked.
func TestDoProfileImport_PromptDeclined_NoInstallHappens(t *testing.T) {
	svc, game, src := setupDoProfileImportTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "mod1", Version: "1.0"}})

	var out string
	withStdin(t, "n\n", func() {
		out = captureStdout(t, func() error {
			return doProfileImport(context.Background(), svc, game, data)
		})
	})

	assert.Equal(t, "Importing profile: target\n"+
		"\n"+
		"Found 1 mod(s) in profile.\n"+
		"  ↓ 1 need to be downloaded:\n"+
		"    - test-src:mod1 v1.0\n"+
		"\n"+
		"Download and install mods? [Y/n]: Skipped. Use 'lmm profile apply target' to install them later.\n"+
		"\n"+
		"✓ Imported profile: target\n", out)

	_, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "target")
	assert.Error(t, err, "declining must leave zero install mutations")

	pm := getProfileManager(svc)
	saved, err := pm.Get("g1", "target")
	require.NoError(t, err, "declining the install must still save the profile itself")
	assert.Len(t, saved.Mods, 1)
}

// TestDoProfileImport_NoInstallFlag_SkipsPromptEntirely pins --no-install:
// no stdin interaction at all (proven by a stdin pipe that is never written
// to), and the count-bearing "Skipped installing N mod(s)" message.
func TestDoProfileImport_NoInstallFlag_SkipsPromptEntirely(t *testing.T) {
	svc, game, src := setupDoProfileImportTest(t)
	profileImportNoInstall = true
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "mod1", Version: "1.0"}})

	// A stdin pipe that is never written to or closed: any accidental read
	// blocks forever, proving --no-install never prompts (mirrors
	// install_test.go's TestDoInstall_DependencyPath_ConflictsNeverBlockStdin).
	oldStdin := os.Stdin
	stdinR, stdinW, perr := os.Pipe()
	require.NoError(t, perr)
	os.Stdin = stdinR
	t.Cleanup(func() {
		os.Stdin = oldStdin
		_ = stdinW.Close()
		_ = stdinR.Close()
	})

	oldStdout := os.Stdout
	outR, outW, perr := os.Pipe()
	require.NoError(t, perr)
	os.Stdout = outW

	done := make(chan error, 1)
	go func() {
		done <- doProfileImport(context.Background(), svc, game, data)
	}()

	var doErr error
	select {
	case doErr = <-done:
	case <-time.After(5 * time.Second):
		os.Stdout = oldStdout
		t.Fatal("doProfileImport blocked reading stdin - --no-install must never prompt")
	}

	os.Stdout = oldStdout
	require.NoError(t, outW.Close())
	outBytes, readErr := io.ReadAll(outR)
	require.NoError(t, readErr)
	out := string(outBytes)

	require.NoError(t, doErr)
	assert.Equal(t, "Importing profile: target\n"+
		"\n"+
		"Found 1 mod(s) in profile.\n"+
		"  ↓ 1 need to be downloaded:\n"+
		"    - test-src:mod1 v1.0\n"+
		"\n"+
		"✓ Imported profile: target\n"+
		"\n"+
		"Skipped installing 1 mod(s). Use 'lmm profile apply target' to install them later.\n", out)

	_, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "target")
	assert.Error(t, err)
}

// TestDoProfileImport_JSONOutputReturnsConfirmationRequired pins the
// non-interactive rule (v2 Phase 3 Ruling 2) at doProfileImport's "Download
// and install mods?" prompt: under --json with no -y, the import must fail
// with core.ErrConfirmationRequired before ever reading stdin. Since Ruling
// 7 moved the prompt before the save, this leaves the profile itself
// unsaved too - a genuine zero-mutation failure, not merely a skipped
// install.
func TestDoProfileImport_JSONOutputReturnsConfirmationRequired(t *testing.T) {
	svc, game, src := setupDoProfileImportTest(t)
	withJSONOutput(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "mod1", Version: "1.0"}})

	err := assertStdinNeverRead(t, func() error {
		return doProfileImport(context.Background(), svc, game, data)
	})

	require.ErrorIs(t, err, core.ErrConfirmationRequired)
	_, instErr := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "target")
	assert.Error(t, instErr, "must leave zero install mutations")
	pm := getProfileManager(svc)
	_, profErr := pm.Get("g1", "target")
	assert.Error(t, profErr, "must not even save the profile - the prompt precedes the save (Ruling 7)")
}

// TestDoProfileImport_YesFlagSkipsPromptEntirely pins -y: the prompt text
// never prints and the download/install proceeds without reading stdin,
// matching the accepted-prompt path byte-for-byte minus the prompt line
// itself.
func TestDoProfileImport_YesFlagSkipsPromptEntirely(t *testing.T) {
	svc, game, src := setupDoProfileImportTest(t)
	profileImportYes = true
	t.Cleanup(func() { profileImportYes = false })
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
	src.AddDownload("main", []byte("mod1 content"))

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "mod1", Version: "1.0"}})

	out := captureStdout(t, func() error {
		return doProfileImport(context.Background(), svc, game, data)
	})

	assert.NotContains(t, out, "Download and install mods?")
	assert.Equal(t, "Importing profile: target\n"+
		"\n"+
		"Found 1 mod(s) in profile.\n"+
		"  ↓ 1 need to be downloaded:\n"+
		"    - test-src:mod1 v1.0\n"+
		"\n"+
		"✓ Imported profile: target\n"+
		"\n"+
		"Downloading and installing mods...\n"+
		"  Installing test-src:mod1...\n"+
		"\n"+
		"    ✓ Installed: Mod One\n"+
		"\n"+
		"--- Summary ---\n"+
		"Installed: 1\n", stripDownloadProgress(out))

	_, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "target")
	require.NoError(t, err)
}

// TestDoProfileImport_YesFlagUnderJSON_ProceedsWithoutReadingStdin guards
// the combination the Task 9 review flagged as untested: -y under --json
// together completes the import rather than hitting the --json/stdin guard.
func TestDoProfileImport_YesFlagUnderJSON_ProceedsWithoutReadingStdin(t *testing.T) {
	svc, game, src := setupDoProfileImportTest(t)
	withJSONOutput(t)
	profileImportYes = true
	t.Cleanup(func() { profileImportYes = false })
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})
	src.AddDownload("main", []byte("mod1 content"))

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "mod1", Version: "1.0"}})

	var importErr error
	out := captureStdout(t, func() error {
		importErr = assertStdinNeverRead(t, func() error {
			return doProfileImport(context.Background(), svc, game, data)
		})
		return nil
	})

	require.NoError(t, importErr)
	assert.NotContains(t, out, "Download and install mods?")

	_, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "target")
	require.NoError(t, err)
}

// TestDoProfileImport_ForceOverwritesExistingProfile pins --force: importing
// over an already-saved profile of the same name succeeds and replaces its
// mod list.
func TestDoProfileImport_ForceOverwritesExistingProfile(t *testing.T) {
	svc, game, _ := setupDoProfileImportTest(t)
	profileImportNoInstall = true
	profileImportForce = true

	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "test-src", ModID: "old-mod", Version: "1.0"}))

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "new-mod", Version: "1.0"}})

	out := captureStdout(t, func() error {
		return doProfileImport(context.Background(), svc, game, data)
	})

	assert.Equal(t, "Importing profile: target\n"+
		"\n"+
		"Found 1 mod(s) in profile.\n"+
		"  ↓ 1 need to be downloaded:\n"+
		"    - test-src:new-mod v1.0\n"+
		"\n"+
		"✓ Imported profile: target\n"+
		"\n"+
		"Skipped installing 1 mod(s). Use 'lmm profile apply target' to install them later.\n", out)

	saved, err := pm.Get(game.ID, "target")
	require.NoError(t, err)
	require.Len(t, saved.Mods, 1)
	assert.Equal(t, "new-mod", saved.Mods[0].ModID, "--force must overwrite the existing profile's mod list")
}

// TestDoProfileImport_PromptReadFailure_PropagatesErrorWithoutSummary pins
// the prompt's genuine-I/O-failure path (fix wave 1, Important 1): when
// readPromptLine fails with a non-EOF error, doProfileImport must propagate
// that error - the pre-extraction CLI's own `if err != nil { return err }`
// right after the prompt - and must NOT print the "--- Summary ---" block
// (the original regression: the prompt's callback signalled the failure by
// declining, so ApplyImport treated it as an ordinary decline, swallowed
// the error and a spurious "Installed: 0" summary printed). Ruling 7 moved
// the prompt ahead of ApplyImport, so the profile is not saved either - a
// recorded delta. The failure is simulated by
// swapping os.Stdin for an already-CLOSED pipe read end: reading a closed
// *os.File returns os.ErrClosed (non-EOF), which readPromptLineFrom wraps
// as "reading input: ...".
func TestDoProfileImport_PromptReadFailure_PropagatesErrorWithoutSummary(t *testing.T) {
	svc, game, src := setupDoProfileImportTest(t)
	src.AddMod(&domain.Mod{ID: "mod1", SourceID: "test-src", Name: "Mod One", Version: "1.0", GameID: "g1"},
		[]domain.DownloadableFile{{ID: "main", FileName: "mod1.esp", IsPrimary: true}})

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "mod1", Version: "1.0"}})

	oldStdin := os.Stdin
	stdinR, stdinW, perr := os.Pipe()
	require.NoError(t, perr)
	require.NoError(t, stdinW.Close())
	require.NoError(t, stdinR.Close()) // closed BEFORE the read: os.ErrClosed, a non-EOF failure
	os.Stdin = stdinR
	t.Cleanup(func() { os.Stdin = oldStdin })

	out, doErr := captureStdoutErr(t, func() error {
		return doProfileImport(context.Background(), svc, game, data)
	})

	require.Error(t, doErr, "a genuine stdin read failure must propagate, never be swallowed as a decline")
	assert.ErrorIs(t, doErr, os.ErrClosed)
	assert.Contains(t, doErr.Error(), "reading input")
	assert.NotContains(t, out, "--- Summary ---", "a prompt read failure must not fall through to the summary block")
	assert.NotContains(t, out, "Skipped", "a prompt read failure is not a decline")

	_, err := svc.GetInstalledMod(context.Background(), "test-src", "mod1", "g1", "target")
	assert.Error(t, err, "no install may happen after a failed prompt read")

	_, err = getProfileManager(svc).Get("g1", "target")
	assert.Error(t, err, "Ruling 7 delta: the prompt precedes ApplyImport, so a failed read now leaves the profile unsaved too")
}

// TestDoProfileImport_ExistingProfileWithoutForce_ReturnsError pins the
// error path: importing over an existing profile without --force must fail
// with pm.ImportWithOptions' own message, and must not overwrite anything.
// Ruling 7 delta: the prompt now precedes the save, so it is printed before
// the failure - the answer is simply thrown away with the failed import.
func TestDoProfileImport_ExistingProfileWithoutForce_ReturnsError(t *testing.T) {
	svc, game, _ := setupDoProfileImportTest(t)

	pm := getProfileManager(svc)
	_, err := pm.Create(game.ID, "target")
	require.NoError(t, err)
	require.NoError(t, pm.AddMod(game.ID, "target", domain.ModReference{SourceID: "test-src", ModID: "old-mod", Version: "1.0"}))

	data := buildImportProfileData(t, "g1", "target", []domain.ModReference{{SourceID: "test-src", ModID: "new-mod", Version: "1.0"}})

	var out string
	var doErr error
	withStdin(t, "y\n", func() {
		out, doErr = captureStdoutErr(t, func() error {
			return doProfileImport(context.Background(), svc, game, data)
		})
	})

	require.Error(t, doErr)
	assert.Contains(t, doErr.Error(), "profile already exists: target")
	assert.Contains(t, doErr.Error(), "use --force")
	assert.Equal(t, "Importing profile: target\n"+
		"\n"+
		"Found 1 mod(s) in profile.\n"+
		"  ↓ 1 need to be downloaded:\n"+
		"    - test-src:new-mod v1.0\n"+
		"\n"+
		"Download and install mods? [Y/n]: ", out,
		"a failed save must print the preview and the (now-preceding) prompt only - never the success line or anything after it")

	saved, err := pm.Get(game.ID, "target")
	require.NoError(t, err)
	require.Len(t, saved.Mods, 1)
	assert.Equal(t, "old-mod", saved.Mods[0].ModID, "a rejected import must leave the existing profile untouched")
}
