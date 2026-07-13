package tui

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

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
	Name    string
	Author  string
	Version string
	Source  string
	Status  string
}

// ProfileItem is one renderable profile row.
type ProfileItem struct {
	Name     string
	Active   bool
	ModCount int
}

// DataProvider is the narrow, read-only boundary between the TUI and app
// data. Implementations must be safe to call from a Bubble Tea command
// goroutine.
type DataProvider interface {
	Summary(ctx context.Context) (Summary, error)
	InstalledMods(ctx context.Context) ([]ModItem, error)
	SearchResults(ctx context.Context) ([]ModItem, error)
	Profiles(ctx context.Context) ([]ProfileItem, error)
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

func (p prototypeProvider) Summary(_ context.Context) (Summary, error) {
	return Summary{
		GameName:    p.data.Game.Name,
		ProfileName: p.data.Profile.Name,
		Installed:   p.data.Stats.Installed,
		Enabled:     p.data.Stats.Enabled,
		Updates:     p.data.Stats.Updates,
		Conflicts:   p.data.Stats.Conflicts,
	}, nil
}

func (p prototypeProvider) InstalledMods(_ context.Context) ([]ModItem, error) {
	return modItems(p.data.InstalledMods), nil
}

func (p prototypeProvider) SearchResults(_ context.Context) ([]ModItem, error) {
	return modItems(p.data.SearchResults), nil
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
			Name:    mod.Name,
			Author:  mod.Author,
			Version: mod.Version,
			Source:  mod.Source,
			Status:  mod.Status,
		})
	}
	return items
}
