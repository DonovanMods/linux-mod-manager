package core

import (
	"context"
	"errors"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// DownloadError is the failure of fetching one mod file into the cache - a
// source that would not give a download URL, a transfer that failed, a file
// that did not verify - carrying the mod's page on its source as typed data
// (#513). A frontend renders ModURL where a person can act on it ("Download
// it manually from: ..."; the web UI's "Open on <source>" link) instead of
// matching the failure's text.
//
// Error() is the underlying failure's own text, unchanged, and Unwrap
// exposes it, so every errors.Is/As on the cause (source.ErrManualDownload,
// *WorkshopFetchError, domain.ErrAuthRequired, ...) still works through it.
type DownloadError struct {
	// SourceID and ModID name the mod whose file would not download;
	// ModName is its display name.
	SourceID string
	ModID    string
	ModName  string

	// ModURL is the mod's page on its source, only when
	// domain.SafeWebURL accepts it - never a javascript:, file: or
	// relative URL - so a frontend may use it as a link without checking.
	// Empty when the source gave none or an unusable one.
	ModURL string

	// ManualDownload is true when the source refuses to serve this file
	// through its API (source.ErrManualDownload): the file has to be
	// fetched by hand from ModURL, then imported.
	ManualDownload bool

	// Err is the underlying failure.
	Err error
}

// Error is the underlying failure's text.
func (e *DownloadError) Error() string { return e.Err.Error() }

// Unwrap exposes the cause.
func (e *DownloadError) Unwrap() error { return e.Err }

// Details implements the --json error envelope's extension point, which the
// web UI reads from a failed job. A *WorkshopFetchError underneath keeps its
// own keys: they are inlined beside these, so the Steam Workshop explainer
// the SPA already recognises by `published_file_id` is unchanged and gains
// only the page.
func (e *DownloadError) Details() any {
	d := downloadErrorDetails{
		SourceID:       e.SourceID,
		ModID:          e.ModID,
		ModName:        e.ModName,
		ModURL:         e.ModURL,
		ManualDownload: e.ManualDownload,
	}
	var workshop *WorkshopFetchError
	if errors.As(e.Err, &workshop) {
		return workshopDownloadErrorDetails{
			workshopFetchErrorDetails: workshop.Details().(workshopFetchErrorDetails),
			downloadErrorDetails:      d,
		}
	}
	return d
}

type downloadErrorDetails struct {
	SourceID       string `json:"source_id"`
	ModID          string `json:"mod_id"`
	ModName        string `json:"mod_name,omitempty"`
	ModURL         string `json:"mod_url,omitempty"`
	ManualDownload bool   `json:"manual_download"`
}

type workshopDownloadErrorDetails struct {
	workshopFetchErrorDetails
	downloadErrorDetails
}

// asDownloadError wraps err - what fetching file into the cache returned - as
// a *DownloadError for mod. A cancellation is returned as it came: the user
// stopping a download is not a failure with a page to go and read.
func asDownloadError(err error, sourceID string, mod *domain.Mod) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var already *DownloadError
	if errors.As(err, &already) {
		return err
	}
	return &DownloadError{
		SourceID:       sourceID,
		ModID:          mod.ID,
		ModName:        mod.Name,
		ModURL:         mod.PageURL(),
		ManualDownload: errors.Is(err, source.ErrManualDownload),
		Err:            err,
	}
}
