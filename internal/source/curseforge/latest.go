package curseforge

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// CurseForge's latestFiles is NOT "the newest files, newest first". It holds
// the newest file of each class of file - and a file's class is whatever
// latestFilesIndexes classifies it by: its game flavor (WoW Retail, Classic
// Era, Cata, a Minecraft version family, ... - gameVersionTypeId) and its mod
// loader (modLoader: Forge, Fabric, ...; 0 "Any" for the many games with no
// loaders) - in no promised order, so its first entry is routinely an old file
// for some other class (#504). Everything here chooses the "latest" file
// deliberately instead of by position, decides "newer" by file identity
// (CurseForge file ids only ever increase), and only offers a file that
// matches the installed one on EVERY classifying dimension the mod's index
// reports. Nothing is keyed on a game: a game whose files do not vary on a
// dimension simply has one value (or only 0) there, and the dimension drops
// out.

// flavorSet is a set of gameVersionTypeIds.
type flavorSet = idSet

// loaderSet is a set of modLoader values. 0 (ModLoaderAny) means "any loader",
// and an empty set means the loader is not known; both are wildcards.
type loaderSet = idSet

// idSet is a set of CurseForge classification ids.
type idSet map[int]struct{}

func (s idSet) intersects(o idSet) bool {
	for id := range s {
		if _, ok := o[id]; ok {
			return true
		}
	}
	return false
}

// flavorVocabulary is every gameVersionTypeId latestFilesIndexes names for
// the mod: the flavors it is published for. A file's sortableGameVersions
// also carry types that are not flavors (Client/Server, Java, loaders), so
// they are only ever read through this set. Empty when the document has no
// indexes, in which case no file's flavor is known.
func flavorVocabulary(data Mod) flavorSet {
	vocab := flavorSet{}
	for _, idx := range data.LatestFilesIndexes {
		if idx.GameVersionTypeID > 0 {
			vocab[idx.GameVersionTypeID] = struct{}{}
		}
	}
	return vocab
}

// sortableFlavors is f's own flavor types: its sortableGameVersions' type ids
// that are real flavors of the mod.
func sortableFlavors(f File, vocab flavorSet) flavorSet {
	out := flavorSet{}
	for _, v := range f.SortableGameVersions {
		if _, ok := vocab[v.GameVersionTypeID]; ok {
			out[v.GameVersionTypeID] = struct{}{}
		}
	}
	return out
}

// fileFlavors maps each file id the document names to its flavors: those
// latestFilesIndexes assign it, else its own sortableGameVersions. An id
// present with an empty set is a file whose flavor the document does not say.
func fileFlavors(data Mod, vocab flavorSet) map[int]flavorSet {
	out := make(map[int]flavorSet, len(data.LatestFiles))
	for _, idx := range data.LatestFilesIndexes {
		set := out[idx.FileID]
		if set == nil {
			set = flavorSet{}
			out[idx.FileID] = set
		}
		if idx.GameVersionTypeID > 0 {
			set[idx.GameVersionTypeID] = struct{}{}
		}
	}
	for _, f := range data.LatestFiles {
		if set := out[f.ID]; len(set) == 0 {
			out[f.ID] = sortableFlavors(f, vocab)
		}
	}
	return out
}

// wildcard reports whether the set constrains nothing: no loader is known, or
// one of them is "Any".
func (s loaderSet) wildcard() bool {
	if len(s) == 0 {
		return true
	}
	_, any := s[ModLoaderAny]
	return any
}

// matches reports whether a file with loaders s fits an install with loaders
// want: a wildcard on either side is never a mismatch.
func (s loaderSet) matches(want loaderSet) bool {
	return s.wildcard() || want.wildcard() || s.intersects(want)
}

// loaderByLowerName is CurseForge's own vocabulary for the loader entries it
// puts in a file's gameVersions / sortableGameVersions, keyed by lower-cased
// name (the ModLoader values of FileIndex.ModLoader).
var loaderByLowerName = map[string]int{
	"forge":      ModLoaderForge,
	"cauldron":   ModLoaderCauldron,
	"liteloader": ModLoaderLiteLoader,
	"fabric":     ModLoaderFabric,
	"quilt":      ModLoaderQuilt,
	"neoforge":   ModLoaderNeoForge,
}

// loaderByName maps one of CurseForge's loader names to its ModLoader value.
func loaderByName(name string) (int, bool) {
	l, ok := loaderByLowerName[strings.ToLower(strings.TrimSpace(name))]
	return l, ok
}

// loaderVocabulary is every real (non-Any) modLoader latestFilesIndexes names
// for the mod. Empty means the mod has no loader dimension.
func loaderVocabulary(data Mod) loaderSet {
	vocab := loaderSet{}
	for _, idx := range data.LatestFilesIndexes {
		if idx.ModLoader > ModLoaderAny {
			vocab[idx.ModLoader] = struct{}{}
		}
	}
	return vocab
}

// namedLoaders is the loaders f names for itself, in the gameVersions and
// sortableGameVersions entries CurseForge fills in for every file.
func namedLoaders(f File) loaderSet {
	out := loaderSet{}
	for _, n := range f.GameVersions {
		if l, ok := loaderByName(n); ok {
			out[l] = struct{}{}
		}
	}
	for _, v := range f.SortableGameVersions {
		if l, ok := loaderByName(v.GameVersionName); ok {
			out[l] = struct{}{}
		}
	}
	return out
}

// fileLoaders maps each file id the document names to its loaders: those
// latestFilesIndexes assign it, else the ones the file names itself. Only
// meaningful for a mod with a loader dimension (loaderVocabulary non-empty).
func fileLoaders(data Mod) map[int]loaderSet {
	out := make(map[int]loaderSet, len(data.LatestFiles))
	for _, idx := range data.LatestFilesIndexes {
		set := out[idx.FileID]
		if set == nil {
			set = loaderSet{}
			out[idx.FileID] = set
		}
		set[idx.ModLoader] = struct{}{}
	}
	for _, f := range data.LatestFiles {
		if len(out[f.ID]) == 0 {
			out[f.ID] = namedLoaders(f)
		}
	}
	return out
}

// installableFiles is latestFiles without server packs, which are never what
// a player's install follows; when nothing else is listed it is all of them.
func installableFiles(files []File) []File {
	out := make([]File, 0, len(files))
	for _, f := range files {
		if !f.IsServerPack {
			out = append(out, f)
		}
	}
	if len(out) == 0 {
		return files
	}
	return out
}

// newestFile is the most recently uploaded of files: latest FileDate, ties to
// the higher (later) file id, then to the earlier entry. Nil for none.
func newestFile(files []File) *File {
	var best *File
	for i := range files {
		f := &files[i]
		switch {
		case best == nil:
			best = f
		case f.FileDate.After(best.FileDate):
			best = f
		case f.FileDate.Equal(best.FileDate) && f.ID > best.ID:
			best = f
		}
	}
	return best
}

// fileVersion is the version a file is shown as.
func fileVersion(f File) string {
	return extractVersion(f.DisplayName, f.FileName)
}

// installedFileIDs parses an installed mod's recorded source file ids,
// ignoring any that are not CurseForge ids.
func installedFileIDs(ids []string) []int {
	var out []int
	for _, s := range ids {
		if id, err := strconv.Atoi(s); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

// latestUpdate returns the file inst should be offered as its update, or nil
// when it has none. A non-nil error means the check could not be made for this
// mod (the caller reports it once and moves on); a context error is returned
// as such.
//
// With recorded file ids, a candidate is an update only if its file id is
// greater than the newest installed one, matches the installed file on every
// dimension the mod's latestFilesIndexes classify files by (game flavor, and
// mod loader where the mod has loaders; a loader of 0 "Any", or none known, on
// either side is a wildcard), and is no less stable a release type (a release
// install is not offered a beta). The installed file's classes come from its
// own entry in latestFiles / latestFilesIndexes - present while it is still the
// newest of its class. A superseded install (exactly the case with a real
// update) is in neither, so when the mod's index spans more than one value on
// any dimension and a newer file exists, they are read from one GetModFile
// (its gameVersions / sortableGameVersions name the flavor and the loader); a
// mod that is unambiguous on every dimension, or has no newer file at all,
// costs nothing extra.
//
// Without recorded file ids (older installs, imports) there is no identity to
// compare, so it falls back to the shared version comparator: the newest
// file whose version compares newer than the installed one.
func (c *CurseForge) latestUpdate(ctx context.Context, data Mod, inst domain.InstalledMod) (*File, error) {
	files := installableFiles(data.LatestFiles)

	installed := installedFileIDs(inst.FileIDs)
	if len(installed) == 0 {
		var newer []File
		for _, f := range files {
			if v := fileVersion(f); v != "" && domain.IsNewerVersion(inst.Version, v) {
				newer = append(newer, f)
			}
		}
		return newestFile(newer), nil
	}
	maxInstalled := slices.Max(installed)

	if !slices.ContainsFunc(files, func(f File) bool { return f.ID > maxInstalled }) {
		return nil, nil
	}

	vocab := flavorVocabulary(data)
	flavors := fileFlavors(data, vocab)

	// The loader dimension exists only for a mod whose index names real loaders.
	loaderVocab := loaderVocabulary(data)
	var loaders map[int]loaderSet
	if len(loaderVocab) > 0 {
		loaders = fileLoaders(data)
	}

	var want flavorSet
	var wantLoaders loaderSet
	restrict := false
	installedRelease := 0
	known := false
	for _, id := range installed {
		if fs, ok := flavors[id]; ok {
			known = true
			if want == nil {
				want = flavorSet{}
			}
			for t := range fs {
				want[t] = struct{}{}
			}
		}
		if ls, ok := loaders[id]; ok {
			if wantLoaders == nil {
				wantLoaders = loaderSet{}
			}
			for l := range ls {
				wantLoaders[l] = struct{}{}
			}
			if ls.wildcard() {
				wantLoaders[ModLoaderAny] = struct{}{}
			}
		}
	}
	if known {
		restrict = len(want) > 0
		for _, f := range data.LatestFiles {
			if f.ID == maxInstalled {
				installedRelease = f.ReleaseType
			}
		}
		for _, idx := range data.LatestFilesIndexes {
			if idx.FileID == maxInstalled && installedRelease == 0 {
				installedRelease = idx.ReleaseType
			}
		}
	} else if len(vocab) > 1 || len(loaderVocab) > 1 {
		modID, err := strconv.Atoi(inst.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid mod ID: %w", err)
		}
		f, err := c.client.GetModFile(ctx, modID, maxInstalled)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			return nil, fmt.Errorf("looking up installed file %d to learn its game flavor and mod loader: %w", maxInstalled, err)
		}
		if len(vocab) > 1 {
			want = sortableFlavors(*f, vocab)
			restrict = true // a flavor no longer published offers nothing
		}
		if len(loaderVocab) > 0 {
			wantLoaders = namedLoaders(*f)
		}
		installedRelease = f.ReleaseType
	}

	var candidates []File
	for _, f := range files {
		if f.ID <= maxInstalled {
			continue
		}
		if restrict && !flavors[f.ID].intersects(want) {
			continue
		}
		if len(loaderVocab) > 0 && !loaders[f.ID].matches(wantLoaders) {
			continue
		}
		if installedRelease > 0 && f.ReleaseType > installedRelease {
			continue
		}
		if fileVersion(f) == "" {
			continue
		}
		candidates = append(candidates, f)
	}
	return newestFile(candidates), nil
}
