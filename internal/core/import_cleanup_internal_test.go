package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// #310 item 1: discardImportedCacheEntry is the whole of the conflict
// refusal's "nothing happened" promise, and it used to swallow a failed
// cache.Delete into a Debug log - so an orphaned entry, a full copy of the
// archive, could be left behind with no user-visible signal. It now RETURNS
// what it could not remove, for the caller to hang on the *ConflictError
// (or, on the stale-plan path, the Result's Warnings), and logs at Warn.
//
// The failure is injected directly rather than through a whole import: a
// cache write that survives to the gate is exactly what makes the removal
// possible, so there is no point in the flow at which a test could make the
// removal fail without also breaking the ingest that precedes it.
func TestDiscardImportedCacheEntry_ReportsFailedDeletes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this test relies on")
	}
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	mod := &domain.Mod{ID: "B1", SourceID: "acme-source", Version: "1.0", GameID: "g1"}
	gameCache := svc.GetGameCache(game)

	// Both entries the discard touches: the one the ingest wrote (pre-enrich)
	// and the one a successful enrichment rename moved it to.
	for _, e := range []struct{ modID, version string }{{"B1", "unknown"}, {"B1", "1.0"}} {
		dir := gameCache.ModPath("g1", "acme-source", e.modID, e.version)
		locked := filepath.Join(dir, "locked")
		if err := os.MkdirAll(locked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(locked, "pinned"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	}

	result := &ImportArchiveResult{Mod: mod, Renamed: true}
	warnings := svc.discardImportedCacheEntry(game, result, "B1", "unknown", false)

	if len(warnings) != 2 {
		t.Fatalf("want a warning per failed delete, got %d: %v", len(warnings), warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{"B1@unknown", "B1@1.0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings do not name %s: %q", want, joined)
		}
	}
}

// An entry that WAS removed reports nothing, and an entry this call did not
// create is not touched at all.
func TestDiscardImportedCacheEntry_QuietOnSuccessAndPreExisting(t *testing.T) {
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: t.TempDir(), CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	game := &domain.Game{ID: "g1", Name: "Game", ModPath: t.TempDir()}
	mod := &domain.Mod{ID: "B1", SourceID: "acme-source", Version: "1.0", GameID: "g1"}
	gameCache := svc.GetGameCache(game)
	if err := gameCache.Store("g1", "acme-source", "B1", "unknown", "a.esp", []byte("a")); err != nil {
		t.Fatal(err)
	}

	if got := svc.discardImportedCacheEntry(game, &ImportArchiveResult{Mod: mod}, "B1", "unknown", false); got != nil {
		t.Errorf("a successful cleanup must report nothing, got %v", got)
	}
	if gameCache.Exists("g1", "acme-source", "B1", "unknown") {
		t.Error("the entry this call created must be gone")
	}

	if err := gameCache.Store("g1", "acme-source", "B1", "unknown", "a.esp", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if got := svc.discardImportedCacheEntry(game, &ImportArchiveResult{Mod: mod}, "B1", "unknown", true); got != nil {
		t.Errorf("a pre-existing entry is not this call's to remove, got %v", got)
	}
	if !gameCache.Exists("g1", "acme-source", "B1", "unknown") {
		t.Error("a pre-existing entry must be left alone")
	}
}
