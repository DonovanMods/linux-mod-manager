// Package core: this file holds the EXTERNAL-revision rules (#538).
//
// An external mod's installer - today, the Steam client for a Workshop
// item - updates it without lmm. The version an adopt stamped on the row is
// therefore only true until the next Steam-side update, and an update check
// that compared against it reported every item Steam had already updated as
// still needing one.
//
// The installer's own bookkeeping is the truth, so it is read wherever the
// installed revision matters: the update check overlays it on a COPY of each
// external row (CheckGameUpdates stays a pure query), ReconcileExternalMods
// writes it back through a single-row write, and verify reports a record
// that disagrees with it. It is read through source.WorkshopScanner - the
// same seam the adopt flow uses - so any source whose content another agent
// installs plugs in the same way, and core never imports a concrete source.
package core

import (
	"context"
	"fmt"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ExternalModRef names one external mod in a report: its identity and the
// display name a frontend prints.
type ExternalModRef struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name,omitempty"`
}

// ExternalRevisionChange is one row ReconcileExternalMods corrected: the
// revision lmm had recorded and the one the installer reports.
type ExternalRevisionChange struct {
	ExternalModRef
	FromVersion string
	ToVersion   string
	// UpdatedAt is the installer's own time for ToVersion; zero when it
	// gave none, in which case the row's updated_at was left as it was.
	UpdatedAt time.Time
}

// ExternalReconcileResult reports ReconcileExternalMods' outcome.
type ExternalReconcileResult struct {
	// Reconciled is every row whose record now matches its installer, in
	// installed order. Empty when nothing had drifted.
	Reconciled []ExternalRevisionChange
	// Warnings carries why some rows could not be compared or written; a
	// row that could not be compared keeps its recorded revision.
	Warnings []string
}

// externalRevision is one external row's installed revision as its
// installer's bookkeeping reports it right now.
type externalRevision struct {
	version   string
	updatedAt time.Time
}

// externalDrift is what comparing a profile's external rows against their
// installer's bookkeeping found.
type externalDrift struct {
	// revised maps a drifted row's ModKey to the installer's revision.
	revised map[string]externalRevision
	// missing is every external row a fully-read bookkeeping no longer
	// lists - unsubscribed, or removed outside lmm.
	missing []domain.InstalledMod
	// warnings says why rows could not be compared. Such rows keep their
	// recorded revision: an unreadable manifest proves nothing.
	warnings []string
}

// readExternalDrift compares installed's external rows against what their
// installer has on disk. Local reads only - no network call, no write - and
// only when installed holds an external row at all, so a game with none
// never touches Steam's library tree.
//
// An item counts as missing only when the bookkeeping was read IN FULL: at
// least one library held a manifest for the app and none of them warned. A
// damaged manifest or an unreadable library would otherwise make every item
// it holds look unsubscribed. The only error returned is a cancellation;
// every other failure is a warning and a fallback to the recorded revision.
func (s *Service) readExternalDrift(ctx context.Context, game *domain.Game, installed []domain.InstalledMod) (externalDrift, error) {
	var drift externalDrift
	if countExternal(installed) == 0 {
		return drift, nil
	}
	scanner, sourceID, appID, err := s.workshopSourceFor(game)
	if err != nil {
		// An external row on a game with no workshop mapping (the mapping
		// was edited away after adopt): nothing to read it from.
		return drift, nil
	}
	scan, err := scanner.ScanWorkshopItems(ctx, appID)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return drift, cerr
		}
		drift.warnings = append(drift.warnings, fmt.Sprintf("could not read Steam's workshop manifest for app %s, so lmm's recorded revisions were used: %v", appID, err))
		return drift, nil
	}
	complete := len(scan.Roots) > 0 && len(scan.Warnings) == 0
	if !complete {
		if len(scan.Roots) == 0 {
			drift.warnings = append(drift.warnings, fmt.Sprintf("no Steam library holds a workshop manifest for app %s, so lmm's recorded revisions were used", appID))
		}
		drift.warnings = append(drift.warnings, scan.Warnings...)
	}

	items := make(map[string]domain.WorkshopItem, len(scan.Items))
	for _, it := range scan.Items {
		items[it.FileID] = it
	}
	for _, row := range installed {
		if !row.External || row.SourceID != sourceID {
			continue
		}
		item, ok := items[row.ID]
		if !ok {
			if complete {
				drift.missing = append(drift.missing, row)
			}
			continue
		}
		if rev, changed := installedRevision(row, item); changed {
			if drift.revised == nil {
				drift.revised = make(map[string]externalRevision)
			}
			drift.revised[domain.ModKey(row.SourceID, row.ID)] = rev
		}
	}
	return drift, nil
}

// installedRevision is row's revision as item reports it, and whether that
// differs from the record. The manifest is the identity; timeupdated is the
// secondary signal, and the time the revision is dated by. A field the
// installer left empty keeps the row's own value.
func installedRevision(row domain.InstalledMod, item domain.WorkshopItem) (externalRevision, bool) {
	rev := externalRevision{version: row.Version, updatedAt: row.UpdatedAt}
	changed := false
	if item.Manifest != "" && item.Manifest != row.Version {
		rev.version = item.Manifest
		changed = true
	}
	if item.TimeUpdated > 0 && (row.UpdatedAt.IsZero() || row.UpdatedAt.Unix() != item.TimeUpdated) {
		rev.updatedAt = time.Unix(item.TimeUpdated, 0).UTC()
		changed = true
	}
	return rev, changed
}

// overlay returns installed as the installer sees it: a COPY with each
// drifted row's revision replaced and every missing row dropped. installed
// itself is never modified.
func (d externalDrift) overlay(installed []domain.InstalledMod) []domain.InstalledMod {
	if len(d.revised) == 0 && len(d.missing) == 0 {
		return installed
	}
	missing := make(map[string]bool, len(d.missing))
	for _, m := range d.missing {
		missing[domain.ModKey(m.SourceID, m.ID)] = true
	}
	out := make([]domain.InstalledMod, 0, len(installed))
	for _, row := range installed {
		key := domain.ModKey(row.SourceID, row.ID)
		if row.External && missing[key] {
			continue
		}
		if rev, ok := d.revised[key]; ok && row.External {
			row.Version = rev.version
			row.UpdatedAt = rev.updatedAt
		}
		out = append(out, row)
	}
	return out
}

// missingRefs names drift's missing rows for a report.
func (d externalDrift) missingRefs() []ExternalModRef {
	if len(d.missing) == 0 {
		return nil
	}
	out := make([]ExternalModRef, 0, len(d.missing))
	for _, m := range d.missing {
		out = append(out, ExternalModRef{SourceID: m.SourceID, ModID: m.ID, Name: m.Name})
	}
	return out
}

// ReconcileExternalMods records, for every external mod in profileName, the
// revision its installer reports - the version and updated_at Steam's
// manifest names for a Workshop item lmm only tracks (#538). It is the
// write half of what the update check already reads, so the library, `mod
// show` and every other surface reading the record show what is really
// installed.
//
// A single-step mutation: the drift is read first, and only when a row has
// drifted is the mutation slot taken - WITHOUT waiting. The record is a
// convenience the update check does not depend on (it overlays the same
// facts itself), so an in-flight mutation is never worth waiting behind:
// the call fails at once with ErrOperationInProgress (or
// *OperationInProgressError, when another process holds the lock) and a
// caller treats that as a warning; the next call reconciles.
//
// Each row is written through a single UPDATE of its version and
// updated_at - never a full-row save, so no stored checksum is lost (#514),
// and never UpdateModVersion, since there is no earlier copy to roll back
// to. Each unlocked profile ref follows its row (Ruling 16); a locked ref's
// Version is the lock's target and is left alone. Missing items and rows
// whose bookkeeping could not be read are left exactly as they are.
func (s *Service) ReconcileExternalMods(ctx context.Context, game *domain.Game, profileName string) (*ExternalReconcileResult, error) {
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}
	drift, err := s.readExternalDrift(ctx, game, installed)
	if err != nil {
		return nil, err
	}
	if len(drift.revised) == 0 {
		return &ExternalReconcileResult{Warnings: drift.warnings}, nil
	}

	release, err := s.tryBeginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.reconcileExternalMods(ctx, game, profileName, "")
}

// reconcileExternalMods is ReconcileExternalMods' body, for a caller that
// already holds the mutation slot (verify --fix). It re-reads the rows and
// the drift under the slot, so what it writes is never a stale plan. only,
// when set, limits the writes to that one mod id (verify's ModFilter).
func (s *Service) reconcileExternalMods(ctx context.Context, game *domain.Game, profileName, only string) (*ExternalReconcileResult, error) {
	installed, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}
	drift, err := s.readExternalDrift(ctx, game, installed)
	if err != nil {
		return nil, err
	}
	result := &ExternalReconcileResult{Warnings: drift.warnings}

	refVersions := make(map[string]string)
	var cancelled error
	for _, row := range installed {
		key := domain.ModKey(row.SourceID, row.ID)
		rev, ok := drift.revised[key]
		if !ok || !row.External || (only != "" && row.ID != only) {
			continue
		}
		// Checked BEFORE each write: a row written is a row whose profile
		// ref must follow, so a cancellation stops further rows and still
		// completes the ref writes owed for the rows already done.
		if cerr := ctx.Err(); cerr != nil {
			cancelled = cerr
			break
		}
		if err := s.db.SetExternalRevision(ctx, row.SourceID, row.ID, game.ID, profileName, rev.version, rev.updatedAt); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("could not record Steam's revision of %s: %v", row.Name, err))
			continue
		}
		result.Reconciled = append(result.Reconciled, ExternalRevisionChange{
			ExternalModRef: ExternalModRef{SourceID: row.SourceID, ModID: row.ID, Name: row.Name},
			FromVersion:    row.Version,
			ToVersion:      rev.version,
			UpdatedAt:      rev.updatedAt,
		})
		if rev.version != row.Version {
			refVersions[key] = rev.version
		}
	}

	if len(refVersions) > 0 {
		pm := s.NewProfileManager()
		if err := completeProfileWrite(ctx, func(ctx context.Context) error {
			return pm.setUnlockedRefVersions(ctx, game.ID, profileName, refVersions)
		}); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return result, cerr
			}
			result.Warnings = append(result.Warnings, fmt.Sprintf("recorded Steam's revisions, but could not update profile %q: %v", profileName, err))
		}
	}
	return result, cancelled
}
