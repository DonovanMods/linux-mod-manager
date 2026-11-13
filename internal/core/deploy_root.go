// Package core: deploy_root.go is #629's second deploy root - mod content a
// game loads from its install directory rather than from its mod_path (a
// script extender's loader and DLLs, an ENB's d3d11.dll and enbseries/,
// which the bethesda adapter routes there through adapter.RootRouter).
//
// A deployed file is named everywhere in core by its DEPLOYED PATH (see
// internal/domain/deploypath.go): a mod_path file by its path relative to
// the mod_path, exactly as before #629, and an install_path file by
// domain.InstallPathKey of its path relative to the install_path. So the
// maps, sets, records and comparisons every flow keeps by path need no
// second key, and deployed_files stores the root in a column of its own.
// What every flow DOES have to ask is where a deployed path lives -
// deployTarget - and which cache member it is - cacheMember. A key that
// reaches a system call without being resolved holds a NUL byte the kernel
// refuses, so a missed site fails loudly instead of touching the mod_path
// file of the same name.
//
// The install_path holds the game's own files (its executables and DLLs),
// so a game-root file is held to the rules #630 holds an overridden copy to:
// it is written only beneath the install_path, through an openat walk that
// refuses a symlink on the way (rootLinker), and a stock file it replaces is
// captured first, under the originals store's install_path root, and put
// back when lmm's file goes.
package core

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/linker"

	"golang.org/x/sys/unix"
)

// routesToGameRoot reports whether game has a game root distinct from its
// mod_path for an adapter's RootInstallPath answer to mean anything: an
// install_path, and a mod_path that is not that same directory. A game
// whose mod_path is its install_path deploys every file into one
// directory, so a root route there changes nothing and the file keeps its
// plain mod_path name rather than gaining a second one for the same place.
func routesToGameRoot(game *domain.Game) bool {
	if game == nil || game.InstallPath == "" {
		return false
	}
	return game.ModPath == "" || !samePath(game.ModPath, game.InstallPath)
}

// deployedPaths maps one mod's cache listing (cache-entry-relative members)
// to the deployed paths those members take: the adapter's RouteRoots
// answer, asked once for the whole listing because some of its rules are
// about the mod as a whole (an ENB preset recognised by one file).
func deployedPaths(a adapter.GameAdapter, game *domain.Game, files []string) []string {
	out, _ := deployedPathsForType(a, game, "", files)
	return out
}

// pathIndex maps a cache listing's members to their deployed paths.
type pathIndex map[string]string

// deployedPathIndex is deployedPaths as a member -> deployed path index.
func deployedPathIndex(a adapter.GameAdapter, game *domain.Game, files []string) pathIndex {
	keys := deployedPaths(a, game, files)
	idx := make(pathIndex, len(files))
	for i, f := range files {
		idx[f] = keys[i]
	}
	return idx
}

// of is members' deployed paths, in order; a member the index does not
// hold keeps its own name.
func (idx pathIndex) of(members []string) []string {
	out := make([]string, len(members))
	for i, m := range members {
		if k, ok := idx[m]; ok {
			out[i] = k
		} else {
			out[i] = m
		}
	}
	return out
}

// ModTypeUnsupportedError refuses a mod whose kind was STATED - a Vortex mod
// type, as a NexusMods collection records it - with a type the game's
// adapter does not know (adapter.RootForModType reports false). Deploying it
// under the mod_path anyway is exactly the "imports cleanly, does nothing"
// failure the collection gate exists for, so it is an error, never a
// default.
type ModTypeUnsupportedError struct {
	GameID  string
	Adapter string
	ModType string
}

// Error names the type and the adapter that does not know it.
func (e *ModTypeUnsupportedError) Error() string {
	return fmt.Sprintf("the mod's type %q is not one game %s's adapter (%s) knows where to deploy", e.ModType, e.GameID, e.Adapter)
}

// deployedPathsForType is deployedPaths for a mod whose type may have been
// stated: "" asks the adapter file by file (RouteRoots); any other type
// routes the WHOLE mod to the root RootForModType names for it, and a type
// it does not know is a *ModTypeUnsupportedError. Nothing persists a mod
// type yet - the collections converter (#607) is the first caller that
// will have one - so every flow today passes "".
func deployedPathsForType(a adapter.GameAdapter, game *domain.Game, modType string, files []string) ([]string, error) {
	out := make([]string, len(files))
	copy(out, files)
	var roots []adapter.DeployRoot
	if modType == "" {
		slash := make([]string, len(files))
		for i, f := range files {
			slash[i] = filepath.ToSlash(f)
		}
		roots = adapter.RouteRoots(a, game, slash)
	} else {
		root, ok := adapter.RootForModType(a, game, modType)
		if !ok {
			id := ""
			if a != nil {
				id = a.ID()
			}
			gameID := ""
			if game != nil {
				gameID = game.ID
			}
			return nil, &ModTypeUnsupportedError{GameID: gameID, Adapter: id, ModType: modType}
		}
		roots = make([]adapter.DeployRoot, len(files))
		for i := range roots {
			roots[i] = root
		}
	}
	if !routesToGameRoot(game) {
		return out, nil
	}
	for i, r := range roots {
		if r == adapter.RootInstallPath {
			out[i] = gameRootKey(game, files[i])
		}
	}
	return out, nil
}

// gameRootKey is the deployed path of rel, a path relative to game's
// install_path. One that lands inside the mod_path - a whole-mod root
// route's Data/ folder, for a game whose mod_path is install_path/Data -
// is that mod_path file, and keeps its mod_path name: one place on disk
// has one deployed path, so conflicts, records and removals see it once.
func gameRootKey(game *domain.Game, rel string) string {
	if game.ModPath != "" {
		if inside, ok := relUnder(game.ModPath, filepath.Join(game.InstallPath, rel)); ok {
			return filepath.FromSlash(inside)
		}
	}
	return domain.InstallPathKey(rel)
}

// cacheMember is the cache-entry-relative member a deployed path deploys:
// the path relative to its root, which is how the cache entry names it.
func cacheMember(p string) string {
	rel, _ := domain.SplitDeployedPath(p)
	return rel
}

// isGameRoot reports whether p is a deployed path under the game root.
func isGameRoot(p string) bool { return domain.IsInstallPathKey(p) }

// deployRootDir is the directory p is relative to in game: its install_path
// for a game-root path, its mod_path otherwise.
func deployRootDir(game *domain.Game, p string) string {
	if isGameRoot(p) {
		return game.InstallPath
	}
	return game.ModPath
}

// deployTarget is where p lives in game: its root directory joined with its
// relative path.
func deployTarget(game *domain.Game, p string) string {
	rel, root := domain.SplitDeployedPath(p)
	if root {
		return filepath.Join(game.InstallPath, filepath.FromSlash(rel))
	}
	return filepath.Join(game.ModPath, rel)
}

// displayPath is p as a frontend shows it (domain.DisplayDeployedPath), in
// slash form.
func displayPath(p string) string { return domain.DisplayDeployedPath(filepath.ToSlash(p)) }

// displayPaths is displayPath of each of ps, in order; nil for nil.
func displayPaths(ps []string) []string {
	if ps == nil {
		return nil
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = displayPath(p)
	}
	return out
}

// originalRootOf is the originals-store root and relative path p is
// captured and released under: install_path for a game-root file - the
// root a profile override's originals already use, so a stock file both
// replace is one original - and mod_path otherwise.
func originalRootOf(p string) (OriginalRoot, string) {
	rel, root := domain.SplitDeployedPath(p)
	if root {
		return OriginalRootInstallPath, filepath.ToSlash(rel)
	}
	return OriginalRootModPath, filepath.ToSlash(p)
}

// linkerFor is lnk for a mod_path file, and lnk held to the install_path
// (rootLinker) for a game-root one.
func linkerFor(lnk linker.Linker, game *domain.Game, p string) linker.Linker {
	if isGameRoot(p) {
		return rootLinker{inner: lnk, root: game.InstallPath}
	}
	return lnk
}

// cleanupRemoved prunes the directories that became empty because lmm
// removed the deployed paths in removed (linker.CleanupEmptyDirs), each
// under its own root: game-root paths under the install_path, the rest
// under the mod_path.
func cleanupRemoved(game *domain.Game, removed []string) {
	var mod, root []string
	for _, p := range removed {
		if rel, ok := domain.SplitDeployedPath(p); ok {
			root = append(root, rel)
		} else {
			mod = append(mod, p)
		}
	}
	if len(mod) > 0 {
		linker.CleanupEmptyDirs(game.ModPath, mod)
	}
	if len(root) > 0 && game.InstallPath != "" {
		linker.CleanupEmptyDirs(game.InstallPath, root)
	}
}

// errOutsideInstallPath refuses a game-root file whose destination is not
// beneath the game's install_path.
var errOutsideInstallPath = errors.New("it is not beneath the game's install_path")

// rootLinker is the game's linker held to its install_path (#629): every
// write and removal opens the destination's directory beneath the
// install_path one component at a time, refusing a symlink on the way
// (openDirBeneath, #630's walk), and acts on the entry through that handle.
// So a directory under the game root that is - or is swapped for - a link
// to somewhere else can never carry a mod's file, or lmm's removal of one,
// out of the install_path.
type rootLinker struct {
	inner linker.Linker
	root  string
}

var _ linker.Linker = rootLinker{}

// open resolves dst to its directory, opened beneath the install_path, and
// its name there. create makes missing directories.
func (r rootLinker) open(dst string, create bool) (*os.File, string, error) {
	if r.root == "" {
		return nil, "", fmt.Errorf("%s: the game has no install_path", dst)
	}
	absRoot, err := filepath.Abs(r.root)
	if err != nil {
		return nil, "", err
	}
	absDst, err := filepath.Abs(dst)
	if err != nil {
		return nil, "", err
	}
	rel, ok := relUnder(absRoot, absDst)

	if !ok {
		return nil, "", fmt.Errorf("refusing %s: %w", dst, errOutsideInstallPath)
	}
	realRoot, err := resolveExisting(absRoot)
	if err != nil {
		return nil, "", err
	}
	realDir, err := resolveExisting(filepath.Join(absRoot, filepath.Dir(filepath.FromSlash(rel))))
	if err != nil {
		return nil, "", err
	}
	dirRel, ok := dirUnder(realRoot, realDir)
	if !ok {
		return nil, "", fmt.Errorf("refusing %s: it leaves the game's install_path (%s) through a symlink", dst, r.root)
	}
	dir, err := openDirBeneath(realRoot, dirRel, create)
	if err != nil {
		return nil, "", err
	}
	return dir, filepath.Base(filepath.FromSlash(rel)), nil
}

// Deploy places src at dst the way the inner linker would - a symlink, a
// hard link or a copy - replacing a regular file or link there (the caller
// has judged it lmm's to replace, or captured it) and refusing anything
// else.
func (r rootLinker) Deploy(src, dst string) error {
	dir, name, err := r.open(dst, true)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	st, exists, err := lstatAt(dir, name)
	if err != nil {
		return err
	}
	if exists && !isRegularOrLink(st) {
		return fmt.Errorf("deploying %s: %w", dst, errNotRegularTarget)
	}
	dirfd := int(dir.Fd())
	switch r.inner.Method() {
	case domain.LinkCopy:
		return copyAt(dir, name, src)
	case domain.LinkHardlink:
		if exists {
			if err := unix.Unlinkat(dirfd, name, 0); err != nil {
				return &os.PathError{Op: "remove", Path: dst, Err: err}
			}
		}
		if err := unix.Linkat(unix.AT_FDCWD, src, dirfd, name, 0); err != nil {
			return fmt.Errorf("creating hardlink: %w", &os.PathError{Op: "link", Path: dst, Err: err})
		}
		return nil
	default:
		if exists {
			if err := unix.Unlinkat(dirfd, name, 0); err != nil {
				return &os.PathError{Op: "remove", Path: dst, Err: err}
			}
		}
		if err := unix.Symlinkat(src, dirfd, name); err != nil {
			return fmt.Errorf("creating symlink: %w", &os.PathError{Op: "symlink", Path: dst, Err: err})
		}
		return nil
	}
}

// Undeploy removes lmm's file at dst as the inner linker would: under a
// symlink deployment only a link, under the others a regular file or a
// link. A path that is not there - or whose directory is not - is already
// undeployed.
func (r rootLinker) Undeploy(dst string) error {
	return r.remove(dst, r.inner.Method() != domain.LinkSymlink)
}

// removeOwnCopy removes the regular file at dst that a judge found lmm's own
// (#630's overridden copy, under any method).
func (r rootLinker) removeOwnCopy(dst string) error { return r.remove(dst, true) }

func (r rootLinker) remove(dst string, regularToo bool) error {
	dir, name, err := r.open(dst, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = dir.Close() }()
	st, exists, err := lstatAt(dir, name)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	switch {
	case isLink(st):
	case isRegular(st) && regularToo:
	case isRegular(st):
		return fmt.Errorf("not a symlink: %s", dst)
	default:
		return fmt.Errorf("removing %s: %w", dst, errNotRegularTarget)
	}
	if err := unix.Unlinkat(int(dir.Fd()), name, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("removing file: %w", &os.PathError{Op: "remove", Path: dst, Err: err})
	}
	return nil
}

// IsDeployed reports what the inner linker does: it only reads.
func (r rootLinker) IsDeployed(dst string) (bool, error) { return r.inner.IsDeployed(dst) }

// Method is the inner linker's.
func (r rootLinker) Method() domain.LinkMethod { return r.inner.Method() }

// copyAt copies src to name beneath dir as a new regular file: written to a
// temporary file beside it, then renamed over whatever is there, so a link
// there is replaced, never written through (CopyLinker's rule).
func copyAt(dir *os.File, name, src string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source: %w", err)
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}
	dirfd := int(dir.Fd())
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := "." + name + ".lmm-" + hex.EncodeToString(suffix[:])
	fd, err := syscall.Openat(dirfd, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return fmt.Errorf("creating destination: %w", &os.PathError{Op: "create", Path: filepath.Join(dir.Name(), tmp), Err: err})
	}
	out := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), tmp))
	defer func() {
		if err != nil {
			_ = syscall.Unlinkat(dirfd, tmp)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copying file: %w", err)
	}
	if err := out.Chmod(info.Mode().Perm()); err != nil {
		_ = out.Close()
		return fmt.Errorf("setting destination mode: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("closing destination: %w", err)
	}
	if err := syscall.Renameat(dirfd, tmp, dirfd, name); err != nil {
		return fmt.Errorf("creating destination: %w", &os.PathError{Op: "rename", Path: filepath.Join(dir.Name(), name), Err: err})
	}
	return nil
}
