package pakconvert

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/source/icarus"
	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// spikeEnv gates the corpus-driven integration tests. Plain `go test ./...`
// must stay green on any machine, so absence of the env vars is a SKIP.
func spikeEnv(t *testing.T) (icarusDir, corpusDir string) {
	t.Helper()
	icarusDir = os.Getenv("LMM_SPIKE_ICARUS_DIR")
	corpusDir = os.Getenv("LMM_SPIKE_CORPUS_DIR")
	if icarusDir == "" || corpusDir == "" {
		t.Skip("set LMM_SPIKE_ICARUS_DIR and LMM_SPIKE_CORPUS_DIR to run spike integration tests")
	}
	return icarusDir, corpusDir
}

func basePakPath(icarusDir string) string {
	// Mirrors core.resolveBasePak (unexported, service.go:1040).
	return filepath.Join(icarusDir, "Icarus", "Content", "Data", "data.pak")
}

// applyItems applies EXMOD rows to base tables, returning tablePath -> final
// decoded table. Uses the REAL icarus.ApplyRowPatch.
func applyItems(t *testing.T, base *unrealpak.Reader, rows []icarus.ExmodRow) map[string]map[string]any {
	t.Helper()
	state := map[string][]byte{}
	for _, row := range rows {
		if row.CurrentFile == "EndOfMod" { // duplicated: unexported upstream const
			continue
		}
		// Reverse of CurrentFileFor — same rule as icarus matchMountPath.
		tablePath := replaceAllDashes(row.CurrentFile)
		cur, ok := state[tablePath]
		if !ok {
			data, err := base.ReadFile(tablePath)
			if err != nil {
				t.Fatalf("base table %s (from CurrentFile %s): %v", tablePath, row.CurrentFile, err)
			}
			cur = data
		}
		next, err := icarus.ApplyRowPatch(cur, row)
		if err != nil {
			t.Fatalf("ApplyRowPatch %s: %v", row.CurrentFile, err)
		}
		state[tablePath] = next
	}
	out := map[string]map[string]any{}
	for p, data := range state {
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("decoding patched %s: %v", p, err)
		}
		out[p] = decoded
	}
	return out
}

// replaceAllDashes reverses CurrentFileFor — same rule as icarus
// matchMountPath (unexported upstream).
func replaceAllDashes(currentFile string) string {
	return strings.ReplaceAll(currentFile, "-", "/")
}

// rowsByName extracts Rows keyed by Name from a decoded table.
func rowsByName(table map[string]any) map[string]map[string]any {
	out := map[string]map[string]any{}
	rows, _ := table["Rows"].([]any)
	for _, r := range rows {
		if m, ok := r.(map[string]any); ok {
			if n, ok := m["Name"].(string); ok {
				out[n] = m
			}
		}
	}
	return out
}

func TestGroundTruthDualFormMods(t *testing.T) {
	icarusDir, corpusDir := spikeEnv(t)
	basePak := basePakPath(icarusDir)
	base, err := unrealpak.Open(basePak)
	if err != nil {
		t.Fatalf("opening live base pak: %v", err)
	}
	defer base.Close()

	manifest, err := LoadManifest(corpusDir)
	if err != nil {
		t.Fatalf("corpus manifest (run cmd/fetchcorpus first): %v", err)
	}

	diverged := 0
	for _, cm := range manifest.Mods {
		if cm.Pak == "" || cm.Exmodz == "" {
			continue // ground truth needs BOTH forms
		}
		cm := cm
		t.Run(cm.Name, func(t *testing.T) {
			pakPath := filepath.Join(corpusDir, cm.Pak)
			meta := Meta{Name: cm.Name, Author: "spike-converted", Version: cm.Version, Description: "converted from pak"}
			synthesized, report, err := ConvertPak(pakPath, basePak, meta)
			if err != nil {
				t.Fatalf("ConvertPak: %v", err)
			}
			ours, err := icarus.ParseExmodz(synthesized)
			if err != nil {
				t.Fatalf("ParseExmodz(ours): %v", err)
			}
			authorData, err := os.ReadFile(filepath.Join(corpusDir, cm.Exmodz))
			if err != nil {
				t.Fatalf("reading author exmodz: %v", err)
			}
			author, err := icarus.ParseExmodz(authorData)
			if err != nil {
				t.Fatalf("ParseExmodz(author): %v", err)
			}

			oursFinal := applyItems(t, base, ours.Diff.Rows)
			authorFinal := applyItems(t, base, author.Diff.Rows)

			// The stale pak snapshot, for residual classification.
			modPak, err := unrealpak.Open(pakPath)
			if err != nil {
				t.Fatalf("reopening mod pak: %v", err)
			}
			defer modPak.Close()

			gt := GroundTruthReport{ModID: cm.ID, ModName: cm.Name,
				StaleRows: report.StaleRows, Findings: report.Findings}
			tables := map[string]bool{}
			for p := range oursFinal {
				tables[p] = true
			}
			for p := range authorFinal {
				tables[p] = true
			}
			sorted := make([]string, 0, len(tables))
			for p := range tables {
				sorted = append(sorted, p)
			}
			sort.Strings(sorted)

			for _, tablePath := range sorted {
				liveData, err := base.ReadFile(tablePath)
				if err != nil {
					t.Fatalf("live base %s: %v", tablePath, err)
				}
				var live map[string]any
				if err := json.Unmarshal(liveData, &live); err != nil {
					t.Fatalf("decoding live %s: %v", tablePath, err)
				}
				liveRows := rowsByName(live)
				oursRows := rowsByName(oursFinal[tablePath])
				authorRows := rowsByName(authorFinal[tablePath])
				if oursFinal[tablePath] == nil {
					oursRows = liveRows // we didn't touch this table
				}
				if authorFinal[tablePath] == nil {
					authorRows = liveRows
				}

				// Stale pak snapshot rows for this table, if present in the pak.
				staleRows := map[string]map[string]any{}
				for _, e := range modPak.Files() {
					class, rel, nerr := NormalizeEntry(modPak.MountPoint(), e.Path)
					if nerr == nil && class == ClassTable && rel == tablePath {
						if data, rerr := modPak.ReadFile(e.Path); rerr == nil {
							var snap map[string]any
							if json.Unmarshal(data, &snap) == nil {
								staleRows = rowsByName(snap)
							}
						}
					}
				}

				names := map[string]bool{}
				for n := range oursRows {
					names[n] = true
				}
				for n := range authorRows {
					names[n] = true
				}
				for n := range names {
					o, a := oursRows[n], authorRows[n]
					if reflect.DeepEqual(o, a) {
						continue
					}
					res := Residual{Table: tablePath, Row: n, Class: "diverged",
						Detail: fmt.Sprintf("ours=%v author=%v", o, a)}
					l, s := liveRows[n], staleRows[n]
					switch {
					case reflect.DeepEqual(a, l) && reflect.DeepEqual(o, s):
						// Author's current exmodz no longer changes this row;
						// the (older) pak did. Pak is stale relative to exmodz.
						res.Class = "stale-pak-change"
					case reflect.DeepEqual(o, l) && a != nil:
						// We suppressed (pak matches live base); author's
						// exmodz is newer than the pak build.
						res.Class = "exmodz-newer-than-pak"
					}
					gt.Residuals = append(gt.Residuals, res)
				}
			}

			gt.Verdict = "PASS"
			for _, r := range gt.Residuals {
				if r.Class == "diverged" {
					gt.Verdict = "DIVERGED"
					break
				}
				gt.Verdict = "EXPLAINED"
			}

			// Embedded data.EXMOD: exact-oracle check of the differ itself.
			for _, raw := range report.EmbeddedExmods {
				embedded, perr := icarus.ParseExmod(raw)
				if perr != nil {
					gt.EmbeddedMatch = "mismatch: embedded .EXMOD unparseable: " + perr.Error()
					continue
				}
				gt.EmbeddedMatch = compareRowSets(embedded.Rows, ours.Diff.Rows)
			}

			if err := SaveJSON(filepath.Join(corpusDir, "reports", cm.ID+"-groundtruth.json"), gt); err != nil {
				t.Fatalf("writing report: %v", err)
			}
			t.Logf("%s: %s (%d residuals, %d stale rows)", cm.Name, gt.Verdict, len(gt.Residuals), gt.StaleRows)
			if gt.Verdict == "DIVERGED" {
				diverged++
				t.Errorf("DIVERGED: %+v", gt.Residuals)
			}
		})
	}
	if diverged > 0 {
		t.Errorf("%d dual-form mods DIVERGED — see <corpus>/reports/", diverged)
	}
}

// compareRowSets reports whether two EXMOD row sets touch the same
// CurrentFiles with the same item Names (field-level detail goes to the
// ground-truth comparison; this is the embedded-oracle sanity check).
func compareRowSets(a, b []icarus.ExmodRow) string {
	key := func(rows []icarus.ExmodRow) map[string][]string {
		out := map[string][]string{}
		for _, r := range rows {
			if r.CurrentFile == "EndOfMod" {
				continue
			}
			var names []string
			for _, it := range r.FileItems {
				names = append(names, it.Name)
			}
			sort.Strings(names)
			out[r.CurrentFile] = names
		}
		return out
	}
	ka, kb := key(a), key(b)
	if reflect.DeepEqual(ka, kb) {
		return "match"
	}
	return fmt.Sprintf("mismatch: embedded=%v ours=%v", ka, kb)
}
