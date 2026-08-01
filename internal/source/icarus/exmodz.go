package icarus

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"strings"
)

// ExmodzBundle is a parsed .EXMODZ: the diff manifest plus any pre-built
// asset files the mod author already compiled (placed as-is into the output
// pak — never recompiled by LMM).
type ExmodzBundle struct {
	Diff   *ExmodDiff
	Assets map[string][]byte // zip-internal path -> raw content, manifest/readme/image excluded
}

// ParseExmodz unpacks zipData (an in-memory .EXMODZ) into its manifest and
// bundled assets. The manifest lives at "Extracted Mods/<name>.EXMOD" in
// every sample seen so far; this looks for any "*.EXMOD" file under an
// "Extracted Mods/" prefix rather than hard-coding the mod name, since that
// varies per mod.
func ParseExmodz(zipData []byte) (*ExmodzBundle, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("icarus: opening .EXMODZ: %w", err)
	}

	bundle := &ExmodzBundle{Assets: make(map[string][]byte)}
	var manifestPath string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "Extracted Mods/") && strings.HasSuffix(f.Name, ".EXMOD") {
			manifestPath = f.Name
			data, err := readZipFile(f)
			if err != nil {
				return nil, fmt.Errorf("icarus: reading %s: %w", f.Name, err)
			}
			bundle.Diff, err = ParseExmod(data)
			if err != nil {
				return nil, err
			}
			continue
		}
	}
	if manifestPath == "" {
		return nil, fmt.Errorf("icarus: .EXMODZ has no Extracted Mods/*.EXMOD manifest")
	}

	for _, f := range zr.File {
		if f.Name == manifestPath || f.FileInfo().IsDir() {
			continue
		}
		if !strings.HasSuffix(f.Name, ".uasset") && !strings.HasSuffix(f.Name, ".uexp") {
			continue // skip readme/image/other non-asset files — never placed into the output pak
		}
		data, err := readZipFile(f)
		if err != nil {
			return nil, fmt.Errorf("icarus: reading asset %s: %w", f.Name, err)
		}
		bundle.Assets[f.Name] = data
	}

	return bundle, nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck
	return io.ReadAll(rc)
}
