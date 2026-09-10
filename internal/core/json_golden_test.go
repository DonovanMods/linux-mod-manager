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

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
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

// jsonGoldenExternal is the EXTERNAL installed mod (#269) the _external
// goldens below share: a Steam Workshop item lmm tracks but never deploys.
// Deployed is true (its files ARE where the game reads them), there are no
// file ids and no manual_download, and Version is the ACF content id.
var jsonGoldenExternal = domain.InstalledMod{
	Mod: domain.Mod{
		ID: "3617086610", SourceID: "steamworkshop", Name: "Workshop Item",
		Version: "7987119735124793734", Author: "76561198000000000",
		GameID: "space-engineers-2", UpdatedAt: fixedTime,
		SourceURL: "https://steamcommunity.com/sharedfiles/filedetails/?id=3617086610",
	},
	ProfileName:  "default",
	UpdatePolicy: domain.UpdateNotify,
	InstalledAt:  fixedTime,
	Enabled:      true,
	Deployed:     true,
	LinkMethod:   domain.LinkSymlink,
	External:     true,
	ExternalPath: "/home/user/.steam/steam/steamapps/workshop/content/1133870/3617086610",
}

// jsonGoldenGame is the domain.Game shared across the query goldens that
// carry a whole game on the wire - domain's own golden pins Game's full
// shape, so these only need a representative value.
var jsonGoldenGame = domain.Game{
	ID: "skyrim-se", Name: "Skyrim Special Edition",
	InstallPath: "/games/skyrim-se", ModPath: "/games/skyrim-se/Data",
	LinkMethod: domain.LinkSymlink,
	SourceIDs:  map[string]string{"nexusmods": "skyrimspecialedition"},
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
			// #269: the tracking-only uninstall. Files is EMPTY - lmm
			// deployed nothing to remove - and external says so, which is
			// what a confirmation reads to reword itself.
			"uninstall_plan_external",
			core.UninstallPlan{
				Mod:       jsonGoldenExternal,
				External:  true,
				Files:     []string{},
				KeepCache: false,
			},
		},
		{
			"deploy_result",
			core.DeployResult{
				Deployed:       5,
				Skipped:        []core.InstalledRef{{SourceID: "nexusmods", ModID: "7", Name: "OptionalAddon", Version: "0.9", Reason: "no files selected"}},
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
			// #380: the ref is LOCKED here. PlanDeploy built its refs from
			// the installed row, which carries no lock, so this golden's
			// hand-built literal (locked: false) matched a plan that could
			// never say otherwise - and every locked mod's deploy preview
			// disagreed with GET /api/v1/mods about the same mod.
			// TestPlanDeploy_StampsTheProfileRefsLock is the behaviour half.
			"deploy_plan_mod",
			core.DeployPlanMod{
				Ref:    domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.3", Locked: true},
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
			// #269: an external mod in a deploy plan - class "external",
			// nothing linked, nothing removed. The plan is a complete
			// account of the profile; the deploy itself does nothing here.
			"deploy_plan_external",
			core.DeployPlan{
				Profile: "default",
				Mods: []core.DeployPlanMod{{
					Ref:    domain.ModReference{SourceID: "steamworkshop", ModID: "3617086610", Version: "7987119735124793734"},
					Name:   "Workshop Item",
					Class:  core.DeployModExternal,
					Link:   []string{},
					Remove: nil,
				}},
				Purge:     []string{},
				Hooks:     []string{},
				NoChanges: false,
			},
		},
		{
			"purge_result",
			core.PurgeResult{
				Purged:   2,
				Skipped:  []core.InstalledRef{{SourceID: "nexusmods", ModID: "9", Name: "LockedMod", Version: "1.0", Reason: "locked"}},
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
				// Non-nil here and nil in uninstall_plan above, so the two
				// goldens between them pin both halves of Ruling 8's
				// optional field: the nested object and the JSON null a
				// "nothing would change" plan carries.
				MergedArtifact: &core.MergedArtifactEffect{Action: core.MergedArtifactRemove, Path: "zzz_LMM_Merged_P.pak"},
			},
		},
		{
			// #269: a purge that will not touch the profile's one Steam
			// Workshop item. Mods is EMPTY (nothing purgeable) and external
			// names what the preview is leaving alone - the count being
			// short is otherwise unexplained.
			"purge_plan_external",
			core.PurgePlan{
				Profile:  "default",
				Mods:     []domain.InstalledMod{},
				External: []string{"Workshop Item"},
				Hooks:    []string{},
			},
		},
		{
			"merged_artifact_effect",
			core.MergedArtifactEffect{Action: core.MergedArtifactResync, Path: "zzz_LMM_Merged_P.pak"},
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
			// Task 13 review round 1, Minor 7: the install loop's UpsertMod
			// refusal is a Warning (no "Warning: " prefix baked in - the
			// caller renders one), ahead of the end-of-switch merged-pak
			// diagnostics - mirroring profile_apply_result's identical #294
			// shape below; Notes keeps only the disable/enable loops'
			// --verbose-only entries, which still carry their historical
			// prefix.
			"switch_result",
			core.SwitchResult{
				Disabled:  1,
				Enabled:   2,
				Installed: 1,
				Notes:     []string{"Warning: failed to update Realistic Needs: some error"},
				Warnings: []string{
					"could not update profile: mod is locked",
					"could not sync merged pak: base pak missing",
				},
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
				// FilePool (#331): the candidate list Files' pick came FROM -
				// two versions here, so a picker has something to group.
				FilePool: []domain.DownloadableFile{
					{ID: "file-1", Name: "Main File", FileName: "sample-mod-1.2.3.zip", Version: "1.2.3", Size: 104857600, IsPrimary: true},
					{ID: "file-0", Name: "Main File", FileName: "sample-mod-1.2.2.zip", Version: "1.2.2", Size: 104857600},
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
			// v2 Phase 3 Ruling 15: the document `lmm profile
			// create/delete/reorder` emit. The nested domain.Profile is
			// pinned in full by domain's own golden, so this one only has
			// to pin the single-key wrapper around it.
			"profile_result",
			core.ProfileResult{
				Profile: domain.Profile{
					Name:   "default",
					GameID: "skyrim-se",
					Mods: []domain.ModReference{
						{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"},
					},
					LinkMethod: domain.LinkSymlink,
					IsDefault:  true,
				},
			},
		},
		{
			// v2 Phase 3 Ruling 15: the document `lmm game
			// set-default`/`clear-default` emit. A cleared default is the
			// empty string, which is why the field carries no omitempty -
			// "cleared" must be visible on the wire, not absent.
			"settings_result",
			core.SettingsResult{DefaultGame: "skyrim-se"},
		},
		{
			// #294 (Ruling 5): the install loop's UpsertMod refusal is a
			// Warning now (no "Warning: " prefix baked in - the caller
			// renders one), ahead of the end-of-apply merged-pak
			// diagnostics; Notes keeps only the disable/enable loops'
			// --verbose-only entries, which still carry their historical
			// prefix.
			"profile_apply_result",
			core.ProfileApplyResult{
				Disabled:  1,
				Enabled:   2,
				Installed: 1,
				Replaced:  1,
				Failed:    []core.InstalledRef{{SourceID: "nexusmods", ModID: "8", Reason: "failed to fetch mod: rate limited"}},
				Notes:     []string{"Warning: failed to undeploy Sample Mod: permission denied"},
				Warnings: []string{
					"could not update profile: mod is locked",
					"could not sync merged pak: base pak missing",
				},
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
			// Score/ScoreClass are #27's additive confidence pair, present
			// exactly because a Mod is - a refused candidate carries
			// neither.
			"adopt_match",
			core.AdoptMatch{
				Untracked: core.ScanResult{
					FilePath: "/games/skyrim/Data/sample-mod-1.2.3.zip", FileName: "sample-mod-1.2.3.zip",
					Mod: &jsonGoldenMod, MatchedSource: "nexusmods",
					ResolvedFile: &domain.DownloadableFile{ID: "file-1", Name: "Main File", FileName: "sample-mod-1.2.3.zip", Version: "1.2.3", IsPrimary: true},
				},
				Mod:        &jsonGoldenMod,
				Score:      1,
				ScoreClass: core.AdoptMatchExact,
				File:       &domain.DownloadableFile{ID: "file-1", Name: "Main File", FileName: "sample-mod-1.2.3.zip", Version: "1.2.3", IsPrimary: true},
			},
		},
		{
			// Every optional key of a plan populated at once, across TWO
			// match entries rather than one, because AdoptMatch's own
			// invariants make some of them mutually exclusive and a golden
			// is the frozen contract a frontend reads (Track C review,
			// finding 5): Score/ScoreClass are present "only alongside a
			// Mod", so the entry that carries them carries a Mod, and the
			// entry that carries Error - set only when EVERY source failed,
			// which is not a match - carries neither.
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
				Matches: []core.AdoptMatch{
					{
						// Matched, but its source's file listing failed:
						// the match stands, the adoption is marker-less.
						Untracked: core.ScanResult{
							FilePath: "/games/skyrim/Data/sample-mod-1.2.3.zip", FileName: "sample-mod-1.2.3.zip",
							Mod: &jsonGoldenMod, MatchedSource: "nexusmods",
						},
						Mod:        &jsonGoldenMod,
						Score:      0.82,
						ScoreClass: core.AdoptMatchProbable,
						FileError:  "listing source files: rate limited",
					},
					{
						// Every searchable source failed, so there is no
						// match, no score and no class - only the error.
						Untracked: core.ScanResult{
							FilePath: "/games/skyrim/Data/other-mod-2.0.zip", FileName: "other-mod-2.0.zip",
							Mod: &jsonGoldenMod, MatchedSource: "local",
						},
						Error: "search failed: rate limited",
					},
				},
				Duplicates: []string{"already-installed-1.0.zip"},
				SkipMatch:  false,
			},
		},
		{
			"adopt_backfill_result",
			core.AdoptBackfillResult{Backfilled: 2},
		},
		{
			// Every optional key populated at once (a conflicting import
			// into a compile game, with an enrichment warning), to pin each
			// one's wire shape - a real plan rarely carries all of them.
			"import_archive_plan",
			core.ImportArchivePlan{
				Archive:      "/downloads/sample-mod-1.2.3.zip",
				Mod:          jsonGoldenMod,
				LinkedSource: "nexusmods",
				AutoDetected: true,
				Files:        []string{"Data/Sample.esp"},
				Conflicts: []core.Conflict{
					{RelativePath: "Data/Sample.esp", CurrentSourceID: "nexusmods", CurrentModID: "7"},
				},
				MergedArtifact: &core.MergedArtifactEffect{Action: core.MergedArtifactResync, Path: "zzz_LMM_Merged_P.pak"},
				Hooks:          []string{"install.before_all", "install.after_all"},
				EntryPreExists: true,
				Warnings:       []string{"could not resolve source file for archive: rate limited"},
			},
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
			// Backfilled is #333's additive, omitzero member: a caller that
			// ran ApplyAdoptBackfill as part of the same user-level adopt
			// folds its count in here (`lmm serve` does; the CLI reports it
			// separately and leaves this zero, in which case the document
			// is byte-identical to what it was before the field existed).
			// #308's shared per-item failure entry, goldened on its OWN
			// (the AST coverage ratchet requires it, and rightly: it is a
			// type two documents embed, so its keys are contract twice
			// over). Every member populated - an adopt failure that matched
			// no catalogue mod carries no source_id/mod_id, which the two
			// result goldens below already show.
			"item_failure",
			core.ItemFailure{SourceID: "nexusmods", ModID: "99", Name: "Sample Mod", Reason: "failed to fetch mod: upstream timeout"},
		},
		{
			"adopt_result",
			core.AdoptResult{
				Adopted: 2, Skipped: 1, Failed: 1, Backfilled: 3,
				Warnings: []string{"merge sync produced 1 raw fallback"},
				// #308: the per-item detail behind the Failed counter, so
				// --json (which suppresses the event stream by design) says
				// WHICH entry failed and why. Named by file, the way the
				// plain "✗ <file>: <reason>" line names it.
				Failures: []core.ItemFailure{{Name: "GoneMod-1.0.zip", Reason: "copying to cache: no such file or directory"}},
			},
		},
		{
			// #294 (Ruling 5): the toUpdate loop's UpsertMod refusal is a
			// Warning now (no "Warning: " prefix baked in), ahead of the
			// end-of-sync merged-pak diagnostics - the add/remove loops'
			// failures remain event-only.
			"profile_sync_result",
			core.ProfileSyncResult{
				Added: 1, Removed: 1, Updated: 1,
				Warnings: []string{
					"could not update nexusmods:42: mod is locked",
					"could not sync merged pak: base pak missing",
				},
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
				// #312: exercised here so the golden pins the additive flag
				// rather than leaving it omitted by omitzero (review M7 -
				// the tag is omitzero, and that IS the distinction: under
				// encoding/json/v2, omitempty would keep a false bool and
				// so change every existing install document).
				ProfileWriteFailed: true,
				Warnings:           []string{"merged pak sync failed"},
				Notes:              []string{"installed dependency Realistic Needs"},
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
			// Force is set here (#336) to pin its key's wire shape; it is
			// omitempty, so the default (memo-eligible) options document is
			// byte-identical to what it was before the field existed.
			"verify_options",
			core.VerifyOptions{Tier: core.VerifyFull, Fix: true, ModFilter: "Sample Mod", Force: true},
		},
		{
			// Fixable true here on purpose (#332): version_mismatch on an
			// unlocked, source-backed mod is exactly the case --fix acts
			// on, so this golden pins the field PRESENT. verify_report's
			// own "ok" finding below pins the other half - omitzero, so a
			// non-fixable row carries no key at all.
			// Fixable and FixableReason are populated together here so both
			// keys pin their wire shape; a real finding carries a reason
			// only when Fixable is false (#334).
			"verify_finding",
			core.VerifyFinding{
				ModID: "42", ModName: "Sample Mod", FileID: "file-1", Status: "version_mismatch",
				Note: "recorded version does not match effective", Recorded: "1.2.2", Effective: "1.2.3", Version: "1.2.3",
				Fixable:       true,
				FixableReason: "the mod was imported locally, so there is no source to re-download from",
			},
		},
		{
			// Findings is deliberately left nil to pin that a nil slice
			// marshals as "[]", not "null" - a clean verify run reports it.
			// Cached (#336) is set for the same reason Force is on
			// verify_options above: this is the only golden that carries
			// the key, and a real run omits it entirely.
			"verify_result",
			core.VerifyResult{Findings: nil, Issues: 2, Warnings: 1, Checked: 10, HasFiles: true, CheckedAt: fixedTime, Cached: true},
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
			// SkippedUnauthenticated is populated (#383, F1) to pin the key
			// itself: it is omitempty, so an unpopulated sample would leave
			// the field's wire name untested.
			"aggregate_search_result",
			core.AggregateSearchResult{
				Mods:       []domain.Mod{jsonGoldenMod},
				TotalCount: 25,
				Warnings:   []core.SourceWarning{{SourceID: "curseforge", ErrorMessage: "search request timed out"}},
				Exhausted:  true, AttemptedCount: 2,
				SkippedUnauthenticated: []string{"steamworkshop"},
			},
		},
		{
			"deployed_file",
			core.DeployedFile{SourceID: "nexusmods", ModID: "42", FileID: "file-1", Checksum: "abc123"},
		},
		{
			// Notes is populated here (unlike every earlier ModDetail use in
			// this file/package) specifically to pin the "notes" key's wire
			// shape - moddetail_test.go's TestModDetail_Changelog only ever
			// asserted the Go struct field, so this golden was the only place
			// in the JSON-contract suite that ever exercised it (task-2
			// review, Important #1).
			"mod_detail",
			core.ModDetail{
				Mod: &jsonGoldenMod,
				Installed: &core.InstalledDetail{
					Version: "1.2.3", Profile: "default", UpdatePolicy: domain.UpdateAuto,
					Locked: true, LockedVersion: "1.2.3", ConvertPaks: boolPtr(true),
				},
				// #342: the plain-text sibling of the raw Mod.Description
				// above, pinned here because it is the only golden that
				// carries it - a mod with no description omits the key.
				DescriptionText: "Adds bigger backpacks.",
				Changelog:       "Fixed a crash on load.",
				Notes:           []string{"changelog unavailable: upstream timeout"},
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
			// ConvertPaks is a non-nil pointer to false, the tri-state case a
			// plain bool cannot express ("applies here, and is off"), and
			// the one that proves the outer pointer - not the embedded
			// InstalledMod.ConvertPaks bool - owns the convert_paks key.
			"mod_listing",
			core.ModListing{
				InstalledMod: domain.InstalledMod{
					Mod:          jsonGoldenMod,
					ProfileName:  "default",
					UpdatePolicy: domain.UpdateNotify,
					InstalledAt:  fixedTime,
					LinkMethod:   domain.LinkSymlink,
					Enabled:      true,
					Deployed:     true,
					ConvertPaks:  true,
				},
				Locked:        true,
				LockedVersion: "1.2.2",
				ConvertPaks:   boolPtr(false),
			},
		},
		{
			// #269: the two new InstalledMod keys riding along on a listing
			// row for free - ModListing embeds InstalledMod, so `lmm list`
			// and the web UI's library both see them with no field of their
			// own. convert_paks is absent (a non-compile game).
			"mod_listing_external",
			core.ModListing{InstalledMod: jsonGoldenExternal},
		},
		{
			// ConvertPaks left nil - pak conversion does not apply at all
			// (a non-compile game), the common shape mod_listing above does
			// NOT cover (phase-end review Minor 8 / Unit N M3): the
			// omitempty tag must drop convert_paks entirely here, not
			// emit false or null.
			"mod_listing_not_applicable",
			core.ModListing{
				InstalledMod: domain.InstalledMod{
					Mod:          jsonGoldenMod,
					ProfileName:  "default",
					UpdatePolicy: domain.UpdateNotify,
					InstalledAt:  fixedTime,
					LinkMethod:   domain.LinkSymlink,
					Enabled:      true,
					Deployed:     true,
				},
				ConvertPaks: nil,
			},
		},
		{
			// Mods is deliberately left nil (no `omitempty` on the tag) to
			// pin that an empty listing marshals as "[]", not "null".
			"mod_list",
			core.ModList{GameID: "skyrim-se", Profile: "default", Mods: nil},
		},
		{
			// Profiles deliberately populated: ModList above already pins a
			// nil list field encoding as "[]", and what this type adds is
			// the game_id stamp beside the names.
			"profile_names",
			core.ProfileNames{GameID: "skyrim-se", Profiles: []string{"default", "survival"}},
		},
		{
			// `lmm profile list --json`'s document (#309): the same
			// ProfileSummary rows profile_summary/game_status pin, wrapped
			// with the game_id stamp - two profiles so both the default
			// marker and a non-default row are visible.
			"profile_listing",
			core.ProfileListing{GameID: "skyrim-se", Profiles: []core.ProfileSummary{
				{Name: "default", ModCount: 3, IsDefault: true},
				{Name: "survival", ModCount: 0, IsDefault: false},
			}},
		},
		{
			// Profiles deliberately left nil (no `omitempty` on the tag,
			// task A review round 1, Minor 6) to pin that a game with no
			// profiles marshals its listing as "[]", not "null" - the
			// game_summary/mod_list precedent above, for the one new list
			// document that lacked it.
			"profile_listing_empty",
			core.ProfileListing{GameID: "skyrim-se", Profiles: nil},
		},
		{
			// Profiles is deliberately left nil (no `omitempty` on the tag) to
			// pin that a game with no profiles marshals as "[]", not "null".
			"game_summary",
			core.GameSummary{
				Game:       jsonGoldenGame,
				LinkMethod: domain.LinkSymlink,
				Profiles:   nil,
				ModCount:   3,
				IsDefault:  true,
			},
		},
		{
			"status_report",
			core.StatusReport{Games: []core.GameSummary{{
				Game:       jsonGoldenGame,
				LinkMethod: domain.LinkSymlink,
				Profiles:   []string{"default", "hardcore"},
				ModCount:   3,
				IsDefault:  true,
			}}},
		},
		{
			"profile_summary",
			core.ProfileSummary{Name: "default", ModCount: 3, IsDefault: true},
		},
		{
			// `lmm game show-default --json`'s document (#309): the fully
			// populated shape - a game with no default set is the
			// zero-value core.DefaultGame{}, already covered by every
			// other golden's implicit "omitzero drops the key" pattern.
			"default_game",
			core.DefaultGame{Set: true, ID: "skyrim-se", Name: "Skyrim SE"},
		},
		{
			// EffectiveLinkMethod deliberately differs from LinkMethod (the
			// profile override case, #155), and every optional key is
			// populated, so the golden pins each key's wire shape rather than
			// being a plausible status (same convention as deploy_plan above).
			"game_status",
			core.GameStatus{
				Game:                jsonGoldenGame,
				LinkMethod:          domain.LinkSymlink,
				EffectiveLinkMethod: domain.LinkHardlink,
				LinkMethodSource:    "profile",
				ResolvedCachePath:   "/home/user/.local/share/lmm",
				Profiles:            []core.ProfileSummary{{Name: "default", ModCount: 3, IsDefault: true}},
				ActiveProfile:       "default",
				InstalledModCount:   3,
				EnabledModCount:     2,
				LastDeploy:          &fixedTime,
				ConversionFailures:  1,
			},
		},
		{
			// #269: the counts that let a readout say "3 installed
			// (2 tracked from Steam)" instead of implying lmm deployed
			// three mods it never touched.
			"game_status_external",
			core.GameStatus{
				Game:                jsonGoldenGame,
				LinkMethod:          domain.LinkSymlink,
				EffectiveLinkMethod: domain.LinkSymlink,
				LinkMethodSource:    "game",
				ResolvedCachePath:   "/home/user/.local/share/lmm",
				Profiles:            []core.ProfileSummary{{Name: "default", ModCount: 3, IsDefault: true}},
				ActiveProfile:       "default",
				InstalledModCount:   3,
				EnabledModCount:     3,
				ExternalCount:       2,
			},
		},
		{
			"search_hit",
			core.SearchHit{Mod: jsonGoldenMod, Installed: true},
		},
		{
			// Warnings carries the structured SourceWarning (SourceID + the
			// error's message, never a pre-formatted line), and Mods is left
			// nil to pin that an empty result marshals as "[]", not "null".
			"search_report",
			core.SearchReport{
				GameID: "skyrim-se", Query: "armor",
				Mods:           nil,
				Warnings:       []core.SourceWarning{{SourceID: "curseforge", ErrorMessage: "network down"}},
				TotalResults:   0,
				AttemptedCount: 2,
				// #383 (F1): omitempty, so the key only appears when a
				// searchable source really was skipped for want of a key.
				SkippedUnauthenticated: []string{"steamworkshop"},
			},
		},
		{
			"game_list_entry",
			core.GameListEntry{Game: jsonGoldenGame, Default: true},
		},
		{
			"verify_report",
			core.VerifyReport{
				GameID: "skyrim-se", Profile: "default",
				Result: &core.VerifyResult{
					Findings: []core.VerifyFinding{{ModID: "42", ModName: "Sample Mod", FileID: "file-1", Status: "ok"}},
					Issues:   0, Warnings: 1, Checked: 1, HasFiles: true,
				},
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
			// Conflicts deliberately left nil (no `omitempty` on the tag):
			// a profile with nothing to report must marshal as "[]", which
			// is what `lmm conflicts --json` emits for both an empty profile
			// and a clean one.
			"conflict_report",
			core.ConflictReport{GameID: "skyrim-se", Profile: "default", Conflicts: nil},
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
			// Updates deliberately left nil (no `omitempty` on the tag) to
			// pin that a check with nothing to report marshals as "[]", not
			// "null", and ErrorMessage left empty to pin that a COMPLETE
			// check carries no "error" key at all.
			"update_check_report",
			core.UpdateCheckReport{
				GameID:  "skyrim-se",
				Profile: "default",
				Updates: nil,
				Skipped: core.UpdateSkips{Pinned: 2, Local: 1},
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
			// marshals as "[]", not "null" - Installed/AlreadyCached/
			// NeedsRedownload (also non-omitempty) each carry an entry
			// instead.
			"import_plan",
			core.ImportPlan{
				Profile: &domain.Profile{
					Name: "default", GameID: "skyrim-se",
					Mods:       []domain.ModReference{{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"}},
					LinkMethod: domain.LinkSymlink,
				},
				Installed: []domain.ModReference{{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"}},
				// #371: a mod installed under some OTHER profile - its bytes
				// are cached, so this import writes the row without
				// downloading anything.
				AlreadyCached:   []domain.ModReference{{SourceID: "nexusmods", ModID: "13", Version: "2.0.0"}},
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
				// #308: the structured half of Warnings above - same
				// information, without a consumer having to parse
				// "source:mod: reason" back apart.
				Failures: []core.ItemFailure{
					{SourceID: "nexusmods", ModID: "99", Name: "Sample Mod", Reason: "failed to fetch mod: upstream timeout"},
				},
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
				Refusal:         "Sample Mod is locked at v1.2.2 in profile default - unlock with 'lmm mod unlock -s nexusmods -p default 42' first",
			},
		},
		{
			// #269: what a workshop scan says - the app, the libraries it
			// was found in, and the items themselves. Warnings left empty
			// to pin that its omitempty drops the key on a clean scan.
			"workshop_scan",
			core.WorkshopScan{
				GameID:    "space-engineers-2",
				AppID:     "1133870",
				Libraries: []string{"/home/user/.steam/steam"},
				Items: []domain.WorkshopItem{{
					FileID:      "3617086610",
					Path:        "/home/user/.steam/steam/steamapps/workshop/content/1133870/3617086610",
					SizeOnDisk:  572330,
					Manifest:    "7987119735124793734",
					TimeUpdated: 1764767935,
				}},
				Tracked:   1,
				Untracked: 1,
			},
		},
		{
			// One entry Steam described in full. Its ACF facts and its
			// metadata are separate keys on purpose: the manifest is what
			// is on disk, the mod is what Steam publishes about it.
			"workshop_adopt_entry",
			core.WorkshopAdoptEntry{
				FileID:      "3617086610",
				Path:        "/home/user/.steam/steam/steamapps/workshop/content/1133870/3617086610",
				SizeOnDisk:  572330,
				Manifest:    "7987119735124793734",
				TimeUpdated: 1764767935,
				Mod: &domain.Mod{
					ID: "3617086610", SourceID: "steamworkshop", Name: "Sample Workshop Item",
					Version: "7987119735124793734", Author: "76561198000000000",
					GameID: "space-engineers-2", Category: "Blueprint", UpdatedAt: fixedTime,
					SourceURL: "https://steamcommunity.com/sharedfiles/filedetails/?id=3617086610",
				},
			},
		},
		{
			// A plan carrying one describable entry and one Steam refuses to
			// describe - the golden's job is to pin every key's wire shape,
			// including the unavailable/note pair, not to be a plausible
			// plan. The unexported snapshot field must not appear at all.
			"workshop_adopt_plan",
			core.WorkshopAdoptPlan{
				GameID:  "space-engineers-2",
				Profile: "default",
				Scan: &core.WorkshopScan{
					GameID: "space-engineers-2", AppID: "1133870",
					Libraries: []string{"/home/user/.steam/steam"},
					Items:     []domain.WorkshopItem{},
					Untracked: 2,
					Warnings:  []string{"could not fetch Steam metadata (items are still adoptable): timeout"},
				},
				Entries: []core.WorkshopAdoptEntry{
					{
						FileID: "3617086610", Manifest: "7987119735124793734", TimeUpdated: 1764767935,
						Path: "/home/user/.steam/steam/steamapps/workshop/content/1133870/3617086610",
					},
					{
						FileID:      "2900001111",
						Path:        "/home/user/.steam/steam/steamapps/workshop/content/1133870/2900001111",
						Unavailable: true,
						Note:        "Steam does not describe this item - it may be delisted, deleted or private",
					},
				},
			},
		},
		{
			"workshop_adopt_result",
			core.WorkshopAdoptResult{
				Adopted:  28,
				Skipped:  1,
				Failed:   1,
				Warnings: []string{"Workshop item 2900001111: could not update profile: profile is read-only"},
			},
		},
		{
			// #269 W2: the collection document. It rides on ImportPlan
			// (below) and is the `collection` half of the combined
			// --workshop-collection --json document
			// (workshop_collection_import_result). One item of each kind -
			// already adopted from a Steam subscription, and one the user
			// has to subscribe to first.
			"workshop_collection",
			core.WorkshopCollection{
				SourceID:     "steamworkshop",
				CollectionID: "2500900001",
				Name:         "Cargo Ships",
				URL:          "https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001",
				GameID:       "space-engineers-2",
				ProfileName:  "cargo-ships",
				Items: []core.WorkshopCollectionItem{
					{
						FileID: "3617086610", Name: "Sample Workshop Item",
						URL:     "https://steamcommunity.com/sharedfiles/filedetails/?id=3617086610",
						Tracked: true,
					},
					{
						FileID: "3512001122", Name: "Second Workshop Item",
						URL:  "https://steamcommunity.com/sharedfiles/filedetails/?id=3512001122",
						Note: "subscribe in Steam and re-run `lmm import --workshop`",
					},
				},
				Tracked:       1,
				NotSubscribed: 1,
			},
		},
		{
			// One row of the collection above, on its own: an item the user
			// is NOT subscribed to, which is the shape carrying the note.
			"workshop_collection_item",
			core.WorkshopCollectionItem{
				FileID: "3512001122",
				Name:   "Second Workshop Item",
				URL:    "https://steamcommunity.com/sharedfiles/filedetails/?id=3512001122",
				Note:   "subscribe in Steam and re-run `lmm import --workshop`",
			},
		},
		{
			// The plan half: an ordinary ImportPlan whose refs are Workshop
			// items, carrying the collection. Its buckets pin #365's two
			// additive display fields - external, and the revision timestamp
			// a renderer shows INSTEAD of the 19-digit content id in
			// Version. Missing is nil, as import_plan's is, so the "[]" not
			// "null" rule stays pinned here too.
			"profile_import_workshop_plan",
			core.ImportPlan{
				Profile: &domain.Profile{
					Name: "cargo-ships", GameID: "space-engineers-2",
					Mods: []domain.ModReference{
						{SourceID: "steamworkshop", ModID: "3617086610", External: true, UpdatedAt: fixedTime},
						{SourceID: "steamworkshop", ModID: "3512001122", External: true},
					},
					LinkMethod: domain.LinkSymlink,
				},
				Installed: []domain.ModReference{
					{SourceID: "steamworkshop", ModID: "3617086610", Version: "7987119735124793734", External: true, UpdatedAt: fixedTime},
				},
				NeedsRedownload: []domain.ModReference{},
				Missing: []domain.ModReference{
					{SourceID: "steamworkshop", ModID: "3512001122", External: true},
				},
				WorkshopCollection: &core.WorkshopCollection{
					SourceID: "steamworkshop", CollectionID: "2500900001", Name: "Cargo Ships",
					GameID: "space-engineers-2", ProfileName: "cargo-ships",
					Items: []core.WorkshopCollectionItem{
						{FileID: "3617086610", Name: "Sample Workshop Item", Tracked: true},
						{FileID: "3512001122", Note: "subscribe in Steam and re-run `lmm import --workshop`"},
					},
					Tracked: 1, NotSubscribed: 1,
				},
			},
		},
		{
			// The result half: a collection import installs NOTHING (Tier 3
			// owns the download path), so every un-subscribed item is
			// Skipped and the note says what to do about them.
			"profile_import_workshop_result",
			core.ProfileImportResult{
				ProfileName: "cargo-ships",
				Skipped:     1,
				Notes:       []string{"1 item(s) are not subscribed: subscribe in Steam and re-run `lmm import --workshop`"},
			},
		},
		{
			// The whole `lmm profile import --workshop-collection --json`
			// document (W2 review, Minor 6): the result's counts say what
			// happened, and the collection beside them says to WHICH items -
			// the per-item note being the only machine-readable form of the
			// remedy. Emitting the result alone dropped all of it.
			"workshop_collection_import_result",
			core.WorkshopCollectionImportResult{
				Collection: &core.WorkshopCollection{
					SourceID: "steamworkshop", CollectionID: "2500900001", Name: "Cargo Ships",
					URL:    "https://steamcommunity.com/sharedfiles/filedetails/?id=2500900001",
					GameID: "space-engineers-2", ProfileName: "cargo-ships",
					Items: []core.WorkshopCollectionItem{
						{
							FileID: "3617086610", Name: "Sample Workshop Item",
							URL:     "https://steamcommunity.com/sharedfiles/filedetails/?id=3617086610",
							Tracked: true,
						},
						{
							FileID: "3512001122", Name: "Second Workshop Item",
							URL:  "https://steamcommunity.com/sharedfiles/filedetails/?id=3512001122",
							Note: "subscribe in Steam and re-run `lmm import --workshop`",
						},
					},
					Tracked: 1, NotSubscribed: 1,
				},
				Result: &core.ProfileImportResult{
					ProfileName: "cargo-ships",
					Skipped:     1,
					Notes:       []string{"1 item(s) are not subscribed: subscribe in Steam and re-run `lmm import --workshop`"},
				},
			},
		},
		{
			// #365 on the OTHER leaking plan: a sync bucket holding an
			// external ref. Same two fields, same reason - the renderer
			// prints the date, never the content id.
			"profile_sync_plan_external",
			core.ProfileSyncPlan{
				GameID:  "space-engineers-2",
				Profile: "default",
				ToAdd: []domain.ModReference{
					{SourceID: "steamworkshop", ModID: "3617086610", Version: "7987119735124793734", External: true, UpdatedAt: fixedTime},
				},
				Names: map[string]string{"steamworkshop:3617086610": "Sample Workshop Item"},
			},
		},
		{
			// #269: an update lmm can REPORT but never apply. external says
			// which kind of refusal this is; refusal is the existing field,
			// reused rather than a second refusal-rendering path.
			"update_plan_external",
			core.UpdatePlan{
				Mod:      jsonGoldenExternal,
				External: true,
				Update: &domain.Update{
					InstalledMod: jsonGoldenExternal,
					NewVersion:   "8100000000000000001",
				},
				Refusal: core.ReasonExternalNoUpdate,
			},
		},
		{
			// Every optional key populated at once (Changelog/Reason rarely
			// co-occur on a real result), same convention as install_plan
			// above - the golden's job is to pin every key's wire shape.
			"update_apply_result",
			core.UpdateApplyResult{
				Mod:         domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.4", FileIDs: []string{"file-1"}},
				Name:        "Sample Mod",
				FromVersion: "1.2.3",
				ToVersion:   "1.2.4",
				Changelog:   "Fixed a crash on load.",
				Status:      core.UpdateUpdated,
				Reason:      "locked",
				Warnings:    []string{"could not sync merged pak"},
				Notes:       []string{"applied update for Sample Mod"},
			},
		},
		{
			// #324. Every optional key populated at once (a real plan whose
			// selection matched everything carries no NotFound) - the
			// golden's job is to pin each key's wire shape. The unexported
			// snapshot field must not appear at all, same as update_plan.
			"update_batch_plan",
			core.UpdateBatchPlan{
				GameID:  "skyrim-se",
				Profile: "default",
				Updates: []domain.Update{{
					InstalledMod: domain.InstalledMod{
						Mod:         domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.3", GameID: "skyrim-se", UpdatedAt: fixedTime},
						ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateNotify,
					},
					NewVersion: "1.2.4",
					Changelog:  "Fixed a crash on load.",
				}},
				NotFound: []string{"curseforge:7"},
			},
		},
		{
			// #324. Applied is non-omitzero, so a batch that applied
			// nothing still pins as "[]" rather than "null"; Failed and
			// Skipped each carry one entry. The skip is a locked ref (#97) -
			// an UpdateApplyResult with UpdateSkipped and the engine's own
			// refusal sentence as its Reason.
			"update_batch_result",
			core.UpdateBatchResult{
				GameID:  "skyrim-se",
				Profile: "default",
				Applied: []core.UpdateApplyResult{{
					Mod:         domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.4", FileIDs: []string{"file-1"}},
					Name:        "Sample Mod",
					FromVersion: "1.2.3",
					ToVersion:   "1.2.4",
					Status:      core.UpdateUpdated,
				}},
				Failed: []core.UpdateBatchFailure{{
					Mod:   "curseforge:7",
					Name:  "Broken Mod",
					Error: "fetching mod: source unavailable",
				}},
				Skipped: []core.UpdateApplyResult{{
					Mod:         domain.ModReference{SourceID: "nexusmods", ModID: "9", Version: "1.0", Locked: true},
					Name:        "Locked Mod",
					FromVersion: "1.0",
					ToVersion:   "2.0",
					Status:      core.UpdateSkipped,
					Reason:      "Locked Mod is locked at v1.0 in profile default - unlock with 'lmm mod unlock -s nexusmods -p default 9' first",
				}},
			},
		},
		{
			"update_batch_failure",
			core.UpdateBatchFailure{
				Mod:   "curseforge:7",
				Name:  "Broken Mod",
				Error: "fetching mod: source unavailable",
			},
		},
		{
			"rollback_result",
			core.RollbackResult{
				Mod:     domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.2", FileIDs: []string{"file-0"}},
				ModName: "Sample Mod", FromVersion: "1.2.3", ToVersion: "1.2.2",
				Status:   core.UpdateRolledBack,
				Warnings: []string{"could not sync merged pak"},
				Notes:    []string{"rolled back Sample Mod"},
			},
		},
		{
			// Every optional key populated at once (Locked/LockedVersion/
			// Refusal, TargetInstalled) - on a real plan a locked re-link
			// and an already-occupied target can co-occur, but the golden's
			// job is to pin each key's wire shape, not to be a plausible
			// plan (same convention as update_plan/rollback_plan below).
			"relink_plan",
			core.RelinkPlan{
				Mod: domain.InstalledMod{
					Mod:         domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.3", GameID: "skyrim-se", UpdatedAt: fixedTime},
					ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateNotify,
				},
				From:            domain.ModReference{SourceID: "nexusmods", ModID: "42", Version: "1.2.3"},
				To:              domain.ModReference{SourceID: "curseforge", ModID: "99"},
				Relink:          true,
				TargetInstalled: true,
				Locked:          true,
				LockedVersion:   "1.2.3",
				// #294 (Ruling 5, M1): lockedRefUnlockOnlyMessage's
				// canonical wording, SENTENCE ONLY - none of update_plan's,
				// rollback_plan's, or this Refusal carries the "mod is
				// locked: " sentinel prefix; that prefix comes only from
				// the wrapping error's Error(), which cobra prints for a
				// failing command. This is exactly what PlanRelinkMod produces.
				Refusal:           "Sample Mod is locked at v1.2.3 in profile default - unlock with 'lmm mod unlock -s nexusmods -p default 42' first",
				MergedPakAffected: true,
				Profile:           "default",
			},
		},
		{
			// Notes/Warnings both populated at once (a real edit rarely
			// produces both), same convention as install_plan above.
			"relink_result",
			core.RelinkResult{
				Mod: domain.InstalledMod{
					Mod:         domain.Mod{ID: "99", SourceID: "curseforge", Name: "Sample Mod", Version: "1.2.3", GameID: "skyrim-se", UpdatedAt: fixedTime},
					ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateNotify,
				},
				Changes:  []string{"source -> curseforge (was nexusmods)"},
				Notes:    []string{"Warning: could not update profile: mod is locked"},
				Warnings: []string{"could not sync merged pak: base pak missing"},
			},
		},
		{
			"mod_setting_result",
			core.ModSettingResult{
				Mod: domain.InstalledMod{
					Mod:         domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.3", GameID: "skyrim-se", UpdatedAt: fixedTime},
					ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateAuto,
				},
				Locked:        true,
				LockedVersion: "1.2.3",
				UpdatePolicy:  domain.UpdateAuto,
				ConvertPaks:   boolPtr(true),
			},
		},
		{
			"mod_file_entry",
			core.ModFileEntry{Path: "Data/Sample.esp", Size: 1234, Deployed: true},
		},
		{
			// Files is deliberately non-empty (MergedPakOnly and a non-empty
			// Files never co-occur on a real report) to pin ModFileEntry's
			// wire shape nested inside - same convention as install_plan
			// above.
			"mod_files_report",
			core.ModFilesReport{
				Mod: domain.InstalledMod{
					Mod:         domain.Mod{ID: "42", SourceID: "nexusmods", Name: "Sample Mod", Version: "1.2.3", GameID: "skyrim-se", UpdatedAt: fixedTime},
					ProfileName: "default", InstalledAt: fixedTime, UpdatePolicy: domain.UpdateNotify,
				},
				Files: []core.ModFileEntry{
					{Path: "Data/Sample.esp", Size: 1234, Deployed: true},
				},
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
				Refusal:       "Sample Mod is locked at v1.2.3 in profile default - unlock with 'lmm mod unlock -s nexusmods -p default 42' first",
				CacheMissing:  true,
			},
		},
		{
			// The catalog match row: every key populated, including the
			// derived local game_id that keeps a CurseForge add keyed
			// "minecraft" rather than its numeric identifier (#307).
			"game_catalog_match",
			core.GameCatalogMatch{Identifier: "432", Name: "Minecraft", Slug: "minecraft", GameID: "minecraft"},
		},
		{
			// `lmm game add --query`'s document: the query echoed back
			// beside its matches, so a stored response is self-describing.
			"game_catalog_report",
			core.GameCatalogReport{
				SourceID: "curseforge",
				Query:    "mine",
				Matches: []core.GameCatalogMatch{
					{Identifier: "432", Name: "Minecraft", Slug: "minecraft", GameID: "minecraft"},
				},
			},
		},
		{
			// #333's source-removal refusal: the games that still map the
			// source, so a frontend names them instead of saying "in use".
			"source_in_use_error",
			core.SourceInUseError{SourceID: "my-mods", Games: []string{"alpha", "zeta"}},
		},
		{
			// #326 fix wave's M5 (epic review M-4): the mirror one level
			// down - installed mods, not games, still referencing a source
			// UpdateGameSources was asked to drop from the map.
			"game_source_in_use_error",
			core.GameSourceInUseError{SourceID: "nexusmods", GameID: "skyrim-se", Count: 1, Mods: []string{"nexusmods:m1"}},
		},
		{
			// #373: a bare mod ID that matched more than one source. Caveat
			// is populated here because `update` alone adds one; the
			// uninstall/mod-edit refusals omit it (omitempty).
			"ambiguous_mod_error",
			core.AmbiguousModError{
				ModID: "alpha", Profile: "default",
				Sources: []string{"localmods", "repo"},
				Flag:    "--source",
				Caveat:  "(local mods cannot be update-checked)",
			},
		},
		{
			// #79: the credential a frontend cannot read and the action
			// that fixes it. Sources is present because a single damaged
			// row names only itself - a key-file-level failure (missing,
			// permissions, malformed) carries no sources and drops the key.
			// Err is absent from the wire (json:"-"), like every other
			// typed error here: it exists for errors.Is/As, not a client.
			"token_key_error",
			core.TokenKeyError{
				KeyPath: "/home/u/.local/share/lmm/key",
				Reason:  "undecryptable",
				Sources: []string{"nexusmods"},
			},
		},
		{
			// The field-named rejection an SPA form renders against the
			// offending input. Err is deliberately absent from the wire
			// (json:"-"): it exists for errors.Is, not for a client.
			"game_spec_error",
			core.GameSpecError{
				Field:  "install_path",
				Value:  "/games/nope",
				Reason: "path does not exist",
				Err:    domain.ErrInvalidGameID,
			},
		},
		{
			// One detect listing row: the embedded DetectedGame flat (as
			// every whole-record wire type in this file embeds its record),
			// plus the 1-based index a selection names and the
			// already-configured marker.
			"game_detect_entry",
			core.GameDetectEntry{
				DetectedGame: domain.DetectedGame{
					SteamAppID: "489830", Slug: "skyrim-se", Name: "Skyrim Special Edition",
					InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data",
					NexusID: "skyrimspecialedition", Known: true,
				},
				Index:             1,
				AlreadyConfigured: true,
			},
		},
		{
			// #206's unknown row, pinned as its own golden because a client
			// branches on exactly these two absences: no "known" member
			// (omitzero) and "index": 0, which is what says "installed, but
			// a detect selection cannot name it - add it from the detected
			// game instead".
			"game_detect_entry_unknown",
			core.GameDetectEntry{
				DetectedGame: domain.DetectedGame{
					SteamAppID: "526870", Slug: "satisfactory", Name: "Satisfactory",
					InstallPath: "/games/satisfactory",
				},
			},
		},
		{
			// The pre-selection listing GET /api/v1/games/detect answers
			// with: rows plus the scan's own warnings, carried in the
			// document rather than written to stderr (Ruling 15).
			"game_detect_listing",
			core.GameDetectListing{
				Games: []core.GameDetectEntry{{
					DetectedGame: domain.DetectedGame{
						SteamAppID: "489830", Slug: "skyrim-se", Name: "Skyrim Special Edition",
						InstallPath: "/games/skyrim", ModPath: "/games/skyrim/Data",
						NexusID: "skyrimspecialedition", Known: true,
					},
					Index: 1,
				}, {
					// The wider listing (GET /api/v1/games/detect?all=1,
					// `lmm game detect --include-unknown`) mixes both row
					// kinds in scan order; the known rows keep 1..N and the
					// unknown ones carry no index at all (#206).
					DetectedGame: domain.DetectedGame{
						SteamAppID: "526870", Slug: "satisfactory", Name: "Satisfactory",
						InstallPath: "/games/satisfactory",
					},
				}},
				Warnings: []string{"steam library /mnt/games could not be read"},
			},
		},
		{
			// #350's originals-store manifest row, which is also what
			// every snapshot document carries as "the originals in
			// force". The deploy shape (a mod identity beside the op) -
			// a profile override's row carries the same keys with
			// source_id/mod_id omitted.
			"original_file",
			core.OriginalFile{
				Root:         core.OriginalRootModPath,
				RelativePath: "Data/shipped.esp",
				SHA256:       "3f786850e387550fdab836ed7e6dc881de23001b1b4bd0e0e2a4a9d1d8d1a1f1",
				Size:         4096,
				// Review finding 6: the mode the file had, so an
				// executable comes back executable.
				Mode:       0o755,
				CapturedAt: fixedTime,
				Op:         core.OriginalOpDeploy,
				SourceID:   "nexusmods",
				ModID:      "42",
				Profile:    "default",
			},
		},
		{
			// The snapshot document itself (#350): the file on disk AND
			// the wire shape /api/v1 hands back. Four halves - the
			// profile export, the installed rows, the deployed-files
			// manifest, the originals in force.
			"snapshot",
			core.Snapshot{
				Name: "before-tweaks", GameID: "skyrim-se", Profile: "default",
				CreatedAt: fixedTime,
				ProfileDocument: &domain.ExportedProfile{
					Name: "default", GameID: "skyrim-se",
					Mods: []domain.ModReference{{
						SourceID: "nexusmods", ModID: "42", Version: "1.2.3",
						FileIDs: []string{"1"}, Locked: true,
					}},
				},
				Installed: []domain.InstalledMod{{
					Mod:         jsonGoldenMod,
					ProfileName: "default", UpdatePolicy: domain.UpdateNotify,
					InstalledAt: fixedTime, Enabled: true, Deployed: true,
					LinkMethod: domain.LinkSymlink, FileIDs: []string{"1"},
				}},
				DeployedFiles: []core.SnapshotFile{{
					RelativePath: "Data/mod.esp", SourceID: "nexusmods", ModID: "42",
					SHA256: "3f786850e387550fdab836ed7e6dc881de23001b1b4bd0e0e2a4a9d1d8d1a1f1", Size: 2048,
				}},
				Originals: []core.OriginalFile{{
					Root: core.OriginalRootModPath, RelativePath: "Data/shipped.esp",
					SHA256: "9c1185a5c5e9fc54612808977ee8f548b2258d31", Size: 4096,
					CapturedAt: fixedTime, Op: core.OriginalOpDeploy,
					SourceID: "nexusmods", ModID: "42", Profile: "default",
				}},
			},
		},
		{
			// A tracked path that was NOT on disk: recorded rather than
			// dropped, with no checksum, because "tracked and absent" is a
			// different fact from "never tracked".
			"snapshot_file_missing",
			core.SnapshotFile{
				RelativePath: "Data/gone.esp", SourceID: "nexusmods", ModID: "42", Missing: true,
			},
		},
		{
			"snapshot_file",
			core.SnapshotFile{
				RelativePath: "Data/mod.esp", SourceID: "nexusmods", ModID: "42",
				SHA256: "3f786850e387550fdab836ed7e6dc881de23001b1b4bd0e0e2a4a9d1d8d1a1f1", Size: 2048,
			},
		},
		{
			"snapshot_result",
			core.SnapshotResult{
				Name: "before-tweaks", GameID: "skyrim-se", Profile: "default",
				CreatedAt: fixedTime, Path: "/home/u/.local/share/lmm/snapshots/skyrim-se/before-tweaks.json",
				Mods: 12, DeployedFiles: 340, Originals: 3, SizeBytes: 1048576,
			},
		},
		{
			"snapshot_info",
			core.SnapshotInfo{
				Name: "auto-deploy-20260827-120000", GameID: "skyrim-se", Profile: "default",
				CreatedAt: fixedTime, Auto: true,
				Mods: 12, DeployedFiles: 340, Originals: 3, SizeBytes: 1048576,
			},
		},
		{
			"snapshot_listing",
			core.SnapshotListing{
				GameID: "skyrim-se",
				Snapshots: []core.SnapshotInfo{{
					Name: "before-tweaks", GameID: "skyrim-se", Profile: "default",
					CreatedAt: fixedTime, Mods: 12, DeployedFiles: 340, Originals: 3, SizeBytes: 1048576,
				}},
				Warnings: []string{"broken.json could not be read: parsing the snapshot: unexpected EOF"},
			},
		},
		{
			// The restore plan (#350): what `snapshot restore --dry-run`
			// prints and what the confirm modal renders. The refusals
			// list is the point - a version a source can no longer serve
			// is visible BEFORE anything is purged.
			"snapshot_restore_plan",
			core.SnapshotRestorePlan{
				GameID: "skyrim-se", Profile: "default",
				Snapshot: "before-tweaks", CreatedAt: fixedTime,
				ToPurge: []domain.InstalledMod{{
					Mod:         jsonGoldenMod,
					ProfileName: "default", UpdatePolicy: domain.UpdateNotify,
					InstalledAt: fixedTime, Enabled: true, Deployed: true,
					LinkMethod: domain.LinkSymlink,
				}},
				// Review finding 2: the restore also carries the switch
				// back to the snapshot's profile, so the plan says which
				// profile it is switching away from and what that costs.
				ActiveProfile: "survival",
				ToPurgeActive: []domain.InstalledMod{{
					Mod:         jsonGoldenMod,
					ProfileName: "survival", UpdatePolicy: domain.UpdateNotify,
					InstalledAt: fixedTime, Enabled: true, Deployed: true,
					LinkMethod: domain.LinkSymlink,
				}},
				Originals: []core.SnapshotRestoreOriginal{{
					Root: core.OriginalRootModPath, RelativePath: "Data/shipped.esp",
					Status: core.SnapshotOriginalRestorable,
				}, {
					Root: core.OriginalRootInstallPath, RelativePath: "Data/game.ini",
					Status: core.SnapshotOriginalUnavailable,
					Reason: "checksum 9c11 does not match the recorded 3f78",
				}},
				Mods: []core.SnapshotRestoreMod{{
					SourceID: "nexusmods", ModID: "42", Name: "Sample Mod",
					Version: "1.2.3", Cached: true,
				}, {
					SourceID: "curseforge", ModID: "7", Name: "Gone Mod",
					Version: "0.9", Error: "no downloadable files",
				}},
				Refusals: []core.InstalledRef{{
					SourceID: "curseforge", ModID: "7", Name: "Gone Mod",
					Version: "0.9", Reason: "no downloadable files",
				}},
				ProfileChanged: true,
			},
		},
		{
			// #269 x #350: the restore's account of a Steam Workshop item.
			// ToPurge is EMPTY and external names it - the same split
			// purge_plan_external pins - and the Mods row carries
			// external/updated_at with Cached FALSE and no Error, because
			// "not cached" here must not read as "will be downloaded".
			// external_missing is the second row: the item was unsubscribed
			// after the snapshot, which is a finding, not a refusal, so
			// refusals stays absent.
			"snapshot_restore_plan_external",
			core.SnapshotRestorePlan{
				GameID: "skyrim-se", Profile: "default",
				Snapshot: "before-tweaks", CreatedAt: fixedTime,
				ToPurge:   []domain.InstalledMod{},
				External:  []string{"Workshop Item", "Gone Workshop Item"},
				Originals: []core.SnapshotRestoreOriginal{},
				Mods: []core.SnapshotRestoreMod{{
					SourceID: "steamworkshop", ModID: "3617086610", Name: "Workshop Item",
					Version: "7987119735124793734", External: true, UpdatedAt: fixedTime,
				}, {
					SourceID: "steamworkshop", ModID: "3617086611", Name: "Gone Workshop Item",
					Version: "7987119735124793735", External: true, ExternalMissing: true,
				}},
			},
		},
		{
			"snapshot_restore_original",
			core.SnapshotRestoreOriginal{
				Root: core.OriginalRootModPath, RelativePath: "Data/shipped.esp",
				Status: core.SnapshotOriginalRestorable,
			},
		},
		{
			"snapshot_restore_mod",
			core.SnapshotRestoreMod{
				SourceID: "nexusmods", ModID: "42", Name: "Sample Mod",
				Version: "1.2.3", Cached: true,
			},
		},
		{
			// The restore result, in its most informative shape: a
			// partial restore. This is also what
			// SnapshotRestorePartialError's "details" carries.
			"snapshot_restore_result",
			core.SnapshotRestoreResult{
				Snapshot: "before-tweaks", Profile: "default",
				SwitchedFrom:      "survival",
				SafetySnapshot:    "auto-snapshot_restore-20260827-120000",
				Purged:            4,
				OriginalsRestored: 2,
				OriginalsSkipped: []core.SnapshotRestoreOriginal{{
					Root: core.OriginalRootInstallPath, RelativePath: "Data/game.ini",
					Status: core.SnapshotOriginalUnavailable,
					Reason: "the stored copy is missing",
				}},
				Disabled: 1, Enabled: 2, Installed: 3, Replaced: 1, Deployed: 7,
				// #386: a mod the restored profile does not list. Its
				// download and its row are kept, so it is neither a refusal
				// nor a failure - it is the reason `lmm list` counts one
				// more mod than the profile has.
				LeftInstalled: []core.InstalledRef{{
					SourceID: "nexusmods", ModID: "13", Name: "Stock Override",
					Version: "2.0", Reason: "not listed in the restored profile; its download is kept",
				}},
				Refused: []core.InstalledRef{{
					SourceID: "curseforge", ModID: "7", Name: "Gone Mod",
					Version: "0.9", Reason: "no downloadable files",
				}},
				Notes:    []string{"the current state was recorded as auto-snapshot_restore-20260827-120000"},
				Warnings: []string{"could not sync merged pak"},
			},
		},
		{
			"snapshot_delete_result",
			core.SnapshotDeleteResult{Name: "before-tweaks", GameID: "skyrim-se", Deleted: true},
		},
		{
			// #359 unit 3: the loader report `lmm game show` prints and the
			// web game page renders. It carries the declared AND the detected
			// answer, so a disagreement is visible on the wire rather than
			// resolved away.
			"loader_status",
			core.LoaderStatus{
				GameID: "valheim",
				Declared: &domain.GameLoader{
					Kind: domain.LoaderKindBepInEx, Version: "5.4.23.5",
					Bootstrap: domain.LoaderBootstrapProton,
				},
				DetectedRuntime: domain.LoaderRuntimeMono, DetectedBootstrap: domain.LoaderBootstrapProton,
				EffectiveRuntime: domain.LoaderRuntimeMono, EffectiveBootstrap: domain.LoaderBootstrapProton,
				LaunchOption: core.BepInExLaunchOptionProton,
				Installed:    true,
				LoadedAt:     "2026-08-27T12:00:00Z",
			},
		},
		{
			// #359: the loader declaration as a frontend sends it - four
			// unparsed strings, so a rejection can name the wire field.
			"loader_spec",
			core.LoaderSpec{Kind: "bepinex", Version: "5.4.23.5", Runtime: "mono", Bootstrap: "proton"},
		},
		{
			// #359's plan-time precondition. The setup steps are DATA on the
			// wire, which is what lets the web UI render the same sentences
			// the terminal prints instead of carrying its own copy.
			"loader_required_error",
			core.LoaderRequiredError{
				GameID: "lethal-company", Kind: "bepinex", ModName: "Skinwalkers",
				Layout: "game-root-relative",
				Setup: []string{
					"Install BepInEx into the game directory yourself.",
					"Record it: `lmm game edit lethal-company --loader bepinex`.",
				},
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
