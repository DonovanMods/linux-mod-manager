package core

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// VerifyStatusCacheIncomplete is the status of a verify row for a file of an
// installed mod whose cache entry is there but no longer holds that file's
// content (#642): a member was deleted from the entry, with or without the
// completion marker that vouched for it. It is an issue; --fix re-downloads
// the file into the row's own entry (a locally imported mod has no source,
// so its row is not fixable), and until the entry is whole again no
// deployed copy of the lost member is removed as stale. The note names what
// is gone.
const VerifyStatusCacheIncomplete = "cache_incomplete"

// lostNoteLimit caps how many lost paths a cache_incomplete note names.
const lostNoteLimit = 3

// lostCacheFiles reports which of ids - an installed row's recorded source
// files - k's cache entry no longer holds (#642), each with the note saying
// what is gone. The entry is judged against the row, never only against the
// markers present, because a marker deleted with its member leaves nothing
// behind to say the member was ever there:
//
//   - a file whose marker records its members is lost when one of them is
//     gone from the entry;
//   - a file with no marker in an entry that carries markers for others is
//     lost: nothing vouches for its content, which is cache.HasFileIDs'
//     judgement too;
//   - a file the entry cannot vouch for either way - no marker in an entry
//     with none, or a legacy bare marker - is lost only when the entry
//     carries the identity marker (#614) and the profile's own deployed
//     records name a path of the mod the entry no longer provides
//     (unprovided). Every commit that stamps the identity marker records
//     its file's marker too, so such an entry with none lost them; an
//     entry without it may be from before markers, which says nothing.
//     An entry that still provides every path is not reported either:
//     nothing says it lost anything.
//
// A row that records no files (from before lmm recorded them) has no set
// to judge the entry against: only its markers' own member lists are
// checked. An absent entry reports nothing: that is the "missing" row's
// case. unprovided is called at most once, and only when needed.
func lostCacheFiles(c *cache.Cache, k cache.Key, ids []string, unprovided func() ([]domain.DeployPath, error)) (map[string]string, error) {
	if !c.Exists(k) {
		return nil, nil
	}
	manifests, err := c.FileManifests(k)
	if err != nil {
		return nil, err
	}
	recorded := len(ids) > 0
	if !recorded {
		ids = slices.Sorted(maps.Keys(manifests))
	}
	var gonePaths []domain.DeployPath
	var goneErr error
	asked := false
	deployedGone := func() ([]domain.DeployPath, error) {
		if !asked {
			asked = true
			gonePaths, goneErr = unprovided()
		}
		return gonePaths, goneErr
	}

	dir := c.ModPath(k)
	lost := make(map[string]string)
	var unmarked, unvouched []string
	for _, id := range ids {
		if !cache.VerifiableFileID(id) {
			continue
		}
		m, ok := manifests[id]
		switch {
		case !ok && len(manifests) > 0:
			unmarked = append(unmarked, id)
		case !ok || !m.Recorded:
			unvouched = append(unvouched, id)
		default:
			if gone := goneMembers(dir, m.Members); len(gone) > 0 {
				lost[id] = "the cache entry is missing " + nameList(gone)
			}
		}
	}
	if len(unmarked) > 0 {
		paths, err := deployedGone()
		if err != nil {
			return nil, err
		}
		note := "the cache entry no longer records this file's content"
		if len(paths) > 0 {
			note = deployedGoneNote(paths)
		}
		for _, id := range unmarked {
			lost[id] = note
		}
	}
	if len(lost) > 0 || len(unvouched) == 0 || !recorded || !stampedEntry(dir) {
		return lost, nil
	}
	paths, err := deployedGone()
	if err != nil || len(paths) == 0 {
		return nil, err
	}
	note := deployedGoneNote(paths)
	for _, id := range unvouched {
		lost[id] = note
	}
	return lost, nil
}

// stampedEntry reports whether dir carries the identity marker (#614).
func stampedEntry(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, cache.KeyMarkerName))
	return err == nil
}

// goneMembers is the members, of those a marker records, that are no
// longer in dir.
func goneMembers(dir string, members []string) []string {
	var gone []string
	for _, m := range members {
		if _, err := os.Lstat(filepath.Join(dir, m)); errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, filepath.ToSlash(m))
		}
	}
	return gone
}

// deployedGoneNote is the note for a loss the deployed records show.
func deployedGoneNote(paths []domain.DeployPath) string {
	names := make([]string, len(paths))
	for i, p := range paths {
		names[i] = p.String()
	}
	return "the cache entry is missing " + nameList(names) + ", which this profile deployed"
}

// nameList joins names for a note, naming at most lostNoteLimit of them.
func nameList(names []string) string {
	if len(names) <= lostNoteLimit {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:lostNoteLimit], ", "), len(names)-lostNoteLimit)
}

// rowLostFiles is lostCacheFiles for row in profile: its entry, its
// recorded files, and - when asked - the paths the profile's records say
// it deployed that its entry no longer provides. stranded is the profile's
// rows recorded under another mod_path (#451), which say nothing about the
// entry and are left out.
func (s *Service) rowLostFiles(ctx context.Context, game *domain.Game, a adapter.GameAdapter, profile string, row *domain.InstalledMod, stranded map[domain.DeployPath]bool) (map[string]string, error) {
	c := s.GetGameCache(game)
	k := modCacheKey(game.ID, row.Mod)
	return lostCacheFiles(c, k, row.FileIDs, func() ([]domain.DeployPath, error) {
		rows, err := s.db.GetDeployedFilesForMod(ctx, game.ID, profile, row.SourceID, row.ID)
		if err != nil || len(rows) == 0 {
			return nil, err
		}
		items, err := deployableFiles(c, a, game, k, row.ModType)
		if err != nil {
			return nil, err
		}
		own := make(map[domain.DeployPath]bool, len(items))
		for _, it := range items {
			own[it.path] = true
		}
		return unprovidedPaths(rows, own, stranded), nil
	})
}

// unprovidedPaths is the deployed records in rows that own does not
// provide, leaving out the stranded and the unsafe ones convergence never
// judges either.
func unprovidedPaths(rows []domain.DeployPath, own, stranded map[domain.DeployPath]bool) []domain.DeployPath {
	var out []domain.DeployPath
	for _, p := range rows {
		if own[p] || stranded[p] || !filepath.IsLocal(filepath.FromSlash(p.Rel)) {
			continue
		}
		out = append(out, p)
	}
	return out
}
