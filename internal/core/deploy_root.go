// Package core: deploy_root.go is #629's second deploy root - mod content a
// game loads from its install directory rather than from its mod_path (a
// script extender's loader and DLLs, an ENB's d3d11.dll and enbseries/,
// which the bethesda adapter routes there through adapter.RootRouter).
//
// A deployed file is named everywhere in core by a domain.DeployPath: its
// root (the mod_path, as every file was before #629, or the install_path)
// and its path relative to that root. It is the identity every record, map,
// set and plan/apply match keeps a file by, so a game-root "textures/a.dds"
// is never the mod_path file of that name. A deployItem pairs it with the
// cache member the file's bytes come from, which differs from the path
// where a whole-mod game-root route lands inside the mod_path (an ENB
// preset's Data/ folder, gameRootPath). Every flow asks deployTarget where
// a path lives, and frontends are handed the path and its root apart - or,
// in a plain list of strings, DeployPath.String's "<game root>/..." - and
// spell it at their own render step.
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
	"strings"
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

// deployItem is one cache member as the deployed file it becomes (#629):
// path is the file's identity in the game - its root and its path relative
// to that root, what every record, map and comparison keeps it by - and
// member is the cache-entry-relative member its bytes come from. The two
// paths are the same except where a game-root route lands inside the
// mod_path (gameRootPath): an ENB preset's Data/Weather.esp is the mod_path
// file Weather.esp, deployed from the member Data/Weather.esp.
type deployItem struct {
	path   domain.DeployPath
	member string
}

// recordedItem is the item a deployed path known only from its record
// stands for: its member is taken to be its relative path, which is what
// every record but a Data/-folder game-root route's names.
func recordedItem(p domain.DeployPath) deployItem {
	return deployItem{path: p, member: filepath.FromSlash(p.Rel)}
}

// pathsOf is the deployed paths of items, in order.
func pathsOf(items []deployItem) []domain.DeployPath {
	out := make([]domain.DeployPath, len(items))
	for i, it := range items {
		out[i] = it.path
	}
	return out
}

// membersOf is the cache members of items, in order.
func membersOf(items []deployItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.member
	}
	return out
}

// displayStrings is each of ps as its String spells it, for a wire list of
// strings, which has no room for a root beside each path (#629); nil for
// none.
func displayStrings(ps []domain.DeployPath) []string {
	if len(ps) == 0 {
		return nil
	}
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.String()
	}
	return out
}

// deployItems maps one mod's cache listing (cache-entry-relative members)
// to the deployed files those members become: the adapter's RouteRoots
// answer, asked once for the whole listing because some of its rules are
// about the mod as a whole (an ENB preset recognised by one file).
//
// modType is the mod's stated type (domain.Mod.ModType, #607). A type the
// adapter does not know routes by file here: this direction lists what a
// mod may have put on disk, and a mod of an unknown type was never
// deployed (deployableFiles refuses it).
func deployItems(a adapter.GameAdapter, game *domain.Game, modType string, files []string) []deployItem {
	out, err := deployItemsForType(a, game, modType, files)
	if err != nil {
		out, _ = deployItemsForType(a, game, "", files)
	}
	return out
}

// itemIndex maps a cache listing's members to the items they become.
type itemIndex map[string]deployItem

// deployItemIndex is deployItems as a member -> item index.
func deployItemIndex(a adapter.GameAdapter, game *domain.Game, modType string, files []string) itemIndex {
	items := deployItems(a, game, modType, files)
	idx := make(itemIndex, len(files))
	for _, it := range items {
		idx[it.member] = it
	}
	return idx
}

// of is members' items, in order; a member the index does not hold is a
// mod_path file of its own name.
func (idx itemIndex) of(members []string) []deployItem {
	out := make([]deployItem, len(members))
	for i, m := range members {
		if it, ok := idx[m]; ok {
			out[i] = it
		} else {
			out[i] = deployItem{path: domain.ModPathFile(m), member: m}
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

// statedType is ref's stated mod type (#607), "" when there is no ref: what
// an install from a profile reference stamps on the row it writes, so every
// later deploy of the row routes by it.
func statedType(ref *domain.ModReference) string {
	if ref == nil {
		return ""
	}
	return ref.ModType
}

// deployItemsForType is deployItems for a mod whose type may have been
// stated: "" asks the adapter file by file (RouteRoots); any other type
// routes the WHOLE mod to the root RootForModType names for it, and a type
// it does not know is a *ModTypeUnsupportedError. The type is the
// installed row's (domain.Mod.ModType), from its profile reference (#607).
func deployItemsForType(a adapter.GameAdapter, game *domain.Game, modType string, files []string) ([]deployItem, error) {
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
	toRoot := routesToGameRoot(game)
	out := make([]deployItem, len(files))
	for i, f := range files {
		out[i] = deployItem{path: domain.ModPathFile(f), member: f}
		if toRoot && roots[i] == adapter.RootInstallPath {
			out[i].path = gameRootPath(game, f)
		}
	}
	return out, nil
}

// gameRootPath is the deployed path of rel, a path relative to game's
// install_path. One that lands inside the mod_path - a whole-mod root
// route's Data/ folder, for a game whose mod_path is install_path/Data - is
// that mod_path file, and keeps its mod_path name: one place on disk has
// one deployed path, so conflicts, records and removals see it once. The
// mod_path's own components are matched case-insensitively, since an
// archive's data/ means the game's Data/ (the game reads it on a
// case-insensitive filesystem), and a data/ folder beside Data/ is one it
// never loads.
func gameRootPath(game *domain.Game, rel string) domain.DeployPath {
	if prefix, ok := modPathUnderInstall(game); ok {
		comps := strings.Split(filepath.ToSlash(rel), "/")
		if len(comps) > len(prefix) {
			match := true
			for i, p := range prefix {
				if !strings.EqualFold(comps[i], p) {
					match = false
					break
				}
			}
			if match {
				return domain.ModPathFile(filepath.FromSlash(strings.Join(comps[len(prefix):], "/")))
			}
		}
	}
	return domain.GameRootFile(rel)
}

// modPathUnderInstall is game's mod_path relative to its install_path, as
// its components, when the mod_path lies strictly inside it.
func modPathUnderInstall(game *domain.Game) ([]string, bool) {
	if game.ModPath == "" || game.InstallPath == "" {
		return nil, false
	}
	rel, ok := relUnder(game.InstallPath, game.ModPath)
	if !ok {
		return nil, false
	}
	return strings.Split(rel, "/"), true
}

// deployRootDir is the directory p is relative to in game: its install_path
// for a game-root path, its mod_path otherwise.
func deployRootDir(game *domain.Game, p domain.DeployPath) string {
	if p.IsGameRoot() {
		return game.InstallPath
	}
	return game.ModPath
}

// deployTarget is where p lives in game: its root directory joined with its
// relative path. A game-root path of a game with no install_path has no
// place, and is "" - never a path relative to the working directory.
func deployTarget(game *domain.Game, p domain.DeployPath) string {
	dir := deployRootDir(game, p)
	if dir == "" && p.IsGameRoot() {
		return ""
	}
	return filepath.Join(dir, filepath.FromSlash(p.Rel))
}

// rootOf is the wire root a deployed path reports beside its relative path
// (#629): the zero value for a mod_path file, so a document about one is
// unchanged.
func rootOf(p domain.DeployPath) domain.DeployRoot { return p.Root }

// relOf is p's relative path in slash form, as a wire document carries it.
func relOf(p domain.DeployPath) string { return filepath.ToSlash(p.Rel) }

// originalRootOf is the originals-store root and relative path p is
// captured and released under: install_path for a game-root file - the
// root a profile override's originals already use, so a stock file both
// replace is one original - and mod_path otherwise.
func originalRootOf(p domain.DeployPath) (OriginalRoot, string) {
	if p.IsGameRoot() {
		return OriginalRootInstallPath, filepath.ToSlash(p.Rel)
	}
	return OriginalRootModPath, filepath.ToSlash(p.Rel)
}

// linkerFor is lnk for a mod_path file, and lnk held to the install_path
// (rootLinker) for a game-root one.
func linkerFor(lnk linker.Linker, game *domain.Game, p domain.DeployPath) linker.Linker {
	if p.IsGameRoot() {
		return rootLinker{inner: lnk, root: game.InstallPath}
	}
	return lnk
}

// cleanupRemoved prunes the directories that became empty because lmm
// removed the deployed paths in removed (linker.CleanupEmptyDirs), each
// under its own root: game-root paths under the install_path, the rest
// under the mod_path.
func cleanupRemoved(game *domain.Game, removed []domain.DeployPath) {
	var mod, root []string
	for _, p := range removed {
		if p.IsGameRoot() {
			root = append(root, filepath.FromSlash(p.Rel))
		} else {
			mod = append(mod, filepath.FromSlash(p.Rel))
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
	// followInRoot relaxes the no-link rule to the one a profile
	// override's write has always used (writeOverriddenCopy, released in
	// v2.3.0): a folder that is a link is followed when it resolves inside
	// the install_path, and refused only when it leaves it. Only the
	// restore of an override's original sets it, so that original goes
	// back wherever the override wrote; a game-root MOD file (#629) is
	// held to the strict rule.
	followInRoot bool
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
	if r.followInRoot {
		return r.openFollowingInRoot(dst, absRoot, rel, create)
	}
	// The install_path itself may be reached through a link (a Steam
	// library on another mount); nothing beneath it may. Each folder on
	// the way is checked here for a clear refusal, and openDirBeneath's
	// no-follow walk refuses one swapped for a link after the check.
	realRoot, err := resolveExisting(absRoot)
	if err != nil {
		return nil, "", err
	}
	dirRel := filepath.Dir(filepath.FromSlash(rel))
	if dirRel == "." {
		dirRel = ""
	}
	walked := realRoot
	for _, comp := range strings.Split(dirRel, string(filepath.Separator)) {
		if comp == "" {
			continue
		}
		walked = filepath.Join(walked, comp)
		info, err := os.Lstat(walked)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return nil, "", err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, "", fmt.Errorf("refusing %s: %s is a link, and lmm never writes or removes anything beneath the game's install_path (%s) through one", dst, walked, r.root)
		}
	}
	dir, err := openDirBeneath(realRoot, dirRel, create)
	if err != nil {
		return nil, "", err
	}
	return dir, filepath.Base(filepath.FromSlash(rel)), nil
}

// openFollowingInRoot is open under followInRoot: the destination's folder
// resolved through its links, refused only when that leaves the resolved
// install_path, then opened beneath it with the no-follow walk - the rule
// writeOverriddenCopy applies to an override's write.
func (r rootLinker) openFollowingInRoot(dst, absRoot, rel string, create bool) (*os.File, string, error) {
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
		return nil, "", fmt.Errorf("refusing %s: it leaves the game's install_path (%s) through a link", dst, r.root)
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
		return copyAt(dir, name, src, 0)
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
//
// perm is the new file's mode; 0 takes the source's. It is set on the
// temporary file's own handle before the rename, so nothing swapped in at
// name afterwards is ever chmod-ed.
func copyAt(dir *os.File, name, src string, perm fs.FileMode) (err error) {
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
	if perm == 0 {
		perm = info.Mode().Perm()
	}
	if err := out.Chmod(perm); err != nil {
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

// cachedMembersOf is the cache members a deployed path known only from its
// record may have come from, most likely first: its relative path, and -
// for a mod_path file of a game whose mod_path lies inside its install_path
// - that path under the mod_path's own folders, where a whole-mod game-root
// route's Data/ member came from (gameRootPath).
func cachedMembersOf(game *domain.Game, p domain.DeployPath) []string {
	out := []string{filepath.FromSlash(p.Rel)}
	if p.IsGameRoot() || game == nil || !routesToGameRoot(game) {
		return out
	}
	if prefix, ok := modPathUnderInstall(game); ok {
		out = append(out, filepath.Join(append(append([]string{}, prefix...), filepath.FromSlash(p.Rel))...))
	}
	return out
}
