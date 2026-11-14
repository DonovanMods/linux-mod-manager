package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
