package core

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/wineprefix"
)

// Reading and writing a game's list files inside its Wine prefix (#616).
// The prefix is resolved once (ResolvePrefix), but PrefixStatus.UserPath is
// a lexical join checked only at that moment (#605 review). So every write
// here re-checks the user directory with wineprefix.CheckUserDir right
// before it, then opens the directory that holds the list file one
// component at a time with no symlink followed (openDirBeneath's walk,
// #630) - a link swapped in after the check is refused, never written
// through - and replaces the file by renaming a new one over it, so a link
// at the file's own name is broken, not followed.
//
// Names are matched case-insensitively, as Wine matches them: on a
// case-sensitive filesystem "plugins.txt" and "Plugins.txt" are two files
// to Linux and one to the game, so lmm writes to the one already there and
// creates the canonical spelling only when there is none.

// maxListFileSize bounds a list file lmm reads: a real one is a few KiB
// (4,351 plugins at most, each a short name).
const maxListFileSize = 4 << 20

// prefixPath is one directory inside a game's prefix: the user directory
// and the components under it, each matched case-insensitively.
type prefixPath struct {
	prefix string
	user   string
	comps  []string
}

// listDirOf is where spec's list file lives for folder: the user
// directory's AppData/Local/<folder>.
func listDirOf(st *PrefixStatus, folder string) prefixPath {
	return prefixPath{prefix: st.Path, user: st.User, comps: []string{"AppData", "Local", folder}}
}

// display is the path as a person reads it, with the canonical spellings.
func (p prefixPath) display() string {
	return filepath.Join(append([]string{p.prefix, "drive_c", "users", p.user}, p.comps...)...)
}

// open opens p's directory, checking the user directory first and walking
// beneath it without following a link. create makes missing components
// with their canonical spelling. The answer's Name() is the real path.
func (p prefixPath) open(create bool) (*os.File, error) {
	if err := wineprefix.CheckUserDir(p.prefix, p.user); err != nil {
		return nil, err
	}
	realPrefix, err := filepath.EvalSymlinks(p.prefix)
	if err != nil {
		return nil, err
	}
	realUser, err := filepath.EvalSymlinks(filepath.Join(p.prefix, "drive_c", "users", p.user))
	if err != nil {
		return nil, err
	}
	rel, ok := relUnder(realPrefix, realUser)
	if !ok {
		return nil, fmt.Errorf("%s %w", realUser, wineprefix.ErrUserOutsidePrefix)
	}
	dir, err := openDirBeneath(realPrefix, rel, false)
	if err != nil {
		return nil, err
	}
	for _, comp := range p.comps {
		next, err := openChildFold(dir, comp, create)
		_ = dir.Close()
		if err != nil {
			return nil, err
		}
		dir = next
	}
	return dir, nil
}

// openChildFold opens the directory beneath dir whose name matches name
// case-insensitively (an exact match first), without following a link;
// with create, one that is missing is made as name.
func openChildFold(dir *os.File, name string, create bool) (*os.File, error) {
	real, err := entryFold(dir, name)
	if err != nil {
		return nil, err
	}
	if real == "" {
		if !create {
			return nil, &os.PathError{Op: "open", Path: filepath.Join(dir.Name(), name), Err: os.ErrNotExist}
		}
		if err := syscall.Mkdirat(int(dir.Fd()), name, 0o755); err != nil && !errors.Is(err, syscall.EEXIST) {
			return nil, &os.PathError{Op: "mkdir", Path: filepath.Join(dir.Name(), name), Err: err}
		}
		real = name
	}
	fd, err := openDirAt(int(dir.Fd()), real)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) || errors.Is(err, syscall.ENOTDIR) {
			return nil, fmt.Errorf("%s is not a directory (it is a link, or a file), so lmm writes nothing beneath it", filepath.Join(dir.Name(), real))
		}
		return nil, &os.PathError{Op: "open", Path: filepath.Join(dir.Name(), real), Err: err}
	}
	return os.NewFile(uintptr(fd), filepath.Join(dir.Name(), real)), nil
}

// entryFold is the name of dir's entry that matches name: name itself when
// it is there, else the first, in byte order, that matches it
// case-insensitively, else "".
func entryFold(dir *os.File, name string) (string, error) {
	if _, exists, err := lstatAt(dir, name); err != nil {
		return "", err
	} else if exists {
		return name, nil
	}
	names, err := dirNames(dir)
	if err != nil {
		return "", err
	}
	for _, n := range names {
		if strings.EqualFold(n, name) {
			return n, nil
		}
	}
	return "", nil
}

// dirNames lists dir's entries, sorted. It reads through a duplicate
// handle so dir's own offset is left alone.
func dirNames(dir *os.File) ([]string, error) {
	fd, err := syscall.Openat(int(dir.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: dir.Name(), Err: err}
	}
	f := os.NewFile(uintptr(fd), dir.Name())
	defer func() { _ = f.Close() }()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	return names, nil
}

// readAt reads the regular file name beneath dir without following a
// link. exists is false when nothing is there. A link, a directory or
// another non-regular file there is an error: lmm never reads one as a
// list.
func readAt(dir *os.File, name string) (data []byte, perm os.FileMode, exists bool, err error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	switch {
	case errors.Is(err, syscall.ENOENT):
		return nil, 0, false, nil
	case errors.Is(err, syscall.ELOOP):
		return nil, 0, true, fmt.Errorf("%s is a link, and lmm never reads or writes a list file through one", filepath.Join(dir.Name(), name))
	case err != nil:
		return nil, 0, true, &os.PathError{Op: "open", Path: filepath.Join(dir.Name(), name), Err: err}
	}
	f := os.NewFile(uintptr(fd), filepath.Join(dir.Name(), name))
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, true, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, true, fmt.Errorf("%s: %w", f.Name(), errNotRegularTarget)
	}
	data, err = io.ReadAll(io.LimitReader(f, maxListFileSize+1))
	if err != nil {
		return nil, 0, true, err
	}
	if len(data) > maxListFileSize {
		return nil, 0, true, fmt.Errorf("%s is larger than %d bytes, which no plugin list is", f.Name(), maxListFileSize)
	}
	return data, info.Mode().Perm(), true, nil
}

// removeAt removes the regular file name beneath dir, if one is there; a
// link or anything else there is left, and refused.
func removeAt(dir *os.File, name string) error {
	st, exists, err := lstatAt(dir, name)
	if err != nil || !exists {
		return err
	}
	if !isRegular(st) {
		return fmt.Errorf("%s: %w", filepath.Join(dir.Name(), name), errNotRegularTarget)
	}
	if err := syscall.Unlinkat(int(dir.Fd()), name); err != nil && !errors.Is(err, syscall.ENOENT) {
		return &os.PathError{Op: "remove", Path: filepath.Join(dir.Name(), name), Err: err}
	}
	return nil
}
