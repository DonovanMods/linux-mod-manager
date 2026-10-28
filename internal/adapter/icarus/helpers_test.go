package icarus

import (
	"path/filepath"
	"testing"

	"github.com/DonovanMods/go-unrealpak"
)

// writeTestBasePak builds a stored, unencrypted version-11 pak holding one
// entry per (mount-relative path, content) pair, via the Task 4 Writer. It is
// the shared fixture builder for this package's compile tests.
func writeTestBasePak(t *testing.T, files map[string][]byte) string {
	t.Helper()
	pakPath := filepath.Join(t.TempDir(), "data.pak")
	w, err := unrealpak.Create(pakPath)
	if err != nil {
		t.Fatalf("creating test base pak: %v", err)
	}
	for rel, data := range files {
		if err := w.AddFile(rel, data); err != nil {
			t.Fatalf("AddFile(%q): %v", rel, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("closing test base pak: %v", err)
	}
	return pakPath
}
