package domain_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/require"
)

// updateJSONGoldens re-records the JSON contract goldens for every
// domain type. Run ONCE:
//
//	go test ./internal/domain/ -run TestJSONGoldens -update-json-goldens
//
// After that the files are frozen: they pin the exact wire shape (snake_case
// keys, enums as text names, nil slices as "[]") that Task 19's json tags
// promise, so any future field/tag change shows up as a diff here instead of
// silently reaching a future JSON frontend. Named -update-json-goldens
// rather than -update: cmd/lmm already registers package-level "-update"
// (verify_golden_test.go) and "-update-modshow" (mod_show_golden_test.go),
// and internal/core registers "-update-events" (events_golden_test.go) -
// Go's flag package panics on a duplicate registration within the same test
// binary, so this name follows that established disambiguation convention.
var updateJSONGoldens = flag.Bool("update-json-goldens", false, "rewrite internal/domain/testdata/json/*.golden")

// fixedTime is the sample timestamp every golden with a time.Time field
// uses, so a golden diff is never just "the clock moved."
var fixedTime = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

func TestJSONGoldens(t *testing.T) {
	endorsements := int64(500)

	tests := []struct {
		name  string
		value any
	}{
		{
			"mod_file",
			domain.ModFile{
				Path:     "Data/textures/armor/mesh.dds",
				Size:     2048576,
				Checksum: "3b8d5a1f2c9e7d6b4a0f1e2d3c4b5a6978869788978978978978978978978a",
			},
		},
		{
			"downloadable_file",
			domain.DownloadableFile{
				ID:          "file-1",
				Name:        "Main File",
				FileName:    "sample-mod-1.2.3.zip",
				Version:     "1.2.3",
				Size:        104857600,
				IsPrimary:   true,
				Category:    "MAIN",
				Description: "The primary archive for this mod.",
				SHA256:      "d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2d2",
			},
		},
		{
			"mod_reference",
			domain.ModReference{
				SourceID: "nexusmods",
				ModID:    "12345",
				Version:  "2.0.0",
				FileIDs:  []string{"file-1", "file-2"},
				Locked:   true,
			},
		},
		{
			// Dependencies is deliberately left nil (no `omitempty` on the
			// tag) to pin that a nil slice marshals as "[]", not "null".
			"mod",
			domain.Mod{
				ID:           "42",
				SourceID:     "nexusmods",
				Name:         "Sample Mod",
				Version:      "1.2.3",
				Author:       "Sample Author",
				Summary:      "A short one-line summary.",
				Description:  "A longer, multi-sentence description of the mod.",
				GameID:       "skyrim-se",
				Category:     "Gameplay",
				Downloads:    15000,
				Endorsements: &endorsements,
				PictureURL:   "https://example.invalid/picture.jpg",
				SourceURL:    "https://example.invalid/mods/42",
				Files: []domain.ModFile{
					{Path: "Data/textures/armor/mesh.dds", Size: 2048576, Checksum: "abc123"},
				},
				Dependencies: nil,
				UpdatedAt:    fixedTime,
			},
		},
		{
			// Files (embedded via Mod) is deliberately left nil this time -
			// Mod's own golden above nils Dependencies instead - so both of
			// Mod's non-omitempty slice fields are pinned as "[]" somewhere
			// in the set.
			"installed_mod",
			domain.InstalledMod{
				Mod: domain.Mod{
					ID:           "42",
					SourceID:     "nexusmods",
					Name:         "Sample Mod",
					Version:      "1.2.3",
					Author:       "Sample Author",
					Summary:      "A short one-line summary.",
					Description:  "A longer, multi-sentence description of the mod.",
					GameID:       "skyrim-se",
					Category:     "Gameplay",
					Downloads:    15000,
					Endorsements: &endorsements,
					PictureURL:   "https://example.invalid/picture.jpg",
					SourceURL:    "https://example.invalid/mods/42",
					Files:        nil,
					Dependencies: []domain.ModReference{{SourceID: "nexusmods", ModID: "99", Version: "1.0.0"}},
					UpdatedAt:    fixedTime,
				},
				ProfileName:     "default",
				UpdatePolicy:    domain.UpdateAuto,
				InstalledAt:     fixedTime,
				Enabled:         true,
				Deployed:        true,
				PreviousVersion: "1.2.2",
				PreviousFileIDs: []string{"file-0"},
				LinkMethod:      domain.LinkHardlink,
				FileIDs:         []string{"file-1"},
				ManualDownload:  true,
				ConvertPaks:     true,
			},
		},
		{
			// #269 Tier 1: the same struct for an EXTERNAL mod - a Steam
			// Workshop item lmm tracks but never deploys. Its absences are
			// the contract as much as its two new keys: no file ids (lmm
			// downloaded nothing), no manual_download, and Version is the
			// ACF content id rather than anything human-readable.
			"installed_mod_external",
			domain.InstalledMod{
				Mod: domain.Mod{
					ID:          "3617086610",
					SourceID:    "steamworkshop",
					Name:        "Sample Workshop Item",
					Version:     "7987119735124793734",
					Author:      "76561198000000000",
					Summary:     "An item subscribed in the Steam client.",
					Description: "An item subscribed in the Steam client.",
					GameID:      "space-engineers-2",
					Category:    "Blueprint",
					PictureURL:  "https://example.invalid/preview.jpg",
					SourceURL:   "https://steamcommunity.com/sharedfiles/filedetails/?id=3617086610",
					UpdatedAt:   fixedTime,
				},
				ProfileName:  "default",
				UpdatePolicy: domain.UpdateNotify,
				InstalledAt:  fixedTime,
				Enabled:      true,
				Deployed:     true,
				LinkMethod:   domain.LinkSymlink,
				External:     true,
				ExternalPath: "/home/user/.steam/steam/steamapps/workshop/content/1133870/3617086610",
			},
		},
		{
			// The scan row #269's adopt flow is built from: what Steam's
			// own appworkshop manifest says about one installed item.
			"workshop_item",
			domain.WorkshopItem{
				FileID:      "3617086610",
				Path:        "/home/user/.steam/steam/steamapps/workshop/content/1133870/3617086610",
				SizeOnDisk:  572330,
				Manifest:    "7987119735124793734",
				TimeUpdated: 1764767935,
			},
		},
		{
			"update",
			domain.Update{
				InstalledMod: domain.InstalledMod{
					Mod: domain.Mod{
						ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.2",
						GameID: "skyrim-se", UpdatedAt: fixedTime,
					},
					ProfileName:  "default",
					UpdatePolicy: domain.UpdateNotify,
					InstalledAt:  fixedTime,
					Enabled:      true,
					Deployed:     true,
					LinkMethod:   domain.LinkSymlink,
				},
				NewVersion:         "1.2.3",
				Changelog:          "Fixed a crash on load.",
				FileIDReplacements: map[string]string{"file-0": "file-1"},
				RecompileNeeded:    true,
				RecompileReason:    "base pak updated",
			},
		},
		{
			"game",
			domain.Game{
				ID:                  "skyrim-se",
				Name:                "Skyrim Special Edition",
				InstallPath:         "/home/user/games/skyrim-se",
				ModPath:             "/home/user/games/skyrim-se/Data",
				SourceIDs:           map[string]string{"nexusmods": "skyrimspecialedition", "curseforge": "skyrim-special-edition"},
				LinkMethod:          domain.LinkSymlink,
				LinkMethodExplicit:  true,
				CachePath:           "/home/user/.cache/lmm/skyrim-se",
				Hooks:               domain.GameHooks{Install: domain.HookConfig{BeforeAll: "scripts/before_all.sh"}, Uninstall: domain.HookConfig{AfterAll: "scripts/after_all.sh"}},
				DeployMode:          domain.DeployCompile,
				ConvertPaks:         true,
				ConvertPaksExplicit: true,
			},
		},
		{
			// #359's loader block on its own: the four members, with the
			// two closed enums as their wire NAMES rather than ints.
			"game_loader",
			domain.GameLoader{
				Kind:      domain.LoaderKindBepInEx,
				Version:   "5.4.23.5",
				Runtime:   domain.LoaderRuntimeMono,
				Bootstrap: domain.LoaderBootstrapProton,
			},
		},
		{
			// A BepInEx game: the game-root shape (mod_path IS install_path,
			// #358) plus the loader block, so the ADDITIVE "loader" member's
			// placement in the Game document is pinned. game.golden above is
			// the loaderless case, and stays byte-identical to what it was
			// before this field existed.
			"game_bepinex",
			domain.Game{
				ID:          "lethal-company",
				Name:        "Lethal Company",
				InstallPath: "/home/user/.steam/steam/steamapps/common/Lethal Company",
				ModPath:     "/home/user/.steam/steam/steamapps/common/Lethal Company",
				SourceIDs:   map[string]string{"nexusmods": "lethalcompany"},
				LinkMethod:  domain.LinkSymlink,
				Loader: &domain.GameLoader{
					Kind:      domain.LoaderKindBepInEx,
					Version:   "5.4.23.5",
					Runtime:   domain.LoaderRuntimeMono,
					Bootstrap: domain.LoaderBootstrapProton,
				},
			},
		},
		{
			// A CURATED candidate: every field the known-games entry
			// supplies, plus #206's "known": true.
			"detected_game",
			domain.DetectedGame{
				SteamAppID:  "489830",
				Slug:        "skyrim-se",
				Name:        "Skyrim Special Edition",
				InstallPath: "/home/user/.steam/steam/steamapps/common/Skyrim Special Edition",
				ModPath:     "/home/user/.steam/steam/steamapps/common/Skyrim Special Edition/Data",
				NexusID:     "skyrimspecialedition",
				DeployMode:  "extract",
				Sources:     map[string]string{"nexusmods": "skyrimspecialedition"},
				Known:       true,
			},
		},
		{
			// #206's OTHER half, pinned as its own golden because its
			// absences are the contract: an installed game nobody has
			// curated carries the manifest's name, a derived slug and an
			// install path - an EMPTY mod path, no sources, no nexus id,
			// and no "known" member at all (omitzero). A client that reads
			// this row must default the mod path itself and say so.
			"detected_game_unknown",
			domain.DetectedGame{
				SteamAppID:  "526870",
				Slug:        "satisfactory",
				Name:        "Satisfactory",
				InstallPath: "/home/user/.steam/steam/steamapps/common/Satisfactory",
			},
		},
		{
			// #416: a curated entry that declares the game's mod loader.
			// The catalog answers kind and version only - runtime and
			// bootstrap are facts about a particular installation, which
			// `lmm game show` reads off the game directory - so those two
			// members are absent, and so is `loader` itself on every other
			// detect document (omitempty), which is what keeps the three
			// goldens above byte-identical.
			"detected_game_loader",
			domain.DetectedGame{
				SteamAppID:  "892970",
				Slug:        "valheim",
				Name:        "Valheim",
				InstallPath: "/home/user/.steam/steam/steamapps/common/Valheim",
				ModPath:     "/home/user/.steam/steam/steamapps/common/Valheim",
				NexusID:     "valheim",
				Sources:     map[string]string{"nexusmods": "valheim"},
				Known:       true,
				Loader:      &domain.GameLoader{Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5"},
			},
		},
		{
			// #412: a curated entry that names the game's ADAPTER. Only a
			// curated entry can - nothing on disk says what a game does
			// with mod content - so `adapter` is absent from every other
			// detect document (omitempty), which is what keeps the goldens
			// above byte-identical. Icarus carries `deploy_mode: compile`
			// beside it: the two coexist for 2.0 (design OQ1), and the
			// catalog states the adapter rather than relying on the
			// migration.
			"detected_game_adapter",
			domain.DetectedGame{
				SteamAppID:  "1149460",
				Slug:        "icarus",
				Name:        "Icarus",
				InstallPath: "/home/user/.steam/steam/steamapps/common/Icarus",
				ModPath:     "/home/user/.steam/steam/steamapps/common/Icarus/Icarus/Content/Paks/mods",
				DeployMode:  "compile",
				Adapter:     "icarus",
				Sources:     map[string]string{"icarus": "icarus"},
				Known:       true,
			},
		},
		{
			// #269's detection prefill: an app whose appworkshop manifest
			// declares installed items gets `steamworkshop: <appid>` added
			// to its sources map, and the item count that justified it.
			"detected_game_workshop",
			domain.DetectedGame{
				SteamAppID:    "1133870",
				Slug:          "space-engineers-2",
				Name:          "Space Engineers 2",
				InstallPath:   "/home/user/.steam/steam/steamapps/common/SpaceEngineers2",
				ModPath:       "/home/user/.steam/steam/steamapps/common/SpaceEngineers2/Mods",
				Sources:       map[string]string{"steamworkshop": "1133870"},
				Known:         true,
				WorkshopItems: 30,
			},
		},
		{
			// Mods is deliberately left nil to pin "[]" for a load-order
			// list that is genuinely empty for a brand-new profile.
			"profile",
			domain.Profile{
				Name:               "default",
				GameID:             "skyrim-se",
				Mods:               nil,
				Overrides:          map[string][]byte{"config/game.ini": []byte("[General]\nsLanguage=ENGLISH\n")},
				LinkMethod:         domain.LinkHardlink,
				LinkMethodExplicit: true,
				IsDefault:          true,
				Hooks: domain.GameHooks{
					Install:   domain.HookConfig{BeforeAll: "scripts/before_all.sh", BeforeEach: "scripts/before_each.sh", AfterEach: "scripts/after_each.sh", AfterAll: "scripts/after_all.sh"},
					Uninstall: domain.HookConfig{BeforeAll: "scripts/uninstall_before_all.sh"},
				},
				HooksExplicit: domain.GameHooksExplicit{
					Install:   domain.HookExplicitFlags{BeforeAll: true, BeforeEach: true, AfterEach: true, AfterAll: true},
					Uninstall: domain.HookExplicitFlags{BeforeAll: true},
				},
			},
		},
		{
			// Mods is deliberately left nil, mirroring Profile's own golden.
			// The hook pair (#296) mirrors Profile's too, including an
			// explicitly-DISABLED uninstall.after_all - set in
			// HooksExplicit with an empty value in Hooks - which is the
			// distinction the export exists to carry.
			"exported_profile",
			domain.ExportedProfile{
				Name:       "default",
				GameID:     "skyrim-se",
				Mods:       nil,
				LinkMethod: "hardlink",
				Overrides:  map[string]string{"config/game.ini": "[General]\nsLanguage=ENGLISH\n"},
				Hooks: domain.GameHooks{
					Install:   domain.HookConfig{AfterAll: "scripts/after_all.sh"},
					Uninstall: domain.HookConfig{AfterAll: ""},
				},
				HooksExplicit: domain.GameHooksExplicit{
					Install:   domain.HookExplicitFlags{AfterAll: true},
					Uninstall: domain.HookExplicitFlags{AfterAll: true},
				},
			},
		},
		{
			"hook_config",
			domain.HookConfig{
				BeforeAll:  "scripts/before_all.sh",
				BeforeEach: "scripts/before_each.sh",
				AfterEach:  "scripts/after_each.sh",
				AfterAll:   "scripts/after_all.sh",
			},
		},
		{
			"game_hooks",
			domain.GameHooks{
				Install:   domain.HookConfig{BeforeAll: "scripts/install_before_all.sh", AfterAll: "scripts/install_after_all.sh"},
				Uninstall: domain.HookConfig{BeforeAll: "scripts/uninstall_before_all.sh", AfterAll: "scripts/uninstall_after_all.sh"},
			},
		},
		{
			"hook_explicit_flags",
			domain.HookExplicitFlags{BeforeAll: true, BeforeEach: true, AfterEach: true, AfterAll: true},
		},
		{
			"game_hooks_explicit",
			domain.GameHooksExplicit{
				Install:   domain.HookExplicitFlags{BeforeAll: true, BeforeEach: true, AfterEach: true, AfterAll: true},
				Uninstall: domain.HookExplicitFlags{BeforeAll: true},
			},
		},
	}

	seen := make(map[string]bool, len(tests))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Falsef(t, seen[tt.name], "duplicate golden name %q", tt.name)
			seen[tt.name] = true

			b, err := json.Marshal(tt.value, json.Deterministic(true), jsontext.WithIndent("  "))
			require.NoError(t, err)

			path := filepath.Join("testdata", "json", tt.name+".golden")
			if *updateJSONGoldens {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, append(b, '\n'), 0o644))
				return
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "golden %s missing - record it with -update-json-goldens BEFORE relying on this test", path)
			require.Equal(t, string(want), string(b)+"\n", "%s JSON shape drifted from the recorded golden", tt.name)
		})
	}
}
