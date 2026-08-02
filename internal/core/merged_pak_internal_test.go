package core

import "testing"

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
