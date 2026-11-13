package domain

import "strings"

// A deployed path is how lmm names one file it deploys into a game (#629).
// Until #629 every deployed file was relative to the game's mod_path, and a
// deployed path was simply that relative path. A file a game adapter routes
// to the game's install_path - a script extender's loader, an ENB's
// d3d11.dll - is relative to that directory instead, and its deployed path
// carries the root in a reserved prefix: InstallPathKey's.
//
// The prefix starts with a NUL byte, which no file name can hold. So the
// key can never collide with a real mod_path-relative path, and a code path
// that joins one onto a directory without asking which root it names gets
// a path every system call refuses, rather than silently touching a file
// under the wrong directory. A key is internal: frontends are handed
// DisplayDeployedPath's spelling, and storage records the root in a column
// of its own.
const installPathKeyPrefix = "\x00install_path/"

// GameRootLabel is how a deployed path under the game's install_path is
// spelled for a person: "<game root>/d3d11.dll", beside a bare
// "textures/a.dds" for one under the mod_path.
const GameRootLabel = "<game root>"

// InstallPathKey returns the deployed path of rel, a slash- or
// OS-separated path relative to the game's install_path.
func InstallPathKey(rel string) string {
	return installPathKeyPrefix + rel
}

// SplitDeployedPath splits a deployed path into the path relative to its
// root and whether that root is the game's install_path (false: the
// mod_path, which every path without the prefix is).
func SplitDeployedPath(p string) (rel string, installPath bool) {
	if rest, ok := strings.CutPrefix(p, installPathKeyPrefix); ok {
		return rest, true
	}
	return p, false
}

// IsInstallPathKey reports whether p is a deployed path under the game's
// install_path.
func IsInstallPathKey(p string) bool {
	return strings.HasPrefix(p, installPathKeyPrefix)
}

// DisplayDeployedPath is p as a person reads it: a mod_path file as its
// relative path, unchanged, and an install_path file with the root named in
// front ("<game root>/d3d11.dll").
func DisplayDeployedPath(p string) string {
	rel, root := SplitDeployedPath(p)
	if !root {
		return p
	}
	return GameRootLabel + "/" + rel
}
