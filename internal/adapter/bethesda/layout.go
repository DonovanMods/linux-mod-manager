package bethesda

import (
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
)

// dataDir is the folder a game-root-relative archive keeps its mod_path
// content in. Windows archives spell it every way, so it is matched without
// case.
const dataDir = "Data"

// layoutKind is the Layout's diagnostic label.
const layoutKind = "game-root-relative"

// NormalizeArchive lays a game-root-relative archive out relative to
// mod_path (Data/). Most Bethesda mods are packed relative to Data/ already
// and are left exactly as they are. Two shapes are not:
//
//   - An archive with a top-level Data/ folder is rooted at the game
//     directory: Data/ is lifted, so its content lands in mod_path, and
//     the members beside it (a script extender's loader and DLLs) stay at
//     the top level, where RouteRoots finds them.
//   - The same thing inside ONE wrapper folder, as SKSE64 ships
//     (skse64_2_02_06/...): the wrapper goes too. A single top-level folder
//     is only a wrapper when what is inside it is recognisably game-root
//     content - a Data/ folder, a script extender binary, an ENB - because
//     "textures/" or "SKSE/" at the top of a Data-relative archive is
//     ordinary mod content.
//
// A lift that would merge two members into one destination (an archive
// with both Data/Mod.esp and Mod.esp) is not made: the archive is left as
// packaged, with a warning naming the clash.
func (*Adapter) NormalizeArchive(req adapter.NormalizeRequest) (adapter.Layout, error) {
	members := req.Members
	wrapper := wrapperOf(members)

	rewrites := make(map[string]string)
	lifted := false
	for _, m := range members {
		rel := strings.TrimPrefix(m, wrapper)
		if top, rest, ok := strings.Cut(rel, "/"); ok && rest != "" && strings.EqualFold(top, dataDir) {
			rel = rest
			lifted = true
		}
		if rel != m {
			rewrites[m] = rel
		}
	}
	if wrapper == "" && !lifted {
		// Nothing is rewritten, so nothing the adapter did can merge two
		// members: an archive with case-variant names is left as it was,
		// without a warning about a clash the adapter did not cause.
		return adapter.NewLayout(layoutKind, nil), nil
	}
	dests := make(map[string]string, len(members))
	for _, m := range members {
		rel := m
		if r, ok := rewrites[m]; ok {
			rel = r
		}
		if other, clash := dests[strings.ToLower(rel)]; clash {
			return adapter.Layout{
				Kind:     layoutKind,
				Warnings: []string{fmt.Sprintf("%s and %s would both land at %s; the archive is left as packaged", other, m, rel)},
			}, nil
		}
		dests[strings.ToLower(rel)] = m
	}
	return adapter.NewLayout(layoutKind, rewrites), nil
}

// wrapperOf returns "<folder>/" when every member is under one top-level
// folder that is not Data/ and whose own top level is recognisably
// game-root content, or "" when the archive has no such wrapper.
func wrapperOf(members []string) string {
	if len(members) == 0 {
		return ""
	}
	top, _, ok := strings.Cut(members[0], "/")
	if !ok || strings.EqualFold(top, dataDir) {
		return ""
	}
	prefix := top + "/"
	marked := false
	for _, m := range members {
		inner, found := strings.CutPrefix(m, prefix)
		if !found || inner == "" {
			return ""
		}
		name, _, nested := strings.Cut(inner, "/")
		switch {
		case nested && (strings.EqualFold(name, dataDir) || isRootDir(name)):
			marked = true
		case !nested && isRootFile(name):
			marked = true
		}
	}
	if !marked {
		return ""
	}
	return prefix
}
