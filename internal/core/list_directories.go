package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// DirectoryErrorReason is why a ListDirectories request was refused. It is
// the wire string a frontend branches on (DirectoryListError.Reason).
type DirectoryErrorReason string

// The four ways ListDirectories refuses a path (#529).
const (
	// DirectoryNotAbsolute: the path is empty, relative, or names another
	// user's home (~user). Only an absolute path - or ~ and ~/x, expanded
	// first - names a folder on the machine unambiguously.
	DirectoryNotAbsolute DirectoryErrorReason = "not_absolute"
	// DirectoryNotFound: nothing is at the path (or a file is in the way
	// of one of its components).
	DirectoryNotFound DirectoryErrorReason = "not_found"
	// DirectoryNotADirectory: the path names something that is not a folder.
	DirectoryNotADirectory DirectoryErrorReason = "not_a_directory"
	// DirectoryPermissionDenied: the folder exists but this process may not
	// read it.
	DirectoryPermissionDenied DirectoryErrorReason = "permission_denied"
)

// DirectoryListError is ListDirectories' refusal (#529): the path as the
// caller gave it and the reason. It unwraps to fs.ErrNotExist for a missing
// path and fs.ErrPermission for a denied one, so errors.Is works the way it
// does for the underlying os call.
type DirectoryListError struct {
	Path   string               `json:"path"`
	Reason DirectoryErrorReason `json:"reason"`
	err    error
}

// Error names the path and what is wrong with it.
func (e *DirectoryListError) Error() string {
	switch e.Reason {
	case DirectoryNotAbsolute:
		if strings.TrimSpace(e.Path) == "" {
			return "a folder path is required, and it must be absolute (or start with ~)"
		}
		return fmt.Sprintf("%q is not an absolute path: give a path starting with / or ~", e.Path)
	case DirectoryNotFound:
		return fmt.Sprintf("%s does not exist", e.Path)
	case DirectoryNotADirectory:
		return fmt.Sprintf("%s is not a folder", e.Path)
	case DirectoryPermissionDenied:
		return fmt.Sprintf("permission denied reading %s", e.Path)
	}
	return fmt.Sprintf("cannot list %s: %s", e.Path, string(e.Reason))
}

// Unwrap exposes the os error behind a not-found or permission refusal.
func (e *DirectoryListError) Unwrap() error { return e.err }

// Details returns the error itself for the --json error envelope's
// "details" field (Ruling 3) and the web UI's envelope: the path and the
// reason, so the folder chooser can branch without matching a sentence.
func (e *DirectoryListError) Details() any { return e }

// ListDirectoriesOptions tunes ListDirectories.
type ListDirectoriesOptions struct {
	// Hidden includes dot-folders; off by default.
	Hidden bool
	// Nearest answers for the closest existing folder instead of refusing
	// a path that is empty, relative, missing, or not a folder: it walks up
	// to the nearest ancestor that is one, and starts at the home folder
	// when there is none to walk from. It is the folder chooser's "open
	// where the field points" - a permission refusal is still a refusal,
	// since the folder is there and the caller should be told it is shut.
	Nearest bool
}

// DirectoryEntry is one subfolder in a DirectoryListing.
type DirectoryEntry struct {
	// Name is the folder's own name.
	Name string `json:"name"`
	// Path is its absolute path - the link's own path for a symlink, not
	// the target's, so navigating through it stays where the user was.
	Path string `json:"path"`
	// Hidden marks a dot-folder (only listed when asked for).
	Hidden bool `json:"hidden"`
	// Readable is false for a folder this process cannot open.
	Readable bool `json:"readable"`
	// Symlink marks a symbolic link to a folder. It is listed and marked,
	// never walked: nothing here recurses, so a link loop costs one listing
	// per request.
	Symlink bool `json:"symlink"`
}

// DirectoryListing is ListDirectories' answer: where it looked and the
// folders there. Regular files are never in it.
type DirectoryListing struct {
	// Path is the cleaned absolute folder that was listed.
	Path string `json:"path"`
	// Parent is Path's parent folder; empty at the filesystem root.
	Parent string `json:"parent"`
	// Requested is what the caller asked for, set only when Nearest
	// answered for a different folder.
	Requested string `json:"requested,omitempty"`
	// Entries are the subfolders, sorted case-insensitively by name.
	Entries []DirectoryEntry `json:"entries"`
}

// ListDirectories lists the subfolders of one absolute path on the machine
// lmm runs on - the read-only question behind the folder chooser (#529). A
// browser cannot report a real path (an <input type="file"> yields a name,
// never a location), so the web UI browses the server's filesystem instead;
// the CLI's completion is the shell's own.
//
// path is expanded (~, ~/x) and cleaned. It is never resolved through
// symlinks: the folder listed is the one the caller named, and its Parent is
// the lexical one. Only folders are returned - a symlink counts when it
// leads to one, and a dangling link or a link to a file is dropped - and no
// file contents are read. Hidden folders are opt-in.
//
// Refusals are *DirectoryListError: not absolute, not found, not a folder,
// permission denied. A cancelled ctx returns ctx.Err().
func (s *Service) ListDirectories(ctx context.Context, path string, opts ListDirectoriesOptions) (*DirectoryListing, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	requested := path
	target, ok := cleanAbsolutePath(path)
	want := target
	if !ok {
		if !opts.Nearest {
			return nil, &DirectoryListError{Path: requested, Reason: DirectoryNotAbsolute}
		}
		target = homeFolder()
	}

	for {
		entries, err := readSubdirectories(ctx, target, opts.Hidden)
		if err == nil {
			l := &DirectoryListing{Path: target, Parent: parentFolder(target), Entries: entries}
			if target != want && strings.TrimSpace(requested) != "" {
				l.Requested = requested
			}
			return l, nil
		}
		var de *DirectoryListError
		if !opts.Nearest || !errors.As(err, &de) ||
			(de.Reason != DirectoryNotFound && de.Reason != DirectoryNotADirectory) {
			return nil, err
		}
		up := parentFolder(target)
		if up == "" {
			return nil, err
		}
		target = up
	}
}

// cleanAbsolutePath expands a leading ~ and cleans path, reporting false
// when the result is not absolute.
func cleanAbsolutePath(path string) (string, bool) {
	path = config.ExpandPath(strings.TrimSpace(path))
	if !filepath.IsAbs(path) {
		return "", false
	}
	return filepath.Clean(path), true
}

// homeFolder is where the chooser starts with nothing to start from.
func homeFolder() string {
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		return filepath.Clean(home)
	}
	return string(filepath.Separator)
}

// parentFolder is dir's lexical parent, or "" at the root.
func parentFolder(dir string) string {
	parent := filepath.Dir(dir)
	if parent == dir {
		return ""
	}
	return parent
}

// readSubdirectories lists dir's subfolders, classifying a failure as a
// *DirectoryListError.
func readSubdirectories(ctx context.Context, dir string, hidden bool) ([]DirectoryEntry, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, classifyDirError(dir, err)
	}
	if !info.IsDir() {
		return nil, &DirectoryListError{Path: dir, Reason: DirectoryNotADirectory}
	}
	dirents, err := os.ReadDir(dir)
	if err != nil {
		return nil, classifyDirError(dir, err)
	}

	entries := make([]DirectoryEntry, 0, len(dirents))
	for _, d := range dirents {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := d.Name()
		isHidden := strings.HasPrefix(name, ".")
		if isHidden && !hidden {
			continue
		}
		full := filepath.Join(dir, name)
		symlink := d.Type()&fs.ModeSymlink != 0
		if symlink {
			// Follow ONE level, only to learn whether it leads to a folder.
			target, err := os.Stat(full)
			if err != nil || !target.IsDir() {
				continue
			}
		} else if !d.IsDir() {
			continue
		}
		entries = append(entries, DirectoryEntry{
			Name: name, Path: full, Hidden: isHidden,
			Readable: canOpenFolder(full), Symlink: symlink,
		})
	}
	slices.SortStableFunc(entries, func(a, b DirectoryEntry) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return entries, nil
}

// canOpenFolder reports whether this process may list dir.
func canOpenFolder(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// classifyDirError maps an os error on dir to the typed refusal.
func classifyDirError(dir string, err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return &DirectoryListError{Path: dir, Reason: DirectoryPermissionDenied, err: err}
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return &DirectoryListError{Path: dir, Reason: DirectoryNotFound, err: fs.ErrNotExist}
	default:
		return fmt.Errorf("listing %s: %w", dir, err)
	}
}
