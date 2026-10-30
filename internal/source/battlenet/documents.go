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
// Public is never a candidate: it is the profile every user shares, never
// the one a game writes its Documents into. Anything else is ambiguous,
// and an error naming the candidates - lmm does not guess.
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
	docs := func(user string) string { return filepath.Join(usersDir, user, "Documents") }

	candidates := users
	var withGame []string
	for _, u := range users {
		if st, err := os.Stat(filepath.Join(docs(u), gameFolder)); err == nil && st.IsDir() {
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
	case slices.Contains(candidates, registryUser(prefix)):
		pick = registryUser(prefix)
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
	return dir, nil
}

// registryUser is the user whose Documents the prefix's user.reg names as
// its Personal shell folder ("C:\users\dyoung\Documents" -> "dyoung"), or
// "" when user.reg says nothing lmm can read.
func registryUser(prefix string) string {
	personal, _ := readShellFolder(filepath.Join(prefix, "user.reg"), "Personal")
	path := strings.ReplaceAll(personal, `\`, "/")
	if len(path) < 2 || path[1] != ':' {
		return ""
	}
	parts := strings.Split(strings.Trim(path[2:], "/"), "/")
	if len(parts) < 2 || !strings.EqualFold(parts[0], "users") {
		return ""
	}
	return parts[1]
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
