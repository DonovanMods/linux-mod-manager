package serve

import (
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// modsPageData is "/mods"'s template data. List is nil whenever
// resolveSelection didn't resolve a ready game+profile (docs/plans/2026-08-30-serve-impl.md
// Task 4 ruling on game/profile selection): the template renders the
// friendly empty state in that case instead of an empty table.
type modsPageData struct {
	pageChrome
	List *core.ModList
}

// handleMods renders "/mods": the profile's installed mods
// (docs/plans/2026-08-30-serve-design.md §HTTP surface: "installed list +
// enable/disable/uninstall forms" via ListMods). The per-row mutation forms
// target the routes Task 8 (#322) wires (POST /mods/{source}/{id}/enable,
// /disable, /uninstall) but render disabled until then.
func (s *Server) handleMods(w http.ResponseWriter, r *http.Request) {
	sel, err := s.resolveSelection(r)
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := modsPageData{pageChrome: s.chrome(r, "Mods", &sel)}
	if sel.ready() {
		list, err := s.svc.ListMods(r.Context(), sel.Game, sel.Profile)
		if err != nil {
			s.renderError(w, err)
			return
		}
		data.List = list
	}

	s.render(w, modsTemplate, data)
}

// modDetailPageData is "/mods/{source}/{id}"'s template data. Detail is nil
// when the selection isn't ready (docs/plans/2026-08-30-serve-impl.md Task 4
// ruling) or the mod itself could not be found (NotFound); Files/Versions
// are best-effort and stay nil when their own lookup fails, matching
// ModDetail's own best-effort Changelog.
type modDetailPageData struct {
	pageChrome
	SourceID string
	ModID    string
	Detail   *core.ModDetail
	Files    *core.ModFilesReport
	Versions []string
	NotFound bool
}

// handleModDetail renders "/mods/{source}/{id}": full prose (#232),
// changelog (#87), deployed files (ModFiles), available versions
// (AvailableModVersions), and an install form shell targeting the route
// Task 8 (#322) wires (docs/plans/2026-08-30-serve-design.md §HTTP surface).
// Mod.Description and ModDetail.Changelog render as auto-escaped TEXT only -
// never template.HTML - per the Global Constraints escaping ruling.
func (s *Server) handleModDetail(w http.ResponseWriter, r *http.Request) {
	sourceID := r.PathValue("source")
	modID := r.PathValue("id")

	sel, err := s.resolveSelection(r)
	if err != nil {
		s.renderError(w, err)
		return
	}

	data := modDetailPageData{pageChrome: s.chrome(r, "Mod", &sel), SourceID: sourceID, ModID: modID}
	if !sel.ready() {
		s.render(w, modDetailTemplate, data)
		return
	}

	ctx := r.Context()
	detail, err := s.svc.ModDetail(ctx, sel.Game, sel.Profile, sourceID, modID)
	if err != nil {
		data.NotFound = true
		s.renderStatus(w, http.StatusNotFound, modDetailTemplate, data)
		return
	}
	data.Detail = detail
	data.Title = detail.Mod.Name

	if detail.Installed != nil {
		if files, ferr := s.svc.ModFiles(ctx, sel.Game, sel.Profile, sourceID, modID); ferr == nil {
			data.Files = files
		} else {
			s.log.Debug("mod detail page: ModFiles failed", "source", sourceID, "mod", modID, "err", ferr)
		}
	}

	if versions, verr := s.svc.AvailableModVersions(ctx, sourceID, detail.Mod); verr == nil {
		data.Versions = versions
	} else {
		s.log.Debug("mod detail page: AvailableModVersions failed", "source", sourceID, "mod", modID, "err", verr)
	}

	s.render(w, modDetailTemplate, data)
}
