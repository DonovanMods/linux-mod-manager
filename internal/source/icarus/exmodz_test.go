package icarus

import (
	"archive/zip"
	"bytes"
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
