// Package d2r is the game adapter for Diablo II: Resurrected (#546).
//
// D2R loads a mod from <install>/mods/<name>/<name>.mpq/ - a FOLDER named
// like an MPQ archive - and only when the game is started with
// `-mod <name> -txt`. The game's catalog entry sets mod_path to mods, so
// the two rules here are:
//
//	NormalizeArchive  lays every archive out as <name>/<name>.mpq/, whether
//	                  it was packed that way, inside a mods/ folder, or as
//	                  the bare <name>.mpq folder
//	LaunchAdvisor     names the -mod the deployed profile needs: Battle.net's
//	                  --exec cannot pass game arguments on, so the user sets
//	                  them in Battle.net's "Additional command line
//	                  arguments", and lmm says exactly what to enter
//
// D2R loads one mod folder at a time, so the advice names the folder of the
// mod last in load order - the one that wins every conflict - and says so
// when mods in other folders are enabled. Mods that carry the same
// <name>.mpq deploy into one folder, merged under the usual conflict rules,
// and load together; the advice says that too. A mod whose name has a
// space is refused at ingest: Battle.net splits its arguments on
// whitespace, so no -mod could ever name it.
package d2r

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ID is this adapter's games.yaml value and registry key.
const ID = "d2r"

// Adapter is the Diablo II: Resurrected game adapter. It holds no state.
type Adapter struct{}

// New returns the D2R adapter.
func New() *Adapter { return &Adapter{} }

// ID returns the adapter's games.yaml value ("d2r").
func (*Adapter) ID() string { return ID }

// Label returns the display name a frontend shows.
func (*Adapter) Label() string { return "Diablo II: Resurrected (-mod folders)" }

// Compile-time proof of the capabilities the package documents.
var (
	_ adapter.GameAdapter   = (*Adapter)(nil)
	_ adapter.LaunchAdvisor = (*Adapter)(nil)
)

// mpqFolder finds the <name>.mpq folder in a slash-separated member path:
// the index of that element and the name, or -1 when the member is under
// none. The last element is the file itself, never a folder.
func mpqFolder(member string) (int, string) {
	parts := strings.Split(member, "/")
	for i, p := range parts[:len(parts)-1] {
		if len(p) > len(".mpq") && strings.EqualFold(p[len(p)-len(".mpq"):], ".mpq") {
			return i, p[:len(p)-len(".mpq")]
		}
	}
	return -1, ""
}

// NormalizeArchive maps the archive onto <name>/<name>.mpq/, with <name>
// taken from its one <name>.mpq folder. A member under that folder keeps
// its path inside it; any other member moves into <name>/ beside it,
// relative to the folder that holds the .mpq (the archive root when the
// member is not under it). An archive with no .mpq folder, or with
// several, is left as packaged and warned about. One whose <name> holds
// whitespace is refused: it could never be loaded (see the package doc).
func (*Adapter) NormalizeArchive(req adapter.NormalizeRequest) (adapter.Layout, error) {
	var names []string
	parent := ""
	for _, m := range req.Members {
		i, name := mpqFolder(m)
		if i < 0 {
			continue
		}
		if !slices.Contains(names, name) {
			names = append(names, name)
			parent = strings.Join(strings.Split(m, "/")[:i], "/")
		}
	}
	switch len(names) {
	case 0:
		return adapter.Layout{Kind: "no-mpq-folder", Warnings: []string{
			"no <name>.mpq folder in this archive: Diablo II: Resurrected loads a mod only from mods/<name>/<name>.mpq/, so this one is deployed as packaged and may not load",
		}}, nil
	case 1:
	default:
		return adapter.Layout{Kind: "several-mpq-folders", Warnings: []string{
			fmt.Sprintf("this archive holds several mods (%s), deployed as packaged; Diablo II: Resurrected loads one at a time", strings.Join(names, ", ")),
		}}, nil
	}

	name := names[0]
	if strings.ContainsFunc(name, unicode.IsSpace) {
		fixed := strings.Join(strings.Fields(name), "_")
		return adapter.Layout{}, fmt.Errorf("the mod folder %q has a space in its name, and Diablo II: Resurrected loads a mod only through -mod %s, which Battle.net's \"Additional command line arguments\" split on spaces - so this mod could never load; repack it with both folders renamed without spaces (for example %s/%s.mpq/) and add it with lmm import <archive>",
			name+".mpq", name, fixed, fixed)
	}
	rewrites := make(map[string]string)
	for _, m := range req.Members {
		var dest string
		if i, _ := mpqFolder(m); i >= 0 {
			dest = name + "/" + strings.Join(strings.Split(m, "/")[i:], "/")
		} else if rest, ok := strings.CutPrefix(m, parent+"/"); ok && parent != "" {
			dest = name + "/" + rest
		} else {
			dest = name + "/" + m
		}
		if dest != m {
			rewrites[m] = dest
		}
	}
	if len(rewrites) == 0 {
		return adapter.Layout{Kind: "mpq-folder"}, nil
	}
	return adapter.NewLayout("mpq-folder", rewrites), nil
}

// LaunchAdvice says what to enter in Battle.net's "Additional command line
// arguments" for the deployed profile: -mod with the folder of the last
// mod in load order that has one. It says when several mods share that
// folder (merged, loaded together) and when mods in other folders are
// enabled (they do not load). With no such mod but a previous -mod, it
// says to remove that.
func (*Adapter) LaunchAdvice(g *domain.Game, mods []adapter.DeployedMod, previous string) adapter.Advice {
	type found struct{ mod, folder string }
	var all []found
	for _, m := range mods {
		for _, f := range m.Files {
			parts := strings.Split(f, "/")
			if len(parts) < 3 {
				continue
			}
			if i, name := mpqFolder(f); i == 1 && strings.EqualFold(name, parts[0]) {
				all = append(all, found{m.Name, parts[0]})
				break
			}
		}
	}
	if len(all) == 0 {
		if previous == "" {
			return adapter.Advice{}
		}
		return adapter.Advice{Lines: []string{fmt.Sprintf("no mod is enabled for %s now: in Battle.net, open the game's settings (the gear beside Play) > Game Settings and remove %s from \"Additional command line arguments\", so the game starts unmodded",
			g.Name, previous)}}
	}
	folder := all[len(all)-1].folder
	var loaded, unloaded []string
	for _, f := range all {
		if strings.EqualFold(f.folder, folder) {
			loaded = append(loaded, f.mod)
		} else {
			unloaded = append(unloaded, fmt.Sprintf("%s (mods/%s/)", f.mod, f.folder))
		}
	}
	args := "-mod " + folder + " -txt"
	instead := ""
	if previous != "" && previous != args {
		instead = ", in place of " + previous
	}
	lines := []string{fmt.Sprintf("%s loads %s only when started with %q: in Battle.net, open the game's settings (the gear beside Play) > Game Settings, tick \"Additional command line arguments\" and enter: %s%s",
		g.Name, strings.Join(loaded, " and "), args, args, instead)}
	if len(loaded) > 1 {
		lines = append(lines, fmt.Sprintf("%s all deploy into mods/%s/: lmm merges them into that one folder, and where two carry the same file the later in load order wins; %s loads them together, as the one mod %s",
			strings.Join(loaded, ", "), folder, g.Name, folder))
	}
	if len(unloaded) > 0 {
		lines = append(lines, fmt.Sprintf("%d mods are enabled, but %s loads one mod at a time: mods/%s/ (%s), the last in load order; %s will not load - disable them, or move the one you want last",
			len(all), g.Name, folder, strings.Join(loaded, ", "), strings.Join(unloaded, ", ")))
	}
	return adapter.Advice{Args: args, Lines: lines}
}
