package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

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
// compare. The exception is a row on an imported archive's tree (#644),
// which records no source file because it is none: a ref pinning one
// drifts from it.
func fileSetDrift(ref domain.ModReference, row *domain.InstalledMod) bool {
	if row != nil && isArchiveVariant(row.Variant) && len(ref.FileIDs) > 0 && !sameFileIDs(ref.FileIDs, row.FileIDs) {
		// #644: an imported archive's tree records no source file, so a ref
		// that pins one names another tree - the source's.
		return true
	}
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
// files row was installed from - or its archive variant (#644).
func rowFitsRef(row *domain.InstalledMod, ref *domain.ModReference) bool {
	base := refVariant(ref)
	ids := ref.FileIDs
	if len(ids) == 0 {
		ids = row.FileIDs
	}
	return row.Variant == base || (len(ids) > 0 && row.Variant == fileSetVariant(base, ids)) || archiveTreeFits(ref, row, base)
}

// archiveTreeFits reports whether row's archive tree (#644) is one ref
// names: an archive variant of ref's base, for a ref that pins no source
// file - or only the files the row itself records (a DeployCompile
// import's retained archive, #197). A ref naming a source file the row
// does not record names the source's tree, never an imported archive's.
func archiveTreeFits(ref *domain.ModReference, row *domain.InstalledMod, base string) bool {
	return isArchiveTreeOf(row.Variant, base) && (len(ref.FileIDs) == 0 || sameFileIDs(ref.FileIDs, row.FileIDs))
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

// installerTree reports whether row's variant names a tree a FOMOD
// installer built (#644). Every plain tree is told apart from one: the
// plain entry, a plain mod's file lists and patches (#610, inputs - the
// ones the row's profile records for its files), and either one moved to
// its file-set variant (#639) or ingested from an archive no source file
// is (archiveVariant), so a plain mod is never mistaken for a FOMOD
// install.
func installerTree(row *domain.InstalledMod, inputs map[string]domain.TreeInputs) bool {
	if row.Variant == "" {
		return false
	}
	for _, base := range []string{"", treeInputsVariant("", inputs)} {
		if isTreeOf(row.Variant, base, row.FileIDs) || isArchiveTreeOf(row.Variant, base) {
			return false
		}
	}
	return true
}

// archiveVariantPrefix starts every archive variant, which no other
// variant (12 hex digits) can.
const archiveVariantPrefix = "archive-"

// archiveVariantScheme names the archive variant's base tag (#644).
const archiveVariantScheme = "lmm-archive-1"

// THE ARCHIVE VARIANT (#644). An archive imported, or updated from, under a
// source's mod ID that is none of the files the source lists records no
// file set, so #639's file-set entry cannot keep its tree apart: keyed by
// the version alone, it was the plain entry a download of the source's
// own file builds - another profile's, which then deployed the archive's
// tree. Such an archive's tree is instead an entry of its own: the
// variant of the base tree the ingest built (the plain one, or an
// installer's) for the archive's sha256. The same archive names the same
// entry again; any other archive, or the source's own file, never does.
//
// It spells archive-<base tag>-<12 hex digits of the sha256>, so a row on
// one is recognised as its base's tree (isArchiveTreeOf) without the
// archive in hand: a converge flow compares the profile's recorded
// choices with the base, as it does for the base itself.

// archiveVariant is the variant of base's tree built from the archive
// whose sha256 (hex) is sum.
func archiveVariant(base, sum string) string {
	return archiveBaseTag(base) + sum[:min(12, len(sum))]
}

// archiveBaseTag is the prefix every archive variant of base starts with.
func archiveBaseTag(base string) string {
	h := sha256.Sum256([]byte(archiveVariantScheme + "\n" + base))
	return archiveVariantPrefix + hex.EncodeToString(h[:])[:8] + "-"
}

// isArchiveTreeOf reports whether variant is an archive variant of base.
func isArchiveTreeOf(variant, base string) bool {
	return strings.HasPrefix(variant, archiveBaseTag(base)) && len(variant) > len(archiveBaseTag(base))
}

// ReasonArchiveTree is why a mod whose cache entry is gone is not
// downloaded again (#644): its files came from an archive that is none of
// the files its source lists, so no download rebuilds them.
const ReasonArchiveTree = "its files came from an imported archive its source does not list; import the archive again"

// isArchiveVariant reports whether variant is an archive variant of any
// base: a tree no source download can rebuild.
func isArchiveVariant(variant string) bool {
	return strings.HasPrefix(variant, archiveVariantPrefix)
}

// archiveEntryApplies reports whether an archive ingest at sourceID that
// records the source files ids goes to its archive variant: it records
// none, and its source is one that downloads build entries for - not a
// local mod, whose entries only imports fill.
func archiveEntryApplies(sourceID string, ids []string) bool {
	return len(ids) == 0 && sourceID != domain.SourceLocal
}

// archiveEntryFor is an archive ingest's ImportOptions.entryFor for an
// archive no source file is (#644): the ingest key's archive variant for
// the archive at path, whose sha256 is sum when a plan already hashed it
// (archiveSum), else hashed here. An archive that cannot be hashed fails
// the ingest: the plain entry is never the fallback.
func archiveEntryFor(path, sum string) (func(cache.Key) cache.Key, error) {
	if sum == "" {
		var err error
		if sum, _, err = hashFile(path); err != nil {
			return nil, err
		}
	}
	return func(k cache.Key) cache.Key {
		k.Variant = archiveVariant(k.Variant, sum)
		return k
	}, nil
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
// archive matched to no source file records no set, and is kept apart by
// its archive variant instead (archiveEntryFor, #644).
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

// ingestEntry is the cache entry an archive ingest of the files ids fills
// at mod's identity (#639), judged before the ingest runs: fileSetEntry
// over the base the ingest itself builds - the variant the profile's
// recorded (or answers') FOMOD choices name and, only when the ingest
// applies them (treeFile, ImportOptions.treeFileID), that file's list and
// patches (#610). It is a PREDICTION, for a plan's entry-pre-exists preview
// only: answers carry no installer config hash, a recording may meet an
// archive that is no installer, and the ingest may resolve otherwise. What
// a refused or failed ingest may discard is never decided by it - only by
// the commit's own answer at the key it wrote (stagedEntry.commit).
func (s *Service) ingestEntry(ctx context.Context, game *domain.Game, profileName string, mod domain.Mod, ids []string, treeFile string, answers map[string]domain.FomodChoices) cache.Key {
	fx := s.fomodInputFor(ctx, game, profileName, s.recordedRef(ctx, game.ID, profileName, mod.SourceID, mod.ID), mod.SourceID, mod.ID, answers)
	fx.fileIDs = ids
	if treeFile == "" {
		fx.fileLists, fx.patches = nil, nil
	}
	k := cache.Key{GameID: game.ID, SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version, Variant: fx.expectedVariant()}
	k, _ = s.fileSetEntry(ctx, s.GetGameCache(game), k, ids, false)
	return k
}

// archiveIngestEntry is the entry an archive no source file is fills at
// mod's identity (#644), predicted for a plan's entry-pre-exists preview
// only, as ingestEntry predicts a file's: the archive variant of the base
// the profile's recorded (or answers') FOMOD choices name. It also returns
// the archive's sha256, which the plan carries so its apply need not hash
// the archive again; an archive that cannot be hashed predicts the plain
// entry and returns no sum - the ingest itself refuses it.
func (s *Service) archiveIngestEntry(ctx context.Context, game *domain.Game, profileName string, mod domain.Mod, archivePath string, answers map[string]domain.FomodChoices) (cache.Key, string) {
	k := cache.Key{GameID: game.ID, SourceID: mod.SourceID, ModID: mod.ID, Version: mod.Version}
	sum, _, err := hashFile(archivePath)
	if err != nil {
		return k, ""
	}
	entryFor, _ := archiveEntryFor(archivePath, sum)
	fx := s.fomodInputFor(ctx, game, profileName, s.recordedRef(ctx, game.ID, profileName, mod.SourceID, mod.ID), mod.SourceID, mod.ID, answers)
	fx.fileLists, fx.patches = nil, nil
	k.Variant = fx.expectedVariant()
	return entryFor(k), sum
}
