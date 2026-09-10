// Package steamworkshop: this file is Tier 3's download half - what a
// Workshop item looks like as a downloadable file, and Path A, the legacy
// `file_url` a handful of old UGC-era items still carry.
//
// Path B (an anonymous steamcmd shell-out, for everything else) is
// steamcmd.go. Everything past either path is deliberately ordinary: core
// ingests the bytes through the same cache/linker route every other source
// uses, and the resulting mod is lmm-managed, not EXTERNAL.
package steamworkshop

import (
	"context"
	"fmt"
	"net/url"
	"path"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

var (
	_ source.Fetcher        = (*Source)(nil)
	_ source.ExactFileSizer = (*Source)(nil)
)

// GetModFiles describes the item as the single downloadable file it is.
//
// A Workshop item has no concept of alternate, optional or patch files:
// there is one published file, and its own id is the only identity it has.
// Size comes from the API's file_size, which Valve reports to the byte -
// see ExactFileSizes.
func (s *Source) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	if mod == nil {
		return nil, fmt.Errorf("source %q: file listing: no mod given", sourceID)
	}
	d, err := s.client.detailsFor(ctx, mod.ID, false)
	if err != nil {
		return nil, fmt.Errorf("source %q: %w", sourceID, err)
	}
	return []domain.DownloadableFile{{
		ID:        d.PublishedFileID,
		Name:      d.Title,
		FileName:  downloadFileName(d),
		Version:   contentVersion(d),
		Size:      int64(d.FileSize),
		IsPrimary: true,
		Category:  "MAIN",
	}}, nil
}

// GetDownloadURL returns the item's legacy `file_url` when Valve publishes
// one - Path A - and reports ErrNotSupported otherwise, which is what
// sends core to the source.Fetcher seam and Path B's steamcmd.
//
// It performs NO request of its own. The download CDN 404s a HEAD, so a
// source that "verified" a URL before handing it over would break every
// item it was meant to help; the URL's validity is settled by the GET that
// core makes next, and by the size check that follows it.
func (s *Source) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	id := fileID
	if id == "" && mod != nil {
		id = mod.ID
	}
	d, err := s.client.detailsFor(ctx, id, false)
	if err != nil {
		return "", fmt.Errorf("source %q: %w", sourceID, err)
	}
	if d.FileURL == "" {
		// Not a failure: a modern item simply is not served this way.
		return "", fmt.Errorf("source %q: item %s has no direct download URL: %w", sourceID, id, source.ErrNotSupported)
	}
	return d.FileURL, nil
}

// ExactFileSizes implements source.ExactFileSizer: Valve reports file_size
// to the byte, and the legacy CDN publishes no checksum, so the size IS
// the integrity check for Path A - a short body means a truncated file,
// not a rounding difference.
func (s *Source) ExactFileSizes() bool { return true }

// downloadFileName names the file core writes into staging. For a legacy
// item that is whatever the URL's last path segment is called (it decides
// whether core extracts an archive or copies a plain file); for everything
// else the published file id is a name that can never collide and never
// looks like an archive lmm should try to open.
func downloadFileName(d itemDetails) string {
	if d.FileURL == "" {
		return d.PublishedFileID
	}
	if u, err := url.Parse(d.FileURL); err == nil {
		if base := path.Base(u.Path); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return d.PublishedFileID + ".zip"
}
