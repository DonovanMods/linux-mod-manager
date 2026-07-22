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
	// Mods is optional: installed-mod IDs this profile references, used
	// only to seed PlanProfileSwitch's NeedsDownloads demo scenario (see
	// NeedsDownloadProfileName below and actions_provider.go's
	// prototypeProvider.PlanProfileSwitch). Every other canned profile
	// leaves this nil - the alternating Enable/Disable plan logic never
	// consults it.
	Mods []string
}

// NeedsDownloadProfileName names the one canned profile (see Load) whose
// Mods list references an ID absent from InstalledMods, so
// prototypeProvider.PlanProfileSwitch (actions_provider.go) can produce a
// NeedsDownloads plan and --prototype mode can demo the refusal state
// without any core.Service.
const NeedsDownloadProfileName = "requiem-overhaul"

type Stats struct {
	Installed int
	Enabled   int
	Updates   int
	Conflicts int
}

type Mod struct {
	ID              string // stable, invented demo identifier - addresses the mod alongside Source for action calls
	Name            string
	Source          string
	Author          string
	Version         string
	Status          string
	Summary         string
	Downloads       int64
	Endorsements    int64
	HasEndorsements bool

	// Dependencies/Conflicts/SizeBytes feed prototypeProvider.PlanInstall's
	// fake plan (actions_provider.go) for a SearchResults entry: canned
	// dependency/conflict display lines and a declared download size. Every
	// InstalledMods entry (and most SearchResults entries) leaves these
	// unset, matching the "never invent a phantom X" convention Profile.Mods
	// already follows; SizeBytes <= 0 means "size unknown" (InstallPlanView.
	// SizeLabel's documented contract).
	Dependencies []string
	Conflicts    []string
	SizeBytes    int64

	// UpdatePolicy/AvailableVersion feed prototypeProvider.CheckUpdates' fake
	// canned set: an InstalledMods entry with a non-empty AvailableVersion
	// reports an available update from Version to AvailableVersion.
	// UpdatePolicy ("auto" or "notify") is canned alongside it for a future
	// keybinding layer to consult - CheckUpdates itself (an ActionProvider
	// method) doesn't project policy into UpdateItem, see that type's doc
	// comment. Every other InstalledMods entry leaves both unset.
	UpdatePolicy     string
	AvailableVersion string
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
			{ID: "skyui", Name: "SkyUI", Source: "nexusmods", Author: "schlangster", Version: "5.2", Status: "installed", Summary: "Immersive user interface overhaul.", Downloads: 12_500_000, Endorsements: 850_000, HasEndorsements: true, UpdatePolicy: "auto", AvailableVersion: "5.3"},
			{ID: "ussep", Name: "USSEP", Source: "nexusmods", Author: "Arthmoor", Version: "4.3", Status: "update", Summary: "Unofficial Skyrim Special Edition Patch.", Downloads: 11_000_000, Endorsements: 420_000, HasEndorsements: true, UpdatePolicy: "notify", AvailableVersion: "4.4"},
			{ID: "skse-address-library", Name: "SKSE Address Library", Source: "nexusmods", Author: "meh321", Version: "11", Status: "installed", Summary: "Address library for SKSE plugins.", Downloads: 8_900_000, Endorsements: 150_000, HasEndorsements: true},
			{ID: "immersive-armors", Name: "Immersive Armors", Source: "nexusmods", Author: "hothtrooper44", Version: "8.1", Status: "conflict", Summary: "Adds hundreds of new armor variants.", Downloads: 6_700_000, Endorsements: 380_000, HasEndorsements: true},
			{ID: "alternate-start", Name: "Alternate Start", Source: "nexusmods", Author: "Arthmoor", Version: "4.2", Status: "disabled", Summary: "Alternative character start scenarios.", Downloads: 5_200_000, Endorsements: 220_000, HasEndorsements: true},
		},
		SearchResults: []Mod{
			{ID: "campfire", Name: "Campfire", Source: "nexusmods", Author: "Chesko", Version: "1.12", Status: "available", Summary: "Camping and survival skill system.", Downloads: 4_200_000, Endorsements: 180_000, HasEndorsements: true, Dependencies: []string{"SKSE64"}, SizeBytes: 4_500_000},
			{ID: "frostfall", Name: "Frostfall", Source: "nexusmods", Author: "Chesko", Version: "3.4", Status: "available", Summary: "Hypothermia and survival overhaul.", Downloads: 3_800_000, Endorsements: 165_000, HasEndorsements: true, Conflicts: []string{"textures/frost.dds (owned by ussep)"}},
			{ID: "hunterborn", Name: "Hunterborn", Source: "nexusmods", Author: "unuroboros", Version: "1.6", Status: "available", Summary: "Hunting and harvesting overhaul.", Downloads: 2_900_000, Endorsements: 95_000, HasEndorsements: true},
			{ID: "legacy-of-the-dragonborn", Name: "Legacy of the Dragonborn", Source: "nexusmods", Author: "icecreamassassin", Version: "6.5", Status: "available", Summary: "Museum and player home museum.", Downloads: 2_100_000, Endorsements: 78_000, HasEndorsements: true},
			// skyui deliberately reuses an InstalledMods (Source, ID) pair so
			// --prototype mode can demo Phase 5b's Reinstall path (i on an
			// already-installed search result): prototypeProvider.PlanInstall
			// computes Reinstall by checking InstalledMods live, so this entry
			// needs no special-casing beyond simply existing here.
			{ID: "skyui", Name: "SkyUI", Source: "nexusmods", Author: "schlangster", Version: "5.2", Status: "installed", Summary: "Immersive user interface overhaul.", Downloads: 12_500_000, Endorsements: 850_000, HasEndorsements: true},
		},
		Profiles: []Profile{
			{Name: "survival", Active: true, ModCount: 42},
			{Name: "vanilla-plus", Active: false, ModCount: 18},
			{Name: "graphics-overkill", Active: false, ModCount: 96},
			{Name: "testing", Active: false, ModCount: 7},
			// requiem-legendary is not in InstalledMods above - switching
			// here always yields a NeedsDownloads plan (see
			// NeedsDownloadProfileName's doc comment).
			{Name: NeedsDownloadProfileName, Active: false, ModCount: 2, Mods: []string{"skyui", "requiem-legendary"}},
		},
	}
}
