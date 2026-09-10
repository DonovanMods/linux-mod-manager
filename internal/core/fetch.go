// Package core: this file is the source.Fetcher half of a download - the
// path taken when a source cannot hand core a URL to GET (#269 Tier 3: a
// Steam Workshop item retrieved by an anonymous steamcmd shell-out).
//
// Everything past the fetch is deliberately ordinary. The fetched content
// lands in a staging directory core created and owns, and is then ingested
// by the SAME ingestLocalToCache the directory-source path uses, so a
// fetched mod goes through cache -> linker -> mod_path with no
// Workshop-specific code anywhere downstream: its row is a normal installed
// mod (External: false), and there is no new job kind, command or plan type
// for it.
package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/cache"
)

// WorkshopFetchError is the typed failure of a Steam Workshop download
// (#269 Tier 3), carrying enough structure for a frontend to explain what
// went wrong instead of printing a subprocess's output at the user.
//
// It wraps one of the three domain sentinels - ErrWorkshopAnonymousRefused,
// ErrWorkshopItemUnavailable, ErrExternalToolMissing - or, for a tool
// failure lmm has no name for, the raw error plus the tail of its output.
// One type for all of them (the ExternalModError precedent) keeps
// cmd/lmm's details_coverage ratchet honest with a single entry.
type WorkshopFetchError struct {
	// AppID is the Steam app id the item belongs to.
	AppID string
	// PublishedFileID identifies the Workshop item.
	PublishedFileID string
	// Reason is the user-facing explanation, already phrased as advice.
	Reason string
	// Tool names the external program that ran ("steamcmd"), empty when
	// the failure happened before any program did.
	Tool string
	// OutputTail is the last few KiB of that program's combined output,
	// present only for a failure lmm could not classify.
	OutputTail string
	// Err is the wrapped cause, exposed through Unwrap.
	Err error
}

// Error renders "downloading Steam Workshop item <id>: <reason>".
func (e *WorkshopFetchError) Error() string {
	reason := e.Reason
	if reason == "" && e.Err != nil {
		reason = e.Err.Error()
	}
	return fmt.Sprintf("downloading Steam Workshop item %s: %s", e.PublishedFileID, reason)
}

// Unwrap exposes the sentinel so errors.Is(err, domain.ErrWorkshop...) and
// errors.Is(err, domain.ErrExternalToolMissing) both work without a type
// assertion.
func (e *WorkshopFetchError) Unwrap() error { return e.Err }

// Details returns the payload the --json error envelope attaches under
// "details" (Ruling 3), and the same payload `lmm serve` hands the SPA in a
// failed job's error envelope - which is how the mod page renders the
// steamcmd explainer without a second mechanism for the same fact.
func (e *WorkshopFetchError) Details() any {
	return workshopFetchErrorDetails{
		AppID:           e.AppID,
		PublishedFileID: e.PublishedFileID,
		Reason:          e.Reason,
		Tool:            e.Tool,
		OutputTail:      e.OutputTail,
	}
}

type workshopFetchErrorDetails struct {
	AppID           string `json:"app_id,omitempty"`
	PublishedFileID string `json:"published_file_id,omitempty"`
	Reason          string `json:"reason"`
	Tool            string `json:"tool,omitempty"`
	OutputTail      string `json:"output_tail,omitempty"`
}

// fetchModToCache runs a source.Fetcher for one file and ingests what it
// produced, and is downloadModToCache's ErrNotSupported fallback.
//
// Core - not the source - creates the staging directory and verifies that
// what came back is inside it, so a Fetcher cannot name a path of its own
// choosing any more than a non-directory source can return a file:// URL
// (#300). sourceGameID is handed to the Fetcher on a COPY of mod: a
// Fetcher's contract is that mod.GameID is the source's own game id, and
// the caller's mod legitimately carries lmm's instead.
func (s *Service) fetchModToCache(ctx context.Context, gameCache *cache.Cache, fetcher source.Fetcher, sourceID string, game *domain.Game, mod *domain.Mod, file *domain.DownloadableFile, sink EventSink) (result *DownloadModResult, err error) {
	destDir, err := newStagingDir(s.stagingRoot(), "lmm-fetch-*")
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := os.RemoveAll(destDir); err == nil && cerr != nil {
			err = fmt.Errorf("removing fetch directory: %w", cerr)
		}
	}()

	fetchMod := *mod
	fetchMod.GameID = sourceGameIDFor(game, sourceID)

	fetched, ferr := fetcher.Fetch(ctx, &fetchMod, file.ID, destDir, fetchProgressSink(sink))
	if ferr != nil {
		return nil, fetchFailure(ferr)
	}
	if verr := verifyFetchedPath(destDir, fetched); verr != nil {
		return nil, verr
	}
	return s.ingestLocalToCache(ctx, gameCache, game, mod, file, fetched)
}

// sourceGameIDFor resolves the identifier sourceID knows game by, falling
// back to lmm's own game id when the game declares no mapping - the same
// rule Service.GetMod applies before calling a source, so a Fetcher sees
// exactly what GetMod saw.
func sourceGameIDFor(game *domain.Game, sourceID string) string {
	if game == nil {
		return ""
	}
	if id, ok := game.SourceIDs[sourceID]; ok && id != "" {
		return id
	}
	return game.ID
}

// verifyFetchedPath enforces the Fetcher contract's one security rule: the
// returned path must exist and must be inside destDir. Symlinks are
// resolved on both sides first, so a symlink planted inside destDir cannot
// be used to point the ingest at, say, the user's home directory.
func verifyFetchedPath(destDir, fetched string) error {
	if fetched == "" {
		return fmt.Errorf("fetching mod: source returned no path")
	}
	realDest, err := filepath.EvalSymlinks(destDir)
	if err != nil {
		return fmt.Errorf("fetching mod: resolving staging directory: %w", err)
	}
	realFetched, err := filepath.EvalSymlinks(fetched)
	if err != nil {
		return fmt.Errorf("fetching mod: resolving fetched path: %w", err)
	}
	rel, err := filepath.Rel(realDest, realFetched)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return fmt.Errorf("fetching mod: source returned %s, which is outside the staging directory it was given", fetched)
	}
	return nil
}

// fetchFailure maps a Fetcher's error onto the typed error a frontend
// branches on. A fetch that attached no domain.WorkshopFetchFailure is
// wrapped plainly: core invents neither a reason nor a tool name it was
// not told.
func fetchFailure(err error) error {
	var failure *domain.WorkshopFetchFailure
	if !errors.As(err, &failure) {
		return fmt.Errorf("fetching mod: %w", err)
	}
	return &WorkshopFetchError{
		AppID:           failure.AppID,
		PublishedFileID: failure.PublishedFileID,
		Reason:          failure.Reason,
		Tool:            failure.Tool,
		OutputTail:      failure.OutputTail,
		Err:             err,
	}
}

// fetchProgressSink adapts a Fetcher's generic phase vocabulary onto the
// DeployPhase stream every flow already emits, so a fetch's progress
// reaches the CLI, the SSE stream and the SPA's job readout with no new
// carrier and no change to any of them. An unrecognized phase becomes a
// progress tick rather than nothing: a Fetcher must never be able to
// silence itself by naming a phase core has not heard of.
//
// The byte count is deliberately NOT put on the wire as a field of its
// own: StepEvent has none, and adding one would change the JSON of every
// step event every flow emits. A Fetcher that has a byte count words it
// into its own detail sentence, which is what reaches the reader anyway.
func fetchProgressSink(sink EventSink) source.FetchProgressFunc {
	return func(phase, detail string, _ int64) {
		if sink == nil {
			return
		}
		p := WorkshopFetchProgress
		switch phase {
		case source.FetchPhaseStarted:
			p = WorkshopFetchStarted
		case source.FetchPhaseDone:
			p = WorkshopFetchDone
		}
		sink(StepEvent{Scope: Scope{Op: OpDownload}, Phase: p, Detail: detail})
	}
}

// isWorkshopFetch reports whether p is one of the phases a source.Fetcher
// produces.
func (p DeployPhase) isWorkshopFetch() bool {
	return p == WorkshopFetchStarted || p == WorkshopFetchProgress || p == WorkshopFetchDone
}

// forwardFetchStep re-emits a source.Fetcher's own progress step into a
// flow's event stream under that flow's scope, reporting whether it
// handled the event.
//
// Every flow that downloads adapts the downloader's raw stream to its own
// vocabulary with a progressFn that keeps DownloadEvents and drops
// everything else - which is correct for the HTTP path, where the raw
// stream is thousands of byte ticks. A fetch produces no DownloadEvents at
// all, so without this clause the ONLY progress a multi-gigabyte steamcmd
// download can report would be dropped one layer above where it was
// emitted, and the readout would sit at "Working…" for twenty minutes.
//
// The phase is passed through unchanged (unlike a download's, which each
// flow renames): a fetch is the same operation whichever flow asked for
// it, and the frontends humanize the phase name rather than table-match it.
func forwardFetchStep(e Event, scope Scope, emit func(Event)) bool {
	step, ok := e.(StepEvent)
	if !ok || !step.Phase.isWorkshopFetch() {
		return false
	}
	emit(StepEvent{Scope: scope, Phase: step.Phase, Detail: step.Detail})
	return true
}
