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
			// Files is deliberately non-empty and Hooks nil, pinning both
			// that a nil slice marshals as "[]" (neither tag carries
			// `omitempty`) and that the plan's whole InstalledMod - not a
			// bare reference - is on the wire.
			"uninstall_plan",
			core.UninstallPlan{
				Mod: domain.InstalledMod{
					Mod:          jsonGoldenMod,
					ProfileName:  "default",
					UpdatePolicy: domain.UpdateNotify,
					InstalledAt:  fixedTime,
					LinkMethod:   domain.LinkSymlink,
					Enabled:      true,
				},
				Files:     []string{"Data/Sample.esp"},
				KeepCache: true,
				Hooks:     nil,
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
			// Remove is deliberately left nil (no `omitempty` on the tag) to
			// pin that a nil slice marshals as "[]", not "null"; Skipped is
			// left empty to pin that its `omitempty` drops the key.
			"deploy_plan_mod",
			core.DeployPlanMod{
				Ref:    domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"},
				Name:   "Sample Mod",
				Class:  core.DeployModMerged,
				Link:   []string{"Data/Sample.esp"},
				Remove: nil,
			},
		},
		{
			"merge_plan",
			core.MergePlan{
				Artifact:     "zzz_LMM_Merged_P.pak",
				Sources:      []string{"Bear Mount"},
				RawFallbacks: []string{"Opted Out Pak"},
			},
		},
		{
			// A plan carrying one deployable mod and one skipped one, a
			// --purge set, a hook readout and a merge plan - the job here is
			// to pin every key's wire shape, not to be a plausible plan
			// (same convention as update_plan below). The unexported
			// snapshot field must not appear at all.
			"deploy_plan",
			core.DeployPlan{
				Profile: "default",
				Mods: []core.DeployPlanMod{
					{
						Ref:    domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"},
						Name:   "Sample Mod",
						Class:  core.DeployModIndividual,
						Link:   []string{"Data/Sample.esp"},
						Remove: []string{"Data/Stale_P.pak"},
					},
					{
						Ref:        domain.ModReference{SourceID: "curseforge", ModID: "7"},
						Name:       "Missing Mod",
						Redownload: true,
					},
				},
				Purge:  []string{"Data/Sample.esp"},
				Hooks:  []string{"install.before_all", "install.after_all"},
				Merged: &core.MergePlan{Artifact: "zzz_LMM_Merged_P.pak", Sources: []string{"Bear Mount"}, RawFallbacks: []string{}},
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
			"purge_plan",
			core.PurgePlan{
				Profile: "default",
				Mods: []domain.InstalledMod{
					{
						Mod:          jsonGoldenMod,
						ProfileName:  "default",
						UpdatePolicy: domain.UpdateNotify,
						InstalledAt:  fixedTime,
						LinkMethod:   domain.LinkSymlink,
						Enabled:      true,
					},
				},
				Uninstall: true,
				Hooks:     []string{"uninstall.before_all", "uninstall.after_all"},
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
				DependencyWarnings:  []core.DependencyWarning{{SourceID: "nexusmods", ModID: "99", Message: "fetch failed"}},
				Conflicts: []core.Conflict{
					{RelativePath: "Data/textures/armor/mesh.dds", CurrentSourceID: "nexusmods", CurrentModID: "7"},
				},
				Replaces: &domain.InstalledMod{
					Mod:         domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.2", GameID: "skyrim-se", UpdatedAt: fixedTime},
					ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateNotify,
				},
				TotalDownloadBytes: 104857600,
				ShowArchived:       true,
				// A real plan is EITHER single-mod (Mod/Files/Dependencies)
				// or a PlanInstallMany batch (Batch) - never both. This row
				// populates both anyway, the same way it already populates
				// MissingDependencies, CycleDetected, Conflicts and Replaces
				// together: the golden's job is to pin every key's wire
				// shape, not to be a plausible plan.
				Batch: []*core.InstallPlanEntry{
					{Mod: &jsonGoldenMod, Version: "1.2.3", Reinstall: true},
				},
			},
		},
		{
			// Every optional key is populated, FetchError included - on a
			// real entry it never co-occurs with File/Version, but the
			// golden's job is to pin each key's wire shape, not to be a
			// plausible entry (the install_plan row above does the same).
			"install_plan_entry",
			core.InstallPlanEntry{
				Mod:       &jsonGoldenMod,
				File:      &domain.DownloadableFile{ID: "file-1", Name: "Main File", FileName: "sample-mod-1.2.3.zip", Version: "1.2.3", Size: 104857600, IsPrimary: true},
				Version:   "1.2.3",
				Reinstall: true,
				Locked:    &domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.2", Locked: true},
				Conflicts: []core.Conflict{
					{RelativePath: "Data/textures/armor/mesh.dds", CurrentSourceID: "nexusmods", CurrentModID: "7"},
				},
				FetchError: "failed to get mod files: rate limited",
			},
		},
		{
			// v2 Phase 2 Unit J (#290). ToDisable is deliberately left nil
			// to pin that a nil slice marshals as "[]", not "null".
			"profile_apply_plan",
			core.ProfileApplyPlan{
				GameID:    "skyrim-se",
				Profile:   "hardcore",
				ToDisable: nil,
				ToEnable: []domain.InstalledMod{
					{
						Mod:          domain.Mod{ID: "7", SourceID: "nexusmods", Name: "Realistic Needs", GameID: "skyrim-se", UpdatedAt: fixedTime},
						ProfileName:  "hardcore",
						UpdatePolicy: domain.UpdateNotify,
						InstalledAt:  fixedTime,
						LinkMethod:   domain.LinkSymlink,
					},
				},
				ToInstall: []core.ProfileApplyInstall{
					{
						Ref:     domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"},
						Mod:     &jsonGoldenMod,
						Files:   []*domain.DownloadableFile{{ID: "file-1", Name: "Main File", FileName: "sample-mod-1.2.3.zip", Version: "1.2.3", Size: 104857600, IsPrimary: true}},
						Version: "1.2.3",
						Cached:  true,
					},
				},
				NoChanges: false,
			},
		},
		{
			// The failure shape of a ToInstall entry: no Mod, no Files, the
			// resolution error as text (#290).
			"profile_apply_install",
			core.ProfileApplyInstall{
				Ref:   domain.ModReference{SourceID: "nexusmods", ModID: "8", Version: "0.9.0"},
				Files: nil,
				Replaces: &domain.InstalledMod{
					Mod:          domain.Mod{ID: "8", SourceID: "nexusmods", Name: "Old Mod", GameID: "skyrim-se", Version: "0.9.0", UpdatedAt: fixedTime},
					ProfileName:  "hardcore",
					InstalledAt:  fixedTime,
					UpdatePolicy: domain.UpdateNotify,
				},
				Error: "failed to fetch mod: rate limited",
			},
		},
		{
			"profile_apply_result",
			core.ProfileApplyResult{
				Disabled:  1,
				Enabled:   2,
				Installed: 1,
				Replaced:  1,
				Failed:    []string{"nexusmods:8: failed to fetch mod: rate limited"},
				Notes:     []string{"Warning: could not update profile: mod is locked"},
				Warnings:  []string{"could not sync merged pak: base pak missing"},
			},
		},
		{
			// v2 Phase 2 Unit J (#290). ToRemove is deliberately left nil to
			// pin that a nil slice marshals as "[]", not "null".
			"profile_sync_plan",
			core.ProfileSyncPlan{
				GameID:   "skyrim-se",
				Profile:  "hardcore",
				ToAdd:    []domain.ModReference{{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"}},
				ToRemove: nil,
				ToUpdate: []domain.ModReference{{SourceID: "nexusmods", ModID: "7", Version: "1.0.0", FileIDs: []string{"file-1"}}},
				Missing:  true,
				Names:    map[string]string{"nexusmods:42": "Sample Mod", "nexusmods:7": "Realistic Needs"},
			},
		},
		{
			// v2 Phase 2 Unit K (#291). Tracked is deliberately left nil to
			// pin that a nil slice marshals as "[]", not "null".
			"local_scan",
			core.LocalScan{
				Tracked: nil,
				Untracked: []core.ScanResult{{
					FilePath: "/games/skyrim/Data/sample-mod-1.2.3.zip", FileName: "sample-mod-1.2.3.zip",
					Mod: &jsonGoldenMod, MatchedSource: "local",
				}},
				Backfill: []domain.InstalledMod{{
					Mod:          domain.Mod{ID: "7", SourceID: "nexusmods", Name: "Realistic Needs", GameID: "skyrim-se", UpdatedAt: fixedTime},
					ProfileName:  "default",
					UpdatePolicy: domain.UpdateNotify,
					InstalledAt:  fixedTime,
				}},
				ExtractModeWarning: true,
			},
		},
		{
			// The matched shape: source hit, resolved file, no errors.
			"adopt_match",
			core.AdoptMatch{
				Untracked: core.ScanResult{
					FilePath: "/games/skyrim/Data/sample-mod-1.2.3.zip", FileName: "sample-mod-1.2.3.zip",
					Mod: &jsonGoldenMod, MatchedSource: "nexusmods",
					ResolvedFile: &domain.DownloadableFile{ID: "file-1", Name: "Main File", FileName: "sample-mod-1.2.3.zip", Version: "1.2.3", IsPrimary: true},
				},
				Mod:  &jsonGoldenMod,
				File: &domain.DownloadableFile{ID: "file-1", Name: "Main File", FileName: "sample-mod-1.2.3.zip", Version: "1.2.3", IsPrimary: true},
			},
		},
		{
			// Every optional key populated at once (Error and FileError are
			// mutually exclusive on a real match) - the point is to pin each
			// key's wire shape, not to be a plausible plan.
			"adopt_plan",
			core.AdoptPlan{
				GameID:  "skyrim-se",
				Profile: "default",
				Scan: &core.LocalScan{
					Untracked: []core.ScanResult{{
						FilePath: "/games/skyrim/Data/sample-mod-1.2.3.zip", FileName: "sample-mod-1.2.3.zip",
						Mod: &jsonGoldenMod, MatchedSource: "local",
					}},
					ExtractModeWarning: false,
				},
				Matches: []core.AdoptMatch{{
					Untracked: core.ScanResult{
						FilePath: "/games/skyrim/Data/sample-mod-1.2.3.zip", FileName: "sample-mod-1.2.3.zip",
						Mod: &jsonGoldenMod, MatchedSource: "local",
					},
					Error:     "search failed: rate limited",
					FileError: "listing source files: rate limited",
				}},
				Duplicates: []string{"already-installed-1.0.zip"},
				SkipMatch:  false,
			},
		},
		{
			"adopt_backfill_result",
			core.AdoptBackfillResult{Backfilled: 2},
		},
		{
			// Every optional key populated at once, to pin each one's wire
			// shape - a real import rarely produces all four diagnostics.
			"import_archive_result",
			core.ImportArchiveResult{
				Mod:             &jsonGoldenMod,
				LinkedSource:    "nexusmods",
				AutoDetected:    true,
				Renamed:         true,
				FileID:          "file-1",
				FileIDs:         []string{"file-1"},
				Deployed:        7,
				MergedPakSynced: true,
				HookWarnings:    []string{"install.after_each hook failed: exit status 1"},
				Warnings:        []string{"could not mark cache entry complete: permission denied"},
				Notes:           []string{"Warning: could not update profile: mod is locked"},
			},
		},
		{
			"adopt_result",
			core.AdoptResult{
				Adopted: 2, Skipped: 1, Failed: 1,
				Warnings: []string{"merge sync produced 1 raw fallback"},
			},
		},
		{
			"profile_sync_result",
			core.ProfileSyncResult{
				Added: 1, Removed: 1, Updated: 1,
				Warnings: []string{"could not sync merged pak: base pak missing"},
			},
		},
		{
			// Warnings is deliberately left nil to pin that a nil slice
			// marshals as "[]" - ApplyGameDetect never populates it today.
			"game_detect_result",
			core.GameDetectResult{
				Saved:    []string{"skyrim-se", "icarus"},
				Profiles: []string{"skyrim-se/default", "icarus/default"},
				Warnings: nil,
			},
		},
		{
			// Installed is deliberately left nil to pin that a nil slice
			// marshals as "[]", not "null".
			"install_result",
			core.InstallResult{
				Installed: nil,
				Skipped: []core.InstalledRef{
					{SourceID: "nexusmods", ModID: "43", Name: "OptionalAddon", Version: "1.0.0", Reason: "already installed"},
				},
				Failed: []core.InstalledRef{
					{SourceID: "nexusmods", ModID: "44", Name: "BrokenMod", Version: "1.0.0", Reason: "already installed"},
				},
				FilesDeployed:       7,
				MergedPakSyncFailed: true,
				Warnings:            []string{"merged pak sync failed"},
				Notes:               []string{"installed dependency Realistic Needs"},
			},
		},
		{
			"installed_ref",
			core.InstalledRef{SourceID: "nexusmods", ModID: "43", Name: "OptionalAddon", Version: "1.0.0", Reason: "already installed"},
		},
		{
			"dependency_warning",
			core.DependencyWarning{SourceID: "nexusmods", ModID: "99", Message: "fetch failed"},
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
			core.SourceWarning{SourceID: "curseforge", ErrorMessage: "search request timed out", Err: errors.New("search request timed out")},
		},
		{
			"aggregate_search_result",
			core.AggregateSearchResult{
				Mods:       []domain.Mod{jsonGoldenMod},
				TotalCount: 25,
				Warnings:   []core.SourceWarning{{SourceID: "curseforge", ErrorMessage: "search request timed out"}},
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
			"download_result",
			core.DownloadResult{Path: "/cache/g/src-1/1.0/file.zip", Size: 1234, Checksum: "md5", SHA256: "abc"},
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
			// Every optional key is populated (Update, RecompileNeeded,
			// Changelog, Refusal all together) - on a real plan RecompileNeeded
			// and a version-bump Update never co-occur, but the golden's job is
			// to pin each key's wire shape, not to be a plausible plan (same
			// convention as install_plan/install_plan_entry above).
			"update_plan",
			core.UpdatePlan{
				Mod: domain.InstalledMod{
					Mod:         domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.2", GameID: "skyrim-se", UpdatedAt: fixedTime},
					ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateNotify,
				},
				Locked:        true,
				LockedVersion: "1.2.2",
				Pinned:        false,
				Update: &domain.Update{
					InstalledMod: domain.InstalledMod{
						Mod:         domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.2", GameID: "skyrim-se", UpdatedAt: fixedTime},
						ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateNotify,
					},
					NewVersion: "1.2.3",
					Changelog:  "Fixed a crash on load.",
				},
				RecompileNeeded: true,
				Changelog:       "Fixed a crash on load.",
				Refusal:         "mod is locked: Sample Mod is locked at v1.2.2 in profile default - move the lock with 'lmm mod lock -s nexusmods -p default 42 <version>' or unlock with 'lmm mod unlock -s nexusmods -p default 42'",
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
		{
			// Every optional key is populated (Locked/LockedVersion/Refusal
			// and CacheMissing together) - on a real plan a locked rollback and
			// a missing cache entry can co-occur, but the golden's job is to
			// pin each key's wire shape, not to be a plausible plan (same
			// convention as update_plan above).
			"rollback_plan",
			core.RollbackPlan{
				Mod: domain.InstalledMod{
					Mod:         domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.3", GameID: "skyrim-se", UpdatedAt: fixedTime},
					ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateNotify,
				},
				FromVersion:   "1.2.3",
				ToVersion:     "1.2.2",
				Locked:        true,
				LockedVersion: "1.2.3",
				Refusal:       "mod is locked: Sample Mod is locked at v1.2.3 in profile default - move the lock with 'lmm mod lock -s nexusmods -p default 42 <version>' or unlock with 'lmm mod unlock -s nexusmods -p default 42'",
				CacheMissing:  true,
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
