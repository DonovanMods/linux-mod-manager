package core

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
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
// edit - would share one directory, and the second set's download would
// layer onto the first's tree (prepareStaging seeds from it), so the old
// file's members stayed deployed. The entry's own completion markers say
// which files built it, though, so the key stays as it always was - the
// plain entry, or the FOMOD variant - for the set the entry holds, and only
// ANOTHER set at the same version is moved to a variant of its own:
// fileSetVariant of the base variant and the sorted ids. No existing entry
// changes key: an entry's markers are the set that built it, so every row
// installed before #639 still names (and finds) its entry, and a set that
// matches what an entry holds keeps using it.

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

// sameFileSet reports whether a and b name the same files, as sets.
func sameFileSet(a, b []string) bool {
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
	return len(a) > 0 && len(b) > 0 && !sameFileSet(a, b)
}

// holdsOtherFiles reports whether k's entry was built from a file outside
// ids: a completion marker names one. Such an entry is another file set's
// tree, which the set ids must neither reuse nor layer onto. An absent
// entry, or one with no markers (a legacy entry), holds none.
func holdsOtherFiles(c *cache.Cache, k cache.Key, ids []string) bool {
	if len(ids) == 0 {
		return false
	}
	manifests, err := c.FileManifests(k)
	if err != nil {
		return false
	}
	for id := range manifests {
		if !slices.Contains(ids, id) {
			return true
		}
	}
	return false
}

// fileSetKey is the entry the files ids of k's mod and version live in: k
// itself, unless k's entry holds another file set (holdsOtherFiles), then
// k with the file-set variant of k's own.
func fileSetKey(c *cache.Cache, k cache.Key, ids []string) cache.Key {
	if holdsOtherFiles(c, k, ids) {
		k.Variant = fileSetVariant(k.Variant, ids)
	}
	return k
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
// unifyFomod has stamped, to their file-set variant when the base entry
// holds another file set (#639) - or, given want, to want when want is
// that file-set variant (a repair of a row installed into one). The first
// file then commits unseeded: a fresh tree, never layered onto another's.
func restampFileSet(gameCache *cache.Cache, gameID string, mod *domain.Mod, files []*acquiredFile, want *string) {
	if len(files) == 0 {
		return
	}
	ids := make([]string, len(files))
	for i, a := range files {
		ids[i] = a.file.ID
	}
	variant := mod.Variant
	switch {
	case want != nil:
		if *want == variant || *want != fileSetVariant(variant, ids) {
			return
		}
		variant = *want
	default:
		k := fileSetKey(gameCache, modCacheKey(gameID, *mod), ids)
		if k.Variant == mod.Variant {
			return
		}
		variant = k.Variant
	}
	for _, a := range files {
		a.mod.Variant = variant
	}
	files[0].unseeded = true
	mod.Variant = variant
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
