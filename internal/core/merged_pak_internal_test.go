package core

import (
	"os"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

func TestMergedFingerprint_Deterministic(t *testing.T) {
	f := MergedFingerprint{
		BaseIndexHash: "abc123",
		Mods: []MergedFingerprintEntry{
			{SourceID: "icarus", ModID: "bear-mount", Version: "1.0", Checksum: "deadbeef"},
			{SourceID: "icarus", ModID: "wolf-mount", Version: "2.0", Checksum: "cafef00d"},
		},
	}
	b1, err := marshalMergedFingerprint(f)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	b2, err := marshalMergedFingerprint(f)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("marshal not deterministic: %q vs %q", b1, b2)
	}
}

func TestMergedFingerprintsEqual_IdenticalInputs(t *testing.T) {
	f := MergedFingerprint{
		BaseIndexHash: "abc123",
		Mods:          []MergedFingerprintEntry{{SourceID: "icarus", ModID: "bear-mount", Version: "1.0", Checksum: "deadbeef"}},
	}
	eq, err := mergedFingerprintsEqual(f, f)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if !eq {
		t.Errorf("identical fingerprints compared unequal")
	}
}

func TestMergedFingerprintsEqual_BaseHashChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc123", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"}}}
	b := a
	b.BaseIndexHash = "def456"
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("base pak change (regeneration trigger) must compare unequal")
	}
}

func TestMergedFingerprintsEqual_ModSetChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"}}}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"},
		{SourceID: "icarus", ModID: "m2", Version: "1.0", Checksum: "y"},
	}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("enabling a mod (regeneration trigger) must compare unequal")
	}
}

func TestMergedFingerprintsEqual_LoadOrderChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"},
		{SourceID: "icarus", ModID: "m2", Version: "1.0", Checksum: "y"},
	}}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m2", Version: "1.0", Checksum: "y"},
		{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"},
	}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("a load-order swap (regeneration trigger) must compare unequal, got equal")
	}
}

func TestMergedFingerprintsEqual_VersionChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"}}}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "2.0", Checksum: "x2"}}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("a mod version bump (regeneration trigger) must compare unequal")
	}
}

func TestMergedFingerprintsEqual_EmptyModsBothSides(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: nil}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if !eq {
		t.Errorf("nil vs empty Mods slice must still compare equal (both marshal to the same JSON array shape)")
	}
}

func TestMergeSourceKind(t *testing.T) {
	tests := map[string]string{
		"pak":          source.MergeSourcePak,
		"MyMod.PAK":    source.MergeSourcePak,
		"exmodz":       source.MergeSourceExmodz,
		"MyMod.exmodz": source.MergeSourceExmodz,
		"weird.zip":    source.MergeSourceExmodz, // unknown retained kind: today's behavior
	}
	for fileID, want := range tests {
		if got := mergeSourceKind(fileID); got != want {
			t.Errorf("mergeSourceKind(%q) = %q, want %q", fileID, got, want)
		}
	}
}

func TestFingerprintEqualityIgnoresOutcomes(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "h", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m", Version: "1", Checksum: "c", Kind: source.MergeSourcePak, Converted: true},
	}}
	b := MergedFingerprint{BaseIndexHash: "h", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m", Version: "1", Checksum: "c", Kind: source.MergeSourcePak, Converted: false, FailReason: "x"},
	}}
	eq, err := mergedFingerprintsEqual(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("outcome fields must not affect input equality (retry only when inputs change)")
	}

	c := b
	c.Mods = []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m", Version: "2", Checksum: "c", Kind: source.MergeSourcePak}}
	eq, err = mergedFingerprintsEqual(a, c)
	if err != nil {
		t.Fatal(err)
	}
	if eq {
		t.Fatal("input fields (Version) must affect equality")
	}
}

func TestReadOldFingerprintMarkerCompat(t *testing.T) {
	// A pre-#221 marker has no Kind/Converted/FailReason - it must read
	// fine and compare EQUAL to a current computation with the same inputs
	// (Kind "" vs "exmodz" must not force a spurious regen).
	dir := t.TempDir()
	old := []byte(`{"BaseIndexHash":"h","Mods":[{"SourceID":"icarus","ModID":"m","Version":"1","Checksum":"c"}]}`)
	if err := os.WriteFile(cache.MergeFingerprintPath(dir), old, 0644); err != nil {
		t.Fatal(err)
	}
	stored, ok := readMergedFingerprint(dir)
	if !ok {
		t.Fatal("old marker unreadable")
	}
	current := MergedFingerprint{BaseIndexHash: "h", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m", Version: "1", Checksum: "c", Kind: source.MergeSourceExmodz, Converted: true},
	}}
	eq, err := mergedFingerprintsEqual(current, stored)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("pre-#221 marker must not trigger a regen for unchanged exmodz inputs")
	}
}
