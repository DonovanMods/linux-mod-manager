package core

// #656: archive/zip decodes only Store and Deflate. Mod authors also ship
// zips compressed with BZIP2, LZMA, Deflate64, Zstandard or PPMd, which the
// native path cannot open ("zip: unsupported compression algorithm"). Such a
// zip goes to the system 7z instead, which lmm already needs for .7z and
// .rar - the whole archive, so one archive is never half native and half 7z.
//
// The fallback takes the same safe route as every other archive 7z
// extracts (extractThrough7z, extractor_7z.go), so it keeps every
// protection the native path gives each member.
//
// The plan-time listing (listArchiveMembers) takes the same route, so a plan
// names exactly the members the ingest will write, and a machine without 7z
// fails at plan time with the same message the ingest would give.

import (
	"archive/zip"
	"context"
	"fmt"
	"os/exec"
)

// zipMethodNames names the zip compression methods (APPNOTE 4.4.5) a mod
// archive plausibly uses; zipMethodName falls back to the number.
var zipMethodNames = map[uint16]string{
	zip.Store:   "Store",
	1:           "Shrink",
	2:           "Reduce",
	3:           "Reduce",
	4:           "Reduce",
	5:           "Reduce",
	6:           "Implode",
	zip.Deflate: "Deflate",
	9:           "Deflate64",
	10:          "PKWARE DCL Implode",
	12:          "BZIP2",
	14:          "LZMA",
	20:          "Zstandard",
	93:          "Zstandard",
	95:          "XZ",
	98:          "PPMd",
	99:          "AES encryption",
}

// zipMethodName is a zip compression method's name, for a message.
func zipMethodName(method uint16) string {
	if name, ok := zipMethodNames[method]; ok {
		return name
	}
	return fmt.Sprintf("method %d", method)
}

// zipNeeds7z returns the first member archive/zip cannot decode, or nil when
// the native path can read the whole archive.
func zipNeeds7z(files []*zip.File) *zip.File {
	for _, f := range files {
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return f
		}
	}
	return nil
}

// zipFallbackTool reports that 7z is available for a zip whose member f
// needs it, or an ErrSevenZipMissing error naming the member's method and
// the remedy.
func zipFallbackTool(f *zip.File) error {
	if _, err := exec.LookPath("7z"); err != nil {
		return fmt.Errorf("%w: the zip member %s uses %s compression, which lmm reads only through the 7z command: install 7-Zip (the p7zip or 7zip package on most distributions) to extract it",
			ErrSevenZipMissing, f.Name, zipMethodName(f.Method))
	}
	return nil
}

// list7zZipMembers lists a zip whose member f needs 7z, through `7z l -slt`,
// refusing a link member (list7zArchive), and returns the members and
// their declared total size.
func list7zZipMembers(ctx context.Context, archivePath string, f *zip.File) ([]archiveMember, uint64, error) {
	if err := zipFallbackTool(f); err != nil {
		return nil, 0, err
	}
	return list7zArchive(ctx, archivePath, "zip")
}

// extractZipWith7z is the #656 fallback: extract a zip archive/zip cannot
// decode (member f is the first such) through the system 7z, by the route
// every 7z extraction takes (extractThrough7z).
func (e *Extractor) extractZipWith7z(ctx context.Context, archivePath, destDir string, f *zip.File) error {
	members, declared, err := list7zZipMembers(ctx, archivePath, f)
	if err != nil {
		return err
	}
	return e.extractThrough7z(ctx, archivePath, destDir, "zip", members, declared)
}

// zipDeclaredSize is the total uncompressed size a zip's central directory
// declares for its members, saturating rather than overflowing.
func zipDeclaredSize(files []*zip.File) uint64 {
	var total uint64
	for _, f := range files {
		total = addSaturating(total, f.UncompressedSize64)
	}
	return total
}
