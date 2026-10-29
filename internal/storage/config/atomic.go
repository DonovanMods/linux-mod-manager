package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// replaceFile writes data to path - games.yaml, config.yaml - so a reader
// in another process never sees a partial document (#403): a temporary file
// beside it is written, synced and renamed over it. what names the file in
// an error ("writing games.yaml: ...").
//
// It keeps what the truncating os.WriteFile it replaces kept:
//
//   - the file's mode, or mode for a file that does not exist yet;
//   - a symlink: it is followed, so a dotfile manager's link survives and its
//     target is what changes;
//   - a refusal of a file the user cannot write (os.ErrPermission), which a
//     rename alone - it only needs the directory to be writable - would not
//     make;
//   - a save into a directory lmm cannot create the temporary file in,
//     written in place instead, as is a file with more than one hard link,
//     which a rename would silently fork from its other names.
func replaceFile(path string, data []byte, mode os.FileMode, what string) error {
	label := "writing " + what
	target, err := filepath.EvalSymlinks(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if _, lerr := os.Lstat(path); lerr == nil {
			// A dangling symlink: create its target through it, as
			// os.WriteFile did. There is no old content to tear.
			if err := os.WriteFile(path, data, mode); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			return nil
		}
		target = path
	case err != nil:
		return fmt.Errorf("%s: %w", label, err)
	default:
		if err := syscall.Access(target, accessWrite); err != nil {
			return fmt.Errorf("%s: %w", label, &os.PathError{Op: "access", Path: target, Err: err})
		}
		info, err := os.Stat(target)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		mode = info.Mode().Perm()
		if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
			if err := os.WriteFile(target, data, mode); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			return nil
		}
	}

	err = renameIntoPlace(target, data, mode, label)
	var noTemp *tempFileError
	if !errors.As(err, &noTemp) || !errors.Is(err, fs.ErrPermission) {
		return err
	}
	if _, serr := os.Stat(target); serr != nil {
		// Nothing to write in place: the directory refuses a new file.
		return err
	}
	if err := os.WriteFile(target, data, mode); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// tempFileError is renameIntoPlace failing to create its temporary file -
// the one failure an in-place fallback answers.
type tempFileError struct {
	label string
	err   error
}

func (e *tempFileError) Error() string { return e.label + ": " + e.err.Error() }
func (e *tempFileError) Unwrap() error { return e.err }

// renameIntoPlace writes data, with mode, to a temporary file beside target
// and renames it to target - creating target, or replacing it whole - so a
// reader never sees a partial file. label prefixes every error ("writing
// profile"). The directory is synced afterwards, best effort, so the rename
// itself survives a crash.
func renameIntoPlace(target string, data []byte, mode os.FileMode, label string) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".*.tmp")
	if err != nil {
		return &tempFileError{label: label, err: err}
	}
	renamed := false
	defer func() {
		// Whatever stopped the write short - an error, or a panic on its
		// way to a caller's recover - leaves no temporary file behind.
		if !renamed {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	renamed = true
	syncDir(dir)
	return nil
}

// syncDir fsyncs dir, best effort: a filesystem that cannot sync a
// directory has still renamed the file.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
