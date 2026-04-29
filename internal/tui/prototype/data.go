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
	Name    string
	Source  string
	Author  string
	Version string
	Status  string
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
			{Name: "SkyUI", Source: "nexusmods", Author: "schlangster", Version: "5.2", Status: "installed"},
			{Name: "USSEP", Source: "nexusmods", Author: "Arthmoor", Version: "4.3", Status: "update"},
			{Name: "SKSE Address Library", Source: "nexusmods", Author: "meh321", Version: "11", Status: "installed"},
			{Name: "Immersive Armors", Source: "nexusmods", Author: "hothtrooper44", Version: "8.1", Status: "conflict"},
			{Name: "Alternate Start", Source: "nexusmods", Author: "Arthmoor", Version: "4.2", Status: "disabled"},
		},
		SearchResults: []Mod{
			{Name: "Campfire", Source: "nexusmods", Author: "Chesko", Version: "1.12", Status: "available"},
			{Name: "Frostfall", Source: "nexusmods", Author: "Chesko", Version: "3.4", Status: "available"},
			{Name: "Hunterborn", Source: "nexusmods", Author: "unuroboros", Version: "1.6", Status: "available"},
			{Name: "Legacy of the Dragonborn", Source: "nexusmods", Author: "icecreamassassin", Version: "6.5", Status: "available"},
		},
		Profiles: []Profile{
			{Name: "survival", Active: true, ModCount: 42},
			{Name: "vanilla-plus", Active: false, ModCount: 18},
			{Name: "graphics-overkill", Active: false, ModCount: 96},
			{Name: "testing", Active: false, ModCount: 7},
		},
	}
}
