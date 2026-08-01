package unrealpak

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestWriter_CreateAndClose_ProducesValidFooter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.pak")
	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := w.AddFile("Icarus/Content/Data/Test.json", []byte(`{"a":1}`)); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading output: %v", err)
	}
	if len(data) <= footerSize {
		t.Fatalf("output is %d bytes, want more than a bare %d-byte footer", len(data), footerSize)
	}
	ft := data[len(data)-footerSize:]
	if got := binary.LittleEndian.Uint32(ft[17:21]); got != magic {
		t.Errorf("footer magic = %#x, want %#x", got, magic)
	}
	if got := int32(binary.LittleEndian.Uint32(ft[21:25])); got != writeVersion {
		t.Errorf("footer version = %d, want %d", got, writeVersion)
	}
	if ft[16] != 0 {
		t.Errorf("bEncryptedIndex = %d, want 0", ft[16])
	}
}

// Output must not depend on AddFile ordering — Close sorts by path.
func TestWriter_Close_IsDeterministic(t *testing.T) {
	build := func(order []string) []byte {
		t.Helper()
		path := filepath.Join(t.TempDir(), "out.pak")
		w, err := Create(path)
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		for _, name := range order {
			if err := w.AddFile(name, []byte(name)); err != nil {
				t.Fatalf("AddFile(%s): %v", name, err)
			}
		}
		if err := w.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading output: %v", err)
		}
		return data
	}

	a := build([]string{"a/one.json", "b/two.json", "root.json"})
	b := build([]string{"root.json", "b/two.json", "a/one.json"})
	if !bytes.Equal(a, b) {
		t.Error("output differs with AddFile order; Close must be deterministic")
	}
}

func TestWriter_AddFile_RejectsDuplicatePath(t *testing.T) {
	w, err := Create(filepath.Join(t.TempDir(), "out.pak"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer w.Close() //nolint:errcheck
	if err := w.AddFile("x.json", []byte("{}")); err != nil {
		t.Fatalf("AddFile: %v", err)
	}
	if err := w.AddFile("x.json", []byte("{}")); err == nil {
		t.Error("expected error adding a duplicate path, got nil")
	}
}

func TestWriter_AddFile_AfterClose_Errors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.pak")
	w, err := Create(path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := w.AddFile("x.json", []byte("{}")); err == nil {
		t.Error("expected error adding file after Close, got nil")
	}
}

// checkEncodedLocationFits is tested directly on the boundary rather than by
// constructing a >2 GiB encoded-index fixture, which would be impractically
// slow and memory-hungry for a unit test.
func TestCheckEncodedLocationFits(t *testing.T) {
	if err := checkEncodedLocationFits(0); err != nil {
		t.Errorf("checkEncodedLocationFits(0): %v", err)
	}
	if err := checkEncodedLocationFits(math.MaxInt32); err != nil {
		t.Errorf("checkEncodedLocationFits(MaxInt32): %v", err)
	}
	if err := checkEncodedLocationFits(math.MaxInt32 + 1); err == nil {
		t.Error("checkEncodedLocationFits(MaxInt32+1) = nil, want error")
	}
}
