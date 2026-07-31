package unrealpak

import (
	"crypto/sha1" //nolint:gosec // pak format uses SHA1, not our choice
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip_WriteThenRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roundtrip.pak")
	files := map[string][]byte{
		"Icarus/Content/Data/AI-D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":235}}`),
		"Icarus/Content/Data/Other.json":         []byte(`{"foo":"bar"}`),
		"DataTableMetadata.json":                 []byte(`{"root":true}`), // root-level: "/" directory key
	}

	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	for name, data := range files {
		if err := w.AddFile(name, data); err != nil {
			t.Fatalf("AddFile(%s): %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()

	got := r.Files()
	if len(got) != len(files) {
		t.Fatalf("got %d files, want %d", len(got), len(files))
	}
	for name, want := range files {
		data, err := r.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", name, err)
		}
		if string(data) != string(want) {
			t.Errorf("ReadFile(%s) = %q, want %q", name, data, want)
		}
	}
}

// Structural assertions on the bytes themselves. Open() proves the Reader
// accepts what the Writer emits, but the Reader is not the audience that
// matters most — Icarus's engine is. These check the properties every real pak
// exhibits, so a drift away from the engine-proven shape fails here rather than
// silently in-game.
func TestRoundTrip_StructuralShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shape.pak")
	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	content := []byte(`{"a":1}`)
	if err := w.AddFile("Icarus/Content/Data/Test.json", content); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	ft := data[len(data)-footerSize:]
	indexOffset := int64(binary.LittleEndian.Uint64(ft[25:33]))
	indexSize := int64(binary.LittleEndian.Uint64(ft[33:41]))

	// The footer's SHA1 must cover the primary index exactly.
	indexSum := sha1.Sum(data[indexOffset : indexOffset+indexSize]) //nolint:gosec
	if string(indexSum[:]) != string(ft[41:61]) {
		t.Error("footer IndexHash does not match the primary index bytes")
	}

	// The data section holds one 53-byte header plus the payload, and the
	// index starts immediately after it — regions tile with no gap.
	if want := int64(storedHeaderSize + len(content)); indexOffset != want {
		t.Errorf("index starts at %d, want %d (53-byte header + %d-byte payload)",
			indexOffset, want, len(content))
	}
	// The per-entry header's Hash field must be the payload's SHA1.
	if sum := sha1.Sum(content); string(sum[:]) != string(data[28:48]) { //nolint:gosec
		t.Error("per-entry header Hash does not match the payload SHA1")
	}
	// Its Offset field is zero, as in every real pak.
	if got := binary.LittleEndian.Uint64(data[0:8]); got != 0 {
		t.Errorf("per-entry header Offset = %d, want 0", got)
	}
}
