package icarus

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// TestMergeCompile_FieldLevelMergeAcrossMods is the crux of #197: two mods
// patch DIFFERENT fields of the SAME row in the SAME table. Whole-pak
// last-wins (the #136 status quo) would lose one mod's field entirely;
// sequential upserts must preserve BOTH.
func TestMergeCompile_FieldLevelMergeAcrossMods(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200,"BaseHealth":500}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)

	modA := writeTestExmodzFile(t, `{"name":"Speed Mod","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`, nil)
	modB := writeTestExmodzFile(t, `{"name":"Health Mod","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseHealth":800}]}]}`, nil)

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	warnings, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:speed-mod", SourcePath: modA},
		{ModRef: "icarus:health-mod", SourcePath: modB},
	}, outputPath)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none (no asset collision in this fixture)", warnings)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck

	merged, err := r.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile merged data table: %v", err)
	}
	if !bytes.Contains(merged, []byte(`"BaseMovementSpeed":235`)) {
		t.Errorf("merged table = %s, want BaseMovementSpeed 235 (mod A's field) to survive", merged)
	}
	if !bytes.Contains(merged, []byte(`"BaseHealth":800`)) {
		t.Errorf("merged table = %s, want BaseHealth 800 (mod B's field) to survive", merged)
	}
}

// TestMergeCompile_DifferentTablesFromDifferentMods proves the OTHER
// whole-pak-last-wins failure mode (#197's issue body point 1): mod A
// patches table X, mod B patches table Y - both must land in the single
// merged pak, not just the last mod's table.
func TestMergeCompile_DifferentTablesFromDifferentMods(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json":       []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}`),
		"Items/D_ItemsStatic.json": []byte(`{"Rows":[{"Name":"Item_Saddle","Weight":5}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)

	modA := writeTestExmodzFile(t, `{"name":"Mount Mod","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":300}]}]}`, nil)
	modB := writeTestExmodzFile(t, `{"name":"Item Mod","Rows":[{"CurrentFile":"Items-D_ItemsStatic.json","File_Items":[{"Name":"Item_Saddle","Weight":1}]}]}`, nil)

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	if _, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:mount-mod", SourcePath: modA},
		{ModRef: "icarus:item-mod", SourcePath: modB},
	}, outputPath); err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck

	aiTable, err := r.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile AI table: %v", err)
	}
	if !bytes.Contains(aiTable, []byte(`"BaseMovementSpeed":300`)) {
		t.Errorf("AI table = %s, want mod A's patch", aiTable)
	}
	itemsTable, err := r.ReadFile("data/Items/D_ItemsStatic.json")
	if err != nil {
		t.Fatalf("ReadFile Items table: %v", err)
	}
	if !bytes.Contains(itemsTable, []byte(`"Weight":1`)) {
		t.Errorf("Items table = %s, want mod B's patch", itemsTable)
	}
}

// TestMergeCompile_SameRowSameField_LastWins pins the EXPECTED (not
// warned-about) outcome when two mods genuinely conflict on the exact same
// field of the exact same row: later-in-order wins, ordinary upsert
// semantics, no special handling needed.
func TestMergeCompile_SameRowSameField_LastWins(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)

	modA := writeTestExmodzFile(t, `{"name":"A","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":300}]}]}`, nil)
	modB := writeTestExmodzFile(t, `{"name":"B","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":400}]}]}`, nil)

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	if _, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:a", SourcePath: modA},
		{ModRef: "icarus:b", SourcePath: modB},
	}, outputPath); err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck
	merged, err := r.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(merged, []byte(`"BaseMovementSpeed":400`)) {
		t.Errorf("merged table = %s, want mod B's (later, order-2) value 400 to win", merged)
	}
	if bytes.Contains(merged, []byte(`"BaseMovementSpeed":300`)) {
		t.Errorf("merged table = %s, mod A's value should have been overwritten", merged)
	}
}

// TestMergeCompile_AssetCollision_LastWinsWithWarning: two mods bundle a
// prebuilt asset at the SAME path - cannot compose like a table row, so
// last-applied wins AND a warning is returned.
func TestMergeCompile_AssetCollision_LastWinsWithWarning(t *testing.T) {
	basePak := writeTestBasePak(t, map[string][]byte{"AI/D_AIGrowth.json": []byte(`{"Rows":[]}`)})

	modA := writeTestExmodzFile(t, `{"name":"A","Rows":[]}`, map[string][]byte{
		"Shared/ASS/SK_Shared.uasset": []byte("from-mod-a"),
	})
	modB := writeTestExmodzFile(t, `{"name":"B","Rows":[]}`, map[string][]byte{
		"Shared/ASS/SK_Shared.uasset": []byte("from-mod-b"),
	})

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	warnings, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:a", SourcePath: modA},
		{ModRef: "icarus:b", SourcePath: modB},
	}, outputPath)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly 1 asset-collision warning", warnings)
	}
	if !bytes.Contains([]byte(warnings[0]), []byte("Shared/ASS/SK_Shared.uasset")) {
		t.Errorf("warning = %q, want it to name the colliding path", warnings[0])
	}
	if !bytes.Contains([]byte(warnings[0]), []byte("icarus:b")) {
		t.Errorf("warning = %q, want it to name the winning mod", warnings[0])
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck
	asset, err := r.ReadFile("Shared/ASS/SK_Shared.uasset")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(asset) != "from-mod-b" {
		t.Errorf("asset content = %q, want mod B's (later-applied) content to win", asset)
	}
}

// TestMergeCompile_ContentAddingModComposesWithPatchMod: one mod ADDS a
// brand-new row (a new mountable species), another PATCHES an existing row
// in the SAME table. Both must survive in the merged output.
func TestMergeCompile_ContentAddingModComposesWithPatchMod(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)

	patchMod := writeTestExmodzFile(t, `{"name":"Patch","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":250}]}]}`, nil)
	addMod := writeTestExmodzFile(t, `{"name":"NewSpecies","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Wolf","BaseMovementSpeed":320}]}]}`, nil)

	outputPath := filepath.Join(t.TempDir(), "merged_P.pak")
	if _, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:patch", SourcePath: patchMod},
		{ModRef: "icarus:add", SourcePath: addMod},
	}, outputPath); err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}

	r, err := unrealpak.Open(outputPath)
	if err != nil {
		t.Fatalf("opening merged output: %v", err)
	}
	defer r.Close() //nolint:errcheck
	merged, err := r.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(merged, []byte(`"BaseMovementSpeed":250`)) {
		t.Errorf("merged table = %s, want the patched Mount_Bear speed", merged)
	}
	if !bytes.Contains(merged, []byte(`"Mount_Wolf"`)) {
		t.Errorf("merged table = %s, want the newly-added Mount_Wolf row", merged)
	}
}

// TestMergeCompile_SingleSource_MatchesCompile proves the N=1 degenerate
// case (a profile with exactly one enabled exmodz mod) produces byte-
// identical table content to the existing single-mod Compile() - the
// merged-only model must not regress the already-shipped single-mod path.
func TestMergeCompile_SingleSource_MatchesCompile(t *testing.T) {
	baseTables := map[string][]byte{
		"AI/D_AIGrowth.json": []byte(`{"Rows":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}`),
	}
	basePak := writeTestBasePak(t, baseTables)
	manifest := `{"name":"Bear Mount","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`
	exmodzPath := writeTestExmodzFile(t, manifest, map[string][]byte{
		"Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset": []byte("fake-asset"),
	})

	compileOut := filepath.Join(t.TempDir(), "compile_P.pak")
	if err := Compile(basePak, exmodzPath, compileOut); err != nil {
		t.Fatalf("Compile: %v", err)
	}
	mergeOut := filepath.Join(t.TempDir(), "merge_P.pak")
	if _, err := MergeCompile(context.Background(), basePak, []source.MergeSource{{ModRef: "icarus:bear-mount", SourcePath: exmodzPath}}, mergeOut); err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}

	cr, err := unrealpak.Open(compileOut)
	if err != nil {
		t.Fatalf("opening Compile output: %v", err)
	}
	defer cr.Close() //nolint:errcheck
	mr, err := unrealpak.Open(mergeOut)
	if err != nil {
		t.Fatalf("opening MergeCompile output: %v", err)
	}
	defer mr.Close() //nolint:errcheck

	cTable, err := cr.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("Compile ReadFile: %v", err)
	}
	mTable, err := mr.ReadFile("data/AI/D_AIGrowth.json")
	if err != nil {
		t.Fatalf("MergeCompile ReadFile: %v", err)
	}
	if !bytes.Equal(cTable, mTable) {
		t.Errorf("Compile table = %s, MergeCompile table = %s, want identical for N=1", cTable, mTable)
	}
}

// TestValidateSource_ValidExmodz_NoError proves ValidateSource accepts a
// well-formed .exmodz without compiling anything (no basePak needed).
func TestValidateSource_ValidExmodz_NoError(t *testing.T) {
	exmodzPath := writeTestExmodzFile(t, `{"name":"OK","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":200}]}]}`, nil)
	if err := ValidateSource(exmodzPath); err != nil {
		t.Errorf("ValidateSource: %v, want nil for a well-formed .exmodz", err)
	}
}

// TestValidateSource_MalformedExmodz_Errors proves a corrupt/unparseable
// .exmodz fails loud at validate time (ingest-time), not silently deferred
// to the next merge.
func TestValidateSource_MalformedExmodz_Errors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.exmodz")
	if err := os.WriteFile(path, []byte("not a zip file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSource(path); err == nil {
		t.Error("ValidateSource: got nil error, want a failure for a non-zip file")
	}
}
