package icarus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DonovanMods/go-unrealpak"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// newFormatTestSource returns an *Icarus suitable for exercising the format
// methods (#256) - none of them touch Firestore, so a nil HTTP client and a
// dummy project ID are fine.
func newFormatTestSource() *Icarus { return New(nil, "test-project") }

func testGame(id, installPath string) *domain.Game {
	return &domain.Game{ID: id, InstallPath: installPath}
}

func TestResolveBaseArtifact_FindsIcarusDataPak(t *testing.T) {
	installDir := t.TempDir()
	basePakDir := filepath.Join(installDir, "Icarus", "Content", "Data")
	if err := os.MkdirAll(basePakDir, 0755); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(basePakDir, "data.pak")
	if err := os.WriteFile(wantPath, []byte("stub"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := newFormatTestSource().ResolveBaseArtifact(testGame("icarus", installDir))
	if err != nil {
		t.Fatalf("ResolveBaseArtifact: %v", err)
	}
	if got != wantPath {
		t.Errorf("ResolveBaseArtifact = %q, want %q", got, wantPath)
	}
}

func TestResolveBaseArtifact_MissingPakErrors(t *testing.T) {
	_, err := newFormatTestSource().ResolveBaseArtifact(testGame("icarus", t.TempDir()))
	if err == nil {
		t.Fatal("ResolveBaseArtifact must error when the base pak is absent")
	}
	// The error names the game, matching core's pre-#256 resolveBasePak
	// wrapping ("locating base pak for %q: ...") which callers' messages
	// were built around.
	if want := `locating base pak for "icarus"`; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not contain %q", err, want)
	}
}

func TestFingerprintBase_MatchesPakIndexHash(t *testing.T) {
	pakPath := writeTestBasePak(t, map[string][]byte{"Data/D_Fixture.json": []byte(`{"fixture":true}`)})

	got, err := newFormatTestSource().FingerprintBase(pakPath)
	if err != nil {
		t.Fatalf("FingerprintBase: %v", err)
	}

	r, err := unrealpak.Open(pakPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close() //nolint:errcheck
	if want := r.IndexHash(); got != want {
		t.Errorf("FingerprintBase = %q, want the pak's IndexHash %q", got, want)
	}
	if got == "" {
		t.Error("FingerprintBase must not be empty for a valid pak")
	}
}

func TestFingerprintBase_InvalidPakErrors(t *testing.T) {
	notAPak := filepath.Join(t.TempDir(), "data.pak")
	if err := os.WriteFile(notAPak, []byte("not a pak"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := newFormatTestSource().FingerprintBase(notAPak); err == nil {
		t.Fatal("FingerprintBase must error on an unparseable pak")
	}
}

func TestIsConvertibleArtifact(t *testing.T) {
	tests := map[string]bool{
		"MyMod.pak":    true,
		"MyMod.PAK":    true, // case-insensitive
		"MyMod.exmodz": false,
		"MyMod.zip":    false,
		// A bare "pak" is a download-path fileID, not a filename - the
		// ingest predicate this backs (pre-#256 isConvertEligiblePakFile)
		// was suffix-only, and stays that way.
		"pak": false,
	}
	s := newFormatTestSource()
	for fileName, want := range tests {
		if got := s.IsConvertibleArtifact(fileName); got != want {
			t.Errorf("IsConvertibleArtifact(%q) = %v, want %v", fileName, got, want)
		}
	}
}

func TestIsNativeMergeSource(t *testing.T) {
	tests := map[string]bool{
		"Bear_Mount.exmodz": true,
		"Bear_Mount.EXMODZ": true, // case-insensitive
		"MyMod.pak":         false,
		"MyMod.zip":         false,
		// Bare "exmodz" is a download-path fileID, not a filename - this
		// predicate backs ingest routing on FILENAMES (pre-#256 core's
		// isExmodzFile), which was suffix-only, and stays that way.
		"exmodz": false,
	}
	s := newFormatTestSource()
	for fileName, want := range tests {
		if got := s.IsNativeMergeSource(fileName); got != want {
			t.Errorf("IsNativeMergeSource(%q) = %v, want %v", fileName, got, want)
		}
	}
}

func TestClassifyMergeSource(t *testing.T) {
	tests := map[string]struct {
		kind        string
		convertible bool
	}{
		"pak":          {MergeSourcePak, true}, // download-path fileID
		"MyMod.PAK":    {MergeSourcePak, true}, // import-path filename
		"exmodz":       {MergeSourceExmodz, false},
		"MyMod.exmodz": {MergeSourceExmodz, false},
		"weird.zip":    {MergeSourceExmodz, false}, // unknown: exmodz, the only pre-#221 kind
		"":             {MergeSourceExmodz, false}, // legacy fingerprint entries carry no Kind
	}
	s := newFormatTestSource()
	for id, want := range tests {
		kind, convertible := s.ClassifyMergeSource(id)
		if kind != want.kind || convertible != want.convertible {
			t.Errorf("ClassifyMergeSource(%q) = (%q, %v), want (%q, %v)",
				id, kind, convertible, want.kind, want.convertible)
		}
	}
}

func TestRestoredArtifactName(t *testing.T) {
	// The exact shape is on-disk state (#250): a heal-restored raw-fallback
	// copy for existing installs is published under <mod-id>_P.pak, "_P"
	// being UE's override-pak suffix convention - byte-identical names must
	// come out of the seam or already-healed caches would orphan.
	if got := newFormatTestSource().RestoredArtifactName("cool-mod"); got != "cool-mod_P.pak" {
		t.Errorf("RestoredArtifactName = %q, want %q", got, "cool-mod_P.pak")
	}
}

func TestMergedArtifactName(t *testing.T) {
	// The exact name is a deploy contract (#197): it must sort last among
	// mounted paks ("zzz"), be greppable as lmm-owned, and keep the "_P"
	// override suffix - changing it would orphan already-deployed files.
	if got := newFormatTestSource().MergedArtifactName(); got != "zzz_LMM_Merged_P.pak" {
		t.Errorf("MergedArtifactName = %q, want %q", got, "zzz_LMM_Merged_P.pak")
	}
}

func TestMergedArtifactLabel(t *testing.T) {
	// User-facing display name for the merged artifact's synthetic mod row
	// (verify/update output) - pre-#256 core hardcoded this string.
	if got := newFormatTestSource().MergedArtifactLabel(); got != "Icarus Merged Pak" {
		t.Errorf("MergedArtifactLabel = %q, want %q", got, "Icarus Merged Pak")
	}
}
