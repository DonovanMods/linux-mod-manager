package pakconvert

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// TestPipelineSeam proves the design's Seam A: a synthesized .exmodz is
// indistinguishable to the real pipeline — icarus.ValidateSource accepts it
// and icarus.MergeCompile merges it alongside a genuine author exmodz with
// zero pipeline changes.
func TestPipelineSeam(t *testing.T) {
	icarusDir, corpusDir := spikeEnv(t)
	basePak := basePakPath(icarusDir)

	manifest, err := LoadManifest(corpusDir)
	if err != nil {
		t.Fatalf("corpus manifest (run cmd/fetchcorpus first): %v", err)
	}

	// Pick one mod with a pak to convert, and one DIFFERENT mod's genuine
	// author exmodz to merge alongside it.
	var convertMod, authorMod *CorpusMod
	for i := range manifest.Mods {
		m := &manifest.Mods[i]
		if convertMod == nil && m.Pak != "" {
			convertMod = m
			continue
		}
		if authorMod == nil && m.Exmodz != "" && (convertMod == nil || m.ID != convertMod.ID) {
			authorMod = m
		}
	}
	if convertMod == nil || authorMod == nil {
		t.Skip("corpus needs at least one pak mod and one other exmodz mod")
	}

	meta := Meta{Name: convertMod.Name, Author: "spike-converted", Version: convertMod.Version}
	synthesized, report, err := ConvertPak(filepath.Join(corpusDir, convertMod.Pak), basePak, meta)
	if err != nil {
		t.Fatalf("ConvertPak: %v", err)
	}

	dir := t.TempDir()
	synthPath := filepath.Join(dir, "converted.exmodz")
	if err := os.WriteFile(synthPath, synthesized, 0o644); err != nil {
		t.Fatalf("writing synthesized exmodz: %v", err)
	}

	// Gate 1: the ingest-time validation hook.
	if err := icarus.ValidateSource(synthPath); err != nil {
		t.Fatalf("icarus.ValidateSource rejected the synthesized exmodz: %v", err)
	}

	// Gate 2: the real merge, author exmodz first, converted mod second
	// (load-order semantics: later wins conflicting fields).
	outPak := filepath.Join(dir, "zzz_LMM_Merged_P.pak")
	sources := []icarus.MergeSource{
		{ModRef: "icarus:" + authorMod.ID, ExmodzPath: filepath.Join(corpusDir, authorMod.Exmodz)},
		{ModRef: "icarus:" + convertMod.ID + "-converted", ExmodzPath: synthPath},
	}
	warnings, err := icarus.MergeCompile(context.Background(), basePak, sources, outPak)
	if err != nil {
		t.Fatalf("MergeCompile: %v", err)
	}
	for _, w := range warnings {
		// Asset collisions across the two mods are legitimate; anything else
		// is unexpected for this corpus pairing.
		if !strings.Contains(w, "is bundled by both") {
			t.Errorf("unexpected merge warning: %s", w)
		}
	}

	// Gate 3: output pak shape — mount point, data/-prefixed tables, and the
	// converted mod's changed rows actually present.
	out, err := unrealpak.Open(outPak)
	if err != nil {
		t.Fatalf("opening merged pak: %v", err)
	}
	defer out.Close()
	// Duplicated literal: icarusContentMountPoint (unexported upstream).
	if got := out.MountPoint(); got != "../../../Icarus/Content/" {
		t.Errorf("merged pak mount point = %q", got)
	}
	for tablePath, td := range report.Tables {
		if len(td.Items) == 0 {
			continue
		}
		// Duplicated literal: icarusDataTablePrefix "data/" (unexported upstream).
		data, err := out.ReadFile("data/" + tablePath)
		if err != nil {
			t.Errorf("converted table %s missing from merged pak: %v", tablePath, err)
			continue
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Errorf("merged table %s is not JSON: %v", tablePath, err)
			continue
		}
		merged := rowsByName(decoded)
		for _, item := range td.Items {
			row, ok := merged[item.Name]
			if !ok {
				t.Errorf("row %s missing from merged %s", item.Name, tablePath)
				continue
			}
			for field, want := range item.Fields {
				if got, ok := row[field]; !ok || !jsonEqual(got, want) {
					t.Errorf("merged %s row %s field %s = %v, want %v", tablePath, item.Name, field, got, want)
				}
			}
		}
	}
	t.Logf("seam validated: converted %q merged alongside %q, %d warnings", convertMod.Name, authorMod.Name, len(warnings))
}

// jsonEqual compares two decoded-JSON values via re-marshaling (normalizes
// map ordering and numeric formatting).
func jsonEqual(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}
