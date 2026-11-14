package core

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/stretchr/testify/assert"
)

func TestMergePlugins_OnOffAndRulesConflicts(t *testing.T) {
	on := func(n string) domain.PluginEntry { return domain.PluginEntry{Name: n, Enabled: true} }
	off := func(n string) domain.PluginEntry { return domain.PluginEntry{Name: n} }
	rules := func(after string) domain.PluginRules {
		return domain.PluginRules{Plugins: []domain.PluginRule{{Name: "A.esp", After: []string{after}}}}
	}
	base := domain.ProfilePlugins{Order: []domain.PluginEntry{on("A.esp")}, Rules: rules("Base.esp")}
	// Both add X, in different states; both change the rules.
	mine := domain.ProfilePlugins{Order: []domain.PluginEntry{on("A.esp"), on("X.esp")}, Rules: rules("Mine.esp")}
	theirs := domain.ProfilePlugins{Order: []domain.PluginEntry{on("A.esp"), off("X.esp")}, Rules: rules("Theirs.esp")}

	none := func(string) bool { return false }
	m := mergePlugins(mine, base, theirs, true, none)
	assert.Equal(t, []string{"X.esp"}, m.enabled)
	assert.True(t, m.rules)
	assert.Equal(t, []domain.PluginEntry{on("A.esp"), on("X.esp")}, m.result.Order, "the user's side by default")
	assert.Equal(t, rules("Mine.esp"), m.result.Rules)

	all := func(string) bool { return true }
	m = mergePlugins(mine, base, theirs, true, all)
	assert.Equal(t, []domain.PluginEntry{on("A.esp"), off("X.esp")}, m.result.Order, "overridden")
	assert.Equal(t, rules("Theirs.esp"), m.result.Rules)

	// No baseline: an empty base, so every shared difference is a conflict
	// the user wins by default.
	m = mergePlugins(mine, domain.ProfilePlugins{}, theirs, false, none)
	assert.Equal(t, []string{"X.esp"}, m.enabled)
	assert.True(t, m.rules)
	assert.Equal(t, rules("Mine.esp"), m.result.Rules)
}
