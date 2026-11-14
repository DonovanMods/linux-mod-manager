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
// (#639), and whether it already holds every one of them. k itself when:
//
//   - k's entry names exactly ids in its markers;
//   - k's entry is absent (or an empty, unmarked directory) and no
//     installed row on k records another set: an entry a row still names
//     for its own files is that row's to refill, never another set's;
//   - k's entry has no markers (an entry from before them) and the
//     installed rows on k all record exactly ids, at least one of them.
//
// Any other set gets k with the file-set variant of k's own. backfill,
// for a flow that commits (never a plan), stamps the markers of an
// unmarked entry it reuses, so the next read finds them.
func (s *Service) fileSetEntry(ctx context.Context, c *cache.Cache, k cache.Key, ids []string, backfill bool) (cache.Key, bool) {
	if len(ids) == 0 {
		return k, c.HasFileIDs(k, ids)
	}
	variant := func() (cache.Key, bool) {
		v := k
		v.Variant = fileSetVariant(k.Variant, ids)
		return v, c.HasFileIDs(v, ids)
	}
	if !c.Exists(k) {
		if s.entryRowsRecord(ctx, k, ids) == entryOtherSet {
			return variant()
		}
		return k, false
	}
	manifests, err := c.FileManifests(k)
	if err != nil {
		return variant()
	}
	if len(manifests) > 0 {
		if sameFileIDs(slices.Collect(maps.Keys(manifests)), ids) {
			return k, c.HasFileIDs(k, ids)
		}
		return variant()
	}
	rows := s.entryRowsRecord(ctx, k, ids)
	if files, err := c.ListFiles(k); err == nil && len(files) == 0 {
		// An unmarked entry with no members (a download that broke off
		// before its first commit) is no tree: as good as absent.
		if rows == entryOtherSet {
			return variant()
		}
		return k, false
	}
	if rows == entryThisSet {
		if backfill {
			backfillMarkers(c, k, ids)
		}
		return k, true
	}
	return variant()
}

// entryRows is what the installed rows on one cache entry record about a
// file set (entryRowsRecord).
type entryRows int

const (
	// entryNoSet: no row on the entry records any files.
	entryNoSet entryRows = iota
	// entryThisSet: every row on the entry that records files records
	// exactly the set asked about, and at least one does.
	entryThisSet
	// entryOtherSet: some row on the entry records another set.
	entryOtherSet
)

// entryRowsRecord reports what the installed rows of k's game that use k's
// entry (its version and variant) record against the files ids. A row
// recording no files says nothing either way. A failed read answers
// entryOtherSet: never reuse an entry lmm cannot vouch for.
func (s *Service) entryRowsRecord(ctx context.Context, k cache.Key, ids []string) entryRows {
	rows, err := s.db.ModVersionRows(ctx, k.SourceID, k.ModID)
	if err != nil {
		return entryOtherSet
	}
	out := entryNoSet
	record := func(fileIDs []string) bool {
		switch {
		case len(fileIDs) == 0:
		case sameFileIDs(fileIDs, ids):
			out = entryThisSet
		default:
			return false
		}
		return true
	}
	for _, row := range rows {
		if row.GameID != k.GameID {
			continue
		}
		current := row.Version == k.Version && row.Variant == k.Variant
		// A row that rolls back to the entry (its previous version and
		// variant) names it for its previous files.
		previous := row.PreviousVersion != "" && row.PreviousVersion == k.Version && row.PreviousVariant == k.Variant
		if !current && !previous {
			continue
		}
		im, err := s.db.GetInstalledMod(ctx, k.SourceID, k.ModID, row.GameID, row.Profile)
		if err != nil {
			return entryOtherSet
		}
		if current && !record(im.FileIDs) {
			return entryOtherSet
		}
		if previous && !record(im.PreviousFileIDs) {
			return entryOtherSet
		}
	}
	return out
}

// rollbackTargetHolds reports whether row's rollback target - its previous
// version's entry - is still there and still holds the files row rolls
// back to (#639), never another set that refilled it:
//
//   - its markers name every one of them. More is the shape a same-version
//     update left before #639 (the old and new files in one entry), which
//     the rollback narrows to the restored files (#150);
//   - an entry from before markers is the row's own current entry (that
//     same pre-#639 shape), or no installed row names it for another set.
//
// A row that records no previous files is judged by presence alone, as
// before.
func (s *Service) rollbackTargetHolds(ctx context.Context, game *domain.Game, row *domain.InstalledMod) bool {
	c, k := s.GetGameCache(game), previousCacheKey(game.ID, row)
	if !c.Exists(k) {
		return false
	}
	if len(row.PreviousFileIDs) == 0 {
		return true
	}
	manifests, err := c.FileManifests(k)
	if err != nil {
		return false
	}
	if len(manifests) > 0 {
		return c.HasFileIDs(k, row.PreviousFileIDs)
	}
	if k == modCacheKey(game.ID, row.Mod) {
		return true
	}
	return s.entryRowsRecord(ctx, k, row.PreviousFileIDs) != entryOtherSet
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

// fileSetEntryFor is an archive ingest's ImportOptions.entryFor (#639): it
// keeps the ingest key's identity and names its variant by fileSetEntry
// for the files ids - judged at final's identity when the ingest key is
// renamed to it afterwards (an import's enrichment). Nil for no ids: an
// archive matched to no source file records no set to keep apart.
func (s *Service) fileSetEntryFor(ctx context.Context, game *domain.Game, ids []string, final *domain.Mod) func(cache.Key) cache.Key {
	if len(ids) == 0 {
		return nil
	}
	return func(k cache.Key) cache.Key {
		at := k
		if final != nil {
			at.SourceID, at.ModID, at.Version = final.SourceID, final.ID, final.Version
		}
		chosen, _ := s.fileSetEntry(ctx, s.GetGameCache(game), at, ids, false)
		k.Variant = chosen.Variant
		return k
	}
}
