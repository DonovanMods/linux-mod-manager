// Package core: overridden.go is #630's overridden mod file - a file a
// deployed mod ships that the acting profile's overrides replace (a
// collection's tweak to a mod's .ini, say).
//
// An overridden mod file is deployed as a regular file of its own holding
// the override, whatever the link method: for symlink and hardlink the link
// into lmm's shared cache is broken, never written through, so the cache
// entry every profile shares stays pristine; for copy the deployed copy
// simply holds the override.
//
// Which files are overridden is DERIVED, never stored: it is the profile's
// override keys resolved against a deploy root (overriddenUnder), asked
// fresh at deploy, verify and removal time. What lmm does persist is what it
// already persists for every file it writes (#466): the deployed_files
// record of the path carries the fingerprint of the copy lmm wrote, so the
// ordinary deployedJudge recognises that copy as lmm's own - a removal
// removes it, a deploy replaces it, and once the override is gone the next
// deploy, switch or apply puts the plain link back.
//
// The concept is keyed by a deploy root so a second root can join it: today
// every mod file deploys under game.ModPath, and #629's game-root deploy
// asks overriddenUnder about game.InstallPath the same way.
package core

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"

	"golang.org/x/sys/unix"
)

// overriddenFiles is the files under one deploy root that a profile's
// overrides replace: each path relative to the root, in slash form, mapped
// to the content its override writes. The zero value overrides nothing.
type overriddenFiles map[string][]byte

// content is the override for rel (relative to the root, either slash
// form), and whether there is one.
func (o overriddenFiles) content(rel string) ([]byte, bool) {
	if len(o) == 0 {
		return nil, false
	}
	c, ok := o[filepath.ToSlash(filepath.Clean(rel))]
	return c, ok
}

// overriddenUnder resolves profile's override keys - each relative to
// game.InstallPath - against root, a deploy root of game, keeping the ones
// that land under it. An override key a deploy would refuse (absolute,
// escaping) lands nowhere. The match is by path, tried as written and then
// with both directories resolved through their symlinks, so a mod_path
// reached through a link still matches.
func overriddenUnder(game *domain.Game, profile *domain.Profile, root string) overriddenFiles {
	if profile == nil || len(profile.Overrides) == 0 || game.InstallPath == "" || root == "" {
		return nil
	}
	base, err := filepath.Abs(game.InstallPath)
	if err != nil {
		return nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil
	}
	realBase, realRoot := base, absRoot
	if r, err := resolveExisting(base); err == nil {
		realBase = r
	}
	if r, err := resolveExisting(absRoot); err == nil {
		realRoot = r
	}
	out := overriddenFiles{}
	for key, content := range profile.Overrides {
		dest, _, err := overrideDest(base, key)
		if err != nil {
			continue
		}
		rel, ok := relUnder(absRoot, dest)
		if !ok {
			if realDest, _, err := overrideDest(realBase, key); err == nil {
				rel, ok = relUnder(realRoot, realDest)
			}
		}
		if ok {
			out[rel] = content
		}
	}
	return out
}

// relUnder is path relative to root in slash form, when path is strictly
// under root.
func relUnder(root, path string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", false
	}
	return filepath.ToSlash(rel), true
}

// overriddenModFiles is the files under game's mod_path that profileName's
// overrides replace. A profile that cannot be read overrides nothing: the
// flow that reads it for its own purposes reports the failure.
func (s *Service) overriddenModFiles(game *domain.Game, profileName string) overriddenFiles {
	if profileName == "" {
		return nil
	}
	profile, err := config.LoadProfile(s.configDir, game.ID, profileName)
	if err != nil {
		return nil
	}
	return overriddenUnder(game, profile, game.ModPath)
}

// errNotRegularTarget refuses an override or an overridden copy whose
// destination is a directory, a FIFO, a device or a socket: lmm writes
// regular files only, and never opens one of those (a FIFO with no reader
// would block the deploy).
var errNotRegularTarget = errors.New("it is not a regular file (a directory, a pipe or a device), and lmm only ever writes regular files")

// writeOverriddenCopy deploys content at rel under root as a regular file
// of its own (#630): whatever link or file is there - lmm's symlink into
// the cache, its hard link, its copy - is replaced, never written through.
// The caller has judged what is there lmm's to replace.
func writeOverriddenCopy(root, rel string, content []byte) error {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	// The deploy root is made if it is not there yet, as a linker makes
	// it on a game's first deploy; what is under it is walked below.
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return err
	}
	realRoot, err := resolveExisting(absRoot)
	if err != nil {
		return err
	}
	realDir, err := resolveExisting(filepath.Join(absRoot, filepath.Dir(filepath.FromSlash(rel))))
	if err != nil {
		return err
	}
	dirRel, ok := dirUnder(realRoot, realDir)
	if !ok {
		return fmt.Errorf("%s leaves %s through a symlink", rel, root)
	}
	dir, err := openDirBeneath(realRoot, dirRel, true)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	name := filepath.Base(filepath.FromSlash(rel))
	st, exists, err := lstatAt(dir, name)
	if err != nil {
		return err
	}
	if exists && !isRegularOrLink(st) {
		return errNotRegularTarget
	}
	if exists && isRegular(st) && st.Nlink == 1 && sameContentAt(dir, name, content) {
		return nil
	}
	return replaceAt(dir, name, content, 0o644)
}

// dirUnder is dir relative to root ("" for root itself), when dir is root
// or under it.
func dirUnder(root, dir string) (string, bool) {
	if filepath.Clean(root) == filepath.Clean(dir) {
		return "", true
	}
	rel, ok := relUnder(root, dir)
	return filepath.FromSlash(rel), ok
}

// openDirBeneath opens rel, a directory path relative to base that the
// caller resolved through its symlinks, one component at a time from an
// open handle on base - an openat walk. Every step refuses a symlink
// (O_NOFOLLOW), so a directory on the path swapped for a link after the
// caller resolved it cannot carry what follows anywhere else (#630, the
// parent-directory race). With create, a missing component is made.
//
// The standard library's os.Root does the same walk but follows a link
// that stays inside the root and refuses any absolute one; the caller here
// has already resolved the path, so a link anywhere on it now is a race to
// refuse, not a layout to follow.
func openDirBeneath(base, rel string, create bool) (*os.File, error) {
	fd, err := syscall.Open(base, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: base, Err: err}
	}
	path := base
	for _, comp := range strings.Split(filepath.ToSlash(rel), "/") {
		if comp == "" || comp == "." {
			continue
		}
		path = filepath.Join(path, comp)
		next, err := openDirAt(fd, comp)
		if errors.Is(err, syscall.ENOENT) && create {
			if merr := syscall.Mkdirat(fd, comp, 0o755); merr != nil && !errors.Is(merr, syscall.EEXIST) {
				_ = syscall.Close(fd)
				return nil, &os.PathError{Op: "mkdir", Path: path, Err: merr}
			}
			next, err = openDirAt(fd, comp)
		}
		_ = syscall.Close(fd)
		if err != nil {
			if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
				return nil, fmt.Errorf("%s is no longer a directory (it was replaced, perhaps by a link), so nothing was written beneath it", path)
			}
			return nil, &os.PathError{Op: "open", Path: path, Err: err}
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

// openDirAt opens the directory name beneath dirfd without following a
// link.
func openDirAt(dirfd int, name string) (int, error) {
	for {
		fd, err := syscall.Openat(dirfd, name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		return fd, err
	}
}

// lstatAt is lstat(2) of name beneath dir, and whether anything is there.
// It opens nothing, so a FIFO there is seen for what it is without anyone
// waiting on it. golang.org/x/sys/unix is already in the lmm binary (by
// way of the terminal libraries); the standard library's syscall package
// exports no fstatat on amd64.
func lstatAt(dir *os.File, name string) (unix.Stat_t, bool, error) {
	var st unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	switch {
	case err == nil:
		return st, true, nil
	case errors.Is(err, unix.ENOENT):
		return st, false, nil
	default:
		return st, false, &os.PathError{Op: "lstat", Path: filepath.Join(dir.Name(), name), Err: err}
	}
}

// isRegular reports whether st describes a regular file.
func isRegular(st unix.Stat_t) bool { return st.Mode&unix.S_IFMT == unix.S_IFREG }

// isLink reports whether st describes a symbolic link.
func isLink(st unix.Stat_t) bool { return st.Mode&unix.S_IFMT == unix.S_IFLNK }

// isRegularOrLink reports whether st describes a regular file or a
// symbolic link: the only things lmm replaces.
func isRegularOrLink(st unix.Stat_t) bool { return isRegular(st) || isLink(st) }

// sameContentAt reports whether the regular file name beneath dir holds
// exactly content. It opens without following a link and without blocking,
// and answers false whenever it cannot tell.
func sameContentAt(dir *os.File, name string, content []byte) bool {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name))
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != int64(len(content)) {
		return false
	}
	got, err := io.ReadAll(io.LimitReader(f, int64(len(content))+1))
	return err == nil && bytes.Equal(got, content)
}

// replaceAt makes name beneath dir a new regular file holding content:
// written to a temporary file beside it, then renamed over whatever is
// there. The rename replaces the directory entry itself, so a link there -
// symbolic or hard - is broken rather than written through, and a reader
// never sees a half-written file.
func replaceAt(dir *os.File, name string, content []byte, perm os.FileMode) (err error) {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := "." + name + ".lmm-" + hex.EncodeToString(suffix[:])
	dirfd := int(dir.Fd())
	fd, err := syscall.Openat(dirfd, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return &os.PathError{Op: "create", Path: filepath.Join(dir.Name(), tmp), Err: err}
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), tmp))
	defer func() {
		if err != nil {
			_ = syscall.Unlinkat(dirfd, tmp)
		}
	}()
	if _, werr := f.Write(content); werr != nil {
		_ = f.Close()
		return werr
	}
	if cerr := f.Chmod(perm); cerr != nil {
		_ = f.Close()
		return cerr
	}
	if cerr := f.Close(); cerr != nil {
		return cerr
	}
	if rerr := syscall.Renameat(dirfd, tmp, dirfd, name); rerr != nil {
		return &os.PathError{Op: "rename", Path: filepath.Join(dir.Name(), name), Err: rerr}
	}
	return nil
}

// holdsOverriddenCopy reports whether dst holds the overridden copy of a
// mod file: a regular file of its own (one link, so not the cache's)
// whose content is the override. It never follows a link or blocks.
func holdsOverriddenCopy(dst string, content []byte) bool {
	info, err := os.Lstat(dst)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		return false
	}
	dir, err := os.Open(filepath.Dir(dst))
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	return sameContentAt(dir, filepath.Base(dst), content)
}

// ownCopy reports whether jd finds the regular file at dst lmm's own: under
// a symlink deployment, the overridden copy a profile override left (#630),
// which is no replacement of the user's.
func ownCopy(ctx context.Context, jd deployedJudge, rel, dst string) bool {
	j := jd.judge(ctx, rel, dst)
	return j.verdict == deployedOurs && j.regular
}

// convergeOverriddenFiles brings the mod files profileName deploys in game
// into line with its overrides (#630), for a flow that changes which
// profile is live without redeploying every mod - a profile switch, whose
// mod set may be unchanged, and a profile apply. Each file the profile
// overrides that holds lmm's plain deployment becomes the overridden
// copy; each overridden copy lmm left for another profile's override,
// which this profile does not have, becomes the plain deployment again.
//
// Only lmm's own files are touched (deployedJudge, as every removal asks):
// a file the user changed stays. The restore direction looks only at paths
// some profile of the game overrides, so a profile with no overrides
// anywhere costs a directory listing of profiles and nothing more. What
// could not be done comes back as warnings, one per file.
//
// It acts only for the game's active profile, whose mods the game
// directory holds (#462), and only when the game's adapter allows a deploy
// at all (deployRefusal): a flow that ends with another profile live, or
// with a refused adapter, converges nothing here.
//
// It runs to the end once started, on a context its caller's cancellation
// does not reach: it finishes the on-disk state of a flow that has already
// succeeded, its work is bounded by the profile's few overrides (and one
// lstat per deployed file), and a cancellation halfway would leave some of
// a profile's overridden files converged and others not.
func (s *Service) convergeOverriddenFiles(ctx context.Context, game *domain.Game, profileName string) []string {
	ctx = context.WithoutCancel(ctx)
	if s.db == nil || game.ModPath == "" {
		return nil
	}
	if live, err := s.liveProfile(ctx, game.ID); err != nil || live != profileName {
		return nil
	}
	if err := s.deployRefusal(ctx, game, profileName); err != nil {
		return nil
	}
	mine := s.overriddenModFiles(game, profileName)
	elsewhere := map[string]bool{}
	if names, err := config.ListProfiles(s.configDir, game.ID); err == nil {
		for _, name := range names {
			if name == profileName {
				continue
			}
			for rel := range s.overriddenModFiles(game, name) {
				if _, ok := mine[rel]; !ok {
					elsewhere[rel] = true
				}
			}
		}
	}
	installer, err := s.getInstallerForProfile(ctx, game, profileName)
	if err != nil {
		return []string{fmt.Sprintf("checking the profile's overridden mod files: %v", err)}
	}
	// Under symlink and hardlink a plain deployment is never a regular
	// file of its own, so an overridden copy is found by one lstat per
	// file - including one whose override was deleted from the profile by
	// hand. A copy deployment is a regular file either way, so for copy
	// only the paths another profile overrides are looked at; `lmm deploy`
	// redeploys every file and catches the rest.
	linked := installer.linker.Method() != domain.LinkCopy
	if len(mine) == 0 && len(elsewhere) == 0 && !linked {
		return nil
	}
	rows, err := s.db.ListDeployedFiles(ctx, game.ID, profileName)
	if err != nil {
		return []string{fmt.Sprintf("checking the profile's overridden mod files: %v", err)}
	}
	others, err := s.otherGamesRecording(ctx, game)
	if err != nil {
		return []string{fmt.Sprintf("checking the profile's overridden mod files: %v", err)}
	}
	jd := installer.judgeFor(game, profileName, others, nil)
	var warnings []string
	for _, row := range rows {
		rel := row.RelativePath
		if !underCurrentRoot(game, row.ModPath) || !filepath.IsLocal(filepath.FromSlash(rel)) {
			continue
		}
		dst := filepath.Join(game.ModPath, filepath.FromSlash(rel))
		content, overridden := mine.content(rel)
		if !overridden && !elsewhere[filepath.ToSlash(rel)] && (!linked || !soleRegularFile(dst)) {
			continue
		}
		if overridden && holdsOverriddenCopy(dst, content) {
			continue
		}
		if _, held := heldElsewhere(others, rel, dst); held {
			continue
		}
		j := jd.judge(ctx, rel, dst)
		if j.verdict != deployedOurs {
			continue
		}
		if overridden {
			if err := writeOverriddenCopy(game.ModPath, rel, content); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: could not write the profile's override over the mod's file: %v", rel, err))
				continue
			}
			s.recordOverriddenCopy(ctx, game, profileName, rel, dst)
			continue
		}
		if !j.regular {
			continue // a link: already the plain deployment
		}
		if err := s.restorePlainDeployment(ctx, installer, game, profileName, row, dst); err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: could not put the mod's own file back in place of the override another profile left: %v", rel, err))
		}
	}
	return warnings
}

// restorePlainDeployment replaces the overridden copy at dst - lmm's own,
// left by another profile's override - with the plain deployment of the
// row's mod as profileName has it installed, and records it as a deploy
// would. A hard link that already is the cache's file, or a copy that
// already holds the mod's bytes, is the plain deployment and is left.
func (s *Service) restorePlainDeployment(ctx context.Context, installer *Installer, game *domain.Game, profileName string, row db.DeployedPath, dst string) error {
	mod, err := s.GetInstalledMod(ctx, row.SourceID, row.ModID, game.ID, profileName)
	if err != nil {
		return err
	}
	src := installer.cache.GetFilePath(modCacheKey(game.ID, mod.Mod), filepath.FromSlash(row.RelativePath))
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("the mod's cached copy: %w", err)
	}
	if info, err := os.Lstat(dst); err == nil {
		switch installer.linker.Method() {
		case domain.LinkHardlink:
			if os.SameFile(info, srcInfo) {
				return nil
			}
		case domain.LinkCopy:
			if a, _, err := hashFile(dst); err == nil {
				if b, _, err := hashFile(src); err == nil && a == b {
					return nil
				}
			}
		}
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := installer.linker.Deploy(src, dst); err != nil {
		return err
	}
	return installer.recordDeployed(ctx, game, profileName, row.RelativePath, &mod.Mod, installer.deployedFingerprint(dst))
}

// soleRegularFile reports whether path is a regular file with one link:
// under symlink or hardlink, never the plain deployment.
func soleRegularFile(path string) bool {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Nlink == 1
}
