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

// #634: a mod whose files each carry an installer records its choices per
// file, under fomod_files; a mod with one installer keeps fomod, and a ref
// with neither writes neither key.
func TestFomodFiles_PerFileRecordRoundTripsAndStaysApart(t *testing.T) {
	var steps []fomod.StepChoice
	require.NoError(t, json.Unmarshal([]byte(vortexChoices), &steps))
	ref := domain.ModReference{SourceID: "nexusmods", ModID: "1", FileIDs: []string{"10", "11"}, FomodFiles: domain.FomodFiles{
		"10": {ConfigSHA256: "ab", Steps: steps},
		"11": {ConfigSHA256: "cd", Steps: []fomod.StepChoice{{Name: "Patch", Groups: []fomod.GroupChoice{{Name: "Which", Choices: []fomod.OptionChoice{{Name: "USSEP"}}}}}}},
	}}
	text, err := yaml.Marshal(ref)
	require.NoError(t, err)
	assert.Contains(t, string(text), "fomod_files:\n    \"10\":\n")
	assert.NotContains(t, string(text), "fomod:", "the single form is not written beside it")
	var back domain.ModReference
	require.NoError(t, yaml.Unmarshal(text, &back))
	assert.True(t, ref.FomodFiles.Equal(back.FomodFiles), "%s", text)

	b, err := json.Marshal(ref)
	require.NoError(t, err)
	var jback domain.ModReference
	require.NoError(t, json.Unmarshal(b, &jback))
	assert.True(t, ref.FomodFiles.Equal(jback.FomodFiles), "%s", b)

	plain := domain.ModReference{SourceID: "s", ModID: "m", Fomod: domain.FomodChoices{ConfigSHA256: "ab"}}
	text, err = yaml.Marshal(plain)
	require.NoError(t, err)
	assert.NotContains(t, string(text), "fomod_files")
	b, err = json.Marshal(plain)
	require.NoError(t, err)
	assert.NotContains(t, string(b), "fomod_files")
}

func TestFomodRecord_SetKeepsOneForm(t *testing.T) {
	single := domain.FomodChoices{ConfigSHA256: "ab"}
	files := domain.FomodFiles{"1": {ConfigSHA256: "cd"}, "2": {ConfigSHA256: "ef"}}
	var ref domain.ModReference
	ref.SetFomodRecord(domain.FomodRecord{Files: files})
	assert.True(t, ref.Fomod.IsZero())
	assert.True(t, files.Equal(ref.FomodFiles))
	files["1"] = domain.FomodChoices{ConfigSHA256: "zz"}
	assert.Equal(t, "cd", ref.FomodFiles["1"].ConfigSHA256, "the record is a copy")

	ref.SetFomodRecord(domain.FomodRecord{Choices: single})
	assert.Nil(t, ref.FomodFiles)
	assert.True(t, single.Equal(ref.Fomod))
	assert.True(t, ref.FomodRecord().Equal(domain.FomodRecord{Choices: single}))

	ref.SetFomodRecord(domain.FomodRecord{})
	assert.True(t, ref.FomodRecord().IsZero())
}

func TestFomodFiles_EqualAndZero(t *testing.T) {
	assert.True(t, domain.FomodFiles(nil).IsZero())
	assert.True(t, domain.FomodFiles{"1": {}}.IsZero(), "a file with no choices records nothing")
	assert.True(t, domain.FomodFiles{"1": {ConfigSHA256: "ab"}, "2": {}}.Equal(domain.FomodFiles{"1": {ConfigSHA256: "ab"}}))
	assert.False(t, domain.FomodFiles{"1": {ConfigSHA256: "ab"}}.Equal(domain.FomodFiles{"2": {ConfigSHA256: "ab"}}))
	assert.False(t, domain.FomodRecord{Files: domain.FomodFiles{"1": {ConfigSHA256: "ab"}}}.Equal(domain.FomodRecord{Choices: domain.FomodChoices{ConfigSHA256: "ab"}}))
	assert.Equal(t, "nexusmods:12604#37471", domain.FomodFileKey("nexusmods", "12604", "37471"))
}
