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
// varies per mod. Matching (both the manifest's prefix/suffix and the asset
// extensions below) is done on the entry name with backslashes normalized to
// forward slashes and case folded — some .EXMODZ producers are Windows tools
// and zip entry casing is not guaranteed — but stored asset keys keep their
// original case, only the slash direction is normalized.
func ParseExmodz(zipData []byte) (*ExmodzBundle, error) {
	zr, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		return nil, fmt.Errorf("icarus: opening .EXMODZ: %w", err)
	}

	var manifests []*zip.File
	for _, f := range zr.File {
		lower := strings.ToLower(normalizeZipName(f.Name))
		if strings.HasPrefix(lower, "extracted mods/") && strings.HasSuffix(lower, ".exmod") {
			manifests = append(manifests, f)
		}
	}
	switch len(manifests) {
	case 0:
		return nil, fmt.Errorf("icarus: .EXMODZ has no Extracted Mods/*.EXMOD manifest")
	case 1:
		// exactly one candidate — proceed below
	default:
		names := make([]string, len(manifests))
		for i, f := range manifests {
			names[i] = f.Name
		}
		return nil, fmt.Errorf("icarus: .EXMODZ has %d ambiguous Extracted Mods/*.EXMOD manifests: %v",
			len(manifests), names)
	}
	manifestFile := manifests[0]
	manifestPath := manifestFile.Name // original (un-normalized) name, used only to exclude this entry below

	manifestData, err := readZipFile(manifestFile)
	if err != nil {
		return nil, fmt.Errorf("icarus: reading %s: %w", manifestPath, err)
	}
	diff, err := ParseExmod(manifestData)
	if err != nil {
		return nil, fmt.Errorf("icarus: %s: %w", manifestPath, err)
	}
	bundle := &ExmodzBundle{Diff: diff, Assets: make(map[string][]byte)}

	for _, f := range zr.File {
		if f.Name == manifestPath || f.FileInfo().IsDir() {
			continue
		}
		normalized := normalizeZipName(f.Name)
		lower := strings.ToLower(normalized)
		if !strings.HasSuffix(lower, ".uasset") && !strings.HasSuffix(lower, ".uexp") {
			continue // skip readme/image/other non-asset files — never placed into the output pak
		}
		data, err := readZipFile(f)
		if err != nil {
			return nil, fmt.Errorf("icarus: reading asset %s: %w", f.Name, err)
		}
		bundle.Assets[normalized] = data
	}

	return bundle, nil
}

// normalizeZipName converts a zip entry's backslashes to forward slashes.
// Zip is a forward-slash format, but some Windows-built .EXMODZ archives
// store entries with backslashes anyway; this makes matching and asset keys
// consistent regardless of which the producing tool used.
func normalizeZipName(name string) string {
	return strings.ReplaceAll(name, `\`, "/")
}

// maxZipEntrySize caps a single .EXMODZ entry's declared (and actual)
// decompressed size, mirroring #136's dump-tar cap (maxTarEntrySize): the
// largest real Icarus base data table is 7.3 MB, and .EXMODZ manifests and
// bundled UE assets are smaller still in every real sample seen. 64 MiB
// leaves generous headroom while refusing to trust an unbounded or lying
// UncompressedSize64 in a third-party, user-downloaded zip archive.
const maxZipEntrySize = 64 << 20

func readZipFile(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxZipEntrySize {
		return nil, fmt.Errorf("icarus: zip entry %s declares a %d-byte uncompressed size, "+
			"exceeding the %d-byte per-entry cap", f.Name, f.UncompressedSize64, uint64(maxZipEntrySize))
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close() //nolint:errcheck
	// The cap above only guards an honest-but-large size field; a header
	// that LIES by understating the real decompressed size must not be able
	// to drive an unbounded read either, so the read itself is bounded one
	// byte past what was declared — enough to detect an overrun without
	// reading further than necessary to prove it.
	data, err := io.ReadAll(io.LimitReader(rc, int64(f.UncompressedSize64)+1))
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) > f.UncompressedSize64 {
		return nil, fmt.Errorf("icarus: zip entry %s decompresses past its declared %d-byte size",
			f.Name, f.UncompressedSize64)
	}
	return data, nil
}
