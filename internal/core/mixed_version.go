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

// mixedVersionSelection returns a *MixedVersionSelectionError when two of
// files' main files are copies of one file at different versions, else nil.
// The grouping follows the files' own order, so the message names versions
// as the selection did.
//
// Only main files are compared (#662): an optional, update or
// miscellaneous file (domain.IsSecondaryFileCategory, the source's own
// category) may carry its own version beside any main file, and is not
// named. A file with no category counts as main, so a source that does not
// classify its files keeps #647's rule. And only copies of one file
// conflict (domain.SameFile): an author may version a mod's main
// components separately ("Core 1.2" beside "Textures 1.0"), and those
// install together.
func mixedVersionSelection(files []domain.DownloadableFile) error {
	ptrs := make([]*domain.DownloadableFile, len(files))
	for i := range files {
		ptrs[i] = &files[i]
	}
	if versions := copyConflicts(ptrs); len(versions) > 1 {
		return &MixedVersionSelectionError{Versions: versions}
	}
	return nil
}

// isVersionedMain reports whether f is a main file that carries a version:
// the only kind a one-version rule compares.
func isVersionedMain(f *domain.DownloadableFile) bool {
	return f != nil && f.Version != "" && !domain.IsSecondaryFileCategory(f.Category)
}

// copyGroups groups files' versioned main files by domain.FileCopyKey -
// each group one file's copies - in the order each first appears.
func copyGroups(files []*domain.DownloadableFile) [][]*domain.DownloadableFile {
	var groups [][]*domain.DownloadableFile
	index := make(map[string]int)
	for _, f := range files {
		if !isVersionedMain(f) {
			continue
		}
		key := domain.FileCopyKey(*f)
		i, ok := index[key]
		if !ok {
			i = len(groups)
			index[key] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], f)
	}
	return groups
}

// spansVersions reports whether group's files carry more than one version.
func spansVersions(group []*domain.DownloadableFile) bool {
	return slices.ContainsFunc(group, func(f *domain.DownloadableFile) bool { return f.Version != group[0].Version })
}

// copyConflicts is files' main files that are copies of one file at
// different versions (#662), grouped by version in the files' own order -
// what #647's rule refuses at an install. Empty when there is none.
func copyConflicts(files []*domain.DownloadableFile) []MixedVersion {
	var conflicting []*domain.DownloadableFile
	for _, g := range copyGroups(files) {
		if spansVersions(g) {
			conflicting = append(conflicting, g...)
		}
	}
	// In the files' own order, not group by group.
	slices.SortStableFunc(conflicting, func(a, b *domain.DownloadableFile) int {
		return cmp.Compare(slices.Index(files, a), slices.Index(files, b))
	})
	return byVersion(conflicting)
}

// byVersion groups files by version, in the files' own order.
func byVersion(files []*domain.DownloadableFile) []MixedVersion {
	var versions []MixedVersion
	index := make(map[string]int)
	for _, f := range files {
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
// or its installed row's - hold main files of more than one version
// (#662): recorded before #647 refused such a selection, or edited in by
// hand. A flow that replays them (profile apply, switch and import, a
// deploy's or an update's download) never refuses the mod.
//
// Only copies of one file (domain.SameFile) at different versions
// conflict: of each such file the copy at the reference's version stays
// (the newest copy, when none is at it) and the others are Dropped. A main
// file that is a separate component at another version is Kept, and said
// so; every optional, update and miscellaneous file stays. The plan names
// the mod, and the flow records the ids it kept on the reference, so the
// next run finds nothing to drop.
//
// A collection's own reference (from_collection) is never healed: the
// curator chose those files; see CuratorMixedFiles.
type StoredFilesHeal struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the mod's display name, when the source described it.
	Name string `json:"name,omitempty"`
	// Version is the version the mod records once healed: the reference's
	// own while a main file it keeps carries it.
	Version string `json:"version"`
	// RecordedVersion is set only when no main file the heal keeps is at
	// the version the reference records (or it records none): Version is
	// then the newest of theirs, and RecordedVersion the one it replaces.
	RecordedVersion string `json:"recorded_version,omitempty"`
	// Dropped is each main file dropped because a copy of the same file at
	// another version stays, by version, in the source's listing order.
	// Absent when only separate components were found (Kept).
	Dropped []MixedVersion `json:"dropped,omitempty"`
	// Kept is each main file kept at another version than Version because
	// it is a separate component, not a copy of a file the mod has at
	// Version, by version. Absent when there is none.
	Kept []MixedVersion `json:"kept,omitempty"`
}

// CuratorMixedFiles is a collection's mod (a from_collection reference)
// whose files hold copies of one main file at more than one version
// (#662). The curator chose and tested exactly these files, so lmm
// installs them as they are and only says so; a user's own reference is
// healed instead (StoredFilesHeal).
type CuratorMixedFiles struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the mod's display name, when the source described it.
	Name string `json:"name,omitempty"`
	// Versions is each version the copies span, with its files. Always
	// two or more.
	Versions []MixedVersion `json:"versions"`
}

// storedSelection is selectStoredFiles' answer.
type storedSelection struct {
	// files is what to download, in the source's listing order.
	files []*domain.DownloadableFile
	// version is the version the mod records for them (#94).
	version string
	// heal is set when a user's reference was healed or kept a separate
	// component at another version; mixed when a collection's reference
	// installs the curator's copies at several versions as they are.
	// Neither carries the mod's identity: the caller adds it.
	heal  *StoredFilesHeal
	mixed []MixedVersion
}

// selectStoredFiles is selectFilesForVersion for a mod's stored file ids,
// with #662's rules on top:
//
//   - A collection's reference (fromCollection) whose stored files are all
//     still offered installs exactly those files, whatever versions they
//     carry: the curator's choice. Copies at several versions are reported
//     in mixed, not healed.
//   - A user's reference whose stored main files hold copies of one file
//     at different versions is healed (healStoredMains): the dropped copies
//     leave the ids before selectFilesForVersion resolves them.
//
// The version the mod records stays the stored one (version) while any
// selected file carries it, so a record stamped by an older rule keeps its
// cache entry and the next converge sees no drift; else the #94 stamp,
// whose last fallback is modVersion, the mod-level version.
func selectStoredFiles(files []domain.DownloadableFile, storedIDs []string, version string, fromCollection bool, modVersion string) (storedSelection, error) {
	found := storedFilesFound(files, storedIDs)
	if fromCollection && len(found) > 0 && len(found) == len(uniqueIDs(storedIDs)) {
		sel := storedSelection{files: found, version: recordedVersionOf(version, modVersion, found)}
		if conflicts := copyConflicts(found); len(conflicts) > 1 {
			sel.mixed = conflicts
		}
		return sel, nil
	}
	var heal *StoredFilesHeal
	if !fromCollection {
		if heal = healStoredMains(found, version); heal != nil {
			dropped := droppedIDs(heal)
			storedIDs = slices.DeleteFunc(slices.Clone(storedIDs), func(id string) bool { return dropped[id] })
			version = heal.Version
		}
	}
	selected, err := selectFilesForVersion(files, storedIDs, version)
	if err != nil {
		return storedSelection{}, err
	}
	return storedSelection{files: selected, version: recordedVersionOf(version, modVersion, selected), heal: heal}, nil
}

// recordedVersionOf is the version to record for selected: version while
// any of them carries it, else domain.EffectiveInstalledVersion's stamp.
func recordedVersionOf(version, modVersion string, selected []*domain.DownloadableFile) string {
	if version != "" && slices.ContainsFunc(selected, func(f *domain.DownloadableFile) bool { return f.Version == version }) {
		return version
	}
	return domain.EffectiveInstalledVersion(modVersion, selected)
}

// droppedIDs is the ids a heal drops.
func droppedIDs(heal *StoredFilesHeal) map[string]bool {
	dropped := make(map[string]bool)
	for _, v := range heal.Dropped {
		for _, f := range v.Files {
			dropped[f.ID] = true
		}
	}
	return dropped
}

// healStoredMains decides #662's heal over a stored set's files (found):
// of each main file with copies at different versions, the copy at
// recorded stays - else the newest copy - and the rest are dropped; a main
// file left at another version than the one the mod then records is a
// separate component, kept. Nil when nothing is dropped and no separate
// component is at another version.
func healStoredMains(found []*domain.DownloadableFile, recorded string) *StoredFilesHeal {
	var kept, dropped []*domain.DownloadableFile
	for _, g := range copyGroups(found) {
		if !spansVersions(g) {
			kept = append(kept, g...)
			continue
		}
		keep := ""
		for _, f := range g {
			if f.Version == recorded {
				keep = recorded
			}
		}
		if keep == "" {
			keep = g[0].Version
			for _, f := range g[1:] {
				if domain.CompareVersions(f.Version, keep) > 0 {
					keep = f.Version
				}
			}
		}
		for _, f := range g {
			if f.Version == keep {
				kept = append(kept, f)
			} else {
				dropped = append(dropped, f)
			}
		}
	}
	heal := &StoredFilesHeal{Version: recorded}
	if !slices.ContainsFunc(kept, func(f *domain.DownloadableFile) bool { return f.Version == recorded }) && len(kept) > 0 {
		heal.Version = kept[0].Version
		for _, f := range kept[1:] {
			if domain.CompareVersions(f.Version, heal.Version) > 0 {
				heal.Version = f.Version
			}
		}
		heal.RecordedVersion = recorded
	}
	var others []*domain.DownloadableFile
	for _, f := range kept {
		if f.Version != heal.Version {
			others = append(others, f)
		}
	}
	if len(dropped) == 0 && len(others) == 0 {
		return nil
	}
	if len(dropped) == 0 {
		// Nothing dropped: a note only, and the record keeps its version.
		heal.Version, heal.RecordedVersion = recorded, ""
	}
	inOrder := func(fs []*domain.DownloadableFile) []*domain.DownloadableFile {
		slices.SortStableFunc(fs, func(a, b *domain.DownloadableFile) int {
			return cmp.Compare(slices.Index(found, a), slices.Index(found, b))
		})
		return fs
	}
	heal.Dropped = byVersion(inOrder(dropped))
	heal.Kept = byVersion(inOrder(others))
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

// Sentence is the heal as one line, for a warning or a plan's text: "Mod
// X: its stored main files are of more than one version; keeping version
// 2.0 and dropping a.zip (1.0) (reinstall the mod at a dropped file's
// version to use it instead); keeping c.zip (1.0), a separate component".
func (h StoredFilesHeal) Sentence() string {
	name := cmp.Or(h.Name, domain.ModKey(h.SourceID, h.ModID))
	files := func(versions []MixedVersion) string {
		var out []string
		for _, v := range versions {
			for _, f := range v.Files {
				out = append(out, fmt.Sprintf("%s (%s)", cmp.Or(f.FileName, f.ID), v.Version))
			}
		}
		return strings.Join(out, ", ")
	}
	var parts []string
	if len(h.Dropped) > 0 {
		keep := fmt.Sprintf("keeping version %s", h.Version)
		if h.RecordedVersion != "" {
			keep = fmt.Sprintf("keeping the newest, %s, since none is at the recorded version %s", h.Version, h.RecordedVersion)
		}
		parts = append(parts, fmt.Sprintf("%s and dropping %s (reinstall the mod at a dropped file's version to use it instead)", keep, files(h.Dropped)))
	}
	if len(h.Kept) > 0 {
		parts = append(parts, fmt.Sprintf("keeping %s, a separate component", files(h.Kept)))
	}
	return fmt.Sprintf("%s: its stored main files are of more than one version; %s", name, strings.Join(parts, "; "))
}

// Sentence is the note as one line: "Mod X: the collection's files mix
// main files of versions 1.0 (a.zip) and 2.0 (b.zip); installed as the
// curator chose them".
func (c CuratorMixedFiles) Sentence() string {
	name := cmp.Or(c.Name, domain.ModKey(c.SourceID, c.ModID))
	return fmt.Sprintf("%s: the collection's files mix main files of versions %s; installed as the curator chose them", name, mixedVersionFilesText(c.Versions))
}

// storedFilesLookupLimit bounds the source lookups one switch or import
// plan makes to preview #662's heals: the rest are decided, and warned
// about, when the run reaches them.
const storedFilesLookupLimit = 25

// previewStoredFiles is what a plan that resolves nothing at plan time
// (switch, import) says about refs' stored file ids (#662): for every user
// ref with more than one stored file, the only kind whose main files can
// hold copies at several versions, the source is asked for the mod's file
// listing and selectStoredFiles' heal taken - at most
// storedFilesLookupLimit of them, unchecked counting the rest. A
// collection's ref is skipped: it is never healed, and the run itself
// notes its curator's files. Best-effort: a mod the source cannot describe
// now is left out, and the run warns about its heal instead
// (unpreviewedStoredFiles). refs carry the ids the run will replay.
func (s *Service) previewStoredFiles(ctx context.Context, game *domain.Game, refs []domain.ModReference) (heals []StoredFilesHeal, unchecked int) {
	looked := 0
	for _, ref := range refs {
		if ref.FromCollection || len(uniqueIDs(ref.FileIDs)) < 2 {
			continue
		}
		if looked == storedFilesLookupLimit || ctx.Err() != nil {
			unchecked++
			continue
		}
		looked++
		mod, err := s.GetMod(ctx, ref.SourceID, game.ID, ref.ModID)
		if err != nil {
			continue
		}
		files, err := s.GetModFiles(ctx, game, ref.SourceID, mod)
		if err != nil {
			continue
		}
		sel, err := selectStoredFiles(files, ref.FileIDs, ref.Version, false, mod.Version)
		if err != nil || sel.heal == nil {
			continue
		}
		sel.heal.SourceID, sel.heal.ModID, sel.heal.Name = ref.SourceID, ref.ModID, mod.Name
		heals = append(heals, *sel.heal)
	}
	return heals, unchecked
}

// uncheckedStoredFilesNote is a plan's note for the refs previewStoredFiles
// left unchecked.
func uncheckedStoredFilesNote(n int) string {
	return fmt.Sprintf("%d more mod(s) with several stored files were not looked up for this plan; a mod among them whose stored main files hold copies of one file at different versions is healed when the run reaches it, with a warning", n)
}

// unpreviewedStoredFiles is the warning for a heal that drops a file, or a
// collection note, that a flow's resolution found and its plan did not
// name (#662): the plan could not look the mod up, or did not (a bounded
// or collection preview), or the source's listing changed since. A dropped
// file is never silent; one the plan named is not said twice. A separate
// component kept at another version drops nothing, so it is not warned
// about.
func unpreviewedStoredFiles(sel storedSelection, ref domain.ModReference, name string, heals []StoredFilesHeal, mixed []CuratorMixedFiles) []string {
	var out []string
	if sel.heal != nil && len(sel.heal.Dropped) > 0 && !slices.ContainsFunc(heals, func(h StoredFilesHeal) bool { return h.SourceID == ref.SourceID && h.ModID == ref.ModID }) {
		h := *sel.heal
		h.SourceID, h.ModID, h.Name = ref.SourceID, ref.ModID, name
		out = append(out, h.Sentence())
	}
	if len(sel.mixed) > 0 && !slices.ContainsFunc(mixed, func(c CuratorMixedFiles) bool { return c.SourceID == ref.SourceID && c.ModID == ref.ModID }) {
		out = append(out, CuratorMixedFiles{SourceID: ref.SourceID, ModID: ref.ModID, Name: name, Versions: sel.mixed}.Sentence())
	}
	return out
}

// fileCopyKeys is InstallPlan.FileCopies for files: each file's
// domain.FileCopyKey by id, or nil when their main files share one key.
func fileCopyKeys(files []domain.DownloadableFile) map[string]string {
	keys := make(map[string]string, len(files))
	mainKeys := make(map[string]bool)
	for _, f := range files {
		keys[f.ID] = domain.FileCopyKey(f)
		if !domain.IsSecondaryFileCategory(f.Category) {
			mainKeys[keys[f.ID]] = true
		}
	}
	if len(mainKeys) < 2 {
		return nil
	}
	return keys
}
