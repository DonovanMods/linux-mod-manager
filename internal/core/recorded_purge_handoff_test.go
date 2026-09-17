package core_test

// The #445 final gate's F-A. A recorded-only purge used to drop its record
// of a path another claimant still recorded (PurgeKeptRecorded,
// PurgeKeptOtherGame), leaving the file to that claimant's purge. That purge
// asks whether the active profile lists ITS OWN row's mod, in ITS OWN game,
// so when the claimant's row names another mod, or belongs to another game,
// the last purge removed a file the active profile lists:
//
//   - R1: a v1.30.1 local import mints a key per import, so one archive
//     imported into two profiles is two mods recording one path;
//   - R10: two mods ship one file, and a flag-only switch leaves the active
//     profile with no rows; R11 is the same under copy, with the user's
//     edit of the shared file;
//   - R2: two games share one mod directory.
//
// A purge now drops such a record only when another record of the path
// carries the protection on - one under the active profile, or one whose
// mod the active profile lists - and otherwise keeps it as PurgeKeptListed.

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// handoffStep is one recorded-only purge: of profile, in fixture's game.
type handoffStep struct {
	fixture *legacyFixture
	profile string
}

// handoffState is a state in which the active profile of f's game lists a
// file that only non-active claimants record.
type handoffState struct {
	f *legacyFixture
	// purges are the non-active claimants' purges.
	purges []handoffStep
	// kept are the paths the active profile lists; gone are the paths only
	// an unlisted mod ships, which the purges remove.
	kept, gone []string
	// listedRecord is the record that has to survive every purge order, so
	// the mod_path refusal still counts it: profile's record of kept[0].
	listedRecord struct{ profile, modID string }
}

// handoffGame is a symlink or copy game whose mod directory sits under its
// install path, so the mod_path refusal can move it.
func handoffGame(t *testing.T, id string, root string, method domain.LinkMethod) *domain.Game {
	t.Helper()
	return &domain.Game{ID: id, Name: id, InstallPath: root, ModPath: filepath.Join(root, "mods"), LinkMethod: method, LinkMethodExplicit: true}
}

// twoKeysState is R1: default and survival each imported one archive, so
// each records Data/a.esp under its own key, and the live file is
// survival's; alt, now active, lists default's key and has no rows.
func twoKeysState(t *testing.T) handoffState {
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink))
	f.profile(t, "default", false, "a1")
	f.profile(t, "survival", false, "a2")
	f.profile(t, "alt", true, "a1")
	f.deployed(t, "default", "a1", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)
	f.deployed(t, "survival", "a2", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)
	s := handoffState{f: f, purges: []handoffStep{{f, "default"}, {f, "survival"}}, kept: []string{"Data/a.esp"}}
	s.listedRecord.profile, s.listedRecord.modID = "default", "a1"
	return s
}

// sharedFileState is R10 (symlink) and R11 (copy): mod-x and mod-y both
// ship Data/common.esp; survival deployed mod-y, then default redeployed
// mod-x over it, and under copy the user then edited the shared file. A
// flag-only switch made alt, which lists mod-x, active with no rows.
//
// With ownFile false, mod-x ships only the shared file: nothing of it is
// then kept for being listed alone, which is the only thing that used to
// make the mod_path refusal name `lmm profile apply` in this state.
func sharedFileState(t *testing.T, method domain.LinkMethod, ownFile bool) handoffState {
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), method))
	f.profile(t, "default", false, "mod-x")
	f.profile(t, "survival", false, "mod-y")
	f.profile(t, "alt", true, "mod-x")
	f.deployed(t, "survival", "mod-y", method, map[string]string{"Data/common.esp": "common from Y", "Data/y.esp": "y"}, nil)
	var edited map[string]string
	if method == domain.LinkCopy {
		edited = map[string]string{"Data/common.esp": "common from X, USER-EDITED"}
	}
	modX := map[string]string{"Data/common.esp": "common from X"}
	kept := []string{"Data/common.esp"}
	if ownFile {
		modX["Data/x.esp"] = "x"
		kept = append(kept, "Data/x.esp")
	}
	f.deployed(t, "default", "mod-x", method, modX, edited)
	s := handoffState{f: f, purges: []handoffStep{{f, "default"}, {f, "survival"}}, kept: kept, gone: []string{"Data/y.esp"}}
	s.listedRecord.profile, s.listedRecord.modID = "default", "mod-x"
	return s
}

// crossGameState is R2: sky and sky2 share one mod directory. sky's default
// records Data/a.esp and sky's active alt lists that mod, with no rows;
// sky2's non-active y records the same file, live as sky2's, and sky2's
// active x lists nothing.
func crossGameState(t *testing.T) handoffState {
	root := t.TempDir()
	f := newLegacyFixture(t, handoffGame(t, "sky", root, domain.LinkSymlink))
	f2 := &legacyFixture{svc: f.svc, game: handoffGame(t, "sky2", root, domain.LinkSymlink)}
	require.NoError(t, f.svc.SaveGame(context.Background(), f2.game))
	f.profile(t, "default", false, "k")
	f.profile(t, "alt", true, "k")
	f2.profile(t, "x", true)
	f2.profile(t, "y", false, "j")
	f.deployed(t, "default", "k", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)
	f2.deployed(t, "y", "j", domain.LinkSymlink, map[string]string{"Data/a.esp": "mod a"}, nil)
	s := handoffState{f: f, purges: []handoffStep{{f, "default"}, {f2, "y"}}, kept: []string{"Data/a.esp"}}
	s.listedRecord.profile, s.listedRecord.modID = "default", "k"
	return s
}

var handoffStates = map[string]func(t *testing.T) handoffState{
	"R1 two keys for one path":                  twoKeysState,
	"R10 a file two mods ship, symlink":         func(t *testing.T) handoffState { return sharedFileState(t, domain.LinkSymlink, true) },
	"R10 with only the shared file":             func(t *testing.T) handoffState { return sharedFileState(t, domain.LinkSymlink, false) },
	"R11 a file two mods ship, copy and edited": func(t *testing.T) handoffState { return sharedFileState(t, domain.LinkCopy, true) },
	"R11 with only the shared file":             func(t *testing.T) handoffState { return sharedFileState(t, domain.LinkCopy, false) },
	"R2 two games on one mod directory":         crossGameState,
}

func TestRecordedPurge_AListedFileOutlivesEveryClaimantsPurge(t *testing.T) {
	for name, build := range handoffStates {
		for _, reversed := range []bool{false, true} {
			order := "in order"
			if reversed {
				order = "reversed"
			}
			t.Run(name+", "+order, func(t *testing.T) {
				s := build(t)
				before := treeOf(t, s.f.game.ModPath)
				purges := s.purges
				if reversed {
					purges = []handoffStep{s.purges[1], s.purges[0]}
				}

				for _, step := range purges {
					step.fixture.purge(t, step.profile)
				}

				after := treeOf(t, s.f.game.ModPath)
				for _, rel := range s.kept {
					key := filepath.FromSlash(rel)
					require.Contains(t, after, key, "%s, which the active profile lists, is still live", rel)
					assert.Equal(t, before[key], after[key], "%s is exactly as it was", rel)
				}
				for _, rel := range s.gone {
					assert.NotContains(t, after, filepath.FromSlash(rel), "%s is only an unlisted mod's", rel)
				}
				assert.Equal(t, []string{s.kept[0]}, filterRecorded(s.f.recorded(t, s.listedRecord.profile, s.listedRecord.modID), s.kept[0]),
					"the listed record stays, so the mod_path refusal still counts it")
			})
		}
	}

	// The record goes when another record carries the protection on: the
	// active profile's own, or another claimant whose mod it lists.
	t.Run("another record the active profile lists takes it over", func(t *testing.T) {
		s := twoKeysState(t)
		s.f.profile(t, "alt", true, "a1", "a2")

		plan, _ := s.f.purge(t, "default")

		assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptRecorded, Profiles: []string{"survival"}}}, plan.Kept)
		assert.Empty(t, s.f.recorded(t, "default", "a1"))

		plan, _ = s.f.purge(t, "survival")
		assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptListed, Profiles: []string{"alt"}}}, plan.Kept)
		assert.FileExists(t, filepath.Join(s.f.game.ModPath, "Data", "a.esp"))
		assert.Equal(t, []string{"Data/a.esp"}, s.f.recorded(t, "survival", "a2"))
	})

	t.Run("the active profile's own record takes it over", func(t *testing.T) {
		s := twoKeysState(t)
		require.NoError(t, s.f.svc.ExecForTest(context.Background(),
			`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES ('sky', 'alt', 'Data/a.esp', 'local', 'a1')`))

		plan, _ := s.f.purge(t, "default")

		assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptRecorded, Profiles: []string{"alt", "survival"}}}, plan.Kept)
		assert.Empty(t, s.f.recorded(t, "default", "a1"))
	})

	// #445 gate 2, V9: not for a mod the active profile's document no
	// longer lists - the apply takes that file down.
	t.Run("the active profile's record of a mod it does not list does not", func(t *testing.T) {
		s := twoKeysState(t)
		require.NoError(t, s.f.svc.ExecForTest(context.Background(),
			`INSERT INTO deployed_files (game_id, profile_name, relative_path, source_id, mod_id) VALUES ('sky', 'alt', 'Data/a.esp', 'local', 'other')`))

		plan, _ := s.f.purge(t, "default")

		assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptListed, Profiles: []string{"alt"}}}, plan.Kept)
		assert.Equal(t, []string{"Data/a.esp"}, s.f.recorded(t, "default", "a1"))
	})

	// What the plan says, and what the refusal counts, for the state the
	// gate reproduced it from.
	t.Run("the plan reports the kept record as listed", func(t *testing.T) {
		s := twoKeysState(t)

		plan, result := s.f.purge(t, "default")

		assert.Empty(t, plan.Remove)
		assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/a.esp", Reason: core.PurgeKeptListed, Profiles: []string{"alt"}}}, plan.Kept)
		assert.Empty(t, plan.Mods, "no record of default's goes")
		assert.Zero(t, result.Purged)

		_, err := s.f.svc.SetGameModPath(context.Background(), "sky", filepath.Join(s.f.game.InstallPath, "mods2"))
		var inUse *core.GameModPathInUseError
		require.ErrorAs(t, err, &inUse)
		assert.Equal(t, 1, inUse.ListedUnrecorded)
		assert.True(t, inUse.NeedsApply)
		assert.Contains(t, err.Error(), "`lmm profile apply alt --game sky`")
	})
}

// filterRecorded is the entries of recorded equal to rel.
func filterRecorded(recorded []string, rel string) []string {
	var out []string
	for _, r := range recorded {
		if r == rel {
			out = append(out, r)
		}
	}
	return out
}

// TestModPathRefusal_ItsOwnCommandsKeepAListedFileAnotherClaimantRecords
// runs the refusal's own commands over each F-A state: the move is allowed
// within three rounds, and every file the active profile lists ends up live
// under the new mod_path - byte for byte its cached copy - and recorded
// under the active profile. Under copy the refusal's `lmm profile apply`
// redeploys the cached copy over the user's edit (a recorded file is lmm's,
// and the move re-creates every file from the cache anyway).
func TestModPathRefusal_ItsOwnCommandsKeepAListedFileAnotherClaimantRecords(t *testing.T) {
	for name, build := range handoffStates {
		t.Run(name, func(t *testing.T) {
			s := build(t)

			refusals := followModPathRefusal(t, s.f.svc, s.f.game.ID, filepath.Join(s.f.game.InstallPath, "mods2"))

			require.NotEmpty(t, refusals)
			assert.Contains(t, refusals[0], "`lmm profile apply alt --game sky`")
			requireActiveListedLive(t, s.f.svc, s.f.game.ID)
			for _, rel := range s.gone {
				assert.NoFileExists(t, filepath.Join(s.f.game.InstallPath, "mods2", filepath.FromSlash(rel)))
			}
		})
	}

	// The other game moves first: its purge leaves sky's file, and sky's
	// refusal still clears.
	t.Run("R2, the other game moving first", func(t *testing.T) {
		s := crossGameState(t)

		followModPathRefusal(t, s.f.svc, "sky2", filepath.Join(s.f.game.InstallPath, "elsewhere"))
		assert.Equal(t, "mod a", readLive(t, filepath.Join(s.f.game.ModPath, "Data", "a.esp")))
		followModPathRefusal(t, s.f.svc, "sky", filepath.Join(s.f.game.InstallPath, "mods2"))

		requireActiveListedLive(t, s.f.svc, "sky")
	})
}
