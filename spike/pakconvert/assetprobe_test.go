package pakconvert

import (
	"errors"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/unrealpak"
)

// AssetProbeReport records the empirical dissection of one mod pak —
// answering spike question 4 (does the inferred asset layout hold?).
type AssetProbeReport struct {
	ModID                  string
	ModName                string
	MountPoint             string
	Entries                []ProbeEntry
	ExtensionCensus        map[string]int
	LayoutMatchesInference bool // all assets join under Icarus/Content/ WITHOUT a data/ prefix
}

// ProbeEntry is one pak entry's classification + readability.
type ProbeEntry struct {
	Path      string
	Size      int64
	Class     string
	TablePath string
	Readable  bool
	ReadError string // "" | "oodle-or-unsupported" | raw error text
}

// TestAssetProbe dissects EVERY corpus pak (the asset-heavy ones are the
// point, but census data on all of them is free) and writes
// <corpus>/reports/<id>-assetprobe.json. Report-only: pak content never
// fails the test.
func TestAssetProbe(t *testing.T) {
	_, corpusDir := spikeEnv(t)
	manifest, err := LoadManifest(corpusDir)
	if err != nil {
		t.Fatalf("corpus manifest (run cmd/fetchcorpus first): %v", err)
	}
	for _, cm := range manifest.Mods {
		if cm.Pak == "" {
			continue
		}
		cm := cm
		t.Run(cm.Name, func(t *testing.T) {
			r, err := unrealpak.Open(filepath.Join(corpusDir, cm.Pak))
			if err != nil {
				// Unreadable pak IS a census result, not a test failure.
				// LayoutMatchesInference is set true VACUOUSLY here — nothing
				// was probed to violate it, so false is reserved exclusively
				// for genuine layout violations in probed paks. The
				// "UNOPENABLE: <err>" census key is the don't-trust-this-report
				// signal a consumer should check first.
				report := AssetProbeReport{ModID: cm.ID, ModName: cm.Name,
					ExtensionCensus:        map[string]int{"UNOPENABLE: " + err.Error(): 1},
					LayoutMatchesInference: true}
				if serr := SaveJSON(filepath.Join(corpusDir, "reports", cm.ID+"-assetprobe.json"), report); serr != nil {
					t.Fatalf("writing report: %v", serr)
				}
				t.Logf("%s: pak unopenable: %v", cm.Name, err)
				return
			}
			defer r.Close()

			report := AssetProbeReport{
				ModID: cm.ID, ModName: cm.Name, MountPoint: r.MountPoint(),
				ExtensionCensus:        map[string]int{},
				LayoutMatchesInference: true,
			}
			for _, e := range r.Files() {
				pe := ProbeEntry{Path: e.Path, Size: e.Size}
				ext := strings.ToLower(path.Ext(e.Path))
				if ext == "" {
					ext = "(none)"
				}
				report.ExtensionCensus[ext]++

				class, rel, nerr := NormalizeEntry(r.MountPoint(), e.Path)
				pe.Class = class.String()
				if nerr != nil {
					pe.Class = "hyphen-error"
				} else if class == ClassTable {
					pe.TablePath = rel
				}

				if _, rerr := r.ReadFile(e.Path); rerr != nil {
					pe.Readable = false
					if errors.Is(rerr, unrealpak.ErrUnsupportedFormat) {
						pe.ReadError = "oodle-or-unsupported"
					} else {
						pe.ReadError = rerr.Error()
					}
				} else {
					pe.Readable = true
				}

				if class == ClassAsset {
					// Inference under probe (pak-divergence-report §2): assets
					// join under Icarus/Content/ with NO data/ prefix.
					full := path.Join(r.MountPoint(), e.Path)
					if !strings.HasPrefix(full, "../../../Icarus/Content/") ||
						strings.HasPrefix(strings.TrimPrefix(full, "../../../Icarus/Content/"), "data/") {
						report.LayoutMatchesInference = false
					}
				}
				report.Entries = append(report.Entries, pe)
			}
			if err := SaveJSON(filepath.Join(corpusDir, "reports", cm.ID+"-assetprobe.json"), report); err != nil {
				t.Fatalf("writing report: %v", err)
			}
			t.Logf("%s: %d entries, mount %q, extensions %v, layoutMatchesInference=%v",
				cm.Name, len(report.Entries), report.MountPoint, report.ExtensionCensus, report.LayoutMatchesInference)
		})
	}
}
