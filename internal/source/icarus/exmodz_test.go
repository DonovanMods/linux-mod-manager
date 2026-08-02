package icarus

import (
	"archive/zip"
	"bytes"
	"fmt"
	"hash/crc32"
	"math"
	"strings"
	"testing"
)

// buildTestExmodz mirrors the real Bear_Mount.EXMODZ layout: a manifest
// under "Extracted Mods/<name>.EXMOD" plus loose asset files at paths that
// mirror in-game mount structure.
func buildTestExmodz(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	manifest := `{"name":"Bear Mount","Rows":[{"CurrentFile":"AI-D_AIGrowth.json","File_Items":[{"Name":"Mount_Bear","BaseMovementSpeed":235}]}]}`
	w, err := zw.Create("Extracted Mods/Bear_Mount.EXMOD")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(manifest)) //nolint:errcheck

	assetW, err := zw.Create("Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset")
	if err != nil {
		t.Fatal(err)
	}
	assetW.Write([]byte("fake-uasset-bytes")) //nolint:errcheck

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestParseExmodz(t *testing.T) {
	bundle, err := ParseExmodz(buildTestExmodz(t))
	if err != nil {
		t.Fatalf("ParseExmodz: %v", err)
	}
	if bundle.Diff == nil || bundle.Diff.Name != "Bear Mount" {
		t.Fatalf("Diff = %+v", bundle.Diff)
	}
	asset, ok := bundle.Assets["Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset"]
	if !ok {
		t.Fatalf("Assets missing expected key; got keys: %v", mapKeys(bundle.Assets))
	}
	if string(asset) != "fake-uasset-bytes" {
		t.Errorf("asset content = %q", asset)
	}
}

func mapKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Some .EXMODZ producers are Windows tools and store entry names with
// backslashes; ParseExmodz must normalize them for matching and store asset
// keys under the normalized (forward-slash) form (#136 review round 3).
func TestParseExmodz_NormalizesBackslashNames(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	manifest := `{"name":"Bear Mount","Rows":[]}`
	w, err := zw.Create(`Extracted Mods\Bear_Mount.EXMOD`)
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(manifest)) //nolint:errcheck

	assetW, err := zw.Create(`Bear_Mount\ASS\ITM\SK_ITM_Saddle_Bear.uasset`)
	if err != nil {
		t.Fatal(err)
	}
	assetW.Write([]byte("fake-uasset-bytes")) //nolint:errcheck

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	bundle, err := ParseExmodz(buf.Bytes())
	if err != nil {
		t.Fatalf("ParseExmodz: %v", err)
	}
	if bundle.Diff == nil || bundle.Diff.Name != "Bear Mount" {
		t.Fatalf("Diff = %+v", bundle.Diff)
	}
	asset, ok := bundle.Assets["Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.uasset"]
	if !ok {
		t.Fatalf("Assets missing expected forward-slash key; got keys: %v", mapKeys(bundle.Assets))
	}
	if string(asset) != "fake-uasset-bytes" {
		t.Errorf("asset content = %q", asset)
	}
}

// The manifest's "Extracted Mods/" prefix + ".EXMOD" suffix, and an asset's
// .uasset/.uexp extension, must match regardless of case — a differently
// cased but otherwise valid entry must not be silently dropped.
func TestParseExmodz_MatchesCaseInsensitively(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	manifest := `{"name":"Bear Mount","Rows":[]}`
	w, err := zw.Create("extracted mods/Bear_Mount.exmod")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(manifest)) //nolint:errcheck

	assetW, err := zw.Create("Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.UASSET")
	if err != nil {
		t.Fatal(err)
	}
	assetW.Write([]byte("fake-uasset-bytes")) //nolint:errcheck

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	bundle, err := ParseExmodz(buf.Bytes())
	if err != nil {
		t.Fatalf("ParseExmodz: %v", err)
	}
	if bundle.Diff == nil || bundle.Diff.Name != "Bear Mount" {
		t.Fatalf("Diff = %+v", bundle.Diff)
	}
	if _, ok := bundle.Assets["Bear_Mount/ASS/ITM/SK_ITM_Saddle_Bear.UASSET"]; !ok {
		t.Fatalf("Assets missing case-varying key (original case preserved); got keys: %v", mapKeys(bundle.Assets))
	}
}

// Two candidate manifests is ambiguous and must fail loudly, naming both —
// not silently pick whichever the zip directory happened to list last.
func TestParseExmodz_MultipleManifests_Errors(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for _, name := range []string{"Extracted Mods/A.EXMOD", "Extracted Mods/B.EXMOD"} {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(`{"name":"X","Rows":[]}`)) //nolint:errcheck
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := ParseExmodz(buf.Bytes())
	if err == nil {
		t.Fatal("expected an error for multiple candidate manifests, got nil")
	}
	if !strings.Contains(err.Error(), "A.EXMOD") || !strings.Contains(err.Error(), "B.EXMOD") {
		t.Errorf("error %q should name both ambiguous manifests", err)
	}
}

func TestParseExmodz_NoManifest_Errors(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("readme.txt")
	w.Write([]byte("no manifest here")) //nolint:errcheck
	zw.Close()                          //nolint:errcheck

	if _, err := ParseExmodz(buf.Bytes()); err == nil {
		t.Error("expected error when no .EXMOD manifest is present, got nil")
	}
}

// A present-but-malformed manifest (invalid JSON) must fail loudly, wrapped
// with the manifest's own path — Task 11 review noted this path returned
// ParseExmod's error unwrapped, unlike every other error path in this file.
func TestParseExmodz_MalformedManifest_Errors(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("Extracted Mods/Bad.EXMOD")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte("{not valid json")) //nolint:errcheck
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = ParseExmodz(buf.Bytes())
	if err == nil {
		t.Fatal("expected an error for a malformed manifest, got nil")
	}
	if !strings.Contains(err.Error(), "Bad.EXMOD") {
		t.Errorf("error %q should name the manifest that failed to parse", err)
	}
}

// An entry (manifest or asset) declaring an uncompressed size over the
// per-entry cap must be rejected before any content is read — guards
// against a user-downloaded, third-party .EXMODZ with a corrupt or lying
// size field driving an unbounded allocation, mirroring #136's dump-tar
// cap. zw.CreateRaw writes the caller-declared size fields verbatim (unlike
// zw.Create/CreateHeader, which recompute them from what's actually
// written), so the fixture never needs 64+ real MiB of content.
func TestParseExmodz_RejectsOversizedAssetDeclaredSize(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("Extracted Mods/X.EXMOD")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(`{"name":"X","Rows":[]}`)) //nolint:errcheck

	content := []byte("tiny")
	rawW, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "Bear_Mount/huge.uasset",
		Method:             zip.Store,
		UncompressedSize64: maxZipEntrySize + 1,
		CompressedSize64:   uint64(len(content)),
		CRC32:              crc32.ChecksumIEEE(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	rawW.Write(content) //nolint:errcheck

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = ParseExmodz(buf.Bytes())
	if err == nil {
		t.Fatal("expected an error for an asset declaring an oversized uncompressed size, got nil")
	}
	if !strings.Contains(err.Error(), "Bear_Mount/huge.uasset") {
		t.Errorf("error %q should name the offending entry", err)
	}
}

// An entry whose declared size is UNDER the cap (so the pre-check passes)
// but whose actual decompressed content exceeds that declared size must
// still be caught — the read itself has to be bounded, not just the
// pre-check, so a lying-but-small header can't drive an unbounded read.
func TestParseExmodz_RejectsLyingAssetDeclaredSize(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("Extracted Mods/X.EXMOD")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(`{"name":"X","Rows":[]}`)) //nolint:errcheck

	content := []byte("hello world") // 11 real bytes
	rawW, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "Bear_Mount/lying.uasset",
		Method:             zip.Store,
		UncompressedSize64: 3, // lies: declares far less than the 11 real bytes
		CompressedSize64:   uint64(len(content)),
		CRC32:              crc32.ChecksumIEEE(content),
	})
	if err != nil {
		t.Fatal(err)
	}
	rawW.Write(content) //nolint:errcheck

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = ParseExmodz(buf.Bytes())
	if err == nil {
		t.Fatal("expected an error for an asset whose real content exceeds its declared uncompressed size, got nil")
	}
	if !strings.Contains(err.Error(), "Bear_Mount/lying.uasset") {
		t.Errorf("error %q should name the offending entry", err)
	}
}

// TestParseExmodz_RejectsOversizedTotalAssetsSize guards a Copilot release-
// review finding on #203: the per-entry cap (maxZipEntrySize, 64 MiB) alone
// lets an archive with MANY entries, each individually under that cap,
// still declare an unbounded total - e.g. five entries at 60 MiB apiece sum
// to 300 MiB despite no single entry tripping the per-entry check. A total
// cap across every bundled asset closes that gap. zw.CreateRaw declares the
// size fields verbatim, so the fixture never needs real hundreds of MiB of
// content, and the check must fire against the DECLARED sizes before any
// asset is read - the fixture's real content is just a few bytes.
func TestParseExmodz_RejectsOversizedTotalAssetsSize(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("Extracted Mods/X.EXMOD")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(`{"name":"X","Rows":[]}`)) //nolint:errcheck

	// 5 entries * 60 MiB declared = 300 MiB, over the 256 MiB total cap,
	// while each individual entry (60 MiB) stays under the 64 MiB per-entry
	// cap.
	const perEntryDeclared = 60 << 20
	content := []byte("tiny")
	for i := 0; i < 5; i++ {
		rawW, err := zw.CreateRaw(&zip.FileHeader{
			Name:               fmt.Sprintf("Bear_Mount/asset%d.uasset", i),
			Method:             zip.Store,
			UncompressedSize64: perEntryDeclared,
			CompressedSize64:   uint64(len(content)),
			CRC32:              crc32.ChecksumIEEE(content),
		})
		if err != nil {
			t.Fatal(err)
		}
		rawW.Write(content) //nolint:errcheck
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = ParseExmodz(buf.Bytes())
	if err == nil {
		t.Fatal("expected an error for bundled assets whose combined declared size exceeds the total cap, got nil")
	}
	if !strings.Contains(err.Error(), "EXMODZ") {
		t.Errorf("error %q should name the archive", err)
	}
}

// TestParseExmodz_TotalAssetsSizeAccumulationIsOverflowSafe guards a
// Copilot round-2 release-review finding on #204: totalDeclaredSize +=
// f.UncompressedSize64 summed attacker-controlled declared sizes with no
// per-entry gate, so two entries whose declared sizes overflow a uint64 sum
// wrap the total to a value far below the 256 MiB cap - the total-cap check
// meant to reject the whole bundle before any asset is read silently
// passes, and the bundle only fails afterward (if at all) via the
// UNRELATED per-entry cap inside readZipFile, once assets are actually
// being opened. The fix must reject against the remaining cap headroom
// BEFORE adding each declared size, so the total-cap error - not the
// per-entry one - fires first, without ever calling readZipFile.
func TestParseExmodz_TotalAssetsSizeAccumulationIsOverflowSafe(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	w, err := zw.Create("Extracted Mods/X.EXMOD")
	if err != nil {
		t.Fatal(err)
	}
	w.Write([]byte(`{"name":"X","Rows":[]}`)) //nolint:errcheck

	content := []byte("tiny")
	crc := crc32.ChecksumIEEE(content)
	// The sum of these two declared sizes overflows uint64 and wraps to 49
	// (math.MaxUint64-50 + 100 == math.MaxUint64+50, mod 2^64), even though
	// the first entry alone already dwarfs the 256 MiB total cap.
	sizes := []uint64{math.MaxUint64 - 50, 100}
	for i, size := range sizes {
		rawW, err := zw.CreateRaw(&zip.FileHeader{
			Name:               fmt.Sprintf("Bear_Mount/huge%d.uasset", i),
			Method:             zip.Store,
			UncompressedSize64: size,
			CompressedSize64:   uint64(len(content)),
			CRC32:              crc,
		})
		if err != nil {
			t.Fatal(err)
		}
		rawW.Write(content) //nolint:errcheck
	}

	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = ParseExmodz(buf.Bytes())
	if err == nil {
		t.Fatal("expected an error for a declared-size sum that overflows uint64, got nil")
	}
	if !strings.Contains(err.Error(), "total cap") {
		t.Errorf("error %q should name the total cap - an overflowed sum must not slip past it undetected", err)
	}
	if strings.Contains(err.Error(), "per-entry cap") {
		t.Errorf("error %q fired from the per-entry check, meaning uint64 overflow bypassed the total-cap check", err)
	}
}
