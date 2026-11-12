package fomod

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// vortexChoices is the shape of a collection.json mod entry's
// choices.options, with the irregularities real manifests carry: a step
// named "", a choice name with a leading space, a select-any group where
// nothing was picked (choices: []), and a non-zero idx.
const vortexChoices = `[{"name":"","groups":[{"name":"Options","choices":[{"name":" Spicy Clean Hacking Screen","idx":2}]},{"name":"Extras","choices":[]}]},{"name":"Main","groups":[{"name":"Textures","choices":[{"name":"2K","idx":0}]}]}]`

func TestStepChoices_VortexJSONRoundTrips(t *testing.T) {
	var steps []StepChoice
	require.NoError(t, json.Unmarshal([]byte(vortexChoices), &steps))
	require.Len(t, steps, 2)
	assert.Equal(t, "", steps[0].Name)
	assert.Equal(t, " Spicy Clean Hacking Screen", steps[0].Groups[0].Choices[0].Name)
	assert.Equal(t, 2, steps[0].Groups[0].Choices[0].Idx)
	assert.NotNil(t, steps[0].Groups[1].Choices, "an empty pick list is an answer")
	assert.Empty(t, steps[0].Groups[1].Choices)
	assert.Nil(t, steps[0].Index)

	out, err := json.Marshal(steps)
	require.NoError(t, err)
	assert.JSONEq(t, vortexChoices, string(out))
	assert.Equal(t, vortexChoices, string(out), "byte-identical: no lmm-only keys appear when unset")
}

func TestStepChoices_IndexRoundTrips(t *testing.T) {
	in := []StepChoice{{Name: "S", Index: intp(1), Groups: []GroupChoice{{Name: "G", Index: intp(0), Choices: []OptionChoice{{Name: "Dup", Idx: 3, Index: intp(1)}}}}}}
	out, err := json.Marshal(in)
	require.NoError(t, err)
	assert.Equal(t, `[{"name":"S","index":1,"groups":[{"name":"G","index":0,"choices":[{"name":"Dup","idx":3,"index":1}]}]}]`, string(out))
	var back []StepChoice
	require.NoError(t, json.Unmarshal(out, &back))
	assert.Equal(t, in, back)
}

func TestStepChoices_ReplayVortexSample(t *testing.T) {
	var steps []StepChoice
	require.NoError(t, json.Unmarshal([]byte(vortexChoices), &steps))
	cfg := mustParse(t, `<config><installSteps order="Explicit">
		<installStep name=""><optionalFileGroups order="Explicit">
			<group name="Options" type="SelectAny"><plugins order="Explicit">`+plugin("Plain", "Recommended")+plugin("Other", "Optional")+plugin(" Spicy Clean Hacking Screen", "Optional")+`</plugins></group>
			<group name="Extras" type="SelectAny"><plugins>`+plugin("Extra", "Recommended")+`</plugins></group>
		</optionalFileGroups></installStep>
		<installStep name="Main"><optionalFileGroups><group name="Textures" type="SelectExactlyOne"><plugins order="Explicit">`+plugin("1K", "Optional")+plugin("2K", "Optional")+`</plugins></group></optionalFileGroups></installStep>
	</installSteps></config>`)
	res := evaluate(t, cfg, Request{Mode: ModeReplay, Choices: steps})
	assert.Equal(t, []string{"/Options/ Spicy Clean Hacking Screen", "Main/Textures/2K"}, selected(res))
	assert.Empty(t, res.Unmatched)
	assert.Empty(t, res.Unanswered)
}
