package icarus

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/go-unrealpak"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
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
	warnings, failed, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:speed-mod", SourcePath: modA},
		{ModRef: "icarus:health-mod", SourcePath: modB},
	}, outputPath)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
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
	_, failed, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:mount-mod", SourcePath: modA},
		{ModRef: "icarus:item-mod", SourcePath: modB},
	}, outputPath)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
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
	_, failed, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:a", SourcePath: modA},
		{ModRef: "icarus:b", SourcePath: modB},
	}, outputPath)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
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
	warnings, failed, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:a", SourcePath: modA},
		{ModRef: "icarus:b", SourcePath: modB},
	}, outputPath)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
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
	_, failed, err := MergeCompile(context.Background(), basePak, []source.MergeSource{
		{ModRef: "icarus:patch", SourcePath: patchMod},
		{ModRef: "icarus:add", SourcePath: addMod},
	}, outputPath)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
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
	_, failed, err := MergeCompile(context.Background(), basePak, []source.MergeSource{{ModRef: "icarus:bear-mount", SourcePath: exmodzPath}}, mergeOut)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	if len(failed) != 0 {
		t.Errorf("failed = %v, want none", failed)
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

func TestMergeCompilePakSource(t *testing.T) {
	dir := t.TempDir()
	basePath := buildTestPak(t, dir, "data.pak", "../../../Icarus/Content/Data/", map[string][]byte{
		"Test/D_Growth.json": []byte(testBaseTable),
	})
	modTable := `{"RowStruct":"/Script/Icarus.Growth","Defaults":{},"Rows":[{"Name":"RowA","XP":99}]}`
	pakPath := buildTestPak(t, dir, "mod.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Growth.json": []byte(modTable),
	})
	out := filepath.Join(dir, "merged.pak")

	warnings, failed, err := MergeCompile(context.Background(), basePath, []MergeSource{
		{ModRef: "icarus:pakmod", SourcePath: pakPath, Kind: MergeSourcePak},
	}, out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(failed) != 0 || len(warnings) != 0 {
		t.Fatalf("unexpected failed/warnings: %v / %v", failed, warnings)
	}
	merged, err := unrealpak.Open(out)
	if err != nil {
		t.Fatalf("opening merged: %v", err)
	}
	defer merged.Close()
	data, err := merged.ReadFile("data/Test/D_Growth.json")
	if err != nil {
		t.Fatalf("merged table missing: %v", err)
	}
	if !strings.Contains(string(data), `"XP":99`) {
		t.Fatalf("converted change not applied: %s", data)
	}
}

func TestMergeCompilePakFailureSkipsModOnly(t *testing.T) {
	dir := t.TempDir()
	basePath := buildTestPak(t, dir, "data.pak", "../../../Icarus/Content/Data/", map[string][]byte{
		"Test/D_Growth.json": []byte(testBaseTable),
	})
	// Irreconcilable pak: its table does not exist in the current base.
	badPak := buildTestPak(t, dir, "bad.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Removed/D_Gone.json": []byte(testBaseTable),
	})
	goodTable := `{"RowStruct":"/Script/Icarus.Growth","Defaults":{},"Rows":[{"Name":"RowB","XP":77}]}`
	goodPak := buildTestPak(t, dir, "good.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Growth.json": []byte(goodTable),
	})
	out := filepath.Join(dir, "merged.pak")

	warnings, failed, err := MergeCompile(context.Background(), basePath, []MergeSource{
		{ModRef: "icarus:bad", SourcePath: badPak, Kind: MergeSourcePak},
		{ModRef: "icarus:good", SourcePath: goodPak, Kind: MergeSourcePak},
	}, out)
	if err != nil {
		t.Fatalf("per-mod failure must not be fatal: %v", err)
	}
	if len(failed) != 1 || failed[0].ModRef != "icarus:bad" {
		t.Fatalf("want icarus:bad failed, got %+v", failed)
	}
	var haveWarning bool
	for _, w := range warnings {
		if strings.Contains(w, "icarus:bad") && strings.Contains(w, "deploying raw") {
			haveWarning = true
		}
	}
	if !haveWarning {
		t.Fatalf("want a deploying-raw warning for icarus:bad, got %v", warnings)
	}
	merged, err := unrealpak.Open(out)
	if err != nil {
		t.Fatalf("opening merged: %v", err)
	}
	defer merged.Close()
	data, err := merged.ReadFile("data/Test/D_Growth.json")
	if err != nil {
		t.Fatalf("good mod's table missing: %v", err)
	}
	if !strings.Contains(string(data), `"XP":77`) {
		t.Fatalf("good mod's change not applied: %s", data)
	}
	if _, err := merged.ReadFile("data/Removed/D_Gone.json"); err == nil {
		t.Fatal("failed mod's table must not be in the merged pak")
	}
}

func TestValidateSourcePak(t *testing.T) {
	dir := t.TempDir()
	pakPath := buildTestPak(t, dir, "ok.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Growth.json": []byte(testBaseTable),
	})
	if err := ValidateSource(pakPath); err != nil {
		t.Fatalf("valid pak rejected: %v", err)
	}
	badPath := filepath.Join(dir, "not-a.pak")
	if err := os.WriteFile(badPath, []byte("junk"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSource(badPath); err == nil {
		t.Fatal("junk .pak must fail validation")
	}
}

// TestMergeCompileWarningsPreferModName proves user-facing warnings identify
// mods by display name when MergeSource.ModName is set, while MergeFailure
// keeps ModRef as the machine identity. Existing tests cover the fallback:
// with no ModName, warnings show the ModRef.
func TestMergeCompileWarningsPreferModName(t *testing.T) {
	dir := t.TempDir()
	basePath := buildTestPak(t, dir, "data.pak", "../../../Icarus/Content/Data/", map[string][]byte{
		"Test/D_Growth.json": []byte(testBaseTable),
	})
	// Divergent Defaults triggers the inexpressible-top-level-key warning.
	modTable := `{"RowStruct":"/Script/Icarus.Growth","Defaults":{"X":1},"Rows":[{"Name":"RowA","XP":99}]}`
	pakPath := buildTestPak(t, dir, "mod.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Test/D_Growth.json": []byte(modTable),
	})
	// Irreconcilable pak triggers the deploying-raw warning.
	badPak := buildTestPak(t, dir, "bad.pak", "../../../Icarus/Content/", map[string][]byte{
		"data/Removed/D_Gone.json": []byte(testBaseTable),
	})
	out := filepath.Join(dir, "merged.pak")

	warnings, failed, err := MergeCompile(context.Background(), basePath, []MergeSource{
		{ModRef: "icarus:pakmod", ModName: "Combined QOL", SourcePath: pakPath, Kind: MergeSourcePak},
		{ModRef: "icarus:badmod", ModName: "Broken Mod", SourcePath: badPak, Kind: MergeSourcePak},
	}, out)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var haveDiff, haveRaw bool
	for _, w := range warnings {
		if strings.Contains(w, "icarus:pakmod") || strings.Contains(w, "icarus:badmod") {
			t.Errorf("warning shows ModRef instead of ModName: %s", w)
		}
		if strings.Contains(w, "mod Combined QOL:") && strings.Contains(w, "Defaults") {
			haveDiff = true
		}
		if strings.Contains(w, "mod Broken Mod:") && strings.Contains(w, "deploying raw") {
			haveRaw = true
		}
	}
	if !haveDiff {
		t.Errorf("want a Defaults-differs warning naming Combined QOL, got %v", warnings)
	}
	if !haveRaw {
		t.Errorf("want a deploying-raw warning naming Broken Mod, got %v", warnings)
	}
	if len(failed) != 1 || failed[0].ModRef != "icarus:badmod" {
		t.Fatalf("MergeFailure must keep ModRef as machine identity, got %+v", failed)
	}
}

// TestApplyBundleAssetCollisionKeyedByRef proves asset-collision detection
// uses the stable ModRef identity, not the display label: two DIFFERENT
// mods sharing a display name still cross-warn (with refs appended for
// disambiguation), while the same mod re-setting its own asset stays quiet.
func TestApplyBundleAssetCollisionKeyedByRef(t *testing.T) {
	dir := t.TempDir()
	basePath := buildTestPak(t, dir, "data.pak", "../../../Icarus/Content/Data/", map[string][]byte{
		"Test/D_Growth.json": []byte(testBaseTable),
	})
	base, err := unrealpak.Open(basePath)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	tables := map[string][]byte{}
	assets := map[string][]byte{}
	owner := map[string]mergeOwner{}
	bundle := func(data string) *ExmodzBundle {
		return &ExmodzBundle{Diff: &ExmodDiff{}, Assets: map[string][]byte{"mod/foo.uasset": []byte(data)}}
	}

	w, err := applyBundle(base, tables, assets, owner, bundle("a"), "icarus:a", "Same Name")
	if err != nil || len(w) != 0 {
		t.Fatalf("first apply: warnings %v, err %v", w, err)
	}
	// Same mod re-sets its own asset: no self-warning.
	w, err = applyBundle(base, tables, assets, owner, bundle("a2"), "icarus:a", "Same Name")
	if err != nil || len(w) != 0 {
		t.Fatalf("same-mod re-apply must not warn: %v, err %v", w, err)
	}
	// Different mod, identical display name: must still warn, with refs
	// appended so the message distinguishes the two parties.
	w, err = applyBundle(base, tables, assets, owner, bundle("b"), "icarus:b", "Same Name")
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 1 {
		t.Fatalf("want exactly one collision warning, got %v", w)
	}
	if !strings.Contains(w[0], "icarus:a") || !strings.Contains(w[0], "icarus:b") {
		t.Fatalf("identical-name collision must disambiguate by ref: %s", w[0])
	}
	if string(assets["mod/foo.uasset"]) != "b" {
		t.Fatal("last-applied must win")
	}
	// Different mod, different name: warning renders names only.
	w, err = applyBundle(base, tables, assets, owner, bundle("c"), "icarus:c", "Other Mod")
	if err != nil {
		t.Fatal(err)
	}
	if len(w) != 1 || !strings.Contains(w[0], "Same Name") || !strings.Contains(w[0], "Other Mod") {
		t.Fatalf("want a name-rendered collision warning, got %v", w)
	}
	if strings.Contains(w[0], "icarus:b") || strings.Contains(w[0], "icarus:c") {
		t.Fatalf("distinct names need no ref disambiguation: %s", w[0])
	}
}
