// Package core: this file holds #543's manual-only record - which mods a
// source will not serve through its API, so they can only be updated from a
// hand-downloaded file - and the refusal a batch update gives for one.
package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"

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
// being tried - and clears that record when the source says explicitly that
// it serves the mod (#627, domain.Mod.Distributable). A source that says
// nothing leaves any earlier record alone.
func (s *Service) noteSourceClassification(ctx context.Context, gameID string, mod *domain.Mod) {
	switch {
	case mod == nil:
	case mod.ManualOnly:
		s.recordManualOnly(ctx, gameID, mod.SourceID, mod.ID)
	case explicitlyDistributable(mod):
		s.clearManualOnly(ctx, gameID, mod.SourceID, mod.ID)
	}
}

// explicitlyDistributable reports a mod its source says, explicitly, that it
// serves through its API (#627). An absent classification is not a yes.
func explicitlyDistributable(mod *domain.Mod) bool {
	return mod != nil && mod.Distributable != nil && *mod.Distributable
}

// ManualOnlyCleared is one mod whose manual-only record (#543) an update
// check cleared because its source now says it serves the mod's files
// through its API (#627): CurseForge's allowModDistribution turned back on.
// The mod takes part in update batches and automatic updates again. A check
// reports it once - the record it cleared is gone - so a frontend says so
// once (UpdateCheckReport.ManualOnlyCleared).
type ManualOnlyCleared struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name,omitempty"`
	// Message is the sentence both frontends show: "<name>: automatic
	// downloads are available again - <source> serves it through its API
	// now".
	Message string `json:"message"`
}

// clearDistributableManualOnly clears the manual-only record of every mod in
// installed that its source said, during this update check, it serves
// through its API (#627) - reported through source.ReportFetchedMod
// (fetched), or on the update the source built for it - and returns those
// it cleared, in installed's order. It costs no request: the check fetched
// the mods anyway. A mod with no record is not news, and an absent
// classification clears nothing. Best-effort, like the record itself: a
// failure is logged and the mod stays recorded until the next check.
func (s *Service) clearDistributableManualOnly(ctx context.Context, gameID string, installed []domain.InstalledMod, updates []domain.Update, fetched *fetchedMods) []ManualOnlyCleared {
	said := fetched.distributableKeys()
	for i := range updates {
		if explicitlyDistributable(&updates[i].InstalledMod.Mod) {
			if said == nil {
				said = map[string]bool{}
			}
			said[domain.ModKey(updates[i].InstalledMod.SourceID, updates[i].InstalledMod.ID)] = true
		}
	}
	if len(said) == 0 || ctx.Err() != nil {
		return nil
	}
	recorded, err := s.db.ManualOnlyMods(ctx, gameID)
	if err != nil {
		s.logger().Debug("cannot read manual-only mods", "game", gameID, "err", err)
		return nil
	}
	var cleared []ManualOnlyCleared
	for _, m := range installed {
		key := domain.ModKey(m.SourceID, m.ID)
		if !said[key] || !recorded[key] {
			continue
		}
		delete(recorded, key) // a mod listed twice is cleared, and reported, once
		// A download refused between the source's answer and this delete
		// may record the mod just before the clear removes it again. That
		// race is benign and heals itself: the next attempt is refused the
		// same way and records it again (noteDownloadOutcome).
		if err := s.db.ClearManualOnly(ctx, gameID, m.SourceID, m.ID); err != nil {
			s.logger().Warn("cannot clear a manual-only mod", "game", gameID, "mod", key, "err", err)
			continue
		}
		name := cmp.Or(m.Name, m.ID)
		sourceName := m.SourceID
		if src, err := s.GetSource(m.SourceID); err == nil {
			sourceName = cmp.Or(src.Name(), m.SourceID)
		}
		cleared = append(cleared, ManualOnlyCleared{
			SourceID: m.SourceID,
			ModID:    m.ID,
			Name:     m.Name,
			Message:  fmt.Sprintf("%s: automatic downloads are available again - %s serves it through its API now", name, sourceName),
		})
	}
	return cleared
}

// unflagCleared takes ManualOnly off the updates of the mods cleared: the
// row they were built from was stamped before the clear.
func unflagCleared(updates []domain.Update, cleared []ManualOnlyCleared) {
	if len(cleared) == 0 {
		return
	}
	keys := make(map[string]bool, len(cleared))
	for _, c := range cleared {
		keys[domain.ModKey(c.SourceID, c.ModID)] = true
	}
	for i := range updates {
		if keys[domain.ModKey(updates[i].InstalledMod.SourceID, updates[i].InstalledMod.ID)] {
			updates[i].InstalledMod.ManualOnly = false
		}
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
