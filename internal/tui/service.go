package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

// SearchPageSize mirrors the CLI picker's displayPageSize (cmd/lmm/install.go).
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

// ModItem is one renderable mod row. ID, together with Source, fully
// addresses the mod for core mutations keyed on (sourceID, modID) - see
// ActionProvider.
type ModItem struct {
	ID              string
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

// SourceInfo is one renderable source-registry row, mirroring the columns of
// `lmm source list` (cmd/lmm/source.go) minus its Error column: the Sources
// screen only lists sources that are actually REGISTERED with the service.
// Source-definition load failures (a malformed YAML file, an ID collision)
// never produce a registered source, so they have no row here and remain a
// CLI-only diagnostic (`lmm source list` / `lmm source validate`).
type SourceInfo struct {
	ID           string
	Name         string
	Type         string // "built-in", "directory", "manifest", or "api"
	Auth         string // "yes", "no", or "n/a" (source has no auth capability)
	Capabilities string // compact list, e.g. "search,updates"
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
	// Warnings holds per-source failures in all-sources mode, already
	// formatted for display (e.g. "<sourceID>: <err>"). Empty for
	// single-source searches.
	Warnings []string
}

// DataProvider is the narrow, read-only boundary between the TUI and app
// data. Implementations must be safe to call from a Bubble Tea command
// goroutine.
type DataProvider interface {
	// Overview returns the dashboard summary and installed-mod rows from a
	// single underlying fetch.
	Overview(ctx context.Context) (Summary, []ModItem, error)
	Profiles(ctx context.Context) ([]ProfileItem, error)
	// Sources lists the game's configured real source IDs, sorted. The TUI
	// prepends the all-sources sentinel ("") ahead of these (see
	// newSearchModel); the CLI instead defaults to an aggregate search
	// across all of them when --source is omitted (see doSearch in
	// cmd/lmm/search.go).
	Sources() []string
	// SourceInfos lists every source registered with the service (built-in
	// and user-defined), sorted by ID, for the read-only Sources screen. See
	// SourceInfo's doc comment for how this differs from Sources.
	SourceInfos() []SourceInfo
	// Search queries one source, or every one of the game's configured
	// sources when source is "" (the documented all-sources sentinel).
	Search(ctx context.Context, source, query string, page int) (SearchPage, error)
	// DeployedFiles lists the relative paths a specific installed mod has
	// deployed into the game directory, sorted, for the read-only files
	// overlay (Task 4). An empty slice with a nil error means the mod is
	// known but has nothing currently deployed (e.g. disabled).
	DeployedFiles(sourceID, modID string) ([]string, error)
}

// prototypeProvider serves the static demo data set. It must never touch
// disk, network, DB, or APIs.
type prototypeProvider struct {
	data prototype.Data
}

// NewPrototypeProvider returns the side-effect-free demo DataProvider used
// by --prototype mode and tests. The returned value also implements
// ActionProvider (see actions_provider.go's prototypeProvider methods): a
// caller that needs both roles for one demo session should type-assert the
// single returned value (`provider.(ActionProvider)`) rather than calling
// this constructor twice, since each call seeds an independent in-memory
// dataset - two calls would silently diverge instead of sharing state.
func NewPrototypeProvider() DataProvider {
	return &prototypeProvider{data: prototype.Load()}
}

func (p *prototypeProvider) Overview(_ context.Context) (Summary, []ModItem, error) {
	return Summary{
		GameName:    p.data.Game.Name,
		ProfileName: p.data.Profile.Name,
		Installed:   p.data.Stats.Installed,
		Enabled:     p.data.Stats.Enabled,
		Updates:     p.data.Stats.Updates,
		Conflicts:   p.data.Stats.Conflicts,
	}, modItems(p.data.InstalledMods), nil
}

func (p *prototypeProvider) Sources() []string {
	return []string{"nexusmods"}
}

func (p *prototypeProvider) SourceInfos() []SourceInfo {
	return []SourceInfo{
		{ID: "curseforge", Name: "CurseForge", Type: "built-in", Auth: "n/a", Capabilities: "search,updates"},
		{ID: "local-mods", Name: "Local Mods", Type: "directory", Auth: "n/a", Capabilities: "search,updates"},
		{ID: "nexusmods", Name: "Nexus Mods", Type: "built-in", Auth: "yes", Capabilities: "search,deps,updates,auth"},
	}
}

func (p *prototypeProvider) Search(_ context.Context, source, query string, _ int) (SearchPage, error) {
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

// DeployedFiles returns 2-3 plausible canned rows derived from item's name
// (falling back to the raw modID when it isn't one of the canned
// InstalledMods/SearchResults entries - see findInstalledIndex/
// findSearchResult in actions_provider.go): deterministic, no randomness,
// and never errors, matching this type's "never touch disk/network"
// contract. Sorted, matching the interface's documented contract (coreProvider
// gets this for free from its query's ORDER BY - see that type's own
// DeployedFiles).
func (p *prototypeProvider) DeployedFiles(sourceID, modID string) ([]string, error) {
	name := modID
	if idx := p.findInstalledIndex(sourceID, modID); idx >= 0 {
		name = p.data.InstalledMods[idx].Name
	} else if idx := p.findSearchResult(sourceID, modID); idx >= 0 {
		name = p.data.SearchResults[idx].Name
	}
	files := []string{
		fmt.Sprintf("%s.esp", name),
		fmt.Sprintf("Data/%s.bsa", name),
		fmt.Sprintf("textures/%s/main.dds", modID),
	}
	sort.Strings(files)
	return files, nil
}

func (p *prototypeProvider) Profiles(_ context.Context) ([]ProfileItem, error) {
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
			ID:              mod.ID,
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
