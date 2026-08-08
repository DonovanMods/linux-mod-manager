package core

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/cache"
)

// testClassify mirrors the icarus classifier for the fingerprint-helper
// tests below: core no longer owns a merge-source classification of its own
// (#256 - ClassifyMergeSource moved behind the MergeCompiler seam), so the
// package-level helpers take the classifier as a parameter and these tests
// supply the canonical one.
func testClassify(id string) (kind string, convertible bool) {
	lower := strings.ToLower(id)
	if lower == "pak" || strings.HasSuffix(lower, ".pak") {
		return "pak", true
	}
	return "exmodz", false
}

// formatOnlyCompilerSource is the minimal source.ModSource +
// source.MergeCompiler TestModHasPakMergeSource needs (#256):
// classification moved behind the MergeCompiler seam, so even the cheap
// FileIDs check resolves the game's compile source now. Only ID and the
// format methods matter; everything else is inert.
type formatOnlyCompilerSource struct{}

func (formatOnlyCompilerSource) ID() string      { return "fake-compiler" }
func (formatOnlyCompilerSource) Name() string    { return "Fake Compiler" }
func (formatOnlyCompilerSource) AuthURL() string { return "" }
func (formatOnlyCompilerSource) ExchangeToken(context.Context, string) (*source.Token, error) {
	return nil, source.ErrNotSupported
}
func (formatOnlyCompilerSource) Search(context.Context, source.SearchQuery) (source.SearchResult, error) {
	return source.SearchResult{}, source.ErrNotSupported
}
func (formatOnlyCompilerSource) GetMod(context.Context, string, string) (*domain.Mod, error) {
	return nil, source.ErrNotSupported
}
func (formatOnlyCompilerSource) GetDependencies(context.Context, *domain.Mod) ([]domain.ModReference, error) {
	return nil, source.ErrNotSupported
}
func (formatOnlyCompilerSource) GetModFiles(context.Context, *domain.Mod) ([]domain.DownloadableFile, error) {
	return nil, source.ErrNotSupported
}
func (formatOnlyCompilerSource) GetDownloadURL(context.Context, *domain.Mod, string) (string, error) {
	return "", source.ErrNotSupported
}
func (formatOnlyCompilerSource) CheckUpdates(context.Context, []domain.InstalledMod) ([]domain.Update, error) {
	return nil, source.ErrNotSupported
}
func (formatOnlyCompilerSource) ValidateSource(string) error { return nil }
func (formatOnlyCompilerSource) MergeCompile(context.Context, string, []source.MergeSource, string) ([]string, []source.MergeFailure, error) {
	return nil, nil, nil
}
func (formatOnlyCompilerSource) ResolveBaseArtifact(*domain.Game) (string, error) {
	return "", os.ErrNotExist
}
func (formatOnlyCompilerSource) FingerprintBase(string) (string, error) { return "", os.ErrNotExist }
func (formatOnlyCompilerSource) IsNativeMergeSource(fileName string) bool {
	return strings.HasSuffix(strings.ToLower(fileName), ".exmodz")
}
func (formatOnlyCompilerSource) IsConvertibleArtifact(fileName string) bool {
	return strings.HasSuffix(strings.ToLower(fileName), ".pak")
}
func (formatOnlyCompilerSource) ClassifyMergeSource(id string) (string, bool) {
	return testClassify(id)
}
func (formatOnlyCompilerSource) MergedArtifactName() string  { return "zzz_LMM_Merged_P.pak" }
func (formatOnlyCompilerSource) MergedArtifactLabel() string { return "Icarus Merged Pak" }

func TestMergedFingerprint_Deterministic(t *testing.T) {
	f := MergedFingerprint{
		BaseIndexHash: "abc123",
		Mods: []MergedFingerprintEntry{
			{SourceID: "icarus", ModID: "bear-mount", Version: "1.0", Checksum: "deadbeef"},
			{SourceID: "icarus", ModID: "wolf-mount", Version: "2.0", Checksum: "cafef00d"},
		},
	}
	b1, err := marshalMergedFingerprint(f)
	if err != nil {
		t.Fatalf("marshal 1: %v", err)
	}
	b2, err := marshalMergedFingerprint(f)
	if err != nil {
		t.Fatalf("marshal 2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Errorf("marshal not deterministic: %q vs %q", b1, b2)
	}
}

func TestMergedFingerprintsEqual_IdenticalInputs(t *testing.T) {
	f := MergedFingerprint{
		BaseIndexHash: "abc123",
		Mods:          []MergedFingerprintEntry{{SourceID: "icarus", ModID: "bear-mount", Version: "1.0", Checksum: "deadbeef"}},
	}
	eq, err := mergedFingerprintsEqual(f, f, testClassify)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if !eq {
		t.Errorf("identical fingerprints compared unequal")
	}
}

func TestMergedFingerprintsEqual_BaseHashChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc123", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"}}}
	b := a
	b.BaseIndexHash = "def456"
	eq, err := mergedFingerprintsEqual(a, b, testClassify)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("base pak change (regeneration trigger) must compare unequal")
	}
}

func TestMergedFingerprintsEqual_ModSetChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"}}}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"},
		{SourceID: "icarus", ModID: "m2", Version: "1.0", Checksum: "y"},
	}}
	eq, err := mergedFingerprintsEqual(a, b, testClassify)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("enabling a mod (regeneration trigger) must compare unequal")
	}
}

func TestMergedFingerprintsEqual_LoadOrderChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"},
		{SourceID: "icarus", ModID: "m2", Version: "1.0", Checksum: "y"},
	}}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m2", Version: "1.0", Checksum: "y"},
		{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"},
	}}
	eq, err := mergedFingerprintsEqual(a, b, testClassify)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("a load-order swap (regeneration trigger) must compare unequal, got equal")
	}
}

func TestMergedFingerprintsEqual_VersionChanged(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "1.0", Checksum: "x"}}}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m1", Version: "2.0", Checksum: "x2"}}}
	eq, err := mergedFingerprintsEqual(a, b, testClassify)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if eq {
		t.Errorf("a mod version bump (regeneration trigger) must compare unequal")
	}
}

func TestMergedFingerprintsEqual_EmptyModsBothSides(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "abc", Mods: nil}
	b := MergedFingerprint{BaseIndexHash: "abc", Mods: []MergedFingerprintEntry{}}
	eq, err := mergedFingerprintsEqual(a, b, testClassify)
	if err != nil {
		t.Fatalf("mergedFingerprintsEqual: %v", err)
	}
	if !eq {
		t.Errorf("nil vs empty Mods slice must still compare equal (both marshal to the same JSON array shape)")
	}
}

// The fileID->kind classification itself moved to the icarus package in
// #256 (ClassifyMergeSource) - internal/source/icarus/format_test.go's
// TestClassifyMergeSource carries the old TestMergeSourceKind table.

func TestModHasPakMergeSource(t *testing.T) {
	reg := source.NewRegistry()
	reg.Register(formatOnlyCompilerSource{})
	svc := &Service{registry: reg}
	game := &domain.Game{ID: "g", SourceIDs: map[string]string{"fake-compiler": "g"}}

	tests := []struct {
		name string
		mod  *domain.InstalledMod
		want bool
	}{
		{"nil mod", nil, false},
		{"no FileIDs", &domain.InstalledMod{}, false},
		{"exmodz-only FileIDs", &domain.InstalledMod{FileIDs: []string{"exmodz"}}, false},
		{"pak FileID", &domain.InstalledMod{FileIDs: []string{"pak"}}, true},
		{"import-path .pak filename", &domain.InstalledMod{FileIDs: []string{"MyMod.PAK"}}, true},
		{"mixed FileIDs, one pak", &domain.InstalledMod{FileIDs: []string{"exmodz", "pak"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := svc.ModHasPakMergeSource(game, tt.mod); got != tt.want {
				t.Errorf("ModHasPakMergeSource(%+v) = %v, want %v", tt.mod, got, tt.want)
			}
		})
	}

	// #256: with no merge-compiler-capable source mapped, there is nothing
	// to classify against - never true, regardless of FileIDs.
	bare := &domain.Game{ID: "bare"}
	if svc.ModHasPakMergeSource(bare, &domain.InstalledMod{FileIDs: []string{"pak"}}) {
		t.Error("ModHasPakMergeSource must be false for a game with no MergeCompiler source")
	}
}

func TestFingerprintEqualityIgnoresOutcomes(t *testing.T) {
	a := MergedFingerprint{BaseIndexHash: "h", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m", Version: "1", Checksum: "c", Kind: "pak", Converted: true},
	}}
	b := MergedFingerprint{BaseIndexHash: "h", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m", Version: "1", Checksum: "c", Kind: "pak", Converted: false, FailReason: "x"},
	}}
	eq, err := mergedFingerprintsEqual(a, b, testClassify)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("outcome fields must not affect input equality (retry only when inputs change)")
	}

	c := b
	c.Mods = []MergedFingerprintEntry{{SourceID: "icarus", ModID: "m", Version: "2", Checksum: "c", Kind: "pak"}}
	eq, err = mergedFingerprintsEqual(a, c, testClassify)
	if err != nil {
		t.Fatal(err)
	}
	if eq {
		t.Fatal("input fields (Version) must affect equality")
	}
}

func TestReadOldFingerprintMarkerCompat(t *testing.T) {
	// A pre-#221 marker has no Kind/Converted/FailReason - it must read
	// fine and compare EQUAL to a current computation with the same inputs
	// (Kind "" vs "exmodz" must not force a spurious regen).
	dir := t.TempDir()
	old := []byte(`{"BaseIndexHash":"h","Mods":[{"SourceID":"icarus","ModID":"m","Version":"1","Checksum":"c"}]}`)
	if err := os.WriteFile(cache.MergeFingerprintPath(dir), old, 0644); err != nil {
		t.Fatal(err)
	}
	stored, ok := readMergedFingerprint(dir)
	if !ok {
		t.Fatal("old marker unreadable")
	}
	current := MergedFingerprint{BaseIndexHash: "h", Mods: []MergedFingerprintEntry{
		{SourceID: "icarus", ModID: "m", Version: "1", Checksum: "c", Kind: "exmodz", Converted: true},
	}}
	eq, err := mergedFingerprintsEqual(current, stored, testClassify)
	if err != nil {
		t.Fatal(err)
	}
	if !eq {
		t.Fatal("pre-#221 marker must not trigger a regen for unchanged exmodz inputs")
	}
}
