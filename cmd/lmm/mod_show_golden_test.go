package main

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// updateModShowGoldens re-records the mod show goldens. Run ONCE, before the
// #86 core extraction:
//
//	go test ./cmd/lmm -run TestModShowGolden -update-modshow
//
// After that the files are frozen: they are the proof that extracting
// Service.ModDetail did not change a byte of CLI output. Task 3 is the ONLY
// later task allowed to re-record them, and only the description cases.
//
// Named -update-modshow rather than -update: verify_golden_test.go (#224)
// already registers a package-level "-update" flag, and Go's flag package
// panics on a duplicate registration within the same package.
var updateModShowGoldens = flag.Bool("update-modshow", false, "re-record mod show goldens")

func assertModShowGolden(t *testing.T, name, actual string) {
	t.Helper()
	path := filepath.Join("testdata", "mod_show_golden", name+".txt")
	if *updateModShowGoldens {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(actual), 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "golden %s missing - record it with -update-modshow BEFORE refactoring", path)
	assert.Equal(t, string(want), actual, "mod show output drifted from the recorded golden")
}

// richMod is the fixture every golden case shows: every optional field
// populated, so the goldens pin the omit-when-empty branches by contrast with
// the sparse cases below.
func richMod(gameID string) *domain.Mod {
	endorsements := int64(84213)
	return &domain.Mod{
		ID: "a", SourceID: "src", GameID: gameID,
		Name: "Mod A", Version: "1.5", Author: "Author A",
		Category:     "Utilities",
		Summary:      "A short summary.",
		Description:  "<p>Line one.</p><br/><p>Line &amp; two.</p>",
		SourceURL:    "https://example.invalid/mods/1",
		PictureURL:   "https://example.invalid/img/1.png",
		Endorsements: &endorsements,
	}
}

func TestModShowGolden_NotInstalled(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(richMod(game.ID), nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "not_installed", out)
}

func TestModShowGolden_InstalledUnlocked(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(richMod(game.ID), nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "installed_unlocked", out)
}

func TestModShowGolden_InstalledLockedWithConvergeHint(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(richMod(game.ID), nil)
	require.NoError(t, svc.NewProfileManager().SetModLock(context.Background(), game.ID, "default", "src", "a", "1.2.3"))

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "installed_locked", out)
}

// Sparse mod: every optional field empty, pinning the omit branches.
func TestModShowGolden_SparseMod(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(&domain.Mod{ID: "a", SourceID: "src", GameID: game.ID, Name: "Mod A", Version: "1.5"}, nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "sparse", out)
}

// TestModShowGolden_JSON's json_installed golden was re-recorded once, in
// v2 Phase 3 Task 6 (#302), when --json switched to core.ModDetail - the
// single deliberate JSON shape change Ruling 3 reserves. The same document
// is also pinned by TestJSONGolden_ModShow ("mod_show_installed",
// -update-json-cli); a future shape change has to move BOTH.
func TestModShowGolden_JSON(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	seedLockableMod(t, svc, game, "a", "Mod A", "1.5")
	src.AddMod(richMod(game.ID), nil)

	old := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = old })

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})
	assertModShowGolden(t, "json_installed", out)
}

// TestDoModShow_DescriptionHTMLCleaned (#86): mod show printed a source's raw
// description HTML - literal <p> tags and &amp; entities in the user's
// terminal. It now runs through the same core.CleanChangelog the update flow
// already uses, so both render one cleaned text.
// JSON is deliberately NOT cleaned: it is a machine contract, and a consumer
// may want the original markup.
func TestDoModShow_DescriptionHTMLCleaned(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(richMod(game.ID), nil)

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	assert.Contains(t, out, "Line one.")
	assert.Contains(t, out, "Line & two.")
	assert.NotContains(t, out, "<p>", "raw HTML tags must not reach the terminal")
	assert.NotContains(t, out, "&amp;", "HTML entities must be decoded")
}

// TestDoModShow_JSONDescriptionStaysRaw pins the deliberate asymmetry above.
func TestDoModShow_JSONDescriptionStaysRaw(t *testing.T) {
	svc, game, src := setupDoModLockTest(t)
	src.AddMod(richMod(game.ID), nil)

	old := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = old })

	out := captureStdout(t, func() error {
		return doModShow(context.Background(), svc, game, "a")
	})

	var got core.ModDetail
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	require.NotNil(t, got.Mod)
	assert.Equal(t, "<p>Line one.</p><br/><p>Line &amp; two.</p>", got.Mod.Description,
		"--json is a machine contract; the raw description must survive")
}
