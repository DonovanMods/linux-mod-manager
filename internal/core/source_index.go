// Package core: this file is the frontend-facing half of the local-index
// seam (#360). A source that answers Search from a locally cached index
// (today, Thunderstore) is the only kind of source with an index to SHOW or
// to rebuild on demand; these two methods are how a frontend reaches it
// without knowing which source that is.
//
// Nothing here imports the concrete source package - the seam is
// source.LocalIndexSource, type-asserted, exactly as WorkshopScanner and
// MergeCompiler are.
package core

import (
	"context"
	"fmt"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// The IndexReport.Status vocabulary: what a refresh DID.
const (
	// IndexStatusBuilt: the index was fetched and written - either there
	// was none, or upstream had moved.
	IndexStatusBuilt = "built"
	// IndexStatusCurrent: the copy on disk was already current, so nothing
	// was rewritten. This is what a conditional GET answering 304 earns,
	// and what a refresh inside the TTL costs.
	IndexStatusCurrent = "current"
	// IndexStatusStale: the refresh FAILED over an index still worth
	// serving. The old copy is what is on disk and what searches answer
	// from; Warnings says why it could not be replaced. Not an error: an
	// unreachable upstream must not take a working index away.
	IndexStatusStale = "stale"
)

// IndexStatus is one source's local search index for one game, as a
// frontend shows it (#360). It is the `--json` document for the read half
// of the index surface.
//
// Game is the SOURCE's own game identifier - a Thunderstore community slug
// - because that, not lmm's game id, is what identifies the index on disk
// and what the user sees named in a "building the index for ..." line.
type IndexStatus struct {
	Source    string    `json:"source"`
	Game      string    `json:"game"`
	Present   bool      `json:"present"`
	Packages  int       `json:"packages"`
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Bytes     int64     `json:"bytes"`
	Stale     bool      `json:"stale"`
}

// IndexReport is what a refresh did, as a frontend shows it (#360).
//
// Status and Changed are deliberately two different facts. Status is the
// ACTION - built, already current, or failed over a copy still being served
// - and Changed is whether the index the user searches is now different
// from the one they had. A forced rebuild that produced an identical index
// is Status "built" and Changed false, which is exactly the answer to "did
// refreshing help?".
type IndexReport struct {
	Source     string   `json:"source"`
	Game       string   `json:"game"`
	Status     string   `json:"status"`
	Changed    bool     `json:"changed"`
	Packages   int      `json:"packages"`
	Bytes      int64    `json:"bytes"`
	DurationMS int64    `json:"duration_ms"`
	Warnings   []string `json:"warnings"`
}

// SourceIndexStatus reports what sourceID has cached for gameID, or nil
// when that source keeps no local index at all - which is how a frontend
// decides whether the index surface exists for it. An unknown source is the
// only error case.
//
// A READ: it takes no mutation slot and makes no request. A source with a
// stale index says so here rather than quietly refreshing it, because a
// frontend asking "what have I got" has not asked for 34 MB to be fetched.
func (s *Service) SourceIndexStatus(ctx context.Context, sourceID, gameID string) (*IndexStatus, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, err
	}
	indexed, ok := src.(source.LocalIndexSource)
	if !ok {
		return nil, nil //nolint:nilnil // "this source keeps no index" is an ANSWER, not a failure - see the doc comment
	}
	status, err := indexed.IndexStatus(ctx, s.sourceGameID(sourceID, gameID))
	if err != nil {
		return nil, fmt.Errorf("reading the %s index: %w", sourceID, err)
	}
	report := indexStatusOf(sourceID, status)
	return &report, nil
}

// RefreshSourceIndex rebuilds sourceID's local index for gameID, skipping
// the source's own TTL when force is set, and reports what it did.
//
// A MUTATION: it writes under the cache root, so it takes the Service's
// mutation slot like every other write. That is the deliberate difference
// from the build Search does for itself, which is a read and must never
// take the slot - a search that queued behind a running deploy would be a
// frontend that appears to hang.
//
// sink receives one StepEvent per tick the source reports. A refresh that
// fails over a usable index is NOT an error: it comes back as a report with
// Status "stale" and the reason in Warnings.
func (s *Service) RefreshSourceIndex(ctx context.Context, sourceID, gameID string, force bool, sink EventSink) (*IndexReport, error) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return nil, err
	}
	indexed, ok := src.(source.LocalIndexSource)
	if !ok {
		return nil, fmt.Errorf("source %q keeps no local index: %w", sourceID, source.ErrNotSupported)
	}

	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	sourceGameID := s.sourceGameID(sourceID, gameID)
	before, err := indexed.IndexStatus(ctx, sourceGameID)
	if err != nil {
		return nil, fmt.Errorf("reading the %s index: %w", sourceID, err)
	}

	started := time.Now()
	after, refreshErr := indexed.RefreshIndex(ctx, sourceGameID, force, func(phase, detail string, _ int64) {
		if sink == nil {
			return
		}
		sink(StepEvent{Scope: Scope{Op: OpSourceIndex}, Phase: indexPhase(phase), Detail: detail})
	})
	elapsed := time.Since(started)

	if refreshErr != nil && !after.Present {
		// Nothing usable on disk and nothing to be had: the caller has no
		// index, which is a failure however it is worded.
		return nil, refreshErr
	}

	report := &IndexReport{
		Source:     sourceID,
		Game:       sourceGameID,
		Status:     IndexStatusCurrent,
		Packages:   after.Packages,
		Bytes:      after.Bytes,
		DurationMS: elapsed.Milliseconds(),
		Warnings:   []string{},
	}
	switch {
	case refreshErr != nil:
		report.Status = IndexStatusStale
		report.Warnings = append(report.Warnings, refreshErr.Error())
	case indexContentMoved(before, after):
		report.Status = IndexStatusBuilt
		report.Changed = true
	}
	return report, nil
}

// indexContentMoved reports whether the index a user searches is now
// different from the one they had. Package count and footprint are the two
// things that cannot stay equal across a real change, and both come back
// from the source without a second read of the files themselves.
func indexContentMoved(before, after source.IndexStatus) bool {
	if !before.Present {
		return after.Present
	}
	return before.Packages != after.Packages || before.Bytes != after.Bytes
}

// indexStatusOf renders the seam's status as the wire document.
func indexStatusOf(sourceID string, status source.IndexStatus) IndexStatus {
	return IndexStatus{
		Source:    sourceID,
		Game:      status.GameID,
		Present:   status.Present,
		Packages:  status.Packages,
		FetchedAt: status.FetchedAt,
		Bytes:     status.Bytes,
		Stale:     status.Stale,
	}
}

// indexPhase maps a source's own progress vocabulary onto the DeployPhase
// the event stream carries - the same mapping the Fetcher seam gets.
func indexPhase(phase string) DeployPhase {
	switch phase {
	case source.FetchPhaseStarted:
		return IndexRefreshStarted
	case source.FetchPhaseDone:
		return IndexRefreshDone
	default:
		return IndexRefreshProgress
	}
}

// sourceGameID translates lmm's game id into the identifier sourceID knows
// the game by, exactly as SearchMods does. An empty mapping (a directory
// source's "this applies to any game") must not blank the id out.
func (s *Service) sourceGameID(sourceID, gameID string) string {
	if game, ok := s.game(gameID); ok {
		if id, ok := game.SourceIDs[sourceID]; ok && id != "" {
			return id
		}
	}
	return gameID
}
