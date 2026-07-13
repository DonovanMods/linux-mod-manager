package prototype

// Data is the fake, side-effect-free data set used for visual TUI iteration.
type Data struct {
	Game          Game
	Profile       Profile
	Stats         Stats
	InstalledMods []Mod
	SearchResults []Mod
	Profiles      []Profile
}

type Game struct {
	ID   string
	Name string
}

type Profile struct {
	Name     string
	Active   bool
	ModCount int
}

type Stats struct {
	Installed int
	Enabled   int
	Updates   int
	Conflicts int
}

type Mod struct {
	Name            string
	Source          string
	Author          string
	Version         string
	Status          string
	Summary         string
	Downloads       int64
	Endorsements    int64
	HasEndorsements bool
}

// Load returns static demo data. It must never touch disk, network, DB, or APIs.
func Load() Data {
	return Data{
		Game:    Game{ID: "skyrim-se", Name: "Skyrim Special Edition"},
		Profile: Profile{Name: "survival", Active: true, ModCount: 42},
		Stats: Stats{
			Installed: 42,
			Enabled:   39,
			Updates:   3,
			Conflicts: 1,
		},
		InstalledMods: []Mod{
			{Name: "SkyUI", Source: "nexusmods", Author: "schlangster", Version: "5.2", Status: "installed", Summary: "Immersive user interface overhaul.", Downloads: 12_500_000, Endorsements: 850_000, HasEndorsements: true},
			{Name: "USSEP", Source: "nexusmods", Author: "Arthmoor", Version: "4.3", Status: "update", Summary: "Unofficial Skyrim Special Edition Patch.", Downloads: 11_000_000, Endorsements: 420_000, HasEndorsements: true},
			{Name: "SKSE Address Library", Source: "nexusmods", Author: "meh321", Version: "11", Status: "installed", Summary: "Address library for SKSE plugins.", Downloads: 8_900_000, Endorsements: 150_000, HasEndorsements: true},
			{Name: "Immersive Armors", Source: "nexusmods", Author: "hothtrooper44", Version: "8.1", Status: "conflict", Summary: "Adds hundreds of new armor variants.", Downloads: 6_700_000, Endorsements: 380_000, HasEndorsements: true},
			{Name: "Alternate Start", Source: "nexusmods", Author: "Arthmoor", Version: "4.2", Status: "disabled", Summary: "Alternative character start scenarios.", Downloads: 5_200_000, Endorsements: 220_000, HasEndorsements: true},
		},
		SearchResults: []Mod{
			{Name: "Campfire", Source: "nexusmods", Author: "Chesko", Version: "1.12", Status: "available", Summary: "Camping and survival skill system.", Downloads: 4_200_000, Endorsements: 180_000, HasEndorsements: true},
			{Name: "Frostfall", Source: "nexusmods", Author: "Chesko", Version: "3.4", Status: "available", Summary: "Hypothermia and survival overhaul.", Downloads: 3_800_000, Endorsements: 165_000, HasEndorsements: true},
			{Name: "Hunterborn", Source: "nexusmods", Author: "unuroboros", Version: "1.6", Status: "available", Summary: "Hunting and harvesting overhaul.", Downloads: 2_900_000, Endorsements: 95_000, HasEndorsements: true},
			{Name: "Legacy of the Dragonborn", Source: "nexusmods", Author: "icecreamassassin", Version: "6.5", Status: "available", Summary: "Museum and player home museum.", Downloads: 2_100_000, Endorsements: 78_000, HasEndorsements: true},
		},
		Profiles: []Profile{
			{Name: "survival", Active: true, ModCount: 42},
			{Name: "vanilla-plus", Active: false, ModCount: 18},
			{Name: "graphics-overkill", Active: false, ModCount: 96},
			{Name: "testing", Active: false, ModCount: 7},
		},
	}
}
