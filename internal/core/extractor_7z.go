package core

// Every archive the system 7z extracts - a .7z, a .rar (#658), and a zip
// archive/zip cannot decode (#656) - goes through one pattern, which keeps
// the protections the native zip path gives each member:
//   - the archive's table of contents is read first (`7z l -slt`) and every
//     member is judged before anything is written: a link is refused, and a
//     name that escapes the destination or claims lmm's reserved cache
//     namespace is refused (sanitizePath, the native path's own check);
//   - the members' declared total size is checked against the destination's
//     free space (checkFreeSpace), with no fixed cap: a mod archive can
//     legitimately be tens of gigabytes;
//   - 7z extracts into a private directory inside destDir, never into
//     destDir itself, so nothing reaches destDir unchecked;
//   - that tree is walked without following links: anything but a regular
//     file or a directory, or a file with more than one link, is refused,
//     and each entry is moved into place through sanitizePath again, never
//     through a link already in destDir;
//   - the private directory is made writable before it is removed, so a
//     no-write mode 7z applied cannot leave it behind;
//   - both 7z runs end on the caller's ctx or on the no-progress watchdog
//     (run7z), never on a fixed time limit.
//
// The nlink check runs on the private directory only: 7z wrote everything
// in it, so a second link to one of its files can only be the archive's.
// destDir itself may hold a reseeded cache entry (prepareStaging), which is
// judged by its own rules (seedFromCache).

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// list7zArchive lists an archive through `7z l -slt`, refusing a link
// member: the extraction keeps no links, so a listing that promised one
// would describe an extraction that cannot happen. label names the format
// in a message ("zip", "7z", "rar"). It returns the members and their
// declared total size. The caller has already checked 7z is on PATH.
func list7zArchive(ctx context.Context, archivePath, label string) ([]archiveMember, uint64, error) {
	stdout, stderr, err := run7z(ctx, archivePath, "listing", "", "l", "-slt", "--", archivePath)
	if err != nil {
		if errors.Is(err, errSevenZipStalled) || ctx.Err() != nil {
			return nil, 0, err
		}
		return nil, 0, fmt.Errorf("7z listing failed: %w\nOutput: %s", err, sevenZipOutput(stdout, stderr))
	}
	output := string(stdout)
	members := parse7zListing(output)
	for _, m := range members {
		if m.Link {
			return nil, 0, fmt.Errorf("the %s member %s is a link, which lmm does not extract", label, m.Path)
		}
	}
	return members, sum7zSizes(output), nil
}

// sum7zSizes is the total of every entry's "Size" in `7z l -slt` output -
// the bytes extraction will write. The archive's own header block, before
// the "----------" separator, is skipped. A size it cannot read counts as
// nothing, and the sum saturates rather than overflows.
func sum7zSizes(out string) uint64 {
	var total uint64
	started := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if !started {
			started = strings.TrimSpace(line) == "----------"
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok || strings.TrimSpace(key) != "Size" {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		total = addSaturating(total, n)
	}
	return total
}

// addSaturating is a+b, or the largest uint64 when that would overflow: an
// archive's declared sizes are untrusted.
func addSaturating(a, b uint64) uint64 {
	if a > ^uint64(0)-b {
		return ^uint64(0)
	}
	return a + b
}

// extractThrough7z extracts archivePath into destDir through the system 7z,
// given its listing (members) and declared total size. label names the
// format in a message. See the file comment for what it guards.
func (e *Extractor) extractThrough7z(ctx context.Context, archivePath, destDir, label string, members []archiveMember, declared uint64) error {
	for _, m := range members {
		if _, err := e.sanitizePath(destDir, m.Path); err != nil {
			return err
		}
	}
	if err := checkFreeSpace(archivePath, destDir, declared); err != nil {
		return err
	}

	// Inside destDir, so the moves below are renames on one filesystem, and
	// under the reserved prefix, which no member may use: it cannot collide
	// with anything the archive holds.
	tmp, err := os.MkdirTemp(destDir, cache.ReservedPrefix+"7z-")
	if err != nil {
		return fmt.Errorf("creating extraction directory: %w", err)
	}
	defer removePrivateTree(tmp)

	stdout, stderr, err := run7z(ctx, archivePath, "extraction", tmp, "x", "-y", "-o"+tmp, "--", archivePath)
	if err != nil {
		if errors.Is(err, errSevenZipStalled) || ctx.Err() != nil {
			return err
		}
		return fmt.Errorf("7z extraction failed: %w\nOutput: %s", err, sevenZipOutput(stdout, stderr))
	}

	if err := checkExtractedTree(tmp, label); err != nil {
		return err
	}
	return e.moveExtractedTree(tmp, destDir)
}

// removePrivateTree removes a private extraction directory, first making
// every directory in it writable: 7z applies the modes an archive records,
// and a no-write directory would otherwise keep its entries, and the tree,
// on disk. Best-effort, as a cleanup is.
func removePrivateTree(dir string) {
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

// checkExtractedTree refuses, without following any link, anything 7z wrote
// under dir that is not a regular file or a directory, and any file with
// more than one link. It runs before anything is moved, so a refused archive
// leaves destDir as it was. label names the format in a message.
func checkExtractedTree(dir, label string) error {
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
			return fmt.Errorf("the %s member %s is a link or special file, which lmm does not extract", label, filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		// More than one link, not "not one": a FUSE filesystem may report
		// zero for a file it does not count links on.
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
			return fmt.Errorf("the %s member %s is a hard link, which lmm does not extract", label, filepath.ToSlash(rel))
		}
		return nil
	})
}

// moveExtractedTree moves every file and directory under src into destDir,
// each through sanitizePath, replacing a file already there as the native
// path's truncating write does. Directories are made 0755, as the native
// path makes them. A directory whose destination is already a link is
// refused: moving into it would write through the link.
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
			if err := refuseLinkAt(dest); err != nil {
				return err
			}
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

// refuseLinkAt fails when path exists and is a symbolic link: a directory
// is about to be made or filled there, and doing that through a link would
// write wherever it points. A file's own rename needs no check, since
// rename replaces a link rather than following it, and every directory
// above it was checked on the way down.
func refuseLinkAt(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to write into %s: it is a link", path)
	}
	return nil
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

// hasUnixMode reports whether a `7z l -slt` Attributes value carries a
// Unix mode word (e.g. "-rw-r--r--", "drwxr-xr-x", "lrwxrwxrwx").
func hasUnixMode(attrs string) bool {
	for _, word := range strings.Fields(attrs) {
		if len(word) == 10 && strings.ContainsRune("-dlcbps", rune(word[0])) && strings.Trim(word[1:], "rwxsStT-") == "" {
			return true
		}
	}
	return false
}

// errInsufficientSpace marks checkFreeSpace's refusal.
var errInsufficientSpace = errors.New("not enough free space")

// availableSpace reports the bytes free to an unprivileged writer on dir's
// filesystem, and whether it could tell. A seam: tests cannot fill a disk.
var availableSpace = func(dir string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	free := statfsAvailable(st)
	return free, free > 0 || st.Bavail == 0
}

// statfsAvailable is a statfs result's space free to an unprivileged
// writer, counted as df counts it: available blocks times the fragment
// size, or the block size when the filesystem reports no fragment size.
// Zero when it reports neither.
func statfsAvailable(st syscall.Statfs_t) uint64 {
	unit := st.Frsize
	if unit <= 0 {
		unit = st.Bsize
	}
	if unit <= 0 {
		return 0
	}
	return st.Bavail * uint64(unit)
}

// sameFilesystem reports whether the existing paths a and b sit on one
// filesystem, and true when either cannot be read: the caller then skips
// a check it cannot make. A seam: tests cannot mount a second filesystem.
var sameFilesystem = func(a, b string) bool {
	var sa, sb syscall.Stat_t
	if syscall.Stat(a, &sa) != nil || syscall.Stat(b, &sb) != nil {
		return true
	}
	return sa.Dev == sb.Dev
}

// existingAncestor is path, or the nearest directory above it that exists.
func existingAncestor(path string) string {
	for {
		if _, err := os.Stat(path); err == nil {
			return path
		}
		parent := filepath.Dir(path)
		if parent == path {
			return path
		}
		path = parent
	}
}

// preflightOtherFilesystem checks dest's filesystem has room for what
// archivePath unpacks to, when dest is not on stagingDir's: an extraction
// into stagingDir checks that one itself (checkFreeSpace), and its later
// copy into dest would otherwise find out only part way through. An
// archive whose size cannot be read is not refused here; the extraction
// reports what is wrong with it.
func (e *Extractor) preflightOtherFilesystem(ctx context.Context, archivePath, stagingDir, dest string) error {
	destDir := existingAncestor(dest)
	if sameFilesystem(stagingDir, destDir) {
		return nil
	}
	declared, err := e.declaredSize(ctx, archivePath)
	if err != nil {
		return nil
	}
	return checkFreeSpace(archivePath, destDir, declared)
}

// declaredSize is the total size archivePath's members declare: a zip's
// central directory, or a 7z listing for anything 7z reads (a .7z, a .rar,
// a zip archive/zip cannot decode).
func (e *Extractor) declaredSize(ctx context.Context, archivePath string) (uint64, error) {
	format := e.detectFormatFromPath(archivePath)
	switch format {
	case "zip":
		r, err := zip.OpenReader(archivePath)
		if err != nil {
			return 0, err
		}
		defer func() { _ = r.Close() }()
		if f := zipNeeds7z(r.File); f != nil {
			_, total, err := list7zZipMembers(ctx, archivePath, f)
			return total, err
		}
		return zipDeclaredSize(r.File), nil
	case "7z", "rar":
		if _, err := exec.LookPath("7z"); err != nil {
			return 0, err
		}
		_, total, err := list7zArchive(ctx, archivePath, format)
		return total, err
	}
	return 0, fmt.Errorf("unsupported archive format: %s", filepath.Ext(archivePath))
}

// checkFreeSpace refuses an extraction whose declared size is more than
// destDir's filesystem has free, before anything is written, so a large
// archive fails with a clear message rather than part-way through with
// ENOSPC. There is no fixed cap: only the space actually free decides.
// When the free space cannot be read, nothing is refused.
func checkFreeSpace(archivePath, destDir string, declared uint64) error {
	free, ok := availableSpace(destDir)
	if !ok || declared <= free {
		return nil
	}
	return fmt.Errorf("%w to extract %s: it unpacks to %s, and %s has %s free",
		errInsufficientSpace, filepath.Base(archivePath), formatBytes(declared), destDir, formatBytes(free))
}

// formatBytes renders a byte count for a message, in binary units.
func formatBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 5; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
