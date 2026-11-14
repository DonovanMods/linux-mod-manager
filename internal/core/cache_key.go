package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"slices"
	"strconv"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// modCacheKey is the cache entry m's files live in for gameID: its source
// version AND its variant (#614). Every read of an installed mod's entry
// goes through it (or spells the same literal), because two trees of one
// version - different FOMOD choices - are two entries, and keying by the
// version alone would read the wrong one.
func modCacheKey(gameID string, m domain.Mod) cache.Key {
	return cache.Key{GameID: gameID, SourceID: m.SourceID, ModID: m.ID, Version: m.Version, Variant: m.Variant}
}

// previousCacheKey is the entry m's rollback target lives in: its previous
// version and that version's variant.
func previousCacheKey(gameID string, m *domain.InstalledMod) cache.Key {
	return cache.Key{GameID: gameID, SourceID: m.SourceID, ModID: m.ID, Version: m.PreviousVersion, Variant: m.PreviousVariant}
}

// ReinstallFilesChanged is a converge plan's reason for reinstalling a mod
// at the version it already has (#639): the profile pins another set of
// its files than the installed row records.
const ReinstallFilesChanged = "files_changed"

// fileSetVariantScheme names the file-set variant's inputs (#639).
const fileSetVariantScheme = "lmm-files-1"

// THE FILE-SET VARIANT (#639). A cache entry is keyed by the version and
// the variant, never by the source files it was built from: two sets of
// files at one version - a collection revision that swaps a file, a hand
// edit, `lmm install --file` - would share one directory, and the second
// set's download would layer onto the first's tree (prepareStaging seeds
// from it), so the old file's members stayed deployed - in every profile
// that used the entry. The entry's own completion markers say which files
// built it, so a set reuses its version's base entry (the plain entry, or
// the FOMOD variant) ONLY when the entry is absent or its markers name
// exactly that set; any other set - a subset or a superset too - gets a
// variant of its own, fileSetVariant of the base variant and the sorted
// ids. A superset re-downloads files another entry already holds: the cost
// of never growing a tree another row still deploys.
//
// An entry from before markers existed names no set. It is reused only
// when an installed row on that entry pins exactly the requested set, and
// a flow that reuses it then stamps that set's markers (backfill).
// Installed rows keep the key they recorded either way: the converge
// flows compare file sets, not keys, so a row whose files did not change
// is never reinstalled, and nothing is re-downloaded after an upgrade.

// fileSetVariant is the variant of base's tree built from exactly the files
// ids: the first 12 hex digits of a sha256 over the scheme, base and the
// sorted, de-duplicated ids. Order never matters: a FOMOD mod's layer order
// (#634) is part of base already.
func fileSetVariant(base string, ids []string) string {
	sorted := slices.Compact(slices.Sorted(slices.Values(ids)))
	h := sha256.New()
	_, _ = io.WriteString(h, fileSetVariantScheme+"\n"+base)
	for _, id := range sorted {
		_, _ = io.WriteString(h, "\n"+strconv.Quote(id))
	}
	return hex.EncodeToString(h.Sum(nil))[:12]
}

// sameFileIDs reports whether a and b name the same files, as sets: order
// and repeats never matter.
func sameFileIDs(a, b []string) bool {
	return slices.Equal(slices.Compact(slices.Sorted(slices.Values(a))), slices.Compact(slices.Sorted(slices.Values(b))))
}

// fileSetDrift reports that ref pins another set of files than row was
// installed from (#639): a converge flow reinstalls it, as it does for a
// version drift. Either side recording no files - an unpinned reference,
// a row from before lmm recorded them - is no drift: there is no set to
// compare.
func fileSetDrift(ref domain.ModReference, row *domain.InstalledMod) bool {
	return row != nil && filesDiffer(ref.FileIDs, row.FileIDs)
}

// filesDiffer is fileSetDrift over two bare lists: both record files, and
// not the same ones.
func filesDiffer(a, b []string) bool {
	return len(a) > 0 && len(b) > 0 && !sameFileIDs(a, b)
}

// fileSetEntry is the entry the files ids of k's mod and version live in
// (#639), and whether it already holds every one of them: k itself when
// k's entry is absent (or an empty, unmarked directory), names exactly ids
// in its markers, or - an entry with no markers - is used by an installed
// row that pins exactly ids;
// any other set gets k with the file-set variant of k's own. backfill,
// for a flow that commits (never a plan), stamps the markers of an
// unmarked entry it reuses, so the next read finds them.
func (s *Service) fileSetEntry(ctx context.Context, c *cache.Cache, k cache.Key, ids []string, backfill bool) (cache.Key, bool) {
	if len(ids) == 0 || !c.Exists(k) {
		return k, c.HasFileIDs(k, ids)
	}
	manifests, err := c.FileManifests(k)
	if err == nil {
		marked := slices.Collect(maps.Keys(manifests))
		if len(marked) > 0 && sameFileIDs(marked, ids) {
			return k, c.HasFileIDs(k, ids)
		}
		if len(marked) == 0 {
			// An unmarked entry with no members (a download that broke off
			// before its first commit) is no tree: as good as absent.
			if files, err := c.ListFiles(k); err == nil && len(files) == 0 {
				return k, false
			}
		}
		if len(marked) == 0 && s.rowPinsEntry(ctx, k, ids) {
			if backfill {
				backfillMarkers(c, k, ids)
			}
			return k, true
		}
	}
	k.Variant = fileSetVariant(k.Variant, ids)
	return k, c.HasFileIDs(k, ids)
}

// rowPinsEntry reports whether an installed row of k's game uses k's entry
// (its version and variant) and records exactly the files ids.
func (s *Service) rowPinsEntry(ctx context.Context, k cache.Key, ids []string) bool {
	rows, err := s.db.ModVersionRows(ctx, k.SourceID, k.ModID)
	if err != nil {
		return false
	}
	for _, row := range rows {
		if row.GameID != k.GameID || row.Version != k.Version || row.Variant != k.Variant {
			continue
		}
		if fileIDs, err := s.db.GetModFileIDs(ctx, k.SourceID, k.ModID, row.GameID, row.Profile); err == nil && sameFileIDs(fileIDs, ids) {
			return true
		}
	}
	return false
}

// backfillMarkers stamps the bare completion markers of ids into k's
// unmarked entry (#639), which an installed row pinning exactly ids says
// it holds. Best-effort: an entry it cannot stamp stays unmarked, and the
// next flow decides the same way again.
func backfillMarkers(c *cache.Cache, k cache.Key, ids []string) {
	if manifests, err := c.FileManifests(k); err != nil || len(manifests) > 0 {
		return
	}
	for _, id := range ids {
		_ = cache.MarkFileComplete(c.ModPath(k), id)
	}
}

// rowFitsRef reports whether row's tree is one ref names: the variant ref's
// recorded choices name (refVariant), or that variant's file-set variant
// (#639) of the files ref pins - or, for a ref that pins none, of the
// files row was installed from.
func rowFitsRef(row *domain.InstalledMod, ref *domain.ModReference) bool {
	base := refVariant(ref)
	ids := ref.FileIDs
	if len(ids) == 0 {
		ids = row.FileIDs
	}
	return row.Variant == base || (len(ids) > 0 && row.Variant == fileSetVariant(base, ids))
}

// restampFileSet moves a just-acquired mod's files, whose base variant
// unifyFomod has stamped, to the entry fileSetEntry names for them (#639):
// their file-set variant unless the base entry is theirs. The first file
// then commits unseeded: a fresh tree, never layered onto another's.
func (s *Service) restampFileSet(ctx context.Context, gameCache *cache.Cache, gameID string, mod *domain.Mod, files []*acquiredFile) {
	if len(files) == 0 {
		return
	}
	k, _ := s.fileSetEntry(ctx, gameCache, modCacheKey(gameID, *mod), acquiredIDs(files), false)
	moveToVariant(mod, files, k.Variant)
}

// moveToVariant stamps variant on mod and its acquired files, the first of
// them committing unseeded, unless it is already theirs.
func moveToVariant(mod *domain.Mod, files []*acquiredFile, variant string) {
	if len(files) == 0 || variant == mod.Variant {
		return
	}
	for _, a := range files {
		a.mod.Variant = variant
	}
	files[0].unseeded = true
	mod.Variant = variant
}

// acquiredIDs is the file ids of files, in order.
func acquiredIDs(files []*acquiredFile) []string {
	ids := make([]string, len(files))
	for i, a := range files {
		ids[i] = a.file.ID
	}
	return ids
}

// isTreeOf reports whether want is the tree base resolved to: base itself,
// or base's file-set variant of the files ids (#639).
func isTreeOf(want, base string, ids []string) bool {
	return want == base || (len(ids) > 0 && want == fileSetVariant(base, ids))
}

// fomodTreeVariant is row's variant when it names an installer's tree: ""
// for a plain tree, including one moved to its file-set variant (#639), so
// a plain mod whose files were swapped is never mistaken for a FOMOD
// install.
func fomodTreeVariant(row *domain.InstalledMod) string {
	if row.Variant != "" && len(row.FileIDs) > 0 && row.Variant == fileSetVariant("", row.FileIDs) {
		return ""
	}
	return row.Variant
}

// reinstallReason is a converge plan's reason for reinstalling row at ref:
// ReinstallFilesChanged when ref keeps row's version and pins another set
// of its files (#639), else "" - a version change says why by itself.
func reinstallReason(ref domain.ModReference, row *domain.InstalledMod) string {
	if (ref.Version == "" || ref.Version == row.Version) && fileSetDrift(ref, row) {
		return ReinstallFilesChanged
	}
	return ""
}

// inFileSetEntry reports whether row lives in a file-set variant (#639) of
// its files - of the plain tree, or of the tree ref's recorded choices
// name - rather than in its version's own entry.
func inFileSetEntry(row *domain.InstalledMod, ref *domain.ModReference) bool {
	if row == nil || row.Variant == "" || len(row.FileIDs) == 0 {
		return false
	}
	if row.Variant == fileSetVariant("", row.FileIDs) {
		return true
	}
	return ref != nil && row.Variant == fileSetVariant(refVariant(ref), row.FileIDs)
}
