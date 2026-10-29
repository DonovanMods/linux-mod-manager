package curseforge

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// A game's curseforge.game_version_types (#544) names the gameVersionTypeIds
// it accepts: CurseForge serves every World of Warcraft flavor as one game,
// and the only thing that tells a Classic file from a Retail one is the type
// ids on its sortableGameVersions (latest.go). When the game a call is made
// for sets them, every file choice here - the version a mod is shown at, the
// file list an install picks from, the update check's candidates and a mod's
// dependencies - considers only files carrying one of them, and a mod with
// none is ErrNoFileForGame rather than another flavor's file. When it sets
// none, nothing changes.

// gameVersionTypes is the type ids the call's game accepts (#544): nil when
// it sets none, or when the call carries no game.
func gameVersionTypes(ctx context.Context) flavorSet {
	types := source.GameFromContext(ctx).CurseForgeVersionTypes()
	if len(types) == 0 {
		return nil
	}
	set := make(flavorSet, len(types))
	for _, t := range types {
		set[t] = struct{}{}
	}
	return set
}

// fileHasType reports whether f's sortableGameVersions carry one of want's
// type ids. Every file matches an empty want.
func fileHasType(f File, want flavorSet) bool {
	if len(want) == 0 {
		return true
	}
	for _, v := range f.SortableGameVersions {
		if _, ok := want[v.GameVersionTypeID]; ok {
			return true
		}
	}
	return false
}

// filesOfTypes is files without those want excludes, in order.
func filesOfTypes(files []File, want flavorSet) []File {
	if len(want) == 0 {
		return files
	}
	out := make([]File, 0, len(files))
	for _, f := range files {
		if fileHasType(f, want) {
			out = append(out, f)
		}
	}
	return out
}

// latestFilesOfTypes is the mod document's latestFiles without those want
// excludes. A file is of a type when latestFilesIndexes assigns it one, or
// its own sortableGameVersions carry one.
func latestFilesOfTypes(data Mod, want flavorSet) []File {
	if len(want) == 0 {
		return data.LatestFiles
	}
	indexed := flavorSet{}
	for _, idx := range data.LatestFilesIndexes {
		if _, ok := want[idx.GameVersionTypeID]; ok {
			indexed[idx.FileID] = struct{}{}
		}
	}
	out := make([]File, 0, len(data.LatestFiles))
	for _, f := range data.LatestFiles {
		if _, ok := indexed[f.ID]; ok || fileHasType(f, want) {
			out = append(out, f)
		}
	}
	return out
}

// publishedFor reports whether the mod document names any file of want's
// types - what a search hit needs to be installable for the game.
func publishedFor(data Mod, want flavorSet) bool {
	if len(want) == 0 {
		return true
	}
	for _, idx := range data.LatestFilesIndexes {
		if _, ok := want[idx.GameVersionTypeID]; ok {
			return true
		}
	}
	return len(latestFilesOfTypes(data, want)) > 0
}

// noFileForGame is the ErrNoFileForGame for modID, naming the game's types.
func noFileForGame(modID int, ctx context.Context) error {
	types := source.GameFromContext(ctx).CurseForgeVersionTypes()
	names := make([]string, len(types))
	for i, t := range types {
		names[i] = strconv.Itoa(t)
	}
	return fmt.Errorf("%w: CurseForge mod %d has no file for game version type %s (the game's curseforge.game_version_types)",
		source.ErrNoFileForGame, modID, strings.Join(names, ", "))
}
