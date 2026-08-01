package icarus

import (
	"archive/zip"
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// testDumpStore serves a dump containing exactly files, so validateDump agrees
// it matches the base pak built from the same map. Reuses tarGz from
// datadump_test.go (same package).
func testDumpStore(t *testing.T, files map[string][]byte) *DumpStore {
	t.Helper()
	entries := make(map[string]string, len(files))
	for name, data := range files {
		entries[name] = string(data)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarGz(t, "IcarusData-test", entries))
	}))
	t.Cleanup(srv.Close)
	store := newDumpStore(t.TempDir(), srv.Client())
	store.treeURL = srv.URL
	return store
}

func writeTestExmodzFile(t *testing.T, manifestJSON string, assets map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mod.exmodz")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("Extracted Mods/Test.EXMOD")
	w.Write([]byte(manifestJSON)) //nolint:errcheck
	for name, data := range assets {
		aw, _ := zw.Create(name)
		aw.Write(data) //nolint:errcheck
	}
	zw.Close() //nolint:errcheck
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Base table paths and CurrentFile values below mirror the real shape found
// against a live install + a real Bear_Mount.EXMODZ during Step 5b
// verification: CurrentFile flattens the mount-relative directory path with
// "-" in place of "/" (e.g. "AI-D_AIGrowth.json" for base pak path
// "AI/D_AIGrowth.json"), not a bare filename living at a hyphenated leaf as
// the original brief's fixtures assumed. See task-12-report.md "plan delta".
func TestCompile_AppliesDiffAndBundlesAssets(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	dumps := testDumpStore(t, baseTables)
	manifest := `{"name":"Bear Mount","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, map[string][]byte{
		"Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset": []byte("fake-asset"),
	})
	outputPath := filepath.Join(t.TempDir(), "Bear_Mount_P.pak")

	if err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening compiled output: %v", err)
	}
	defer r.Close()

	patched, err := r.ReadFile("AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile patched data table: %v", err)
	}
	if !bytes.Contains(patched, []byte(`"BaseMovementSpeed":235`)) {
		t.Errorf("patched data table = %s, want BaseMovementSpeed 235", patched)
	}

	asset, err := r.ReadFile("Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset")
	if err != nil {
		t.Fatalf("ReadFile bundled asset: %v", err)
	}
	if string(asset) != "fake-asset" {
		t.Errorf("bundled asset content = %q", asset)
	}
}

// The real .EXMOD ecosystem terminates Rows with {"CurrentFile":"EndOfMod"}
// and no File_Items key — Compile must skip it, not try to resolve it as a
// data table (it has none).
func TestCompile_SkipsEndOfModSentinelRow(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	dumps := testDumpStore(t, baseTables)
	manifest := `{"name":"X","Rows":[` +
		`{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]},` +
		`{"CurrentFile":"EndOfMod"}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, nil)
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	if err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening compiled output: %v", err)
	}
	defer r.Close()
	patched, err := r.ReadFile("AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile patched data table: %v", err)
	}
	if !bytes.Contains(patched, []byte(`"BaseMovementSpeed":235`)) {
		t.Errorf("patched data table = %s, want BaseMovementSpeed 235", patched)
	}
}

// A real (non-sentinel) row with no File_Items is a malformed manifest, not
// something to silently skip — only the EndOfMod sentinel gets that pass.
func TestCompile_RowWithoutFileItems_Errors(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	dumps := testDumpStore(t, baseTables)
	manifest := `{"name":"X","Rows":[{"CurrentFile":"AI-D_AIGrowth.json"}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, nil)
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath)
	if err == nil {
		t.Fatal("expected an error for a non-sentinel row with no File_Items, got nil")
	}
	if !strings.Contains(err.Error(), "AI-D_AIGrowth.json") {
		t.Errorf("error %q should name the offending row", err)
	}
}

// A stale dump must stop the compile before any output pak is written — this
// is the live case today, where the newest dump lags the installed game.
func TestCompile_DumpWeekMismatch_FailsBeforeWriting(t *testing.T) {
	basePak := writeTestBasePak(t, map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	})
	dumps := testDumpStore(t, map[string][]byte{ // different week's content
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":150}}`),
	})
	manifest := `{"name":"X","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, nil)
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath)
	if err == nil {
		t.Fatal("expected an error when the dump is for a different game week, got nil")
	}
	if _, statErr := os.Stat(outputPath); statErr == nil {
		t.Error("no output pak should exist after a week-mismatch failure")
	}
}

// A malicious .EXMODZ whose bundled asset entry escapes the mod's own path
// must fail loudly rather than write outside the pak's intended namespace —
// see task-12-report.md "plan delta" for the exact semantics agreed with the
// coordinator.
func TestCompile_UnsafeAssetPath_Errors(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Mount_Bear":{"BaseMovementSpeed":200}}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	dumps := testDumpStore(t, baseTables)
	manifest := `{"name":"X","Rows":[]}`
	exmodzPath := writeTestExmodzFile(t, manifest, map[string][]byte{
		"../evil.uasset": []byte("payload"),
	})
	outputPath := filepath.Join(t.TempDir(), "out.pak")

	err := Compile(context.Background(), dumps, basePak, "", exmodzPath, outputPath)
	if err == nil {
		t.Fatal("expected an error for an asset path escaping the mod's own namespace, got nil")
	}
	if !strings.Contains(err.Error(), "../evil.uasset") {
		t.Errorf("error %q should name the offending asset path", err)
	}
}

func TestMatchMountPath(t *testing.T) {
	paths := []string{
		"AI/D_AIGrowth.json",
		"Audio/MusicConditions/D_MusicLocationConditions.json",
		"D_Factions.json",
	}

	t.Run("single-level directory", func(t *testing.T) {
		got, err := matchMountPath(paths, "AI-D_AIGrowth.json")
		if err != nil {
			t.Fatalf("matchMountPath: %v", err)
		}
		if got != "AI/D_AIGrowth.json" {
			t.Errorf("matchMountPath = %q, want AI/D_AIGrowth.json", got)
		}
	})

	t.Run("multi-level directory", func(t *testing.T) {
		got, err := matchMountPath(paths, "Audio-MusicConditions-D_MusicLocationConditions.json")
		if err != nil {
			t.Fatalf("matchMountPath: %v", err)
		}
		if got != "Audio/MusicConditions/D_MusicLocationConditions.json" {
			t.Errorf("matchMountPath = %q, want Audio/MusicConditions/D_MusicLocationConditions.json", got)
		}
	})

	t.Run("root-level file, no hyphen to convert", func(t *testing.T) {
		got, err := matchMountPath(paths, "D_Factions.json")
		if err != nil {
			t.Fatalf("matchMountPath: %v", err)
		}
		if got != "D_Factions.json" {
			t.Errorf("matchMountPath = %q, want D_Factions.json", got)
		}
	})

	t.Run("no match is a loud, actionable error", func(t *testing.T) {
		_, err := matchMountPath(paths, "AI-D_Nonexistent.json")
		if err == nil {
			t.Fatal("expected an error for a CurrentFile with no matching base pak file, got nil")
		}
		if !strings.Contains(err.Error(), "AI-D_Nonexistent.json") || !strings.Contains(err.Error(), "AI/D_Nonexistent.json") {
			t.Errorf("error %q should name both the CurrentFile and the expected mount path", err)
		}
	})

	t.Run("ambiguous match is a loud error", func(t *testing.T) {
		dup := []string{"AI/D_AIGrowth.json", "AI/D_AIGrowth.json"}
		_, err := matchMountPath(dup, "AI-D_AIGrowth.json")
		if err == nil {
			t.Fatal("expected an error for an ambiguous match, got nil")
		}
	})
}

func TestSanitizeAssetPath(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "parent traversal", raw: "../evil.json", wantErr: true},
		{name: "absolute unix path", raw: "/evil", wantErr: true},
		{name: "windows drive absolute", raw: `C:\evil`, wantErr: true},
		{name: "backslash-normalized nested path", raw: `Good\Nested\file.uasset`, want: "Good/Nested/file.uasset"},
		{name: "benign nested path", raw: "Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset", want: "Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sanitizeAssetPath(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("sanitizeAssetPath(%q) = %q, nil; want error", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("sanitizeAssetPath(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Errorf("sanitizeAssetPath(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
