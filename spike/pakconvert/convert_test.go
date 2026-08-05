package pakconvert

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// writePak builds a synthetic pak fixture. Mirrors icarus's own test style:
// unrealpak.Writer output is readable by unrealpak.Reader.
func writePak(t *testing.T, path, mount string, entries map[string][]byte) {
	t.Helper()
	var opts []unrealpak.Option
	if mount != "" {
		opts = append(opts, unrealpak.WithMountPoint(mount))
	}
	w, err := unrealpak.Create(path, opts...)
	if err != nil {
		t.Fatalf("Create %s: %v", path, err)
	}
	for p, data := range entries {
		if err := w.AddFile(p, data); err != nil {
			t.Fatalf("AddFile %s: %v", p, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close %s: %v", path, err)
	}
}

const fixtureBaseTable = `{
	"RowStruct": "/Script/Icarus.FakeRow",
	"Defaults": {},
	"Rows": [
		{"Name": "Alpha", "Speed": 10},
		{"Name": "Beta", "Speed": 20}
	]
}`

// The mod pak carries a full-table snapshot: Alpha changed, Delta added,
// Beta absent (stale snapshot, must be ignored).
const fixtureModTable = `{
	"RowStruct": "/Script/Icarus.FakeRow",
	"Defaults": {},
	"Rows": [
		{"Name": "Alpha", "Speed": 99},
		{"Name": "Delta", "Speed": 40}
	]
}`

func TestConvertPak(t *testing.T) {
	dir := t.TempDir()
	basePak := filepath.Join(dir, "data.pak")
	modPak := filepath.Join(dir, "mod.pak")

	// Base data.pak: default mount, tables at bare mount-relative paths
	// (matching the real data.pak layout, e.g. "Test/D_Fake.json").
	writePak(t, basePak, "", map[string][]byte{
		"Test/D_Fake.json": []byte(fixtureBaseTable),
	})
	// Mod pak: real prebuilt layout — Content mount + data/-prefixed table,
	// plus an asset and an embedded .EXMOD.
	writePak(t, modPak, "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Fake.json": []byte(fixtureModTable),
		"Mod/ITM/SK_Hat.uasset": []byte("asset-bytes"),
		"data.EXMOD":            []byte(`{"Rows":[]}`),
		"README.txt":            []byte("junk"),
	})

	meta := Meta{Name: "TestMod", Author: "spike", Version: "1.0"}
	exmodz, report, err := ConvertPak(modPak, basePak, meta)
	if err != nil {
		t.Fatalf("ConvertPak: %v", err)
	}

	// Census: one of each class.
	for class, want := range map[string]int{"table": 1, "asset": 1, "embedded-exmod": 1, "other": 1} {
		if report.Census[class] != want {
			t.Errorf("census[%s] = %d, want %d (census: %v)", class, report.Census[class], want, report.Census)
		}
	}
	if report.StaleRows != 1 { // Beta missing from mod snapshot
		t.Errorf("StaleRows = %d, want 1", report.StaleRows)
	}
	if _, ok := report.EmbeddedExmods["data.EXMOD"]; !ok {
		t.Errorf("embedded .EXMOD not captured: %v", report.EmbeddedExmods)
	}
	td, ok := report.Tables["Test/D_Fake.json"]
	if !ok {
		t.Fatalf("table diff missing: %v", report.Tables)
	}
	if len(td.Items) != 2 {
		t.Fatalf("want 2 items (Alpha changed, Delta new), got %+v", td.Items)
	}

	// The synthesized bundle must parse with the REAL parser and contain
	// exactly the derived upserts + the passed-through asset.
	bundle, err := icarus.ParseExmodz(exmodz)
	if err != nil {
		t.Fatalf("icarus.ParseExmodz rejected ConvertPak output: %v", err)
	}
	// 1 table row + the EndOfMod sentinel (ParseExmod keeps the sentinel).
	if len(bundle.Diff.Rows) != 2 || bundle.Diff.Rows[0].CurrentFile != "Test-D_Fake.json" {
		t.Fatalf("diff rows mismatch: %+v", bundle.Diff.Rows)
	}
	items := bundle.Diff.Rows[0].FileItems
	if len(items) != 2 || items[0].Name != "Alpha" || items[0].Fields["Speed"] != float64(99) ||
		items[1].Name != "Delta" || items[1].Fields["Speed"] != float64(40) {
		t.Fatalf("upserts mismatch: %+v", items)
	}
	if string(bundle.Assets["TestMod/Mod/ITM/SK_Hat.uasset"]) != "asset-bytes" {
		t.Errorf("asset not passed through: keys %v", bundle.Assets)
	}
}

func TestConvertPakTableNotInBase(t *testing.T) {
	dir := t.TempDir()
	basePak := filepath.Join(dir, "data.pak")
	modPak := filepath.Join(dir, "mod.pak")
	writePak(t, basePak, "", map[string][]byte{
		"Test/D_Fake.json": []byte(fixtureBaseTable),
	})
	writePak(t, modPak, "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Unknown.json": []byte(fixtureModTable),
	})
	_, report, err := ConvertPak(modPak, basePak, Meta{Name: "X"})
	if err != nil {
		t.Fatalf("ConvertPak: %v", err)
	}
	found := false
	for _, f := range report.Findings {
		if f.Kind == "table-not-in-base" && f.Table == "Test/D_Unknown.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want table-not-in-base finding, got %+v", report.Findings)
	}
	if len(report.Tables) != 0 {
		t.Errorf("unknown table must not be diffed: %v", report.Tables)
	}
}

func TestSanitizeAssetPath(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: `Mod\ITM\SK_Hat.uasset`, want: "Mod/ITM/SK_Hat.uasset"},
		{in: "Mod/./ITM/SK_Hat.uasset", want: "Mod/ITM/SK_Hat.uasset"},
		{in: "../escape.uasset", wantErr: true},
		{in: "/abs/path.uasset", wantErr: true},
		{in: "C:/abs/path.uasset", wantErr: true},
		{in: "nul\x00byte.uasset", wantErr: true},
	}
	for _, tt := range tests {
		got, err := sanitizeAssetPath(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("sanitizeAssetPath(%q): want error, got %q", tt.in, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("sanitizeAssetPath(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestConvertPakMissingFiles(t *testing.T) {
	if _, _, err := ConvertPak(filepath.Join(t.TempDir(), "nope.pak"), os.DevNull, Meta{}); err == nil {
		t.Fatal("want error for missing pak")
	}
}
