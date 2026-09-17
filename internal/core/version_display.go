package core

import (
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// VersionUnknown is the version lmm records for a mod imported from an
// archive whose name carries no version (#459). It stays on the wire as it
// is - `lmm list` and `--json` show "unknown" - but it is not a version a
// surface may label as one: VersionText reads it as no version at all.
const VersionUnknown = "unknown"

// VersionText is the version text a surface shows for m, in the one place
// that decides it: core's stamped DisplayVersion when there is one (#458),
// "" for a mod with no version - including the VersionUnknown placeholder
// (#459), so a surface that labels a version ("v1.2", "Version: 1.2")
// drops the label rather than printing "vunknown" - and Version otherwise.
func VersionText(m *domain.Mod) string {
	if m == nil {
		return ""
	}
	if m.DisplayVersion != "" {
		return m.DisplayVersion
	}
	if m.Version == VersionUnknown {
		return ""
	}
	return m.Version
}

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
