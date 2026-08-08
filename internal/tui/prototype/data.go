package prototype

import "time"

// Data is the fake, side-effect-free data set used for visual TUI iteration.
type Data struct {
	Game          Game
	Profile       Profile
	Stats         Stats
	InstalledMods []Mod
	SearchResults []Mod
	Profiles      []Profile
	// AltGame/AltMods are Task 8's second canned game, letting --prototype
	// mode demo the in-TUI game switcher ('g') end to end: AltMods is
	// deliberately tiny (1-2 entries) - the demo only needs the switch to
	// visibly work, not a second full dataset. See
	// prototypeProvider.ListGames/SetGame (service.go/actions_provider.go)
	// for how these back the switcher.
	AltGame Game
	AltMods []Mod
	// Conflicts is the PRIMARY game's canned file-conflict set (Task 3),
	// feeding prototypeProvider.Conflicts (service.go) for the Conflicts
	// screen's --prototype demo: one stale entry and one in-sync entry, so
	// the demo shows both the stale marker and both detail-pane hint copy
	// variants. The alt game has none - see prototypeProvider.Conflicts' own
	// doc comment.
	Conflicts []Conflict
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

// Conflict is one canned file-conflict row (Task 3) - see Data.Conflicts.
type Conflict struct {
	Path   string
	Owner  string
	Winner string
	AlsoIn []string
	Stale  bool
}

type Stats struct {
	Installed int
	Enabled   int
	Updates   int
	Conflicts int
	// LastDeploy feeds prototypeProvider.Overview's Summary.LastDeploy
	// (#106a's dashboard "Last deploy" row) for the PRIMARY game only - see
	// that method's doc comment for why the alt game leaves it at the zero
	// value (treated as "never deployed", same as its Updates/Conflicts
	// sentinels). Computed relative to Load()'s own call time rather than a
	// fixed wall-clock constant, so the canned "N ago" demo value stays
	// sensible no matter when --prototype mode is actually run.
	LastDeploy time.Time
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

	// Description/SourceURL/PictureURL feed the mod details view (#86). Only
	// a few entries set them; the view's omit-when-empty rules are exactly
	// what the sparse entries exercise.
	Description string
	SourceURL   string
	PictureURL  string

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

	// Changelog feeds prototypeProvider.CheckUpdates' UpdateItem.Changelog
	// (Phase 6b Task 7): skyui carries a canned multi-line changelog and
	// ussep deliberately leaves it empty, so --prototype mode can demo both
	// the changelog overlay's normal case ('v' on the apply-updates modal)
	// and its "no changelog available" one. Every other InstalledMods entry
	// leaves this unset, matching AvailableVersion's own "never invent a
	// phantom update" convention.
	Changelog string

	// PreviousVersion feeds prototypeProvider.Rollback's fake swap (Task 6):
	// a non-empty value marks the InstalledMods entry as rollback-eligible,
	// mirroring domain.InstalledMod.PreviousVersion's own "version before
	// last update" contract - ModItem.PreviousVersion (service.go) is
	// populated straight from this field. Every other canned mod leaves it
	// unset, so --prototype mode can also demo the "no previous version to
	// roll back to" refusal (mutations.go's rollbackSelectedMod) on those.
	PreviousVersion string

	// Locked/LockedVersion feed prototypeProvider.SetLock/Unlock's in-memory
	// lock flags (#97) and ModItem.Locked/LockedVersion's projection
	// (service.go's modItems) - mirroring UpdatePolicy's own "canned field
	// mutated in place, visible in a repeated Overview" convention above.
	// LockedVersion is only meaningful (and only ever set) while Locked is
	// true - Unlock clears both, matching ModItem.LockedVersion's own
	// "empty whenever Locked is false" contract. Every InstalledMods entry
	// leaves both unset, so --prototype mode can also demo the unlocked
	// case on every canned mod.
	Locked        bool
	LockedVersion string
}

// Load returns static demo data. It must never touch disk, network, DB, or APIs.
func Load() Data {
	return Data{
		Game:    Game{ID: "skyrim-se", Name: "Skyrim Special Edition"},
		AltGame: Game{ID: "fallout4", Name: "Fallout 4"},
		AltMods: []Mod{
			{ID: "f4se", Name: "Fallout 4 Script Extender", Source: "nexusmods", Author: "behippo", Version: "0.6.23", Status: "installed", Summary: "Script extender required by most other mods.", Downloads: 9_100_000, Endorsements: 310_000, HasEndorsements: true},
			{ID: "unofficial-patch", Name: "Unofficial Fallout 4 Patch", Source: "nexusmods", Author: "Arthmoor", Version: "2.1.3", Status: "disabled", Summary: "Community bug-fix compilation.", Downloads: 4_400_000, Endorsements: 190_000, HasEndorsements: true},
		},
		Profile: Profile{Name: "survival", Active: true, ModCount: 42},
		Stats: Stats{
			Installed:  42,
			Enabled:    39,
			Updates:    3,
			Conflicts:  2,
			LastDeploy: time.Now().Add(-3 * time.Hour),
		},
		InstalledMods: []Mod{
			// skyui is the details view's "rich" demo entry (#86): a
			// multi-paragraph Description plus both SourceURL and
			// PictureURL, so --prototype mode can show the full render
			// path. Every other InstalledMods entry deliberately leaves
			// these three unset (the "sparse" path), matching the file's
			// existing "never invent a phantom X" convention.
			{ID: "skyui", Name: "SkyUI", Source: "nexusmods", Author: "schlangster", Version: "5.2", Status: "installed", Summary: "Immersive user interface overhaul.", Downloads: 12_500_000, Endorsements: 850_000, HasEndorsements: true, UpdatePolicy: "auto", AvailableVersion: "5.3", Changelog: "Fixed a crash when opening the inventory with a controller.\nAdded a compatibility patch for the newest SKSE build.\nMinor MCM menu polish.",
				Description: "SkyUI replaces Skyrim's default controller-oriented interface with one built for mouse and keyboard.\n\nIt also ships the MCM (Mod Configuration Menu) framework that most other mods depend on for their own settings screens, so most modded setups install it early.",
				SourceURL:   "https://www.nexusmods.com/skyrimspecialedition/mods/12604",
				PictureURL:  "https://staticdelivery.nexusmods.com/mods/1704/images/12604/12604-1234567890.jpg",
			},
			{ID: "ussep", Name: "USSEP", Source: "nexusmods", Author: "Arthmoor", Version: "4.3", Status: "update", Summary: "Unofficial Skyrim Special Edition Patch.", Downloads: 11_000_000, Endorsements: 420_000, HasEndorsements: true, UpdatePolicy: "notify", AvailableVersion: "4.4"},
			{ID: "skse-address-library", Name: "SKSE Address Library", Source: "nexusmods", Author: "meh321", Version: "11", Status: "installed", Summary: "Address library for SKSE plugins.", Downloads: 8_900_000, Endorsements: 150_000, HasEndorsements: true, PreviousVersion: "10"},
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
		Conflicts: []Conflict{
			// Stale: the DB owner (Immersive Armors) disagrees with the
			// load-order winner (USSEP) - a redeploy would flip who wins.
			{Path: "meshes/armor/steel/f/1stperson/steel_helmet.nif", Owner: "Immersive Armors", Winner: "USSEP", AlsoIn: []string{"USSEP"}, Stale: true},
			// In-sync: owner and winner already agree.
			{Path: "textures/frost.dds", Owner: "USSEP", Winner: "USSEP", AlsoIn: []string{"Immersive Armors"}, Stale: false},
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
