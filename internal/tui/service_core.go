package tui

import (
	"context"
	"fmt"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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
