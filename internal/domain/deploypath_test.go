package domain_test

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeployPath_IdentityIsRootAndPath(t *testing.T) {
	assert.NotEqual(t, domain.GameRootFile("textures/a.dds"), domain.ModPathFile("textures/a.dds"))
	assert.Equal(t, domain.DeployPath{Rel: "a"}, domain.ModPathFile("a"), "the zero root is the mod_path")
	assert.True(t, domain.GameRootFile("d3d11.dll").IsGameRoot())
	assert.False(t, domain.ModPathFile("d3d11.dll").IsGameRoot())
	// A mod_path folder literally named "<game root>" is its own file.
	assert.NotEqual(t, domain.GameRootFile("d3d11.dll"), domain.ModPathFile("<game root>/d3d11.dll"))
}

func TestDeployPath_String(t *testing.T) {
	assert.Equal(t, "<game root>/enbseries/a.fx", domain.GameRootFile("enbseries/a.fx").String())
	assert.Equal(t, "textures/a.dds", domain.ModPathFile("textures/a.dds").String())
}

func TestDeployRoot_Text(t *testing.T) {
	b, err := json.Marshal(struct {
		R domain.DeployRoot `json:"root"`
	}{domain.RootInstallPath})
	require.NoError(t, err)
	assert.JSONEq(t, `{"root":"install_path"}`, string(b))
	var r domain.DeployRoot
	require.NoError(t, r.UnmarshalText([]byte("mod_path")))
	assert.Equal(t, domain.RootModPath, r)
	assert.Error(t, r.UnmarshalText([]byte("modpath")), "an unknown root is refused, never read as mod_path")
	_, err = domain.DeployRoot(7).MarshalText()
	assert.Error(t, err)
}

func TestCompareDeployPaths_ModPathFirstThenByPath(t *testing.T) {
	ps := []domain.DeployPath{domain.GameRootFile("a"), domain.ModPathFile("z"), domain.ModPathFile("b"), domain.GameRootFile("0")}
	slices.SortFunc(ps, domain.CompareDeployPaths)
	assert.Equal(t, []domain.DeployPath{domain.ModPathFile("b"), domain.ModPathFile("z"), domain.GameRootFile("0"), domain.GameRootFile("a")}, ps)
}
