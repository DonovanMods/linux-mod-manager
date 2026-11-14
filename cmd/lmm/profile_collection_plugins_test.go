package main

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
)

// TestPrintCollectionPlan_SummarisesThePluginOrder (#611): a collection
// that carries a plugin list says so in its plan - how many plugins, which
// are inactive, the rules and their groups - and whether lmm applies it,
// as the web UI's plan does.
func TestPrintCollectionPlan_SummarisesThePluginOrder(t *testing.T) {
	c := &core.CollectionImport{Name: "Pack", Plugins: &core.CollectionPluginSummary{
		Plugins: 3, Active: 2, Inactive: []string{"Optional.esp"}, Rules: 4, Groups: []string{"Late Loaders", "default"}, Managed: true,
	}}
	out := captureStdout(t, func() error {
		printCollectionPlan(c, &domain.Game{ID: "fo4", Name: "Fallout 4"})
		return nil
	})
	assert.Contains(t, out, "Plugin order: 3 plugin(s), 2 active; 4 plugin rule(s), groups: Late Loaders, default")
	assert.Contains(t, out, "Inactive: Optional.esp")
	assert.Contains(t, out, "written to Fallout 4's plugin list at every deploy")

	c.Plugins = &core.CollectionPluginSummary{Plugins: 1, Active: 1}
	out = captureStdout(t, func() error {
		printCollectionPlan(c, &domain.Game{ID: "cp", Name: "Cyberpunk 2077"})
		return nil
	})
	assert.Contains(t, out, "Plugin order: 1 plugin(s), 1 active\n")
	assert.NotContains(t, out, "Inactive:")
	assert.Contains(t, out, "Recorded on the profile only: lmm does not manage Cyberpunk 2077's plugin list")

	c.Plugins = nil
	out = captureStdout(t, func() error {
		printCollectionPlan(c, &domain.Game{ID: "cp", Name: "Cyberpunk 2077"})
		return nil
	})
	assert.NotContains(t, out, "Plugin order")
}
