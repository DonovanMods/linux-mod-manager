// Package wineprefix answers questions about a Wine or Proton prefix on
// disk: whether a directory is one, which Proton build made it, which Steam
// library holds a game's compatdata prefix, and which Windows user
// directory inside it is the game's (#605).
//
// It is a leaf package - the standard library only - because two layers
// that may not import each other need the same answers: the Battle.net
// detector (internal/source/battlenet, #545), which finds games inside
// prefixes, and internal/core, which resolves a game's own prefix and may
// not import a concrete source. Everything here reads; nothing writes.
package wineprefix

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// IsPrefix reports whether dir is a Wine prefix: it holds a regular
// system.reg and a drive_c directory.
func IsPrefix(dir string) bool {
	reg, err := os.Stat(filepath.Join(dir, "system.reg"))
	if err != nil || !reg.Mode().IsRegular() {
		return false
	}
	drive, err := os.Stat(filepath.Join(dir, "drive_c"))
	return err == nil && drive.IsDir()
}

// ProtonVersion is the Proton build that last ran prefix, from the version
// file Proton keeps beside its pfx folder - in the prefix itself for one
// umu made (pfx -> .), in its parent for Steam's compatdata/<id>/pfx. ok is
// false for a plain Wine prefix.
func ProtonVersion(prefix string) (string, bool) {
	for _, dir := range []string{prefix, filepath.Dir(prefix)} {
		if _, err := os.Stat(filepath.Join(dir, "pfx")); err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, "version"))
		if err != nil {
			continue
		}
		first, _, _ := strings.Cut(string(data), "\n")
		return strings.TrimSpace(first), true
	}
	return "", false
}

// Candidate is one Steam library's compatdata prefix for an app.
type Candidate struct {
	// Library is the Steam library the prefix is in, as the caller named
	// it.
	Library string
	// Path is the prefix: <Library>/steamapps/compatdata/<appid>/pfx.
	Path string
	// ModTime is the newest modification time among the prefix directory
	// and the registry files Wine rewrites whenever the prefix runs - the
	// best answer a directory can give to "when was this prefix last
	// used".
	ModTime time.Time
}

// SteamCompatdata returns every library's compatdata prefix for appID,
// newest first (ModTime; a tie keeps the libraries' order). A library is
// probed once however many spellings of it libraries holds, and a pfx that
// is not a prefix (IsPrefix) is passed over. An appID that is not all
// digits names no Steam app, and finds nothing.
//
// Every library, not only the one the game is installed in: Steam leaves a
// game's compatdata behind when the game moves to another library, so the
// prefix the game actually uses can be anywhere (#605).
func SteamCompatdata(libraries []string, appID string) []Candidate {
	if appID == "" || strings.Trim(appID, "0123456789") != "" {
		return nil
	}
	var out []Candidate
	seen := make(map[string]bool)
	for _, lib := range libraries {
		real, err := filepath.EvalSymlinks(lib)
		if err != nil {
			real = filepath.Clean(lib)
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		pfx := filepath.Join(lib, "steamapps", "compatdata", appID, "pfx")
		if !IsPrefix(pfx) {
			continue
		}
		out = append(out, Candidate{Library: lib, Path: pfx, ModTime: lastUsed(pfx)})
	}
	slices.SortStableFunc(out, func(a, b Candidate) int { return b.ModTime.Compare(a.ModTime) })
	return out
}

// lastUsed is Candidate.ModTime for the prefix at pfx.
func lastUsed(pfx string) time.Time {
	var newest time.Time
	for _, p := range []string{pfx, filepath.Join(pfx, "system.reg"), filepath.Join(pfx, "user.reg"), filepath.Join(pfx, "userdef.reg")} {
		if info, err := os.Stat(p); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest
}

// UserRuleProton and UserRuleOnly name the rule FindUser picked a user
// directory by: Proton's and umu's fixed "steamuser", or the one directory
// drive_c/users holds besides Public.
const (
	UserRuleProton = "proton"
	UserRuleOnly   = "only_user"
)

// ProtonUser is the Windows user name Proton and umu give every prefix.
const ProtonUser = "steamuser"

// User is FindUser's answer. Name is "" when no single user directory can
// be told apart, and Candidates then lists the directories found (none, or
// several), sorted - or, when the one directory the rules picked leaves the
// prefix through a symlink (CheckUserDir), Outside names it.
type User struct {
	Name       string
	Rule       string
	Candidates []string
	Outside    string
}

// FindUser picks the Windows user directory under prefix's drive_c/users
// that a game run in it writes to: "steamuser" in a prefix Proton or umu
// made (ProtonVersion), when that directory exists; otherwise the one
// directory left after Public is set aside. Zero or several leave Name
// empty - the caller's prefix_user setting is the answer then, which is not
// this package's to read.
//
// The directory picked must pass CheckUserDir: one that is a symlink out of
// drive_c/users is never picked, and is reported in Outside instead.
func FindUser(prefix string) User {
	users := filepath.Join(prefix, "drive_c", "users")
	if _, proton := ProtonVersion(prefix); proton {
		if info, err := os.Stat(filepath.Join(users, ProtonUser)); err == nil && info.IsDir() {
			return checked(prefix, ProtonUser, UserRuleProton)
		}
	}
	entries, err := os.ReadDir(users)
	if err != nil {
		return User{}
	}
	var names []string
	for _, e := range entries {
		if strings.EqualFold(e.Name(), "Public") {
			continue
		}
		if info, err := os.Stat(filepath.Join(users, e.Name())); err == nil && info.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 1 {
		return checked(prefix, names[0], UserRuleOnly)
	}
	return User{Candidates: names}
}

// checked is FindUser's pick of name by rule, or Outside when the
// directory leaves the prefix.
func checked(prefix, name, rule string) User {
	if errors.Is(CheckUserDir(prefix, name), ErrUserOutsidePrefix) {
		return User{Outside: name}
	}
	return User{Name: name, Rule: rule}
}

// ErrNoUserDir and ErrUserOutsidePrefix are CheckUserDir's refusals: there
// is no directory by that name under drive_c/users, or there is one but -
// symlinks resolved - it is not inside the prefix's drive_c/users.
var (
	ErrNoUserDir         = errors.New("is not a directory")
	ErrUserOutsidePrefix = errors.New("resolves outside the prefix's drive_c/users")
)

// CheckUserDir checks that <prefix>/drive_c/users/<name> is a directory
// that, with every symlink resolved, is still inside the prefix's
// drive_c/users - and that drive_c/users, resolved, is still inside the
// prefix. A user directory that is a link to the host's home directory
// would otherwise send anything lmm writes "into the prefix" out of it.
// The users directory itself is not a user directory.
//
// The error wraps ErrNoUserDir or ErrUserOutsidePrefix and names the path.
func CheckUserDir(prefix, name string) error {
	users := filepath.Join(prefix, "drive_c", "users")
	dir := filepath.Join(users, name)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s %w", dir, ErrNoUserDir)
	}
	realPrefix, err1 := filepath.EvalSymlinks(prefix)
	realUsers, err2 := filepath.EvalSymlinks(users)
	realDir, err3 := filepath.EvalSymlinks(dir)
	if err := errors.Join(err1, err2, err3); err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	if !strictlyWithin(realUsers, realPrefix) || !strictlyWithin(realDir, realUsers) {
		return fmt.Errorf("%s %w (it leads to %s)", dir, ErrUserOutsidePrefix, realDir)
	}
	return nil
}

// strictlyWithin reports whether path is below dir, and not dir itself.
func strictlyWithin(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CheckUserName checks that name can name a directory directly under
// drive_c/users: one path element - not empty, not "." or "..", no slash of
// either kind - with no leading or trailing whitespace. The error says
// which rule it breaks.
func CheckUserName(name string) error {
	switch {
	case name == "":
		return errors.New("the user name is empty")
	case strings.TrimSpace(name) != name:
		return fmt.Errorf("%q has leading or trailing whitespace", name)
	case name == "." || name == "..":
		return fmt.Errorf("%q is not a directory name", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("%q contains a slash: it must be one directory name", name)
	}
	return nil
}
