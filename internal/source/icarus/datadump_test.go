package icarus

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// writeTestBasePak builds a stored, unencrypted version-11 pak holding one
// entry per (mount-relative path, content) pair, via the Task 4 Writer. It
// stands in for Task 12's identically-named helper, which does not exist yet
// at this point in the plan's task order.
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

// tarGz builds a dump-shaped tarball: a single top-level directory, then the
// table tree beneath it, LF-terminated exactly as the real repo stores it.
func tarGz(t *testing.T, root string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		hdr := &tar.Header{Name: root + "/" + name, Mode: 0o644, Size: int64(len(body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDetectBuild_ReadsVersionJSON(t *testing.T) {
	root := t.TempDir()
	cfg := filepath.Join(root, "Icarus", "Config")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	const vjson = `{"Name":"Icarus","Version":{"Major":3,"Minor":0,"Patch":21,` +
		`"Changelist":155335,"BuildType":"Shipping","FeatureLevel":"DangerousHorizons"},` +
		`"Data":{"Changelist":155151}}`
	if err := os.WriteFile(filepath.Join(cfg, "version.json"), []byte(vjson), 0o644); err != nil {
		t.Fatal(err)
	}

	b, err := detectBuild(root)
	if err != nil {
		t.Fatalf("detectBuild: %v", err)
	}
	if got := b.String(); got != "3.0.21.155335" {
		t.Errorf("Build.String() = %q, want 3.0.21.155335", got)
	}
	if b.DataChangelist != 155151 {
		t.Errorf("DataChangelist = %d, want 155151", b.DataChangelist)
	}
}

func TestDetectBuild_MissingVersionFile_Errors(t *testing.T) {
	if _, err := detectBuild(t.TempDir()); err == nil {
		t.Fatal("expected error when version.json is absent, got nil")
	}
}

// A dump whose stored tables match the local pak byte-for-byte (after CRLF
// restoration) is accepted, and its tables are exposed with shipped bytes.
func TestDumpStore_DumpForBuild_AcceptsMatchingDump(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	shipped := []byte("{\r\n    \"Rows\": []\r\n}") // CRLF, as the pak stores it
	dumped := "{\n    \"Rows\": []\n}"              // LF, as the repo stores it

	pak := writeTestBasePak(t, map[string][]byte{rel: shipped}) // Task 12's helper
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-abc123", map[string]string{rel: dumped}))
	}))
	defer srv.Close()

	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL // test seam

	dump, err := store.DumpForBuild(context.Background(), pak, "")
	if err != nil {
		t.Fatalf("DumpForBuild: %v", err)
	}
	got, ok := dump.Table(rel)
	if !ok {
		t.Fatalf("dump has no table %q", rel)
	}
	if !bytes.Equal(got, shipped) {
		t.Errorf("table bytes = %q, want the shipped CRLF form %q", got, shipped)
	}
}

// The case that is live today: the newest dump is an older week than the
// install. Must fail loudly and name what disagreed.
func TestDumpStore_DumpForBuild_RejectsWrongWeek(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	pak := writeTestBasePak(t, map[string][]byte{rel: []byte("{\r\n    \"Rows\": [1]\r\n}")})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-old", map[string]string{rel: "{\n    \"Rows\": []\n}"}))
	}))
	defer srv.Close()

	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL

	_, err := store.DumpForBuild(context.Background(), pak, "")
	if err == nil {
		t.Fatal("expected an error for a dump that does not match the install, got nil")
	}
	if !strings.Contains(err.Error(), rel) {
		t.Errorf("error %q should name the table that disagreed (%s)", err, rel)
	}
}

// writeLocalDump lays out an unpacked-data.pak-shaped directory on disk.
func writeLocalDump(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// With a local dump directory configured, the network is never touched.
func TestDumpStore_DumpForBuild_LocalDirOverridesFetch(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	shipped := []byte("{\r\n    \"Rows\": []\r\n}")
	pak := writeTestBasePak(t, map[string][]byte{rel: shipped})
	local := writeLocalDump(t, map[string]string{rel: "{\n    \"Rows\": []\n}"})

	fetched := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetched = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL

	dump, err := store.DumpForBuild(context.Background(), pak, local)
	if err != nil {
		t.Fatalf("DumpForBuild with a local dump dir: %v", err)
	}
	if fetched {
		t.Error("the hosted dump was fetched even though a local dump dir was configured")
	}
	got, ok := dump.Table(rel)
	if !ok || !bytes.Equal(got, shipped) {
		t.Errorf("table bytes = %q (found=%v), want the shipped CRLF form %q", got, ok, shipped)
	}
}

// A local directory already storing CRLF must load unchanged — QuickBMS writes
// whatever the pak stored, so the conversion has to be idempotent.
func TestDumpStore_DumpForBuild_LocalDirAlreadyCRLF(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	shipped := "{\r\n    \"Rows\": []\r\n}"
	pak := writeTestBasePak(t, map[string][]byte{rel: []byte(shipped)})
	local := writeLocalDump(t, map[string]string{rel: shipped})

	store := newDumpStore(t.TempDir(), http.DefaultClient)
	store.treeURL = "http://127.0.0.1:0/never-used"

	if _, err := store.DumpForBuild(context.Background(), pak, local); err != nil {
		t.Fatalf("DumpForBuild with a CRLF local dump dir: %v", err)
	}
}

// A local dir from the wrong week is rejected exactly like a stale hosted
// dump, and the error points at the configured path.
func TestDumpStore_DumpForBuild_LocalDirWrongWeek_Rejected(t *testing.T) {
	const rel = "Factions/D_Factions.json"
	pak := writeTestBasePak(t, map[string][]byte{rel: []byte("{\r\n    \"Rows\": [1]\r\n}")})
	local := writeLocalDump(t, map[string]string{rel: "{\n    \"Rows\": []\n}"})

	store := newDumpStore(t.TempDir(), http.DefaultClient)
	store.treeURL = "http://127.0.0.1:0/never-used"

	_, err := store.DumpForBuild(context.Background(), pak, local)
	if err == nil {
		t.Fatal("expected an error for a local dump dir from a different week, got nil")
	}
	if !strings.Contains(err.Error(), rel) {
		t.Errorf("error %q should name the disagreeing table (%s)", err, rel)
	}
	if !strings.Contains(err.Error(), local) {
		t.Errorf("error %q should name the configured data_dump_path (%s)", err, local)
	}
}

func TestDumpStore_DumpForBuild_LocalDirEmpty_IsActionable(t *testing.T) {
	pak := writeTestBasePak(t, map[string][]byte{"a/B.json": []byte("{}")})
	store := newDumpStore(t.TempDir(), http.DefaultClient)

	_, err := store.DumpForBuild(context.Background(), pak, t.TempDir())
	if err == nil {
		t.Fatal("expected an error for a data_dump_path holding no JSON tables, got nil")
	}
}

func TestDumpStore_DumpForBuild_NetworkFailure_IsActionable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL

	pak := writeTestBasePak(t, map[string][]byte{"a/B.json": []byte("{}")})
	_, err := store.DumpForBuild(context.Background(), pak, "")
	if err == nil {
		t.Fatal("expected an error when the dump host fails, got nil")
	}
}
