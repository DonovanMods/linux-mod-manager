package domain

import (
	"fmt"
	"path/filepath"
	"strings"
)

// DeployRoot names which of a game's directories a deployed file is
// relative to (#629): its mod_path, as every file of every game always
// was, its install_path, for content the game loads from beside its
// executable (a script extender's loader, an ENB's d3d11.dll), or (#636)
// the Windows user directory inside the game's Wine/Proton prefix, for
// content the game loads from its AppData (Baldur's Gate 3's .pak mods).
// The game adapter's RootRouter answers in this type (adapter.DeployRoot
// is an alias), and storage and the wire carry its name.
type DeployRoot uint8

const (
	// RootModPath is the zero value: the file is relative to the game's
	// mod_path.
	RootModPath DeployRoot = iota
	// RootInstallPath is a file relative to the game's install_path.
	RootInstallPath
	// RootPrefix is a file relative to the game's prefix user directory,
	// <prefix>/drive_c/users/<user> (#636): ResolvedPrefix.UserDir.
	RootPrefix
)

// DeployRoots is every root, in CompareDeployPaths' order.
func DeployRoots() []DeployRoot {
	return []DeployRoot{RootModPath, RootInstallPath, RootPrefix}
}

// String returns the root's wire and storage name: "mod_path",
// "install_path" or "prefix" ("unknown" for an out-of-range value).
func (r DeployRoot) String() string {
	switch r {
	case RootModPath:
		return "mod_path"
	case RootInstallPath:
		return "install_path"
	case RootPrefix:
		return "prefix"
	default:
		return "unknown"
	}
}

// MarshalText implements encoding.TextMarshaler, refusing an out-of-range
// value rather than writing "unknown" to a document a reader would refuse.
func (r DeployRoot) MarshalText() ([]byte, error) {
	if r > RootPrefix {
		return nil, fmt.Errorf("unknown deploy root %d", r)
	}
	return []byte(r.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler, refusing a name this
// build does not know rather than reading it as the mod_path.
func (r *DeployRoot) UnmarshalText(b []byte) error {
	root, err := ParseDeployRoot(string(b))
	if err != nil {
		return err
	}
	*r = root
	return nil
}

// ParseDeployRoot is the root a stored or wire name names.
func ParseDeployRoot(name string) (DeployRoot, error) {
	switch name {
	case "mod_path":
		return RootModPath, nil
	case "install_path":
		return RootInstallPath, nil
	case "prefix":
		return RootPrefix, nil
	default:
		return RootModPath, fmt.Errorf("unknown deploy root %q", name)
	}
}

// GameRootLabel is how a file under the game's install_path is spelled for
// a person: "<game root>/d3d11.dll", beside a bare "textures/a.dds" for one
// under the mod_path. Only frontends and messages use it; nothing matches
// on it.
const GameRootLabel = "<game root>"

// PrefixLabel is GameRootLabel's counterpart for a file under the game's
// prefix user directory (#636): "<prefix>/AppData/Local/.../Mods/a.pak".
const PrefixLabel = "<prefix>"

// DeployPath names one deployed file of a game (#629): the root it is
// relative to and its path relative to that root. It is comparable, and it
// is the identity lmm keeps a deployed file by - in its records, its maps
// and its plan/apply matching - so a game-root "textures/a.dds" and a
// mod_path "textures/a.dds" are never the same file.
type DeployPath struct {
	Root DeployRoot
	// Rel is the path relative to Root, as the cache entry and the
	// deployed_files record spell it.
	Rel string
}

// ModPathFile is the deployed path rel under the game's mod_path.
func ModPathFile(rel string) DeployPath { return DeployPath{Root: RootModPath, Rel: rel} }

// GameRootFile is the deployed path rel under the game's install_path.
func GameRootFile(rel string) DeployPath { return DeployPath{Root: RootInstallPath, Rel: rel} }

// PrefixFile is the deployed path rel under the game's prefix user
// directory (#636).
func PrefixFile(rel string) DeployPath { return DeployPath{Root: RootPrefix, Rel: rel} }

// IsGameRoot reports whether p is under the game's install_path.
func (p DeployPath) IsGameRoot() bool { return p.Root == RootInstallPath }

// IsPrefix reports whether p is under the game's prefix user directory.
func (p DeployPath) IsPrefix() bool { return p.Root == RootPrefix }

// String is p as a person reads it: a mod_path file as its relative path,
// and a game-root or prefix file with the root named in front
// ("<game root>/d3d11.dll", "<prefix>/AppData/..."), all slash-separated. It is a spelling for messages and frontends, never a
// key.
func (p DeployPath) String() string { return DisplayDeployPath(p.Root, p.Rel) }

// DisplayDeployPath is the String spelling of rel under root, for a frontend
// that holds the two apart (a wire document's path and root fields).
func DisplayDeployPath(root DeployRoot, rel string) string {
	rel = filepath.ToSlash(rel)
	switch root {
	case RootInstallPath:
		return GameRootLabel + "/" + rel
	case RootPrefix:
		return PrefixLabel + "/" + rel
	}
	return rel
}

// ParseDisplayDeployPath reads String's spelling back into a deployed
// path: "<game root>/rel" is rel under the install_path, "<prefix>/rel"
// rel under the prefix user directory, anything else a mod_path path. It exists for a document a caller assembled from display
// strings alone; lmm's own plans carry the typed paths beside them.
func ParseDisplayDeployPath(s string) DeployPath {
	if rel, ok := strings.CutPrefix(s, GameRootLabel+"/"); ok {
		return GameRootFile(filepath.FromSlash(rel))
	}
	if rel, ok := strings.CutPrefix(s, PrefixLabel+"/"); ok {
		return PrefixFile(filepath.FromSlash(rel))
	}
	return ModPathFile(filepath.FromSlash(s))
}

// CompareDeployPaths orders deployed paths as every listing of them is
// ordered: the mod_path files first, then the game-root ones, then the
// prefix ones, each by relative path (byte order, as the database's BINARY collation sorts).
func CompareDeployPaths(a, b DeployPath) int {
	if a.Root != b.Root {
		if a.Root < b.Root {
			return -1
		}
		return 1
	}
	return strings.Compare(a.Rel, b.Rel)
}

// ResolvedPrefix is a game's Wine/Proton prefix as core resolved it for one
// flow (#636): the prefix (the directory holding drive_c) and the Windows
// user directory name inside it. It is never configuration - games.yaml's
// `prefix:` and `prefix_user:` are Game.Prefix and Game.PrefixUser - and
// never on the wire: core sets it on the copy of a game it hands out, for
// a game whose adapter deploys into the prefix, so a RootPrefix path has a
// directory to live in.
//
// UserDir is a lexical join, checked when it was resolved. A writer
// re-checks it (wineprefix.CheckUserDir) immediately before every write.
type ResolvedPrefix struct {
	Path string
	User string
}

// UserDir is <Path>/drive_c/users/<User>, the directory a RootPrefix path
// is relative to; "" for a nil prefix.
func (r *ResolvedPrefix) UserDir() string {
	if r == nil || r.Path == "" || r.User == "" {
		return ""
	}
	return filepath.Join(r.Path, "drive_c", "users", r.User)
}
