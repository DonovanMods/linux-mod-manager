package icarus

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/DonovanMods/go-unrealpak"
)

// defaultIcarusBasePak is where a stock Steam library keeps the installed
// game's own data.pak; ICARUS_GOLDEN_BASE_PAK overrides it for other layouts.
const defaultIcarusBasePak = "/data/SteamLibrary/steamapps/common/Icarus/Icarus/Content/Data/data.pak"

// TestGoldenDualForm_CompiledExmodzMatchesPublishedPak compiles a real
// .EXMODZ with the full pipeline and checks the resulting pak's virtual
// paths against the SAME mod's author-published _P.pak — an out-of-loop
// oracle no other test in this package has. Every other fixture base pak
// here is built by lmm's own writer and read back by lmm's own reader, a
// closed loop that proves the two agree with each other, not that either
// agrees with the game; bundled assets deployed one directory too deep for
// months inside that blind spot (#237) while the suite stayed green. Any
// future divergence in mount point, data/ prefix, or asset placement fails
// this test immediately (#242).
//
// Gated like go-unrealpak's UNREALPAK_TEST_PAK real-file test: set
// ICARUS_GOLDEN_MOD_DIR to a directory holding ONE dual-form mod — its
// .EXMODZ plus the author's own published .pak — e.g.
//
//	ICARUS_GOLDEN_MOD_DIR=/path/to/dual-form-mod go test ./internal/source/icarus/ -run Golden -v
//
// The mod must carry bundled assets: a table-only mod would pass without
// exercising asset placement at all — exactly the passes-for-the-wrong-reason
// gap this test exists to close — so the test refuses it loudly instead.
// (Of the #220 spike corpus's eleven dual-form pairs, only the two
// Crys_Lvl120Cap mods qualify.) ICARUS_GOLDEN_BASE_PAK overrides the
// default Steam location of the installed game's data.pak.
func TestGoldenDualForm_CompiledExmodzMatchesPublishedPak(t *testing.T) {
	modDir := os.Getenv("ICARUS_GOLDEN_MOD_DIR")
	if modDir == "" {
		t.Skip("set ICARUS_GOLDEN_MOD_DIR to a directory holding one dual-form mod (.EXMODZ + published .pak) to run this test")
	}
	basePak := os.Getenv("ICARUS_GOLDEN_BASE_PAK")
	if basePak == "" {
		basePak = defaultIcarusBasePak
	}
	if _, err := os.Stat(basePak); err != nil {
		t.Fatalf("installed base data.pak not found: %v (set ICARUS_GOLDEN_BASE_PAK to the game's Content/Data/data.pak)", err)
	}

	exmodzPath, publishedPakPath := findDualFormPair(t, modDir)

	exmodzData, err := os.ReadFile(exmodzPath)
	if err != nil {
		t.Fatalf("reading %s: %v", exmodzPath, err)
	}
	bundle, err := ParseExmodz(exmodzData)
	if err != nil {
		t.Fatalf("ParseExmodz(%s): %v", exmodzPath, err)
	}
	if len(bundle.Assets) == 0 {
		t.Fatalf("%s bundles no assets, so this test would pass without exercising asset placement — the exact #237 blind spot it exists to close; point ICARUS_GOLDEN_MOD_DIR at an asset-bearing dual-form mod (e.g. Crys_Lvl120Cap_M25)",
			filepath.Base(exmodzPath))
	}

	compiledPath := filepath.Join(t.TempDir(), "compiled_P.pak")
	if err := Compile(basePak, exmodzPath, compiledPath); err != nil {
		t.Fatalf("Compile: %v", err)
	}

	compiled := readVirtualPathSet(t, compiledPath)
	published := readVirtualPathSet(t, publishedPakPath)

	// UE resolves a pak entry at MountPoint + entry path, case-insensitively;
	// the two paks legitimately split that differently (the author's pak
	// mounts deeper), so only the joined, case-folded form is comparable.
	var missing, extra []string
	for folded, orig := range published {
		if _, ok := compiled[folded]; !ok {
			missing = append(missing, orig)
		}
	}
	for folded, orig := range compiled {
		if _, ok := published[folded]; !ok {
			extra = append(extra, orig)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 || len(extra) > 0 {
		t.Fatalf("compiled pak's virtual paths diverge from the author's published pak:\n"+
			"  in published pak but not compiled output:\n%s\n"+
			"  in compiled output but not published pak:\n%s",
			formatPathList(missing), formatPathList(extra))
	}
	t.Logf("%s: %d virtual paths agree with %s (%d bundled assets)",
		filepath.Base(exmodzPath), len(published), filepath.Base(publishedPakPath), len(bundle.Assets))
}

// findDualFormPair locates the single .EXMODZ and single published .pak in
// dir, failing loudly on anything other than exactly one of each — a
// directory with several candidates would make the test's subject ambiguous.
func findDualFormPair(t *testing.T, dir string) (exmodzPath, pakPath string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading ICARUS_GOLDEN_MOD_DIR: %v", err)
	}
	var exmodzs, paks []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		lower := strings.ToLower(e.Name())
		switch {
		case strings.HasSuffix(lower, ".exmodz"):
			exmodzs = append(exmodzs, filepath.Join(dir, e.Name()))
		case strings.HasSuffix(lower, ".pak"):
			paks = append(paks, filepath.Join(dir, e.Name()))
		}
	}
	if len(exmodzs) != 1 || len(paks) != 1 {
		t.Fatalf("ICARUS_GOLDEN_MOD_DIR must hold exactly one .EXMODZ and one published .pak; found %d .EXMODZ %v and %d .pak %v in %s",
			len(exmodzs), exmodzs, len(paks), paks, dir)
	}
	return exmodzs[0], paks[0]
}

// readVirtualPathSet opens a pak and returns every entry's full virtual path
// (mount point + entry path), keyed by its case-folded form for the
// case-insensitive comparison, with the original spelling as the value for
// readable failure output.
func readVirtualPathSet(t *testing.T, pakPath string) map[string]string {
	t.Helper()
	r, err := unrealpak.Open(pakPath)
	if err != nil {
		t.Fatalf("opening %s: %v", pakPath, err)
	}
	defer r.Close() //nolint:errcheck

	set := make(map[string]string)
	for _, f := range r.Files() {
		full := path.Join(r.MountPoint(), f.Path)
		folded := strings.ToLower(full)
		// Two entries differing only by case would collapse into one key and
		// could hide a divergence; UE couldn't distinguish them either, so a
		// pak like that is malformed — refuse it rather than compare loosely.
		if prev, ok := set[folded]; ok {
			t.Fatalf("%s: case-insensitive path collision between %q and %q", pakPath, prev, full)
		}
		set[folded] = full
	}
	if len(set) == 0 {
		t.Fatalf("%s holds no entries", pakPath)
	}
	return set
}

func formatPathList(paths []string) string {
	if len(paths) == 0 {
		return "    (none)"
	}
	return "    " + strings.Join(paths, "\n    ")
}
