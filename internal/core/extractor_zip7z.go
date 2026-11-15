package core

// #656: archive/zip decodes only Store and Deflate. Mod authors also ship
// zips compressed with BZIP2, LZMA, Deflate64, Zstandard or PPMd, which the
// native path cannot open ("zip: unsupported compression algorithm"). Such a
// zip goes to the system 7z instead, which lmm already needs for .7z and
// .rar - the whole archive, so one archive is never half native and half 7z.
//
// The fallback keeps every protection the native path gives each member:
//   - the archive's table of contents is read first (`7z l -slt`) and every
//     member is judged before anything is written: a link is refused, and a
//     name that escapes the destination or claims lmm's reserved cache
//     namespace is refused (sanitizePath, the native path's own check);
//   - 7z extracts into a private directory inside destDir, never into
//     destDir itself, so nothing reaches destDir unchecked;
//   - that tree is walked without following links: anything but a regular
//     file or a directory, or a file with more than one link, is refused,
//     and each file is moved into place through sanitizePath again;
//   - extract7zTimeout bounds both 7z runs, as it does extract7z.
//
// The plan-time listing (listArchiveMembers) takes the same route, so a plan
// names exactly the members the ingest will write, and a machine without 7z
// fails at plan time with the same message the ingest would give.

import (
	"archive/zip"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
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
// refusing a link member: the fallback extracts no links, so a listing that
// promised one would describe an import that cannot happen.
func list7zZipMembers(ctx context.Context, archivePath string, f *zip.File) ([]archiveMember, error) {
	if err := zipFallbackTool(f); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, extract7zTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "7z", "l", "-slt", "--", archivePath).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("7z listing timed out after %v", extract7zTimeout)
		}
		return nil, fmt.Errorf("7z listing failed: %w\nOutput: %s", err, string(output))
	}
	members := parse7zListing(string(output))
	for _, m := range members {
		if m.Link {
			return nil, fmt.Errorf("the zip member %s is a link, which lmm does not extract", m.Path)
		}
	}
	return members, nil
}

// extractZipWith7z is the #656 fallback: extract a zip archive/zip cannot
// decode (member f is the first such) through the system 7z. See the file
// comment for the protections it keeps.
func (e *Extractor) extractZipWith7z(ctx context.Context, archivePath, destDir string, f *zip.File) error {
	members, err := list7zZipMembers(ctx, archivePath, f)
	if err != nil {
		return err
	}
	for _, m := range members {
		if _, err := e.sanitizePath(destDir, m.Path); err != nil {
			return err
		}
	}

	// Inside destDir, so the moves below are renames on one filesystem, and
	// under the reserved prefix, which no member may use: it cannot collide
	// with anything the archive holds.
	tmp, err := os.MkdirTemp(destDir, cache.ReservedPrefix+"zip7z-")
	if err != nil {
		return fmt.Errorf("creating extraction directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	ctx, cancel := context.WithTimeout(ctx, extract7zTimeout)
	defer cancel()

	output, err := exec.CommandContext(ctx, "7z", "x", "-y", "-o"+tmp, "--", archivePath).CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("7z extraction timed out after %v", extract7zTimeout)
		}
		return fmt.Errorf("7z extraction failed: %w\nOutput: %s", err, string(output))
	}

	if err := checkExtractedTree(tmp); err != nil {
		return err
	}
	return e.moveExtractedTree(tmp, destDir)
}

// checkExtractedTree refuses, without following any link, anything 7z wrote
// under dir that is not a regular file or a directory, and any file with
// more than one link. It runs before anything is moved, so a refused archive
// leaves destDir as it was.
func checkExtractedTree(dir string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("the zip member %s is a link or special file, which lmm does not extract", filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
			return fmt.Errorf("the zip member %s is a hard link, which lmm does not extract", filepath.ToSlash(rel))
		}
		return nil
	})
}

// moveExtractedTree moves every file and directory under src into destDir,
// each through sanitizePath, replacing a file already there as the native
// path's truncating write does. Directories are made 0755, as the native
// path makes them.
func (e *Extractor) moveExtractedTree(src, destDir string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == src {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		dest, err := e.sanitizePath(destDir, rel)
		if err != nil {
			return err
		}
		if d.IsDir() {
			// 0755 as the native path makes every directory, here too: a
			// directory the archive recorded read-only must still let its
			// files be moved out, and the private tree be removed.
			if err := os.Chmod(path, 0o755); err != nil {
				return err
			}
			return os.MkdirAll(dest, 0o755)
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return fmt.Errorf("creating directory for %s: %w", rel, err)
		}
		if err := os.Rename(path, dest); err != nil {
			return fmt.Errorf("writing file %s: %w", dest, err)
		}
		return nil
	})
}

// isLinkAttributes reports whether a `7z l -slt` Attributes value marks a
// symbolic link: its Unix mode word (e.g. "lrwxrwxrwx") starts with "l".
func isLinkAttributes(attrs string) bool {
	for _, word := range strings.Fields(attrs) {
		if len(word) == 10 && word[0] == 'l' {
			return true
		}
	}
	return false
}
