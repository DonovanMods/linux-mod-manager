package curseforge

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// CurseForge's latestFiles is NOT "the newest files, newest first". It holds
// the newest file of each game flavor (WoW Retail, Classic Era, Cata, ... -
// told apart by gameVersionTypeId), in no promised order, so its first entry
// is routinely an old file for some other flavor (#504). Everything here
// chooses the "latest" file deliberately instead of by position, and decides
// "newer" by file identity: CurseForge file ids only ever increase.

// flavorSet is a set of gameVersionTypeIds.
type flavorSet map[int]struct{}

func (s flavorSet) intersects(o flavorSet) bool {
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
// greater than the newest installed one, in the installed file's flavor and
// no less stable a release type (a release install is not offered a beta).
// The flavor comes from the installed file's own entry in latestFiles /
// latestFilesIndexes - present while it is still the newest of its flavor. A
// superseded install (exactly the case with a real update) is in neither, so
// when the mod is published for several flavors and a newer file exists, its
// flavor is read from one GetModFile; a mod with a single flavor, or no
// newer file at all, costs nothing extra.
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

	var want flavorSet
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
	} else if len(vocab) > 1 {
		modID, err := strconv.Atoi(inst.ID)
		if err != nil {
			return nil, fmt.Errorf("invalid mod ID: %w", err)
		}
		f, err := c.client.GetModFile(ctx, modID, maxInstalled)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			return nil, fmt.Errorf("looking up installed file %d to learn its game flavor: %w", maxInstalled, err)
		}
		want = sortableFlavors(*f, vocab)
		restrict = true // a flavor no longer published offers nothing
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
