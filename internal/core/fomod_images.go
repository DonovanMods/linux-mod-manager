// Package core: this file keeps a FOMOD question's option images where a
// frontend can show them (#615).
//
// A question is raised in an Apply's acquire phase, from an archive
// extracted into a staging directory that is removed when the Apply
// returns. The images the question names live in that tree, so the resolve
// step copies them out before it goes: into <staging>/fomod-images/<key>/,
// keyed by the mod and the installer's config hash, each under a file name
// that is a hash of its image path. Nothing from the archive's XML ever
// becomes a path component here, so an image path from an untrusted
// ModuleConfig.xml cannot name a file outside that directory, on either the
// write or the read side.
//
// Only images are kept: a member is copied when it is an archive member
// under the installer root (never a path built from the XML alone), a
// regular file, no larger than maxFomodImageSize, and whose bytes sniff as
// one of the raster types in fomodImageTypes (no SVG: it can carry script).
// One question keeps at most maxFomodImages images and maxFomodImagesTotal
// bytes. Entries expire with the retained downloads' TTL.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// fomodImagesDirName is the staging-root subdirectory holding the images.
const fomodImagesDirName = "fomod-images"

// The caps on what one question keeps.
const (
	// maxFomodImageSize caps one image.
	maxFomodImageSize = 8 << 20
	// maxFomodImagesTotal caps one question's images together.
	maxFomodImagesTotal = 64 << 20
	// maxFomodImages caps how many images one question keeps.
	maxFomodImages = 512
	// maxFomodImagesStash caps every question's images together: past it
	// the oldest questions' images are dropped first.
	maxFomodImagesStash = 256 << 20
)

// fomodImagesInUse is how recently a question's images must have been
// written to be safe from the cap: one Apply asks about several mods in
// one pass (a batch install, a profile import), each kept apart, and none
// of them may be evicted to make room for another of the same question.
const fomodImagesInUse = 10 * time.Minute

// fomodImageTypes are the content types a kept image may sniff as.
var fomodImageTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
	"image/bmp":  true,
}

// ErrFomodImageNotFound reports that no image is kept for the asked path:
// the question expired, the installer named a file the archive lacks, or
// the file was not an image lmm serves.
var ErrFomodImageNotFound = errors.New("no such installer image")

// FomodImage is one kept option image.
type FomodImage struct {
	// Path is the image's file on disk (read-only use: a terminal prints
	// it; serve streams it).
	Path string
	// ContentType is the sniffed image type, one of the raster types lmm
	// keeps.
	ContentType string
	// Size is the file's size in bytes.
	Size int64
}

// fomodImagesDir is the directory one question's images are kept in, or ""
// with no staging root.
func (s *Service) fomodImagesDir(sourceID, modID, configSHA string) string {
	root := s.stagingRoot()
	if root == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(sourceID + "\x00" + modID + "\x00" + configSHA))
	return filepath.Join(root, fomodImagesDirName, hex.EncodeToString(sum[:16]))
}

// fomodImageKey normalises an image path as a ModuleConfig.xml writes it
// (any separator, any case) to the one spelling lookups compare; "" for a
// path that is not a plain relative path.
func fomodImageKey(image string) string {
	p := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(image), `\`, "/"))
	if p == "" || strings.ContainsRune(p, 0) {
		return ""
	}
	p = filepath.ToSlash(filepath.Clean(filepath.FromSlash(p)))
	if !filepath.IsLocal(filepath.FromSlash(p)) {
		return ""
	}
	return p
}

// fomodImageFileName is the file a kept image is stored under: a hash, so
// no part of the XML's path reaches the file system.
func fomodImageFileName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

// stashFomodImages keeps q's option images, read from tree (byRel maps an
// installer-root-relative path to its member), for FomodImage to serve. It
// never fails the question: an image that cannot be kept is simply not
// shown.
func (s *Service) stashFomodImages(tree string, byRel map[string]string, q *FomodQuestion) {
	dir := s.fomodImagesDir(q.SourceID, q.ModID, q.ConfigSHA256)
	if dir == "" {
		return
	}
	s.sweepFomodImages()
	s.capFomodImages(dir)
	// Members by normalised path. Paths are matched case-insensitively, so
	// two members can share a key; the first in sorted order wins, so the
	// same archive always serves the same file. A member spelled exactly as
	// the XML writes the image is preferred over both (exact, below).
	rels := slices.Sorted(maps.Keys(byRel))
	members := make(map[string]string, len(byRel))
	exact := make(map[string]string, len(byRel))
	for _, rel := range rels {
		exact[strings.ReplaceAll(rel, `\`, "/")] = byRel[rel]
		if k := fomodImageKey(rel); k != "" {
			if _, taken := members[k]; !taken {
				members[k] = byRel[rel]
			}
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.logger().Debug("keeping installer images failed", "err", err)
		return
	}
	now := time.Now()
	_ = os.Chtimes(dir, now, now)
	var total int64
	kept := 0
	done := map[string]bool{}
	for _, st := range q.Steps {
		for _, g := range st.Groups {
			for _, o := range g.Options {
				if kept >= maxFomodImages || total >= maxFomodImagesTotal {
					return
				}
				key := fomodImageKey(o.Image)
				member, ok := exact[strings.ReplaceAll(strings.TrimSpace(o.Image), `\`, "/")]
				if !ok {
					member, ok = members[key]
				}
				if key == "" || !ok || done[key] {
					continue
				}
				done[key] = true
				// Written afresh for every question, replacing what an
				// earlier question kept under the same name: a new version
				// with the same installer but new pictures shows the new
				// ones. The write is atomic, so a reader sees one or the
				// other.
				dest := filepath.Join(dir, fomodImageFileName(key))
				n, err := copyFomodImage(filepath.Join(tree, filepath.FromSlash(member)), dest, maxFomodImagesTotal-total)
				if err != nil {
					// Nothing stale may stand in for it: an earlier
					// question's picture under the same name is not this
					// archive's.
					_ = os.Remove(dest)
					s.logger().Debug("keeping an installer image failed", "image", key, "err", err)
					continue
				}
				total += n
				kept++
			}
		}
	}
}

// copyFomodImage copies one image, refusing anything but a regular file of
// a kept image type within the size caps.
func copyFomodImage(src, dest string, budget int64) (int64, error) {
	info, err := os.Lstat(src)
	if err != nil {
		return 0, err
	}
	limit := min(int64(maxFomodImageSize), budget)
	if !info.Mode().IsRegular() || info.Size() > limit {
		return 0, ErrFomodImageNotFound
	}
	f, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer f.Close() //nolint:errcheck // read-only
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return 0, err
	}
	if int64(len(data)) > limit || !fomodImageTypes[http.DetectContentType(data)] {
		return 0, ErrFomodImageNotFound
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".img-*")
	if err != nil {
		return 0, err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return 0, err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		_ = os.Remove(tmp.Name())
		return 0, err
	}
	return int64(len(data)), nil
}

// FomodImage returns the kept option image of the installer a question
// named (#615): the question's SourceID, ModID and ConfigSHA256, and the
// option's Image as the question carries it. It is read-only and serves
// only what the question kept; anything else is ErrFomodImageNotFound.
func (s *Service) FomodImage(sourceID, modID, configSHA, image string) (*FomodImage, error) {
	dir := s.fomodImagesDir(sourceID, modID, configSHA)
	key := fomodImageKey(image)
	if dir == "" || key == "" || !isHexSHA256(configSHA) {
		return nil, ErrFomodImageNotFound
	}
	path := filepath.Join(dir, fomodImageFileName(key))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFomodImageSize {
		return nil, ErrFomodImageNotFound
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrFomodImageNotFound
	}
	defer f.Close() //nolint:errcheck // read-only
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	ct := http.DetectContentType(head[:n])
	if !fomodImageTypes[ct] {
		return nil, ErrFomodImageNotFound
	}
	return &FomodImage{Path: path, ContentType: ct, Size: info.Size()}, nil
}

// isHexSHA256 reports a lower-case hex sha256.
func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// sweepFomodImages removes kept images older than the retained downloads'
// TTL.
func (s *Service) sweepFomodImages() {
	root := s.stagingRoot()
	if root == "" {
		return
	}
	base := filepath.Join(root, fomodImagesDirName)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-retainedDownloadTTL)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if rerr := os.RemoveAll(filepath.Join(base, e.Name())); rerr != nil {
			s.logger().Debug("sweeping installer images failed", "name", e.Name(), "err", rerr)
		}
	}
}

// capFomodImages keeps every question's images together under
// maxFomodImagesStash, with room for one more question: the oldest
// questions' directories (by their last use) go first. keep, the directory
// about to be written, is never dropped, and neither is one written within
// fomodImagesInUse (the same Apply's other mods); while only those remain,
// the cap is exceeded rather than a question's pictures lost.
func (s *Service) capFomodImages(keep string) {
	base := filepath.Dir(keep)
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	type stashDir struct {
		path string
		mod  time.Time
		size int64
	}
	var dirs []stashDir
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || !e.IsDir() {
			continue
		}
		path := filepath.Join(base, e.Name())
		var size int64
		files, _ := os.ReadDir(path)
		for _, f := range files {
			if fi, err := f.Info(); err == nil {
				size += fi.Size()
			}
		}
		total += size
		dirs = append(dirs, stashDir{path: path, mod: info.ModTime(), size: size})
	}
	slices.SortFunc(dirs, func(a, b stashDir) int { return a.mod.Compare(b.mod) })
	cutoff := time.Now().Add(-fomodImagesInUse)
	for _, d := range dirs {
		if total <= maxFomodImagesStash-maxFomodImagesTotal {
			return
		}
		if d.path == keep || d.mod.After(cutoff) {
			continue
		}
		if err := os.RemoveAll(d.path); err == nil {
			total -= d.size
		}
	}
}
