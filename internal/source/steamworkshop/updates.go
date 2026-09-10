// Package steamworkshop: this file is the update check - the capability
// Tier 1 exists for.
//
// A Workshop item's version identity is Steam's CONTENT ID (the ACF's
// `manifest`, the API's `hcontent_file`), not a dotted version string, so
// domain.CompareVersions is never consulted for this source: it computes
// the answer itself, as icarus already does for its own identity scheme.
package steamworkshop

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// CheckUpdates reports which of the given installed items Steam has a newer
// revision of. It is the no-progress, no-refresh form of
// CheckUpdatesRefreshing.
func (s *Source) CheckUpdates(ctx context.Context, installed []domain.InstalledMod) ([]domain.Update, error) {
	return s.CheckUpdatesRefreshing(ctx, installed, false, nil)
}

// CheckUpdatesWithProgress implements source.UpdateProgressReporter so the
// existing per-mod progress line works for this source too.
func (s *Source) CheckUpdatesWithProgress(ctx context.Context, installed []domain.InstalledMod, report source.UpdateProgressFunc) ([]domain.Update, error) {
	return s.CheckUpdatesRefreshing(ctx, installed, false, report)
}

// CheckUpdatesRefreshing implements source.RefreshingUpdateChecker: the
// same check, with refresh bypassing the on-disk metadata cache
// (`lmm update --refresh`, and the SPA's refresh action).
//
// The comparison, per item:
//
//   - PRIMARY: the API's hcontent_file differs from the installed content
//     id (InstalledMod.Version, stamped from the ACF at adopt time) - an
//     exact content-identity mismatch, the strongest signal there is.
//   - SECONDARY: when the API reports no content id, its time_updated is
//     later than the installed revision's. Timestamps can theoretically
//     move without content changing, which is why it is the fallback.
//
// An item Valve refuses to describe (result != 1) is NEVER reported as
// having an update: "delisted" and "out of date" are different facts, and
// conflating them would tell the user to chase an update that does not
// exist. A transport failure is likewise an error, not silence - an item
// lmm could not ask about must not read as "up to date".
//
// It never writes anything and never touches the items themselves. What it
// finds is a NOTIFICATION: Steam applies a Workshop update itself at the
// next game launch, so lmm's job ends at saying so.
func (s *Source) CheckUpdatesRefreshing(ctx context.Context, installed []domain.InstalledMod, refresh bool, report source.UpdateProgressFunc) ([]domain.Update, error) {
	if len(installed) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(installed))
	for _, im := range installed {
		ids = append(ids, im.ID)
	}

	details, err := s.client.fetchDetails(ctx, ids, refresh)
	if err != nil {
		return nil, fmt.Errorf("source %q: checking updates: %w", sourceID, err)
	}

	var updates []domain.Update
	total := len(installed)
	for i, im := range installed {
		if err := ctx.Err(); err != nil {
			return updates, err
		}
		if report != nil {
			report(i+1, total, im.Name)
		}
		d, ok := details[im.ID]
		if !ok || !d.available() {
			// Unavailable: reported by `lmm verify` and `lmm mod show`, never
			// as an update.
			continue
		}
		newVersion, changed := compareRevision(im, d)
		if !changed {
			continue
		}
		updates = append(updates, domain.Update{
			InstalledMod: im,
			NewVersion:   newVersion,
		})
	}
	return updates, nil
}

// compareRevision applies the primary/secondary signals to one item and
// returns the new content identity alongside whether anything changed.
//
// The new version is reported as the API's content id where there is one,
// so an applied update's recorded Version stays the same kind of value the
// ACF holds. Where there is none, the timestamp stands in - it is the only
// identity available, and it is still comparable.
func compareRevision(im domain.InstalledMod, d itemDetails) (newVersion string, changed bool) {
	if remote := contentVersion(d); remote != "" {
		return remote, remote != im.Version
	}
	// Secondary signal: no content id from the API. Compare timestamps
	// against the installed revision's own recorded time.
	installedAt := im.UpdatedAt.Unix()
	if im.UpdatedAt.IsZero() || d.TimeUpdated <= installedAt {
		return im.Version, false
	}
	return time.Unix(d.TimeUpdated, 0).UTC().Format(time.RFC3339), true
}

// DescribeMods implements source.BatchModDescriber: it resolves metadata
// for a batch of published-file ids in as few round trips as Valve's
// hundred-per-request limit allows, and reports one entry per id in the
// order given. refresh bypasses the metadata cache.
//
// It is the batch read a workshop adopt needs: thirty subscribed items
// resolve in one request rather than thirty. Per-item unavailability is
// data, never an error - an item Valve refuses to describe is still on disk
// and still loaded by the game, so it stays adoptable with only the
// identity the ACF carries. Only a failure to reach Valve at all is
// returned as an error.
func (s *Source) DescribeMods(ctx context.Context, gameID string, fileIDs []string, refresh bool) ([]source.ModDescription, error) {
	if len(fileIDs) == 0 {
		return nil, nil
	}
	details, err := s.client.fetchDetails(ctx, fileIDs, refresh)
	if err != nil {
		return nil, fmt.Errorf("source %q: describing items: %w", sourceID, err)
	}
	out := make([]source.ModDescription, 0, len(fileIDs))
	for _, id := range fileIDs {
		d, ok := details[id]
		if !ok || !d.available() {
			out = append(out, source.ModDescription{
				ModID:       id,
				Unavailable: true,
				Note:        "Steam does not describe this item - it may be delisted, deleted or private",
			})
			continue
		}
		out = append(out, source.ModDescription{ModID: id, Mod: modFromDetails(d, gameID)})
	}
	return out, nil
}

// IsUnavailable reports whether err is the per-item "Valve will not
// describe this" outcome, so a caller can branch without importing the
// sentinel's wrapping details.
func IsUnavailable(err error) bool { return errors.Is(err, ErrItemUnavailable) }
