package core

import (
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// NoRevisionDate is domain.Mod.DisplayVersion for a Workshop item lmm has
// no revision date for (#458).
const NoRevisionDate = "-"

// displayVersionFor is domain.Mod.DisplayVersion for a mod of sourceID
// (#458): a Workshop item's revision date - an external row's, or any mod
// of the workshop-capable source, which a Tier-3 download is - and empty
// for every other mod. UTC, as every surface has rendered the date.
func (s *Service) displayVersionFor(external bool, sourceID string, updatedAt time.Time) string {
	if !external && !s.sourceIsWorkshop(sourceID) {
		return ""
	}
	if updatedAt.Unix() <= 0 {
		return NoRevisionDate
	}
	return updatedAt.UTC().Format("2006-01-02")
}

// stampDisplayVersion sets m's DisplayVersion; external is its installed
// row's External, false for a mod with none.
func (s *Service) stampDisplayVersion(m *domain.Mod, external bool) {
	if m != nil {
		m.DisplayVersion = s.displayVersionFor(external, m.SourceID, m.UpdatedAt)
	}
}

// stampInstalledDisplay stamps each row's DisplayVersion.
func (s *Service) stampInstalledDisplay(rows []domain.InstalledMod) {
	for i := range rows {
		s.stampDisplayVersion(&rows[i].Mod, rows[i].External)
	}
}
