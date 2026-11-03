package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withListGroupBy sets `lmm list --group-by` for one test.
func withListGroupBy(t *testing.T, value string) {
	t.Helper()
	old := listGroupBy
	listGroupBy = value
	t.Cleanup(func() { listGroupBy = old })
}

// seedCategorisedMod is seedDeployableMod with a source category name on
// the installed row and, when userCategory is set, the user's category on
// the profile ref (#592).
func seedCategorisedMod(t *testing.T, svc *core.Service, game *domain.Game, modID, name, sourceCategory, userCategory string) {
	t.Helper()
	seedDeployableMod(t, svc, game, modID, name, modID+".esp")
	if sourceCategory != "" {
		mod, err := svc.GetInstalledMod(context.Background(), "src", modID, game.ID, "default")
		require.NoError(t, err)
		mod.CategoryName = sourceCategory
		require.NoError(t, svc.SaveInstalledMod(context.Background(), mod))
	}
	if userCategory != "" {
		require.NoError(t, svc.NewProfileManager().SetModCategory(context.Background(), game.ID, "default", "src", modID, userCategory))
	}
}

// seedGroupFixture is four mods in load order a, b, c, d: a has no
// category, b the source's "Weapons", c the user's "audio" (lower case, so
// the sort is shown to ignore case), d the source's "Weapons" again.
func seedGroupFixture(t *testing.T, svc *core.Service, game *domain.Game) {
	t.Helper()
	seedCategorisedMod(t, svc, game, "a", "Mod A", "", "")
	seedCategorisedMod(t, svc, game, "b", "Mod B", "Weapons", "")
	seedCategorisedMod(t, svc, game, "c", "Mod C", "Weapons", "audio")
	seedCategorisedMod(t, svc, game, "d", "Mod D", "Weapons", "")
}

// TestList_GroupByCategory_OrdersGroupsWithUncategorizedLast: one heading
// per group, sorted case-insensitively, "Uncategorized" last, and each
// group's mods in load order (#592).
func TestList_GroupByCategory_OrdersGroupsWithUncategorizedLast(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedGroupFixture(t, svc, game)
	withListGroupBy(t, "category")

	out := listNonVerbose(t, svc, game)

	want := []string{"audio (1)", "Mod C", "Weapons (2)", "Mod B", "Mod D", "Uncategorized (1)", "Mod A"}
	pos := -1
	for _, s := range want {
		i := strings.Index(out, s)
		require.GreaterOrEqualf(t, i, 0, "%q missing from:\n%s", s, out)
		require.Greaterf(t, i, pos, "%q out of order in:\n%s", s, out)
		pos = i
	}
	// The columns stay one table across the groups.
	assert.Equal(t, "Installed mods in Game (profile: default) — 4 mod(s)\n\n"+
		"ID  NAME   VERSION  AUTHOR  STATE\n"+
		"--  ----   -------  ------  -----\n"+
		"audio (1)\n"+
		"c   Mod C  1.0      -       not deployed\n"+
		"\n"+
		"Weapons (2)\n"+
		"b   Mod B  1.0      -       not deployed\n"+
		"d   Mod D  1.0      -       not deployed\n"+
		"\n"+
		"Uncategorized (1)\n"+
		"a   Mod A  1.0      -       not deployed\n", out)
	assert.NotContains(t, out, "\x1b[", "plain output must never contain ANSI escapes")
}

// TestList_GroupByCategory_HeadingsBoldWhenColored: a heading is bold and
// each row keeps its state tint.
func TestList_GroupByCategory_HeadingsBoldWhenColored(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	resetColorFlags(t)
	seedGroupFixture(t, svc, game)
	withListGroupBy(t, "category")
	withColorCapableStdout(t, true)

	out := listNonVerbose(t, svc, game)

	assert.Contains(t, out, ansiBold+"Weapons (2)"+ansiReset)
	assert.Contains(t, out, ansiBold+"Uncategorized (1)"+ansiReset)
	assert.Contains(t, rowFor(out, "Mod B"), ansiYellow, "an undeployed row keeps its tint")
}

// TestList_WithoutGroupBy_Unchanged: the categories change nothing without
// the flag - the pre-#592 table, byte for byte.
func TestList_WithoutGroupBy_Unchanged(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedGroupFixture(t, svc, game)
	withListGroupBy(t, "")

	out := listNonVerbose(t, svc, game)

	assert.Equal(t, "Installed mods in Game (profile: default) — 4 mod(s)\n\n"+
		"ID  NAME   VERSION  AUTHOR  STATE\n"+
		"--  ----   -------  ------  -----\n"+
		"a   Mod A  1.0      -       not deployed\n"+
		"b   Mod B  1.0      -       not deployed\n"+
		"c   Mod C  1.0      -       not deployed\n"+
		"d   Mod D  1.0      -       not deployed\n", out)
}

// TestList_GroupByCategory_JSONStaysFlat: --json is the flat ModList either
// way; each entry carries its category for a consumer to group by.
func TestList_GroupByCategory_JSONStaysFlat(t *testing.T) {
	svc, game := setupDoDeployTest(t)
	seedGroupFixture(t, svc, game)

	withListGroupBy(t, "")
	flat := listVerbose(t, svc, game, true)
	withListGroupBy(t, "category")
	grouped := listVerbose(t, svc, game, true)

	assert.Equal(t, flat, grouped)
	var doc core.ModList
	require.NoError(t, json.Unmarshal([]byte(grouped), &doc))
	require.Len(t, doc.Mods, 4)
	assert.Equal(t, "audio", doc.Mods[2].EffectiveCategory)
	assert.Equal(t, core.CategorySourceUser, doc.Mods[2].CategorySource)
}

// TestValidateListFlags covers --group-by's usage errors.
func TestValidateListFlags(t *testing.T) {
	oldProfiles := listProfiles
	t.Cleanup(func() { listProfiles = oldProfiles })

	tests := []struct {
		name     string
		groupBy  string
		profiles bool
		wantErr  string
	}{
		{name: "no flags", groupBy: ""},
		{name: "category", groupBy: "category"},
		{name: "unknown value", groupBy: "author", wantErr: `--group-by: unknown value "author" (the only one is "category")`},
		{name: "with --profiles", groupBy: "category", profiles: true, wantErr: "--group-by cannot be used with --profiles"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withListGroupBy(t, tt.groupBy)
			listProfiles = tt.profiles
			err := validateListFlags()
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.EqualError(t, err, tt.wantErr)
		})
	}
}
