package tui

import (
	"context"
	"fmt"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
)

// coreProvider adapts *core.Service to the read-only DataProvider boundary.
type coreProvider struct {
	svc     *core.Service
	game    *domain.Game
	profile string
}

// NewCoreProvider returns a DataProvider backed by the real app service for
// one game/profile pair.
func NewCoreProvider(svc *core.Service, game *domain.Game, profileName string) DataProvider {
	return &coreProvider{svc: svc, game: game, profile: profileName}
}

func (p *coreProvider) Overview(_ context.Context) (Summary, []ModItem, error) {
	mods, err := p.svc.GetInstalledMods(p.game.ID, p.profile)
	if err != nil {
		return Summary{}, nil, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, p.profile, err)
	}

	enabled := 0
	for _, mod := range mods {
		if mod.Enabled {
			enabled++
		}
	}

	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		items = append(items, ModItem{
			Name:    mod.Name,
			Author:  mod.Author,
			Version: mod.Version,
			Source:  mod.SourceID,
			Status:  installedModStatus(mod),
		})
	}

	return Summary{
		GameName:    p.game.Name,
		ProfileName: p.profile,
		Installed:   len(mods),
		Enabled:     enabled,
		Updates:     -1, // unknown: update checks are a Phase 6 workflow
		Conflicts:   -1, // unknown: conflict detection is a Phase 6 workflow
	}, items, nil
}

func (p *coreProvider) Sources() []string {
	sources := make([]string, 0, len(p.game.SourceIDs))
	for id := range p.game.SourceIDs {
		sources = append(sources, id)
	}
	sort.Strings(sources)
	return sources
}

// SourceInfos returns every source registered with the underlying service,
// sorted by ID. Sorting is required, not cosmetic: registry iteration order
// is Go map order, which is intentionally randomized, and an unsorted list
// would jitter row order between renders (mirrors cmd/lmm/auth.go's
// ListSources-sorting note).
func (p *coreProvider) SourceInfos() []SourceInfo {
	srcs := p.svc.ListSources()
	infos := make([]SourceInfo, 0, len(srcs))
	for _, src := range srcs {
		infos = append(infos, SourceInfo{
			ID:           src.ID(),
			Name:         src.Name(),
			Type:         customSourceType(src),
			Auth:         sourceAuthState(src),
			Capabilities: sourceCapabilitySummary(source.CapabilitiesOf(src)),
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
}

// customSourceType classifies a registered source for display. It mirrors
// cmd/lmm/source.go's isCustomSource switch; that helper isn't reused
// directly since cmd/lmm is a `package main` and not importable here, so the
// classification is kept in sync by hand — extend this switch alongside
// isCustomSource if a new custom source type ships.
func customSourceType(src source.ModSource) string {
	switch src.(type) {
	case *custom.Directory:
		return "directory"
	case *custom.Manifest:
		return "manifest"
	case *custom.API:
		return "api"
	default:
		return "built-in"
	}
}

// sourceAuthState reports a source's authentication status for display.
// Mirrors cmd/lmm/source.go's authState (see customSourceType's comment on
// why it's duplicated rather than imported).
func sourceAuthState(src source.ModSource) string {
	if !source.CapabilitiesOf(src).Auth {
		return "n/a"
	}
	if a, ok := src.(interface{ IsAuthenticated() bool }); ok {
		if a.IsAuthenticated() {
			return "yes"
		}
		return "no"
	}
	return "yes"
}

// sourceCapabilitySummary renders capabilities as a compact list, e.g.
// "search,updates". Mirrors cmd/lmm/source.go's capabilitySummary (see
// customSourceType's comment on why it's duplicated rather than imported).
func sourceCapabilitySummary(c source.Capabilities) string {
	out := ""
	add := func(enabled bool, name string) {
		if !enabled {
			return
		}
		if out != "" {
			out += ","
		}
		out += name
	}
	add(c.Search, "search")
	add(c.Dependencies, "deps")
	add(c.Updates, "updates")
	add(c.Auth, "auth")
	return out
}

// Search queries the given source, or every one of the game's configured
// sources when sourceID is "" (the all-sources sentinel), and marks results
// already installed in the active profile.
func (p *coreProvider) Search(ctx context.Context, sourceID, query string, page int) (SearchPage, error) {
	if sourceID == "" {
		agg, err := p.svc.SearchAllSources(ctx, p.game.ID, query, "", nil, page, SearchPageSize)
		if err != nil {
			return SearchPage{}, fmt.Errorf("searching all sources for %q: %w", query, err)
		}

		installedKeys, err := p.installedModKeys()
		if err != nil {
			return SearchPage{}, err
		}

		warnings := make([]string, 0, len(agg.Warnings))
		for _, w := range agg.Warnings {
			warnings = append(warnings, fmt.Sprintf("%s: %v", w.SourceID, w.Err))
		}

		return SearchPage{
			Results:    p.modsToItems(agg.Mods, installedKeys),
			Query:      query,
			Source:     sourceID,
			Page:       page,
			PageSize:   SearchPageSize,
			TotalCount: agg.TotalCount,
			Warnings:   warnings,
		}, nil
	}

	result, err := p.svc.SearchMods(ctx, sourceID, p.game.ID, query, "", nil, page, SearchPageSize)
	if err != nil {
		return SearchPage{}, fmt.Errorf("searching %s for %q: %w", sourceID, query, err)
	}

	installedKeys, err := p.installedModKeys()
	if err != nil {
		return SearchPage{}, err
	}

	return SearchPage{
		Results:    p.modsToItems(result.Mods, installedKeys),
		Query:      query,
		Source:     sourceID,
		Page:       page,
		PageSize:   SearchPageSize,
		TotalCount: result.TotalCount,
	}, nil
}

// installedModKeys returns the set of domain.ModKey(sourceID, modID) values
// installed in the active profile, used to mark search results as installed.
func (p *coreProvider) installedModKeys() (map[string]bool, error) {
	installed, err := p.svc.GetInstalledMods(p.game.ID, p.profile)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, p.profile, err)
	}
	keys := make(map[string]bool, len(installed))
	for _, mod := range installed {
		keys[domain.ModKey(mod.SourceID, mod.ID)] = true
	}
	return keys, nil
}

// modsToItems maps source search results to renderable rows, marking each
// as installed via domain.ModKey(sourceID, modID) against installedKeys.
func (p *coreProvider) modsToItems(mods []domain.Mod, installedKeys map[string]bool) []ModItem {
	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		status := "available"
		if installedKeys[domain.ModKey(mod.SourceID, mod.ID)] {
			status = "installed"
		}
		item := ModItem{
			Name:      mod.Name,
			Author:    mod.Author,
			Version:   mod.Version,
			Source:    mod.SourceID,
			Status:    status,
			Summary:   mod.Summary,
			Downloads: mod.Downloads,
		}
		if mod.Endorsements != nil {
			item.Endorsements = *mod.Endorsements
			item.HasEndorsements = true
		}
		items = append(items, item)
	}
	return items
}

func (p *coreProvider) Profiles(_ context.Context) ([]ProfileItem, error) {
	profiles, err := p.svc.NewProfileManager().List(p.game.ID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles for %s: %w", p.game.ID, err)
	}

	items := make([]ProfileItem, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, ProfileItem{
			Name:     profile.Name,
			Active:   profile.Name == p.profile,
			ModCount: len(profile.Mods),
		})
	}
	return items, nil
}

func installedModStatus(mod domain.InstalledMod) string {
	switch {
	case mod.Enabled && mod.Deployed:
		return "deployed"
	case mod.Enabled:
		return "enabled"
	default:
		return "disabled"
	}
}
