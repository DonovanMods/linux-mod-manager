// Package core: this file holds #543's manual-only record - which mods a
// source will not serve through its API, so they can only be updated from a
// hand-downloaded file - and the refusal a batch update gives for one.
package core

import (
	"cmp"
	"context"
	"errors"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// ReasonManualDownload is the refusal an update batch records for a
// manual-only mod (#543): the update is reported, but lmm never downloads
// it automatically. The CLI and the web UI print it verbatim beside the
// mod's page and the from-file remedy.
const ReasonManualDownload = "its source will not serve this file through its API, so lmm never downloads it automatically - download it by hand from the mod's page and update from that file"

// stampManualOnly sets Mod.ManualOnly on every row of gameID the source is
// recorded as refusing to serve. The fact lives in its own table (see
// db.RecordManualOnly), so the rows read from installed_mods never carry it
// themselves. A read failure leaves the rows as they were and is logged: the
// flag is advisory, and a listing must not fail over it.
func (s *Service) stampManualOnly(ctx context.Context, gameID string, rows []domain.InstalledMod) {
	if len(rows) == 0 {
		return
	}
	recorded, err := s.db.ManualOnlyMods(ctx, gameID)
	if err != nil {
		s.logger().Debug("cannot read manual-only mods", "game", gameID, "err", err)
		return
	}
	for i := range rows {
		if recorded[domain.ModKey(rows[i].SourceID, rows[i].ID)] {
			rows[i].ManualOnly = true
		}
	}
}

// manualOnlyRecorded reports whether sourceID's modID is recorded
// manual-only for gameID, on stampManualOnly's terms.
func (s *Service) manualOnlyRecorded(ctx context.Context, gameID, sourceID, modID string) bool {
	recorded, err := s.db.ManualOnlyMods(ctx, gameID)
	if err != nil {
		s.logger().Debug("cannot read manual-only mods", "game", gameID, "err", err)
		return false
	}
	return recorded[domain.ModKey(sourceID, modID)]
}

// carryManualOnly keeps each update's ManualOnly when the source rebuilt the
// row it reports: the update is flagged when the installed row it is for was
// flagged, or when the source classified it so itself during the check.
func carryManualOnly(updates []domain.Update, installed []domain.InstalledMod) {
	flagged := make(map[string]bool)
	for _, m := range installed {
		if m.ManualOnly {
			flagged[domain.ModKey(m.SourceID, m.ID)] = true
		}
	}
	for i := range updates {
		if flagged[domain.ModKey(updates[i].InstalledMod.SourceID, updates[i].InstalledMod.ID)] {
			updates[i].InstalledMod.ManualOnly = true
		}
	}
}

// recordManualOnly records that sourceID will not serve modID for gameID
// through its API. A failure is logged, never returned: what the flow was
// doing is unaffected, and the next refusal records it again.
func (s *Service) recordManualOnly(ctx context.Context, gameID, sourceID, modID string) {
	if sourceID == "" || sourceID == domain.SourceLocal || modID == "" {
		return
	}
	if err := s.db.RecordManualOnly(ctx, gameID, sourceID, modID); err != nil {
		s.logger().Warn("cannot record a manual-only mod", "game", gameID, "mod", domain.ModKey(sourceID, modID), "err", err)
	}
}

// clearManualOnly removes recordManualOnly's record, on the same terms.
func (s *Service) clearManualOnly(ctx context.Context, gameID, sourceID, modID string) {
	if sourceID == "" || sourceID == domain.SourceLocal || modID == "" {
		return
	}
	if err := s.db.ClearManualOnly(ctx, gameID, sourceID, modID); err != nil {
		s.logger().Warn("cannot clear a manual-only mod", "game", gameID, "mod", domain.ModKey(sourceID, modID), "err", err)
	}
}

// noteDownloadOutcome is what a download of sourceID's mod for gameID
// teaches about the source: a refusal (source.ErrManualDownload) records the
// mod as manual-only - whether an installed row exists yet or not - and a
// file the source served clears that record, since it proves the source
// serves the mod now.
func (s *Service) noteDownloadOutcome(ctx context.Context, gameID, sourceID string, mod *domain.Mod, err error) {
	if mod == nil {
		return
	}
	switch {
	case err == nil:
		s.clearManualOnly(ctx, gameID, sourceID, mod.ID)
	case errors.Is(err, source.ErrManualDownload):
		s.recordManualOnly(ctx, gameID, sourceID, mod.ID)
	}
}

// noteSourceClassification records mod as manual-only when its source
// classified it so in the metadata it returned (CurseForge's
// allowModDistribution: false) - the way an install from a hand-downloaded
// file, an archive import or an adoption learns it without any download
// being tried. A source that says nothing leaves any earlier record alone.
func (s *Service) noteSourceClassification(ctx context.Context, gameID string, mod *domain.Mod) {
	if mod != nil && mod.ManualOnly {
		s.recordManualOnly(ctx, gameID, mod.SourceID, mod.ID)
	}
}

// manualOnlySkip renders a manual-only batch item as the UpdateApplyResult a
// batch records for it (#543): the same document planUpdateSkip produces
// for a locked or external one, with ReasonManualDownload and the page to
// download it from. The ref is the installed one, unflagged - nothing was
// written.
func manualOnlySkip(plan *UpdatePlan, upd domain.Update) UpdateApplyResult {
	return UpdateApplyResult{
		Mod:         domain.ModReference{SourceID: plan.Mod.SourceID, ModID: plan.Mod.ID, Version: plan.Mod.Version},
		Name:        plan.Mod.Name,
		FromVersion: plan.Mod.Version,
		ToVersion:   upd.NewVersion,
		Status:      UpdateSkipped,
		Reason:      ReasonManualDownload,
		ManualOnly:  true,
		ModURL:      cmp.Or(plan.Mod.PageURL(), upd.InstalledMod.PageURL()),
	}
}
