package core

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// MixedVersionFilesCode is the "code" a MixedVersionSelectionError's
// --json details carry (#647), for a script that branches on the refusal
// rather than on its sentence.
const MixedVersionFilesCode = "mixed_version_files"

// MixedVersionSelectionError refuses an install selection whose files
// belong to more than one version of the mod (#647). An installed mod
// records one version, and its cache entry is that version's tree: files of
// two versions would leave a tree the record does not describe, which
// update and rollback then reason about wrongly. Nothing is downloaded or
// written; the remedy is to pick the files of one version.
//
// A file whose source reports no version belongs to no version: it joins a
// selection of any one version and never makes a selection mixed, so it is
// never named here either.
type MixedVersionSelectionError struct {
	// Versions lists each version the selection spans, in the order the
	// selection first names it, with the selected files of that version.
	// Always two or more.
	Versions []MixedVersion
}

// MixedVersion is one version a refused selection spans, and its files.
type MixedVersion struct {
	Version string             `json:"version"`
	Files   []MixedVersionFile `json:"files"`
}

// MixedVersionFile is one selected file, by its source id and file name
// (the display name when the source reports no file name).
type MixedVersionFile struct {
	ID       string `json:"id"`
	FileName string `json:"file_name,omitempty"`
}

// Error names each version and its files: "these files belong to versions
// 1.0 (a.zip) and 2.0 (b.zip); pick one version".
func (e *MixedVersionSelectionError) Error() string {
	return fmt.Sprintf("these files belong to versions %s; pick one version", mixedVersionFilesText(e.Versions))
}

// Details implements the --json error envelope's extension point: the
// refusal's code and every version with its files.
func (e *MixedVersionSelectionError) Details() any {
	return mixedVersionSelectionDetails{Code: MixedVersionFilesCode, Versions: e.Versions}
}

// mixedVersionSelectionDetails is MixedVersionSelectionError's wire shape.
type mixedVersionSelectionDetails struct {
	Code     string         `json:"code"`
	Versions []MixedVersion `json:"versions"`
}

// mixedVersionSelection returns a *MixedVersionSelectionError when files'
// main files span more than one non-empty version, else nil. The grouping
// follows the files' own order, so the message names versions as the
// selection did.
//
// Only main files are compared (#662): an optional, update or
// miscellaneous file (domain.IsSecondaryFileCategory, the source's own
// category) may carry its own version beside any main file, and is not
// named. A file with no category counts as main, so a source that does not
// classify its files keeps #647's rule for every file.
func mixedVersionSelection(files []domain.DownloadableFile) error {
	ptrs := make([]*domain.DownloadableFile, len(files))
	for i := range files {
		ptrs[i] = &files[i]
	}
	versions := mainFileVersions(ptrs)
	if len(versions) < 2 {
		return nil
	}
	return &MixedVersionSelectionError{Versions: versions}
}

// mainFileVersions groups files' versioned main files by version, in the
// files' own order (#662). A file with no version, or a secondary one, is
// in no group.
func mainFileVersions(files []*domain.DownloadableFile) []MixedVersion {
	var versions []MixedVersion
	index := make(map[string]int)
	for _, f := range files {
		if f == nil || f.Version == "" || domain.IsSecondaryFileCategory(f.Category) {
			continue
		}
		i, ok := index[f.Version]
		if !ok {
			i = len(versions)
			index[f.Version] = i
			versions = append(versions, MixedVersion{Version: f.Version})
		}
		versions[i].Files = append(versions[i].Files, mixedVersionFileOf(f))
	}
	return versions
}

// mixedVersionFileOf names f by its id and file name (its display name when
// the source reports no file name).
func mixedVersionFileOf(f *domain.DownloadableFile) MixedVersionFile {
	name := f.FileName
	if name == "" {
		name = f.Name
	}
	return MixedVersionFile{ID: f.ID, FileName: name}
}

// StoredFilesHeal is a mod whose stored file ids - a profile reference's,
// or its installed row's - still hold main files of more than one version
// (#662): recorded before #647 refused such a selection, or edited in by
// hand. A flow that replays them (profile apply, switch and import, a
// deploy's or an update's download) never refuses the mod: it keeps the
// main files of one version, and every optional, update and miscellaneous
// file, and drops the other main files. The plan names it, and the flow
// records the healed file ids on the reference, so the next run finds
// nothing to heal.
//
// A collection's own reference (from_collection) is never healed: the
// curator chose those files; see CuratorMixedFiles.
type StoredFilesHeal struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the mod's display name, when the source described it.
	Name string `json:"name,omitempty"`
	// Version is the version whose main files are kept: the version the
	// reference records.
	Version string `json:"version"`
	// RecordedVersion is set only when none of the stored main files is
	// at the version the reference records (or it records none): Version
	// is then the newest of their versions, and RecordedVersion the one
	// the reference recorded, which the heal replaces.
	RecordedVersion string `json:"recorded_version,omitempty"`
	// Dropped is the main files of every other version, by version, in
	// the source's listing order.
	Dropped []MixedVersion `json:"dropped"`
}

// CuratorMixedFiles is a collection's mod (a from_collection reference)
// whose files mix main files of more than one version (#662). The
// curator chose and tested exactly these files, so lmm installs them as
// they are and only says so; a user's own reference is healed instead
// (StoredFilesHeal).
type CuratorMixedFiles struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the mod's display name, when the source described it.
	Name string `json:"name,omitempty"`
	// Versions is each version the curator's main files span, with its
	// files. Always two or more.
	Versions []MixedVersion `json:"versions"`
}

// storedSelection is selectStoredFiles' answer.
type storedSelection struct {
	// files is what to download, in the source's listing order.
	files []*domain.DownloadableFile
	// version is the version the mod records for them (#94).
	version string
	// heal is set when a user's reference was healed; mixed when a
	// collection's reference installs the curator's mixed files as they
	// are. Neither carries the mod's identity: the caller adds it.
	heal  *StoredFilesHeal
	mixed []MixedVersion
}

// selectStoredFiles is selectFilesForVersion for a mod's stored file ids,
// with #662's rules on top:
//
//   - A collection's reference (fromCollection) whose stored files are all
//     still offered installs exactly those files, whatever versions they
//     carry: the curator's choice. Mixed main versions are reported in
//     mixed, not healed. Its version stays the one the reference records
//     while any of its files carries it, so the record the next converge
//     compares against does not move.
//   - A user's reference whose stored main files span more than one
//     version is healed: the main files of the recorded version stay, else
//     those of the newest version, every secondary file stays, and the
//     rest of the main files are dropped from the ids before
//     selectFilesForVersion resolves them.
//
// modVersion is the mod-level version, the #94 stamp's last fallback.
func selectStoredFiles(files []domain.DownloadableFile, storedIDs []string, version string, fromCollection bool, modVersion string) (storedSelection, error) {
	found := storedFilesFound(files, storedIDs)
	mains := mainFileVersions(found)
	if fromCollection && len(found) > 0 && len(found) == len(uniqueIDs(storedIDs)) {
		sel := storedSelection{files: found, version: domain.EffectiveInstalledVersion(modVersion, found)}
		if version != "" && slices.ContainsFunc(found, func(f *domain.DownloadableFile) bool { return f.Version == version }) {
			sel.version = version
		}
		if len(mains) > 1 {
			sel.mixed = mains
		}
		return sel, nil
	}
	var heal *StoredFilesHeal
	if !fromCollection && len(mains) > 1 {
		heal = healMainVersions(mains, version)
		dropped := make(map[string]bool)
		for _, v := range heal.Dropped {
			for _, f := range v.Files {
				dropped[f.ID] = true
			}
		}
		storedIDs = slices.DeleteFunc(slices.Clone(storedIDs), func(id string) bool { return dropped[id] })
		version = heal.Version
	}
	selected, err := selectFilesForVersion(files, storedIDs, version)
	if err != nil {
		return storedSelection{}, err
	}
	return storedSelection{files: selected, version: domain.EffectiveInstalledVersion(modVersion, selected), heal: heal}, nil
}

// healMainVersions decides a heal over mains (two or more versions): keep
// the recorded version's main files, else the newest version's.
func healMainVersions(mains []MixedVersion, recorded string) *StoredFilesHeal {
	keep := -1
	for i, v := range mains {
		if v.Version == recorded {
			keep = i
		}
	}
	heal := &StoredFilesHeal{}
	if keep < 0 {
		keep = 0
		for i := 1; i < len(mains); i++ {
			if domain.CompareVersions(mains[i].Version, mains[keep].Version) > 0 {
				keep = i
			}
		}
		heal.RecordedVersion = recorded
	}
	heal.Version = mains[keep].Version
	for i, v := range mains {
		if i != keep {
			heal.Dropped = append(heal.Dropped, v)
		}
	}
	return heal
}

// storedFilesFound is the listed files whose ids are among ids, in the
// listing's order.
func storedFilesFound(files []domain.DownloadableFile, ids []string) []*domain.DownloadableFile {
	if len(ids) == 0 {
		return nil
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var found []*domain.DownloadableFile
	for i := range files {
		if want[files[i].ID] {
			found = append(found, &files[i])
		}
	}
	return found
}

// uniqueIDs is ids without repeats.
func uniqueIDs(ids []string) map[string]bool {
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// mixedVersionFilesText is versions as a sentence fragment: "1.0
// (a.zip) and 2.0 (b.zip)" - MixedVersionSelectionError's own list.
func mixedVersionFilesText(versions []MixedVersion) string {
	parts := make([]string, len(versions))
	for i, v := range versions {
		names := make([]string, len(v.Files))
		for j, f := range v.Files {
			names[j] = cmp.Or(f.FileName, f.ID)
		}
		parts[i] = fmt.Sprintf("%s (%s)", v.Version, strings.Join(names, ", "))
	}
	if n := len(parts); n > 1 {
		return strings.Join(parts[:n-1], ", ") + " and " + parts[n-1]
	}
	return parts[0]
}

// Sentence is the heal as one line, for a warning or a plan's text:
// "Mod X: its stored files mix main files of versions ...; keeping 2.0 and
// dropping a.zip (1.0)".
func (h StoredFilesHeal) Sentence() string {
	name := cmp.Or(h.Name, domain.ModKey(h.SourceID, h.ModID))
	var dropped []string
	for _, v := range h.Dropped {
		for _, f := range v.Files {
			dropped = append(dropped, fmt.Sprintf("%s (%s)", cmp.Or(f.FileName, f.ID), v.Version))
		}
	}
	keep := fmt.Sprintf("keeping version %s", h.Version)
	if h.RecordedVersion != "" {
		keep = fmt.Sprintf("keeping the newest, %s, since none is at the recorded version %s", h.Version, h.RecordedVersion)
	} else if h.Version == "" {
		keep = "keeping the newest"
	}
	return fmt.Sprintf("%s: its stored main files are of more than one version; %s and dropping %s", name, keep, strings.Join(dropped, ", "))
}

// Sentence is the note as one line: "Mod X: the collection's files mix
// main files of versions 1.0 (a.zip) and 2.0 (b.zip); installed as the
// curator chose them".
func (c CuratorMixedFiles) Sentence() string {
	name := cmp.Or(c.Name, domain.ModKey(c.SourceID, c.ModID))
	return fmt.Sprintf("%s: the collection's files mix main files of versions %s; installed as the curator chose them", name, mixedVersionFilesText(c.Versions))
}

// previewStoredFiles is what a plan that resolves nothing at plan time
// (switch, import) says about refs' stored file ids (#662): for every ref
// with more than one stored file, the only kind whose main files can mix,
// the source is asked for the mod's file listing and selectStoredFiles'
// heal or collection note taken. Best-effort: a mod the source cannot
// describe now is left out, and the flow's own resolution warns about its
// heal instead (unpreviewedStoredFiles). refs carry the ids the flow will
// replay.
func (s *Service) previewStoredFiles(ctx context.Context, game *domain.Game, refs []domain.ModReference) ([]StoredFilesHeal, []CuratorMixedFiles) {
	var heals []StoredFilesHeal
	var mixed []CuratorMixedFiles
	for _, ref := range refs {
		if len(uniqueIDs(ref.FileIDs)) < 2 || ctx.Err() != nil {
			continue
		}
		mod, err := s.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
		if err != nil {
			continue
		}
		files, err := s.GetModFiles(ctx, game, ref.SourceID, mod)
		if err != nil {
			continue
		}
		sel, err := selectStoredFiles(files, ref.FileIDs, ref.Version, ref.FromCollection, mod.Version)
		if err != nil {
			continue
		}
		if sel.heal != nil {
			sel.heal.SourceID, sel.heal.ModID, sel.heal.Name = ref.SourceID, ref.ModID, mod.Name
			heals = append(heals, *sel.heal)
		}
		if len(sel.mixed) > 0 {
			mixed = append(mixed, CuratorMixedFiles{SourceID: ref.SourceID, ModID: ref.ModID, Name: mod.Name, Versions: sel.mixed})
		}
	}
	return heals, mixed
}

// unpreviewedStoredFiles is the warning for a heal or a collection note a
// flow's resolution found that its plan did not name (#662): the plan
// could not look the mod up, or the source's listing changed since. A heal
// is never silent; one the plan named is not said twice.
func unpreviewedStoredFiles(sel storedSelection, ref domain.ModReference, name string, heals []StoredFilesHeal, mixed []CuratorMixedFiles) []string {
	var out []string
	if sel.heal != nil && !slices.ContainsFunc(heals, func(h StoredFilesHeal) bool { return h.SourceID == ref.SourceID && h.ModID == ref.ModID }) {
		h := *sel.heal
		h.SourceID, h.ModID, h.Name = ref.SourceID, ref.ModID, name
		out = append(out, h.Sentence())
	}
	if len(sel.mixed) > 0 && !slices.ContainsFunc(mixed, func(c CuratorMixedFiles) bool { return c.SourceID == ref.SourceID && c.ModID == ref.ModID }) {
		out = append(out, CuratorMixedFiles{SourceID: ref.SourceID, ModID: ref.ModID, Name: name, Versions: sel.mixed}.Sentence())
	}
	return out
}

// staleUpdateMains is an update's heal (#662) over an installed set's main
// versions: the main files of every version but the installed one (which
// the update replaces) and the target (which already is the update) are
// stale. Nil when the set mixes no versions or none is stale.
func staleUpdateMains(mains []MixedVersion, installed, target string) *StoredFilesHeal {
	if len(mains) < 2 {
		return nil
	}
	heal := &StoredFilesHeal{Version: installed}
	for _, v := range mains {
		if v.Version != installed && v.Version != target {
			heal.Dropped = append(heal.Dropped, v)
		}
	}
	if len(heal.Dropped) == 0 {
		return nil
	}
	return heal
}
