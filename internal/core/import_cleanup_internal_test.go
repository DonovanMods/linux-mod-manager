package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// #644: an archive import stages its tree outside the cache, and a refusal
// discards the staging directory - the whole of the refusal's "nothing
// happened" promise since the ingest commits nothing before the gates.
// #310 item 1 still holds for it: a removal that fails is RETURNED, for the
// caller to hang on the *ConflictError (or the Result's Warnings), never
// swallowed.
//
// The failure is injected directly rather than through a whole import:
// nothing in the flow can make the removal fail without also breaking the
// ingest that precedes it.
func TestStagedEntry_DiscardReportsAFailedRemoval(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory permissions this test relies on")
	}
	root := t.TempDir()
	st := &stagedEntry{root: root, cache: cache.New(root), key: cache.Key{GameID: "g1", SourceID: "s", ModID: "m", Version: "1.0"}}
	locked := filepath.Join(st.path(), "locked")
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

	if err := st.discard(); err == nil {
		t.Fatal("a removal that failed must be reported")
	}
	if err := os.Chmod(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.discard(); err != nil {
		t.Fatalf("a removal that succeeds reports nothing: %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the staging directory must be gone: %v", err)
	}
	var none *stagedEntry
	if err := none.discard(); err != nil {
		t.Errorf("no staged tree, nothing to discard: %v", err)
	}
}

// commit replaces the entry it names, refuses another key's entry and
// anything at the entry's path that is not a directory, and reports
// whether it created the entry.
func TestStagedEntry_Commit(t *testing.T) {
	ctx := context.Background()
	real := cache.New(t.TempDir())
	k := cache.Key{GameID: "g1", SourceID: "s", ModID: "m", Version: "1.0"}
	stage := func(content string) *stagedEntry {
		root := t.TempDir()
		st := &stagedEntry{root: root, cache: cache.New(root), key: cache.Key{GameID: "g1", SourceID: "s", ModID: "m", Version: "unknown"}}
		if err := st.cache.Store(st.key, "a.esp", []byte(content)); err != nil {
			t.Fatal(err)
		}
		return st
	}
	read := func() string {
		data, err := os.ReadFile(filepath.Join(real.ModPath(k), "a.esp"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	created, err := stage("one").commit(ctx, real, k)
	if err != nil || !created {
		t.Fatalf("first commit: created=%v err=%v", created, err)
	}
	if got := read(); got != "one" {
		t.Errorf("entry = %q", got)
	}
	created, err = stage("two").commit(ctx, real, k)
	if err != nil || created {
		t.Fatalf("second commit: created=%v err=%v", created, err)
	}
	if got := read(); got != "two" {
		t.Errorf("entry = %q, want the replacement", got)
	}

	// Another key's entry at the path: a variant's, where a plain key
	// spells the same directory.
	collide := cache.Key{GameID: "g1", SourceID: "s", ModID: "m", Version: "2.0@abcdef123456"}
	variant := cache.Key{GameID: "g1", SourceID: "s", ModID: "m", Version: "2.0", Variant: "abcdef123456"}
	if err := real.Store(variant, "b.esp", []byte("b")); err != nil {
		t.Fatal(err)
	}
	if _, err := stage("three").commit(ctx, real, collide); err == nil {
		t.Error("a commit over another key's entry must be refused")
	}
	if !real.Exists(variant) {
		t.Error("the other key's entry must survive")
	}

	blocked := cache.Key{GameID: "g1", SourceID: "s", ModID: "m", Version: "3.0"}
	if err := os.MkdirAll(filepath.Dir(real.ModPath(blocked)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(real.ModPath(blocked), []byte("blocker"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := stage("four").commit(ctx, real, blocked); err == nil {
		t.Error("a commit over a file must be refused")
	}
	if data, err := os.ReadFile(real.ModPath(blocked)); err != nil || string(data) != "blocker" {
		t.Errorf("the blocking file must be left alone: %q %v", data, err)
	}
}

// A staged import tree a killed lmm left behind is swept when a Service
// opens, once it is older than any import runs; a recent one - another
// process's import in flight - and anything else in the staging root stay.
func TestSweepStaleImportEntries(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, stagingDirName)
	old := filepath.Join(root, importEntryStagingPrefix+"old")
	recent := filepath.Join(root, importEntryStagingPrefix+"recent")
	other := filepath.Join(root, "lmm-download-old")
	for _, d := range []string{old, recent, other} {
		if err := os.MkdirAll(filepath.Join(d, "g1"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	past := time.Now().Add(-2 * staleImportEntryAge)
	for _, d := range []string{old, other} {
		if err := os.Chtimes(d, past, past); err != nil {
			t.Fatal(err)
		}
	}
	svc, err := NewService(ServiceConfig{ConfigDir: t.TempDir(), DataDir: dataDir, CacheDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })

	if _, err := os.Stat(old); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a stale staged import must be swept: %v", err)
	}
	for _, d := range []string{recent, other} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s must stay: %v", filepath.Base(d), err)
		}
	}
}

// A staging root on another filesystem than the cache cannot be renamed
// into it: commit copies the tree instead, with the same result.
func TestStagedEntry_CommitCopiesAcrossFilesystems(t *testing.T) {
	orig := stagedRename
	stagedRename = func(string, string) error { return &os.LinkError{Op: "rename", Err: syscall.EXDEV} }
	t.Cleanup(func() { stagedRename = orig })

	root := t.TempDir()
	st := &stagedEntry{root: root, cache: cache.New(root), key: cache.Key{GameID: "g1", SourceID: "s", ModID: "m", Version: "unknown"}}
	if err := st.cache.Store(st.key, "sub/a.esp", []byte("copied")); err != nil {
		t.Fatal(err)
	}
	real := cache.New(t.TempDir())
	k := cache.Key{GameID: "g1", SourceID: "s", ModID: "m", Version: "1.0", Variant: "archive-0123abcd-0123456789ab"}
	created, err := st.commit(context.Background(), real, k)
	if err != nil || !created {
		t.Fatalf("commit: created=%v err=%v", created, err)
	}
	data, err := os.ReadFile(filepath.Join(real.ModPath(k), "sub", "a.esp"))
	if err != nil || string(data) != "copied" {
		t.Fatalf("entry = %q, %v", data, err)
	}
	if err := cache.CheckKey(real.ModPath(k), k); err != nil {
		t.Errorf("the copied entry is stamped as its key's: %v", err)
	}
	if _, err := os.Stat(real.ModPath(k) + ".staging"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("no staging directory is left beside the entry: %v", err)
	}
}
