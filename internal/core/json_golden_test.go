package core_test

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/stretchr/testify/require"
)

// updateJSONGoldens re-records the JSON contract goldens for every core
// result/plan type. Run ONCE:
//
//	go test ./internal/core/ -run TestJSONGoldens -update-json-goldens
//
// After that the files are frozen: they pin the exact wire shape (snake_case
// keys, enums as text names, nil slices as "[]") that Task 19's json tags
// promise, so any future field/tag change shows up as a diff here instead of
// silently reaching a future JSON frontend. Named -update-json-goldens
// rather than -update: cmd/lmm already registers package-level "-update"
// (verify_golden_test.go) and "-update-modshow" (mod_show_golden_test.go),
// and this package already registers "-update-events" (events_golden_test.go)
// - Go's flag package panics on a duplicate registration within the same
// test binary, so this name follows that established disambiguation
// convention.
var updateJSONGoldens = flag.Bool("update-json-goldens", false, "rewrite internal/core/testdata/json/*.golden")

// fixedTime is the sample timestamp every golden with a time.Time field
// uses, so a golden diff is never just "the clock moved."
var fixedTime = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)

// jsonGoldenMod is the domain.Mod shared across every core golden that
// merely needs a plausible mod payload nested inside it - domain's own
// json_golden_test.go already pins Mod's full shape (including its own
// nil-slice field), so goldens here only need a representative value.
var jsonGoldenMod = domain.Mod{
	ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.3",
	GameID: "skyrim-se", UpdatedAt: fixedTime,
}

func boolPtr(b bool) *bool { return &b }

func TestJSONGoldens(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{
			"enable_result",
			core.EnableResult{
				Changed:  true,
				Notes:    []string{"forced reinstall before enabling"},
				Warnings: []string{"could not sync merged pak"},
			},
		},
		{
			"disable_result",
			core.DisableResult{
				Changed:  true,
				Notes:    []string{"could not undeploy cleanly, forced"},
				Warnings: []string{"could not sync merged pak"},
			},
		},
		{
			"uninstall_result",
			core.UninstallResult{
				Warnings: []string{"could not remove empty profile directory"},
				Notes:    []string{"removed 4 files"},
			},
		},
		{
			"deploy_result",
			core.DeployResult{
				Deployed:       5,
				Skipped:        []string{"OptionalAddon: no files selected"},
				Warnings:       []string{"merge sync produced 1 raw fallback"},
				Notes:          []string{"redeployed after cache miss"},
				MergedArtifact: "merged.pak",
				MergedMods:     3,
				RawFallbacks:   1,
			},
		},
		{
			"purge_result",
			core.PurgeResult{
				Purged:   2,
				Skipped:  []string{"LockedMod: locked"},
				Warnings: []string{"could not remove cache directory"},
				Notes:    []string{"purged orphaned cache entry"},
			},
		},
		{
			// ToDisable is deliberately left nil (no `omitempty` on the tag)
			// to pin that a nil slice marshals as "[]", not "null".
			"switch_plan",
			core.SwitchPlan{
				GameID: "skyrim-se", From: "default", To: "hardcore",
				ToEnable: []domain.InstalledMod{
					{
						Mod:          domain.Mod{ID: "7", SourceID: "nexusmods", Name: "Realistic Needs", GameID: "skyrim-se", UpdatedAt: fixedTime},
						ProfileName:  "hardcore",
						UpdatePolicy: domain.UpdateNotify,
						InstalledAt:  fixedTime,
						LinkMethod:   domain.LinkSymlink,
					},
				},
				ToDisable: nil,
				ToInstall: []domain.ModReference{{SourceID: "nexusmods", ModID: "8", Version: "1.0.0"}},
				PriorVersions: map[string]domain.InstalledMod{
					"nexusmods:8": {
						Mod:          domain.Mod{ID: "8", SourceID: "nexusmods", Name: "Old Mod", GameID: "skyrim-se", Version: "0.9.0", UpdatedAt: fixedTime},
						ProfileName:  "default",
						InstalledAt:  fixedTime,
						UpdatePolicy: domain.UpdateNotify,
					},
				},
				NoChanges:     false,
				AlreadyActive: false,
			},
		},
		{
			"switch_result",
			core.SwitchResult{
				Disabled:  1,
				Enabled:   2,
				Installed: 1,
				Notes:     []string{"enabled Realistic Needs"},
				Warnings:  []string{"failed to set enabled for OldMod"},
			},
		},
		{
			// Dependencies is deliberately left nil to pin that a nil slice
			// marshals as "[]", not "null" - Files (also non-omitempty)
			// carries one entry instead.
			"install_plan",
			core.InstallPlan{
				SourceID: "nexusmods", GameID: "skyrim-se", Profile: "default",
				Mod: jsonGoldenMod,
				Files: []domain.DownloadableFile{
					{ID: "file-1", Name: "Main File", FileName: "sample-mod-1.2.3.zip", Version: "1.2.3", Size: 104857600, IsPrimary: true},
				},
				Dependencies:        nil,
				MissingDependencies: []domain.ModReference{{SourceID: "nexusmods", ModID: "99"}},
				CycleDetected:       true,
				DependencyWarnings:  []string{"nexusmods:99: fetch failed"},
				Conflicts: []core.Conflict{
					{RelativePath: "Data/textures/armor/mesh.dds", CurrentSourceID: "nexusmods", CurrentModID: "7"},
				},
				Replaces: &domain.InstalledMod{
					Mod:         domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.2", GameID: "skyrim-se", UpdatedAt: fixedTime},
					ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateNotify,
				},
				TotalDownloadBytes: 104857600,
				ShowArchived:       true,
			},
		},
		{
			// Installed is deliberately left nil to pin that a nil slice
			// marshals as "[]", not "null".
			"install_result",
			core.InstallResult{
				Installed:           nil,
				Skipped:             []string{"OptionalAddon: already installed"},
				Failed:              []string{"BrokenMod"},
				FilesDeployed:       7,
				MergedPakSyncFailed: true,
				Warnings:            []string{"merged pak sync failed"},
				Notes:               []string{"installed dependency Realistic Needs"},
			},
		},
		{
			"verify_options",
			core.VerifyOptions{Tier: core.VerifyFull, Fix: true, ModFilter: "Sample Mod"},
		},
		{
			"verify_finding",
			core.VerifyFinding{
				ModID: "42", ModName: "Sample Mod", FileID: "file-1", Status: "version_mismatch",
				Note: "recorded version does not match effective", Recorded: "1.2.2", Effective: "1.2.3", Version: "1.2.3",
			},
		},
		{
			// Findings is deliberately left nil to pin that a nil slice
			// marshals as "[]", not "null" - a clean verify run reports it.
			"verify_result",
			core.VerifyResult{Findings: nil, Issues: 2, Warnings: 1, Checked: 10, HasFiles: true},
		},
		{
			"converged_file",
			core.ConvergedFile{
				Path: "Data/textures/old.dds", Reason: "no longer provided by nexusmods/42",
				SourceID: "nexusmods", ModID: "42",
			},
		},
		{
			"converge_result",
			core.ConvergeResult{
				Removed: []core.ConvergedFile{
					{Path: "Data/textures/old.dds", Reason: "no longer provided by nexusmods/42", SourceID: "nexusmods", ModID: "42"},
				},
			},
		},
		{
			"download_mod_result",
			core.DownloadModResult{FilesExtracted: 12, Checksum: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85"},
		},
		{
			// Err is deliberately non-nil (json:"-") to pin that it never
			// reaches the wire even when populated.
			"source_warning",
			core.SourceWarning{SourceID: "curseforge", Message: "search request timed out", Err: errors.New("search request timed out")},
		},
		{
			"aggregate_search_result",
			core.AggregateSearchResult{
				Mods:       []domain.Mod{jsonGoldenMod},
				TotalCount: 25,
				Warnings:   []core.SourceWarning{{SourceID: "curseforge", Message: "search request timed out"}},
				Exhausted:  true, AttemptedCount: 2,
			},
		},
		{
			"deployed_file",
			core.DeployedFile{SourceID: "nexusmods", ModID: "42", FileID: "file-1", Checksum: "abc123"},
		},
		{
			"mod_detail",
			core.ModDetail{
				Mod: &jsonGoldenMod,
				Installed: &core.InstalledDetail{
					Version: "1.2.3", Profile: "default", UpdatePolicy: domain.UpdateAuto,
					Locked: true, LockedVersion: "1.2.3", ConvertPaks: boolPtr(true),
				},
			},
		},
		{
			"installed_detail",
			core.InstalledDetail{
				Version: "1.2.3", Profile: "default", UpdatePolicy: domain.UpdateAuto,
				Locked: true, LockedVersion: "1.2.3", ConvertPaks: boolPtr(true),
			},
		},
		{
			"conflict",
			core.Conflict{RelativePath: "Data/textures/armor/mesh.dds", CurrentSourceID: "nexusmods", CurrentModID: "7"},
		},
		{
			"conflict_mod_ref",
			core.ConflictModRef{Key: "nexusmods:42", Name: "Sample Mod"},
		},
		{
			"profile_conflict",
			core.ProfileConflict{
				Path:            "Data/textures/armor/mesh.dds",
				Owner:           core.ConflictModRef{Key: "nexusmods:42", Name: "Sample Mod"},
				AlsoIn:          []core.ConflictModRef{{Key: "nexusmods:7", Name: "Other Mod"}},
				LoadOrderWinner: core.ConflictModRef{Key: "nexusmods:7", Name: "Other Mod"},
				Stale:           true,
			},
		},
		{
			"import_result",
			core.ImportResult{
				Mod: &jsonGoldenMod, FilesExtracted: 4, LinkedSource: "nexusmods",
				AutoDetected: true, RetainedFileID: "sample-mod.exmodz",
			},
		},
		{
			"scan_result",
			core.ScanResult{
				FilePath: "/home/user/Downloads/sample-mod-1.2.3.zip", FileName: "sample-mod-1.2.3.zip",
				Mod: &jsonGoldenMod, MatchedSource: "nexusmods", AlreadyTracked: true,
				Error:        errors.New("scan failed: permission denied"),
				ErrorMessage: "scan failed: permission denied",
				ResolvedFile: &domain.DownloadableFile{
					ID: "file-1", Name: "Main File", FileName: "sample-mod-1.2.3.zip", Version: "1.2.3",
					Size: 104857600, IsPrimary: true,
				},
			},
		},
		{
			"update_skips",
			core.UpdateSkips{Pinned: 3, Local: 2},
		},
		{
			"hook_result",
			core.HookResult{Stdout: "hook completed\n", Stderr: "warning: deprecated option used\n", ExitCode: 1},
		},
		{
			// Missing is deliberately left nil to pin that a nil slice
			// marshals as "[]", not "null" - Installed/NeedsRedownload
			// (also non-omitempty) each carry an entry instead.
			"import_plan",
			core.ImportPlan{
				Profile: &domain.Profile{
					Name: "default", GameID: "skyrim-se",
					Mods:       []domain.ModReference{{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"}},
					LinkMethod: domain.LinkSymlink,
				},
				Installed:       []domain.ModReference{{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"}},
				NeedsRedownload: []domain.ModReference{{SourceID: "nexusmods", ModID: "7", Version: "1.0.0"}},
				Missing:         nil,
				Exists:          true,
			},
		},
		{
			"profile_import_result",
			core.ProfileImportResult{
				ProfileName: "default", Installed: 3, Failed: 1, Skipped: 1,
				Warnings: []string{"could not install nexusmods:99"},
				Notes:    []string{"created profile default"},
			},
		},
		{
			// Applied is deliberately left nil to pin that a nil slice
			// marshals as "[]", not "null".
			"update_apply_result",
			core.UpdateApplyResult{
				Applied:  nil,
				Warnings: []string{"could not sync merged pak"},
				Notes:    []string{"applied update for Sample Mod"},
			},
		},
		{
			"rollback_result",
			core.RollbackResult{
				ModName: "Sample Mod", FromVersion: "1.2.3", ToVersion: "1.2.2",
				Warnings: []string{"could not sync merged pak"},
				Notes:    []string{"rolled back Sample Mod"},
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
