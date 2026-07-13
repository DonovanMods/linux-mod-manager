package tui

import (
	"context"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

const SearchPageSize = 10

// Summary is the dashboard header data.
type Summary struct {
	GameName    string
	ProfileName string
	Installed   int
	Enabled     int
	Updates     int // -1 = unknown (no update check has run)
	Conflicts   int // -1 = unknown
}

// ModItem is one renderable mod row.
type ModItem struct {
	Name            string
	Author          string
	Version         string
	Source          string
	Status          string
	Summary         string
	Downloads       int64
	Endorsements    int64
	HasEndorsements bool
}

// ProfileItem is one renderable profile row.
type ProfileItem struct {
	Name     string
	Active   bool
	ModCount int
}

// SearchPage is one page of search results for one source/query.
type SearchPage struct {
	Results    []ModItem
	Query      string
	Source     string
	Page       int // 0-based
	PageSize   int
	TotalCount int // 0 if the source doesn't report totals
}

// DataProvider is the narrow, read-only boundary between the TUI and app
// data. Implementations must be safe to call from a Bubble Tea command
// goroutine.
type DataProvider interface {
	// Overview returns the dashboard summary and installed-mod rows from a
	// single underlying fetch.
	Overview(ctx context.Context) (Summary, []ModItem, error)
	Profiles(ctx context.Context) ([]ProfileItem, error)
	// Sources lists the game's configured source IDs, sorted; index 0 is the
	// default (mirrors the CLI's resolveSource).
	Sources() []string
	Search(ctx context.Context, source, query string, page int) (SearchPage, error)
}

// prototypeProvider serves the static demo data set. It must never touch
// disk, network, DB, or APIs.
type prototypeProvider struct {
	data prototype.Data
}

// NewPrototypeProvider returns the side-effect-free demo DataProvider used
// by --prototype mode and tests.
func NewPrototypeProvider() DataProvider {
	return prototypeProvider{data: prototype.Load()}
}

func (p prototypeProvider) Overview(_ context.Context) (Summary, []ModItem, error) {
	return Summary{
		GameName:    p.data.Game.Name,
		ProfileName: p.data.Profile.Name,
		Installed:   p.data.Stats.Installed,
		Enabled:     p.data.Stats.Enabled,
		Updates:     p.data.Stats.Updates,
		Conflicts:   p.data.Stats.Conflicts,
	}, modItems(p.data.InstalledMods), nil
}

func (p prototypeProvider) Sources() []string {
	return []string{"nexusmods"}
}

func (p prototypeProvider) Search(_ context.Context, source, query string, _ int) (SearchPage, error) {
	all := modItems(p.data.SearchResults)
	matched := make([]ModItem, 0, len(all))
	for _, item := range all {
		if strings.Contains(strings.ToLower(item.Name), strings.ToLower(query)) {
			matched = append(matched, item)
		}
	}
	return SearchPage{
		Results:    matched,
		Query:      query,
		Source:     source,
		Page:       0,
		PageSize:   SearchPageSize,
		TotalCount: len(matched),
	}, nil
}

func (p prototypeProvider) Profiles(_ context.Context) ([]ProfileItem, error) {
	items := make([]ProfileItem, 0, len(p.data.Profiles))
	for _, profile := range p.data.Profiles {
		items = append(items, ProfileItem{
			Name:     profile.Name,
			Active:   profile.Active,
			ModCount: profile.ModCount,
		})
	}
	return items, nil
}

func modItems(mods []prototype.Mod) []ModItem {
	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		items = append(items, ModItem{
			Name:            mod.Name,
			Author:          mod.Author,
			Version:         mod.Version,
			Source:          mod.Source,
			Status:          mod.Status,
			Summary:         mod.Summary,
			Downloads:       mod.Downloads,
			Endorsements:    mod.Endorsements,
			HasEndorsements: mod.HasEndorsements,
		})
	}
	return items
}
