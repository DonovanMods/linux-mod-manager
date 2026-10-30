package battlenet

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// shellFoldersKey is the user.reg key whose Personal value is the Documents
// folder Wine made for the prefix's user, as system.reg spells key names.
const shellFoldersKey = `[Software\\Microsoft\\Windows\\CurrentVersion\\Explorer\\Shell Folders]`

// protonUser is the Windows user every Proton (and so umu) prefix runs as,
// whatever the host user is called.
const protonUser = "steamuser"

// documentsGameFolder is the first element of a Documents-relative mod
// path - the game's own folder in Documents ("Warcraft III") - or "" when
// it names none.
func documentsGameFolder(modPath string) string {
	first, _, _ := strings.Cut(filepath.ToSlash(filepath.Clean(modPath)), "/")
	if first == "." || first == "" {
		return ""
	}
	return first
}

// documentsNames are the names a prefix user's Documents folder has had:
// Wine and Proton make "Documents" today; older prefixes made "My
// Documents" and kept it.
var documentsNames = []string{"Documents", "My Documents"}

// documentsDir is the Documents folder of the prefix user who plays the
// game whose folder in Documents is gameFolder (#546), resolved through
// any symlink - Wine may link it to the host's ~/Documents - since the
// resolved path is where the files really are. A prefix can hold several
// users (the Wine user who made it, Proton's steamuser, the shared
// Public), so it picks, in order:
//
//  1. the user whose Documents already holds gameFolder - the game has run
//     as that user;
//  2. among several such users, or when none has it, the prefix's own user:
//     the one whose Documents user.reg's Shell Folders names, then
//     steamuser in a Proton prefix;
//  3. the only user left.
//
// A user's Documents is the folder user.reg's Personal names when it names
// one of that user's (an older prefix's "My Documents"); otherwise the
// documentsNames folder that holds gameFolder, or the first that exists.
//
// Public is never a candidate: it is the profile every user shares, never
// the one a game writes its Documents into. Anything else is ambiguous,
// and an error naming the candidates - lmm does not guess. So is a
// Documents that resolves outside both the prefix and the home directory:
// lmm deploys there, and a link inside a prefix is no reason to write
// anywhere else on the machine.
func documentsDir(prefix, gameFolder string) (string, error) {
	usersDir := filepath.Join(prefix, "drive_c", "users")
	var users []string
	for _, dir := range children(usersDir) {
		if name := filepath.Base(dir); !strings.EqualFold(name, "Public") {
			users = append(users, name)
		}
	}
	if len(users) == 0 {
		return "", fmt.Errorf("%s has no user profile but Public", usersDir)
	}
	regUser, regFolder := registryDocuments(prefix)
	docs := func(user string) string {
		if user == regUser && isDir(filepath.Join(usersDir, user, regFolder)) {
			return filepath.Join(usersDir, user, regFolder)
		}
		for _, name := range documentsNames {
			if isDir(filepath.Join(usersDir, user, name, gameFolder)) {
				return filepath.Join(usersDir, user, name)
			}
		}
		for _, name := range documentsNames {
			if isDir(filepath.Join(usersDir, user, name)) {
				return filepath.Join(usersDir, user, name)
			}
		}
		return filepath.Join(usersDir, user, documentsNames[0])
	}

	candidates := users
	var withGame []string
	for _, u := range users {
		if isDir(filepath.Join(docs(u), gameFolder)) {
			withGame = append(withGame, u)
		}
	}
	if len(withGame) > 0 {
		candidates = withGame
	}
	pick := ""
	switch {
	case len(candidates) == 1:
		pick = candidates[0]
	case slices.Contains(candidates, regUser):
		pick = regUser
	case isProtonPrefix(prefix) && slices.Contains(candidates, protonUser):
		pick = protonUser
	default:
		return "", fmt.Errorf("%s has several user profiles (%s) and nothing says which one plays the game; run the game once so it makes its %s folder in that user's Documents, or add the game with lmm game add",
			usersDir, strings.Join(candidates, ", "), gameFolder)
	}
	dir := docs(pick)
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if !within(dir, prefix) && !within(dir, os.Getenv("HOME")) {
		return "", fmt.Errorf("%s resolves to %s, outside both the prefix and your home directory; lmm will not deploy there - add the game with lmm game add and the mod path you mean", docs(pick), dir)
	}
	return dir, nil
}

// within reports whether path is root or inside it, both resolved through
// any symlink. An empty root holds nothing.
func within(path, root string) bool {
	if root == "" {
		return false
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// isDir reports whether path is a directory, through any symlink.
func isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}

// registryDocuments is the user and folder the prefix's user.reg names as
// its Personal shell folder ("C:\users\dyoung\My Documents" -> "dyoung",
// "My Documents"), or "" for both when user.reg names nothing lmm can read
// inside drive_c's users.
func registryDocuments(prefix string) (user, folder string) {
	personal, _ := readShellFolder(filepath.Join(prefix, "user.reg"), "Personal")
	path := strings.ReplaceAll(personal, `\`, "/")
	if len(path) < 2 || path[1] != ':' || !strings.EqualFold(path[:1], "c") {
		return "", ""
	}
	parts := strings.Split(strings.Trim(path[2:], "/"), "/")
	if len(parts) < 3 || !strings.EqualFold(parts[0], "users") || slices.Contains(parts[1:], "..") || slices.Contains(parts[1:], ".") {
		return "", ""
	}
	return parts[1], filepath.Join(parts[2:]...)
}

// readShellFolder reads one value of user.reg's Shell Folders key.
func readShellFolder(path, name string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close() //nolint:errcheck // read-only

	in := false
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "[") {
			in = strings.HasPrefix(line, shellFoldersKey)
			continue
		}
		if !in {
			continue
		}
		if n, v, ok := regStringValue(line); ok && n == name {
			return v, nil
		}
	}
	return "", scanner.Err()
}

// isProtonPrefix reports whether prefix is one Proton made: a version file
// beside a pfx folder.
func isProtonPrefix(prefix string) bool {
	_, ok := protonVersion(prefix)
	return ok
}
