package domain_test

import (
	"encoding/json/v2"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// vortexChoices is a collection.json `choices.options` block as Vortex
// writes it - a leading-space name, a step named "", a group answered with
// nothing - which a profile must carry through unchanged (#614).
const vortexChoices = `[{"name":"","groups":[{"name":" Spicy Clean Hacking Screen","choices":[{"name":"Yes","idx":0}]},{"name":"Extras","choices":[]}]}]`

func TestFomodChoices_VortexBlockRoundTripsThroughTheProfile(t *testing.T) {
	var steps []fomod.StepChoice
	require.NoError(t, json.Unmarshal([]byte(vortexChoices), &steps))
	ref := domain.ModReference{SourceID: "nexusmods", ModID: "1", Fomod: domain.FomodChoices{ConfigSHA256: "ab", Steps: steps}}

	text, err := yaml.Marshal(ref)
	require.NoError(t, err)
	var back domain.ModReference
	require.NoError(t, yaml.Unmarshal(text, &back))
	assert.True(t, ref.Fomod.Equal(back.Fomod), "the YAML profile form keeps every answer:\n%s", text)

	again, err := json.Marshal(back.Fomod.Steps)
	require.NoError(t, err)
	assert.JSONEq(t, vortexChoices, string(again), "re-encoded, it is Vortex's block again")
	assert.NotContains(t, string(text), "index", "an unset index is never written")
}

func TestFomodChoices_ZeroIsOmitted(t *testing.T) {
	ref := domain.ModReference{SourceID: "s", ModID: "m", Version: "1"}
	text, err := yaml.Marshal(ref)
	require.NoError(t, err)
	assert.NotContains(t, string(text), "fomod")
	b, err := json.Marshal(ref)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "fomod")

	assert.True(t, domain.FomodChoices{}.IsZero())
	assert.True(t, domain.FomodChoices{Steps: []fomod.StepChoice{}}.IsZero())
	assert.False(t, domain.FomodChoices{ConfigSHA256: "ab"}.IsZero(), "an installer with no steps still records its config")
}

func TestFomodChoices_Equal(t *testing.T) {
	one, two := 1, 2
	base := domain.FomodChoices{ConfigSHA256: "ab", Steps: []fomod.StepChoice{{Name: "s", Groups: []fomod.GroupChoice{{Name: "g", Choices: []fomod.OptionChoice{{Name: "o", Index: &one}}}}}}}
	same := domain.FomodChoices{ConfigSHA256: "ab", Steps: []fomod.StepChoice{{Name: "s", Groups: []fomod.GroupChoice{{Name: "g", Choices: []fomod.OptionChoice{{Name: "o", Index: &one}}}}}}}
	assert.True(t, base.Equal(same))

	for name, change := range map[string]func(c *domain.FomodChoices){
		"config":       func(c *domain.FomodChoices) { c.ConfigSHA256 = "cd" },
		"step name":    func(c *domain.FomodChoices) { c.Steps[0].Name = "t" },
		"option index": func(c *domain.FomodChoices) { c.Steps[0].Groups[0].Choices[0].Index = &two },
		"no index":     func(c *domain.FomodChoices) { c.Steps[0].Groups[0].Choices[0].Index = nil },
		"idx":          func(c *domain.FomodChoices) { c.Steps[0].Groups[0].Choices[0].Idx = 3 },
		"no pick":      func(c *domain.FomodChoices) { c.Steps[0].Groups[0].Choices = nil },
	} {
		other := domain.FomodChoices{ConfigSHA256: "ab", Steps: []fomod.StepChoice{{Name: "s", Groups: []fomod.GroupChoice{{Name: "g", Choices: []fomod.OptionChoice{{Name: "o", Index: &one}}}}}}}
		change(&other)
		assert.False(t, base.Equal(other), name)
	}

	assert.True(t, domain.FomodChoices{ConfigSHA256: "ab"}.Equal(domain.FomodChoices{ConfigSHA256: "ab", Steps: []fomod.StepChoice{}}),
		"nil and empty are one record")
}
