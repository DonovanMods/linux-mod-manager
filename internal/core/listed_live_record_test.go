package core_test

// The #445 second gate's V9. A record of a path under the ACTIVE profile
// used to hand a listed path on whatever mod it named (listedHandedOn).
// When the user had edited the active profile's document to list another
// mod shipping the same file, and not applied it yet, that record named a
// mod the document no longer lists: its file comes down with the next
// apply, so it protects nothing. The refusal then named no `lmm profile
// apply`, the purges cleared every record, and the listed mod ended the
// move undeployed. The active profile's record now carries the path on
// only when the document lists its mod, as any other record does.

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveRecordOfAnotherModState is V9: alt, active, has mod-y deployed
// (Data/common.esp, Data/y.esp) and its document lists only mod-x, which p
// deployed later over the shared file - or lists mod-y marked off, then
// mod-x.
func liveRecordOfAnotherModState(t *testing.T, markedOff bool) *legacyFixture {
	t.Helper()
	f := newLegacyFixture(t, handoffGame(t, "sky", t.TempDir(), domain.LinkSymlink))
	f.profile(t, "alt", true, "mod-x")
	if markedOff {
		doc := "name: alt\ngame_id: sky\nmods:\n" +
			"    - source_id: local\n      mod_id: mod-y\n      version: unknown\n      disabled: true\n" +
			"    - source_id: local\n      mod_id: mod-x\n      version: unknown\n" +
			"is_default: true\n"
		require.NoError(t, os.WriteFile(filepath.Join(f.svc.ConfigDir(), "games", "sky", "profiles", "alt.yaml"), []byte(doc), 0o644))
	}
	f.profile(t, "p", false, "mod-x")
	f.deployed(t, "alt", "mod-y", domain.LinkSymlink, map[string]string{"Data/common.esp": "common Y", "Data/y.esp": "y"}, nil)
	f.deployed(t, "p", "mod-x", domain.LinkSymlink, map[string]string{"Data/common.esp": "common X"}, nil)
	return f
}

func TestModPathRefusal_AnActiveRecordOfAnUnlistedModDoesNotHideTheApply(t *testing.T) {
	for name, markedOff := range map[string]bool{"mod-y dropped": false, "mod-y marked off": true} {
		t.Run(name, func(t *testing.T) {
			f := liveRecordOfAnotherModState(t, markedOff)

			plan := f.purgePlan(t, "p")
			assert.Equal(t, []core.PurgeKeptPath{{Path: "Data/common.esp", Reason: core.PurgeKeptListed, Profiles: []string{"alt"}}}, plan.Kept,
				"alt's record names a mod its document does not list, so p's record is the listed file's only claim")

			refusals := followModPathRefusal(t, f.svc, "sky", filepath.Join(f.game.InstallPath, "mods2"))

			require.NotEmpty(t, refusals)
			assert.Contains(t, refusals[0], "`lmm profile apply alt --game sky`")
			requireActiveListedLive(t, f.svc, "sky")
			assert.NoFileExists(t, filepath.Join(f.game.InstallPath, "mods2", "Data", "y.esp"))
		})
	}
}
