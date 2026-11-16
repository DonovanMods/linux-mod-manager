package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// VerifyStatusCacheLink is the status of a verify row for a symbolic link
// or special file (a named pipe, socket or device) inside an installed
// mod's cache entry (#659). Nothing lmm writes into an entry is one, but an
// extraction from before #658 could leave a .7z or .rar symlink member
// there. FileID is the path relative to the entry, slash-separated, and the
// note names it and its kind. A warning: deploy, conflicts and every copy
// of the entry skip it (cache.ListFiles lists regular files only), so
// nothing follows it, but it is not the mod's content either.
//
// It is always fixable: --fix removes the link itself, never what it
// points at. When it stood where one of the row's recorded files belongs,
// the entry is then missing that file, and the same run's cache_incomplete
// row re-downloads it - or says why it cannot.
const VerifyStatusCacheLink = "cache_link"

// VerifyStatusFixedCacheLink is a cache_link row --fix resolved: the link
// or special file is gone from the entry. Its note says what was removed.
const VerifyStatusFixedCacheLink = "fixed_cache_link"

// cacheLink is one entry cacheLinks found: rel is entry-relative (OS
// separators), kind the words for what it is.
type cacheLink struct {
	rel  string
	kind string
}

// linkKind is the words for a non-regular, non-directory mode, read off
// the directory entry and never off what a link points at.
func linkKind(m fs.FileMode) string {
	switch {
	case m&fs.ModeSymlink != 0:
		return "symbolic link"
	case m&fs.ModeNamedPipe != 0:
		return "named pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeDevice != 0:
		return "device"
	default:
		return "special file"
	}
}

// cacheLinks walks the cache entry at dir and returns every entry in it
// that is neither a directory nor a regular file. Nothing is followed:
// filepath.WalkDir reads each entry's type from its directory and never
// descends into a link to a directory, which is reported itself; no
// target is stat'ed or opened. dir itself is not judged (a data directory
// may sit behind a link of the user's own). A missing dir has none.
func cacheLinks(dir string) ([]cacheLink, error) {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	var out []cacheLink
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == dir || d.IsDir() || d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, cacheLink{rel: rel, kind: linkKind(d.Type())})
		return nil
	})
	return out, err
}

// removeCacheLink removes rel from the cache entry at dir: Lstat-checked
// again through an os.Root confined to the entry, so a path that has since
// become a regular file or a directory is left alone, and a parent swapped
// for a link cannot take the removal outside the entry. The final
// component is unlinked, never followed.
func removeCacheLink(dir, rel string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(rel)
	if err != nil {
		return err
	}
	if info.IsDir() || info.Mode().IsRegular() {
		return fmt.Errorf("%s is no longer a link or special file, so it was left", filepath.ToSlash(rel))
	}
	return root.Remove(rel)
}

// recordedUnder reports whether rel - or, for a link to a directory, a
// path under it - is a member one of manifests records.
func recordedUnder(manifests map[string]cache.FileManifest, rel string) bool {
	prefix := rel + string(filepath.Separator)
	for _, m := range manifests {
		if slices.ContainsFunc(m.Members, func(member string) bool {
			member = filepath.FromSlash(member)
			return member == rel || strings.HasPrefix(member, prefix)
		}) {
			return true
		}
	}
	return false
}

// cacheLinkNote is a cache_link row's sentence.
func cacheLinkNote(mod *domain.InstalledMod, l cacheLink, recorded bool) string {
	path := filepath.ToSlash(l.rel)
	switch {
	case !recorded:
		return fmt.Sprintf("%s in the cache entry is a %s; lmm keeps none in a mod's cache, never deploys or follows one, and --fix removes it", path, l.kind)
	case redownloadRepairs(mod):
		return fmt.Sprintf("%s in the cache entry is a %s, where one of the mod's files belongs; --fix removes it and downloads the file again", path, l.kind)
	default:
		return fmt.Sprintf("%s in the cache entry is a %s, where one of the mod's files belongs; --fix removes it, and the mod has to be imported again to restore the file", path, l.kind)
	}
}

// cacheLinkPass reports, per installed mod the run covers, every link or
// special file in its cache entry (#659), and under --fix removes each.
// It runs before every pass that reads an entry, so a removal is what the
// per-file walk's cache_incomplete check (lostFiles) then sees: a recorded
// member the removal took is re-downloaded by that row's own repair, not
// by anything here. A walk that fails is a verbose diagnostic: the checks
// that list the entry report an unreadable one.
func (r *verifyRun) cacheLinkPass(installedMods []domain.InstalledMod) error {
	c := r.svc.GetGameCache(r.game)
	for i := range installedMods {
		if err := r.ctx.Err(); err != nil {
			return err
		}
		mod := &installedMods[i]
		if mod.External || (r.opts.ModFilter != "" && mod.ID != r.opts.ModFilter) {
			continue
		}
		k := modCacheKey(r.game.ID, mod.Mod)
		dir := c.ModPath(k)
		links, err := cacheLinks(dir)
		if err != nil {
			r.emitEv(VerifyEvent{Kind: VerifyEvVerbose, Detail: fmt.Sprintf("could not check the cache entry of %s for links: %v", mod.Name, err)})
			continue
		}
		if len(links) == 0 {
			continue
		}
		manifests := r.linkManifests(c, k, links)
		for _, l := range links {
			r.cacheLinkFinding(mod, dir, l, recordedUnder(manifests, l.rel))
		}
	}
	return nil
}

// linkManifests is k's completion markers, to tell which links stand for a
// recorded member - or none when a marker is itself among links, since
// reading the markers would then open it.
func (r *verifyRun) linkManifests(c *cache.Cache, k cache.Key, links []cacheLink) map[string]cache.FileManifest {
	for _, l := range links {
		if strings.HasPrefix(filepath.Base(l.rel), cache.ReservedPrefix) {
			return nil
		}
	}
	manifests, err := c.FileManifests(k)
	if err != nil {
		return nil
	}
	return manifests
}

// cacheLinkFinding reports one link. Under --fix it is removed first, so
// the row carries the outcome: fixed_cache_link, or cache_link with the
// failure and a repair sub-line.
func (r *verifyRun) cacheLinkFinding(mod *domain.InstalledMod, dir string, l cacheLink, recorded bool) {
	path := filepath.ToSlash(l.rel)
	row := VerifyFinding{ModID: mod.ID, ModName: mod.Name, FileID: path, Status: VerifyStatusCacheLink,
		Note: cacheLinkNote(mod, l, recorded), Fixable: true}
	if !r.opts.Fix {
		r.result.Warnings++
		r.finding(row, VerifyEvent{})
		return
	}
	// A row a --fix run produced is past fixing (VerifyFinding.Fixable).
	row.Fixable = false
	if err := removeCacheLink(dir, l.rel); err != nil {
		r.result.Warnings++
		row.Note = fmt.Sprintf("could not remove %s: %v", path, err)
		row.FixableReason = "this --fix run already attempted a repair for it"
		r.finding(row, VerifyEvent{})
		r.emitEv(VerifyEvent{Kind: VerifyEvRepairDetail, Detail: "Removal failed: " + err.Error()})
		return
	}
	row.Status = VerifyStatusFixedCacheLink
	row.Note = fmt.Sprintf("removed %s, a %s, from the cache entry", path, l.kind)
	r.finding(row, VerifyEvent{})
	// The entry changed: a recorded member it took is judged afresh.
	delete(r.lost, domain.ModKey(mod.SourceID, mod.ID))
}
