package fomod

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fileStates is a FileStates over a fixed map; absent paths are Missing.
type fileStates map[string]FileState

func (f fileStates) State(path string) FileState { return f[path] }

func intp(i int) *int { return &i }

// selected lists "step/group/option" for every selected option, in walk order.
func selected(res *Result) []string {
	var out []string
	for _, s := range res.Steps {
		for _, g := range s.Groups {
			for _, o := range g.Options {
				if o.Selected {
					out = append(out, s.Name+"/"+g.Name+"/"+o.Name)
				}
			}
		}
	}
	return out
}

func optionType(t *testing.T, res *Result, step, group, option string) PluginType {
	t.Helper()
	for _, s := range res.Steps {
		for _, g := range s.Groups {
			for _, o := range g.Options {
				if s.Name == step && g.Name == group && o.Name == option {
					return o.Type
				}
			}
		}
	}
	t.Fatalf("no option %s/%s/%s in result", step, group, option)
	return 0
}

func evaluate(t *testing.T, cfg *Config, req Request) *Result {
	t.Helper()
	res, err := Evaluate(cfg, req)
	require.NoError(t, err)
	return res
}

// plugin renders one <plugin> with a static type and optional flags.
func plugin(name, typ string, flags ...string) string {
	var b strings.Builder
	b.WriteString(`<plugin name="` + name + `"><description/>`)
	if len(flags) > 0 {
		b.WriteString(`<conditionFlags>`)
		for i := 0; i+1 < len(flags); i += 2 {
			b.WriteString(`<flag name="` + flags[i] + `">` + flags[i+1] + `</flag>`)
		}
		b.WriteString(`</conditionFlags>`)
	}
	b.WriteString(`<typeDescriptor><type name="` + typ + `"/></typeDescriptor></plugin>`)
	return b.String()
}

// oneGroup renders a config with one step holding one group.
func oneGroup(groupType string, plugins ...string) string {
	return `<config><installSteps order="Explicit"><installStep name="S"><optionalFileGroups order="Explicit">
		<group name="G" type="` + groupType + `"><plugins order="Explicit">` + strings.Join(plugins, "") + `</plugins></group>
	</optionalFileGroups></installStep></installSteps></config>`
}

func TestEvaluate_DefaultsPreselection(t *testing.T) {
	tests := []struct {
		name string
		doc  string
		want []string
	}{
		{"exactly-one takes the first", oneGroup("SelectExactlyOne", plugin("a", "Optional"), plugin("b", "Optional")), []string{"S/G/a"}},
		{"exactly-one takes the recommended", oneGroup("SelectExactlyOne", plugin("a", "Optional"), plugin("b", "Recommended")), []string{"S/G/b"}},
		{
			// Several recommended in a one-pick group: each clears the group
			// before selecting itself, so the last one stands.
			"at-most-one with two recommended keeps the last",
			oneGroup("SelectAtMostOne", plugin("a", "Recommended"), plugin("b", "Recommended"), plugin("c", "Optional")),
			[]string{"S/G/b"},
		},
		{"any selects recommended and required only", oneGroup("SelectAny", plugin("a", "Optional"), plugin("b", "Recommended"), plugin("c", "Required")), []string{"S/G/b", "S/G/c"}},
		{"at-least-one selects nothing optional", oneGroup("SelectAtLeastOne", plugin("a", "Optional"), plugin("b", "CouldBeUsable")), nil},
		{"select-all selects everything usable", oneGroup("SelectAll", plugin("a", "Optional"), plugin("b", "NotUsable"), plugin("c", "Optional")), []string{"S/G/a", "S/G/c"}},
		{"not-usable first of exactly-one stays off", oneGroup("SelectExactlyOne", plugin("a", "NotUsable"), plugin("b", "Optional")), nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := evaluate(t, mustParse(t, tt.doc), Request{Mode: ModeDefaults})
			assert.Equal(t, tt.want, selected(res))
		})
	}
}

func TestEvaluate_FixStepsDowngradesGroupsWithSeveralRequired(t *testing.T) {
	res := evaluate(t, mustParse(t, oneGroup("SelectExactlyOne", plugin("a", "Required"), plugin("b", "Required"), plugin("c", "Optional"))), Request{Mode: ModeDefaults})
	assert.Equal(t, SelectAtLeastOne, res.Steps[0].Groups[0].Type)
	assert.Equal(t, []string{"S/G/a", "S/G/b"}, selected(res))
	assert.Empty(t, res.Violations)

	res = evaluate(t, mustParse(t, oneGroup("SelectAtMostOne", plugin("a", "Required"), plugin("b", "Required"))), Request{Mode: ModeDefaults})
	assert.Equal(t, SelectAny, res.Steps[0].Groups[0].Type)
}

func TestEvaluate_DependencyTypeFirstMatchingPatternWins(t *testing.T) {
	cfg, err := Parse(readFixture(t, "full.xml"))
	require.NoError(t, err)
	base := fileStates{"skyrim.esm": FileActive}
	tests := []struct {
		name   string
		states fileStates
		want   PluginType
	}{
		{"active", fileStates{"skyrim.esm": FileActive, "dawnguard.esm": FileActive}, Recommended},
		{"inactive", fileStates{"skyrim.esm": FileActive, "dawnguard.esm": FileInactive}, CouldBeUsable},
		{"missing falls to the default", base, NotUsable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := evaluate(t, cfg, Request{Mode: ModeDefaults, Files: tt.states})
			assert.Equal(t, tt.want, optionType(t, res, "Main", "Patches", "Dawnguard Patch"))
		})
	}
}

func TestEvaluate_Prerequisites(t *testing.T) {
	cfg, err := Parse(readFixture(t, "full.xml"))
	require.NoError(t, err)

	// No FileStates: every file is Missing, so "Skyrim.esm Active" fails.
	_, err = Evaluate(cfg, Request{Mode: ModeDefaults})
	var pe *PrerequisitesError
	require.ErrorAs(t, err, &pe)
	assert.Contains(t, pe.Error(), "skyrim.esm")

	_, err = Evaluate(cfg, Request{Mode: ModeDefaults, Files: fileStates{"skyrim.esm": FileActive}})
	assert.NoError(t, err)
}

func TestEvaluate_FlagsDriveVisibility(t *testing.T) {
	cfg, err := Parse(readFixture(t, "full.xml"))
	require.NoError(t, err)
	files := fileStates{"skyrim.esm": FileActive}

	// Defaults: the lighting patch is Optional, so the gated step stays hidden.
	res := evaluate(t, cfg, Request{Mode: ModeDefaults, Files: files})
	require.Len(t, res.Steps, 1)
	assert.Equal(t, []string{"Main/Texture Resolution/2K"}, selected(res))

	// Picking it sets lighting=On and the next step appears.
	res = evaluate(t, cfg, Request{Mode: ModeReplay, Files: files, Choices: []StepChoice{
		{Name: "Main", Groups: []GroupChoice{
			{Name: "Texture Resolution", Choices: []OptionChoice{{Name: "4K"}}},
			{Name: "Patches", Choices: []OptionChoice{{Name: "Lighting Patch", Idx: 1}}},
		}},
		{Name: "Lighting Options", Groups: []GroupChoice{
			{Name: "Brightness", Choices: []OptionChoice{{Name: "Dark"}}},
		}},
	}})
	require.Len(t, res.Steps, 2)
	assert.Equal(t, []string{"Main/Texture Resolution/4K", "Main/Patches/Lighting Patch", "Lighting Options/Brightness/Dark"}, selected(res))
	assert.Empty(t, res.Unanswered)
	assert.Empty(t, res.Unmatched)
}

func TestEvaluate_FlagOwnership(t *testing.T) {
	// a sets f=A, then b takes f over with f=B. a turns NotUsable once f=B,
	// so the step's fix-up deselects it: that must clear only the flags a
	// still owns, leaving b's f=B (and the step it gates) in place.
	doc := `<config><installSteps order="Explicit">
		<installStep name="S"><optionalFileGroups order="Explicit">
			<group name="G1" type="SelectAny"><plugins>
				<plugin name="a"><description/><conditionFlags><flag name="f">A</flag></conditionFlags>
					<typeDescriptor><dependencyType><defaultType name="Recommended"/><patterns>
						<pattern><dependencies><flagDependency flag="f" value="B"/></dependencies><type name="NotUsable"/></pattern>
					</patterns></dependencyType></typeDescriptor></plugin>
			</plugins></group>
			<group name="G2" type="SelectAny"><plugins>` + plugin("b", "Recommended", "f", "B") + `</plugins></group>
		</optionalFileGroups></installStep>
		<installStep name="T"><visible><flagDependency flag="f" value="B"/></visible><optionalFileGroups>
			<group name="H" type="SelectAny"><plugins>` + plugin("x", "Optional") + `</plugins></group>
		</optionalFileGroups></installStep>
	</installSteps></config>`
	res := evaluate(t, mustParse(t, doc), Request{Mode: ModeDefaults})
	assert.Equal(t, []string{"S/G2/b"}, selected(res))
	require.Len(t, res.Steps, 2, "b still owns f=B")

	// An empty flag value matches an unset flag.
	doc2 := `<config><installSteps order="Explicit">
		<installStep name="S"><optionalFileGroups>
			<group name="G" type="SelectAny"><plugins>` + plugin("a", "Optional", "f", "A") + `</plugins></group>
		</optionalFileGroups></installStep>
		<installStep name="T"><visible><flagDependency flag="f" value=""/></visible><optionalFileGroups>
			<group name="H" type="SelectAny"><plugins>` + plugin("x", "Optional") + `</plugins></group>
		</optionalFileGroups></installStep>
	</installSteps></config>`
	cfg := mustParse(t, doc2)
	res = evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{
		{Name: "S", Groups: []GroupChoice{{Name: "G", Choices: []OptionChoice{}}}},
	}})
	assert.Len(t, res.Steps, 2, "f is unset, which matches value=\"\"")
	res = evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{
		{Name: "S", Groups: []GroupChoice{{Name: "G", Choices: []OptionChoice{{Name: "a"}}}}},
	}})
	assert.Len(t, res.Steps, 1, "f=A does not match value=\"\"")
}

func TestEvaluate_ReplayPresetRules(t *testing.T) {
	doc := `<config><installSteps order="Explicit"><installStep name="S"><optionalFileGroups order="Explicit">
		<group name="One" type="SelectExactlyOne"><plugins order="Explicit">` + plugin("a", "Optional") + plugin("b", "Recommended") + `</plugins></group>
		<group name="Any" type="SelectAny"><plugins order="Explicit">` + plugin("r", "Recommended") + plugin("q", "Required") + plugin("n", "NotUsable") + `</plugins></group>
		<group name="Empty" type="SelectAny"><plugins order="Explicit">` + plugin("x", "Recommended") + plugin("y", "Optional") + `</plugins></group>
	</optionalFileGroups></installStep></installSteps></config>`
	cfg := mustParse(t, doc)

	res := evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{{Name: "S", Groups: []GroupChoice{
		{Name: "Any", Choices: []OptionChoice{{Name: "n"}}},
		{Name: "Empty", Choices: []OptionChoice{}},
	}}}})
	// One is unanswered: replay ignores Recommended and takes the first.
	// Any: the recorded NotUsable option is forced selectable and kept;
	// Required stays; the unrecorded Recommended is off.
	// Empty: recorded as an empty pick, so nothing.
	assert.Equal(t, []string{"S/One/a", "S/Any/q", "S/Any/n"}, selected(res))
	assert.Equal(t, CouldBeUsable, optionType(t, res, "S", "Any", "n"))
	assert.Equal(t, []GroupRef{{Step: "S", Group: "One"}}, res.Unanswered)

	// The same unanswered group under ModeDefaults takes the recommended.
	res = evaluate(t, cfg, Request{Mode: ModeDefaults, Choices: []StepChoice{{Name: "S", Groups: []GroupChoice{
		{Name: "Any", Choices: []OptionChoice{}},
		{Name: "Empty", Choices: []OptionChoice{}},
	}}}})
	assert.Equal(t, []string{"S/One/b", "S/Any/q"}, selected(res))
}

func TestEvaluate_UnansweredSkipsGroupsWithNoFreedom(t *testing.T) {
	doc := `<config><installSteps order="Explicit"><installStep name="S"><optionalFileGroups order="Explicit">
		<group name="All" type="SelectAll"><plugins>` + plugin("a", "Optional") + `</plugins></group>
		<group name="Forced" type="SelectAny"><plugins>` + plugin("b", "Required") + plugin("c", "NotUsable") + `</plugins></group>
		<group name="Free" type="SelectAny"><plugins>` + plugin("d", "Optional") + `</plugins></group>
	</optionalFileGroups></installStep></installSteps></config>`
	res := evaluate(t, mustParse(t, doc), Request{Mode: ModeReplay})
	assert.Equal(t, []GroupRef{{Step: "S", Group: "Free"}}, res.Unanswered)
	assert.False(t, res.Steps[0].Groups[0].Answered)
}

func TestEvaluate_ChoiceMatching(t *testing.T) {
	doc := `<config><installSteps order="Explicit">
		<installStep name=""><optionalFileGroups order="Explicit">
			<group name="G" type="SelectAny"><plugins order="Explicit">` +
		plugin(" Spicy Clean Hacking Screen", "Optional") + plugin("Spicy Clean Hacking Screen", "Optional") +
		plugin("Dup", "Optional") + plugin("Dup", "Optional") + `
			</plugins></group>
			<group name="G" type="SelectAny"><plugins>` + plugin("z", "Optional") + `</plugins></group>
		</optionalFileGroups></installStep>
		<installStep name="Hidden"><visible><flagDependency flag="never" value="1"/></visible><optionalFileGroups>
			<group name="H" type="SelectAny"><plugins>` + plugin("h", "Optional") + `</plugins></group>
		</optionalFileGroups></installStep>
	</installSteps></config>`
	cfg := mustParse(t, doc)

	t.Run("names are matched exactly, never trimmed", func(t *testing.T) {
		res := evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{{Name: "", Groups: []GroupChoice{
			{Name: "G", Choices: []OptionChoice{{Name: " Spicy Clean Hacking Screen", Idx: 3}}},
		}}}})
		assert.Equal(t, []string{"/G/ Spicy Clean Hacking Screen"}, selected(res))
	})

	t.Run("a name alone selects every same-named sibling, as Vortex does", func(t *testing.T) {
		res := evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{{Name: "", Groups: []GroupChoice{
			{Name: "G", Choices: []OptionChoice{{Name: "Dup"}}},
		}}}})
		// Both groups named G are answered by the one recorded group.
		assert.Equal(t, []string{"/G/Dup", "/G/Dup"}, selected(res))
		assert.Empty(t, res.Unanswered)
	})

	t.Run("an index picks one same-named sibling", func(t *testing.T) {
		res := evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{{Name: "", Groups: []GroupChoice{
			{Name: "G", Index: intp(0), Choices: []OptionChoice{{Name: "Dup", Index: intp(1)}}},
			{Name: "G", Index: intp(1), Choices: []OptionChoice{}},
		}}}})
		require.Len(t, res.Steps[0].Groups, 2)
		opts := res.Steps[0].Groups[0].Options
		assert.False(t, opts[2].Selected)
		assert.True(t, opts[3].Selected)
		assert.Empty(t, res.Unanswered)
	})

	t.Run("unmatched choices are reported", func(t *testing.T) {
		res := evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{
			{Name: "", Groups: []GroupChoice{
				{Name: "G", Choices: []OptionChoice{{Name: "Spicy Clean Hacking Screen "}, {Name: "Dup", Index: intp(2)}}},
				{Name: "Gone", Choices: []OptionChoice{{Name: "x"}}},
				{Name: "G", Index: intp(5), Choices: []OptionChoice{}},
			}},
			{Name: "Removed Step", Groups: []GroupChoice{{Name: "G"}}},
			{Name: "Hidden", Groups: []GroupChoice{{Name: "H", Choices: []OptionChoice{{Name: "h"}}}}},
		}})
		assert.Equal(t, []Unmatched{
			{Step: "", Group: "G", Option: "Spicy Clean Hacking Screen ", Reason: NoSuchOption},
			{Step: "", Group: "G", Option: "Dup", OptionIndex: intp(2), Reason: NoSuchOption},
			{Step: "", Group: "Gone", Reason: NoSuchGroup},
			{Step: "", Group: "G", GroupIndex: intp(5), Reason: NoSuchGroup},
			{Step: "Removed Step", Reason: NoSuchStep},
			{Step: "Hidden", Reason: StepNotVisible},
		}, res.Unmatched)
	})
}

func TestEvaluate_CanonicalChoicesRoundTrip(t *testing.T) {
	cfg, err := Parse(readFixture(t, "full.xml"))
	require.NoError(t, err)
	files := fileStates{"skyrim.esm": FileActive, "dawnguard.esm": FileActive}
	first := evaluate(t, cfg, Request{Mode: ModeDefaults, Files: files, Choices: []StepChoice{
		{Name: "Main", Groups: []GroupChoice{{Name: "Patches", Choices: []OptionChoice{{Name: "Lighting Patch"}}}}},
	}})
	require.Len(t, first.Choices, 2)
	assert.Equal(t, StepChoice{Name: "Main", Groups: []GroupChoice{
		{Name: "Texture Resolution", Choices: []OptionChoice{{Name: "2K", Idx: 0}}},
		{Name: "Patches", Choices: []OptionChoice{{Name: "Lighting Patch", Idx: 1}}},
	}}, first.Choices[0])
	assert.Equal(t, StepChoice{Name: "Lighting Options", Groups: []GroupChoice{
		{Name: "Brightness", Choices: []OptionChoice{{Name: "Dark", Idx: 0}}},
	}}, first.Choices[1])

	again := evaluate(t, cfg, Request{Mode: ModeReplay, Files: files, Choices: first.Choices})
	assert.Equal(t, first.Ops, again.Ops)
	assert.Equal(t, selected(first), selected(again))
	assert.Empty(t, again.Unanswered)
	assert.Empty(t, again.Unmatched)
}

func TestEvaluate_CanonicalChoicesCarryIndexOnlyForDuplicates(t *testing.T) {
	doc := `<config><installSteps order="Explicit"><installStep name="S"><optionalFileGroups order="Explicit">
		<group name="G" type="SelectAny"><plugins order="Explicit">` + plugin("Dup", "Optional") + plugin("Dup", "Optional") + plugin("U", "Optional") + `</plugins></group>
	</optionalFileGroups></installStep></installSteps></config>`
	res := evaluate(t, mustParse(t, doc), Request{Mode: ModeReplay, Choices: []StepChoice{{Name: "S", Groups: []GroupChoice{
		{Name: "G", Choices: []OptionChoice{{Name: "Dup", Index: intp(1)}, {Name: "U"}}},
	}}}})
	assert.Equal(t, []StepChoice{{Name: "S", Groups: []GroupChoice{{Name: "G", Choices: []OptionChoice{
		{Name: "Dup", Idx: 1, Index: intp(1)},
		{Name: "U", Idx: 2},
	}}}}}, res.Choices)
}

func TestEvaluate_Violations(t *testing.T) {
	cfg := mustParse(t, oneGroup("SelectExactlyOne", plugin("a", "Optional"), plugin("b", "Optional")))
	res := evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{{Name: "S", Groups: []GroupChoice{
		{Name: "G", Choices: []OptionChoice{{Name: "a"}, {Name: "b"}}},
	}}}})
	assert.Equal(t, []Violation{{Step: "S", Group: "G", Type: SelectExactlyOne, Selected: 2}}, res.Violations)

	cfg = mustParse(t, oneGroup("SelectAtLeastOne", plugin("a", "Optional")))
	res = evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{{Name: "S", Groups: []GroupChoice{{Name: "G"}}}}})
	assert.Equal(t, []Violation{{Step: "S", Group: "G", Type: SelectAtLeastOne, Selected: 0}}, res.Violations)
}

func TestEvaluate_UncheckedDependenciesWarn(t *testing.T) {
	doc := `<config><moduleDependencies>
		<gameDependency version="1.9.32"/>
		<skseDependency version="2.2.3"/>
		<gameDependency version="1.9.32"/>
		<futureDependency/>
	</moduleDependencies></config>`
	res := evaluate(t, mustParse(t, doc), Request{Mode: ModeReplay})
	assert.Equal(t, []string{
		"gameDependency (version 1.9.32) is not checked: treated as satisfied",
		"skseDependency (version 2.2.3) is not checked: treated as satisfied",
		"futureDependency is not understood: treated as satisfied",
	}, res.Warnings)
}

func TestEvaluate_CompositeOperators(t *testing.T) {
	tests := []struct {
		name string
		cond Condition
		want bool
	}{
		{"empty and", Condition{Kind: ConditionComposite, Operator: OperatorAnd}, true},
		{"empty or", Condition{Kind: ConditionComposite, Operator: OperatorOr}, false},
		{"or with one true", Condition{Kind: ConditionComposite, Operator: OperatorOr, Conditions: []Condition{
			{Kind: ConditionFlag, Flag: "x", Value: "1"}, {Kind: ConditionFile, File: "a.esp", State: FileMissing},
		}}, true},
		{"and with one false", Condition{Kind: ConditionComposite, Operator: OperatorAnd, Conditions: []Condition{
			{Kind: ConditionFlag, Flag: "x", Value: "1"}, {Kind: ConditionFile, File: "a.esp", State: FileMissing},
		}}, false},
		{"flag values are case-sensitive", Condition{Kind: ConditionFlag, Flag: "y", Value: "on"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := &evaluator{flags: map[string]flagValue{"y": {value: "On"}}, warned: map[warnKey]bool{}}
			assert.Equal(t, tt.want, e.test(&tt.cond))
		})
	}
}

func TestEvaluate_NilConfig(t *testing.T) {
	_, err := Evaluate(nil, Request{})
	assert.True(t, errors.Is(err, ErrNotModuleConfig))
}

func TestEvaluate_LargeGroupsStayLinear(t *testing.T) {
	// A hostile config near the size cap: one exactly-one group of tens of
	// thousands of Recommended options, each setting its own flag, with every
	// option recorded as picked (6 elements each, so near MaxNodes). A
	// quadratic walk (each Recommended clearing the whole group, each
	// deselect scanning every flag, each option scanning every pick) would
	// take minutes; a linear one, milliseconds.
	const n = 16000
	var b strings.Builder
	b.WriteString(`<config><installSteps><installStep name="S"><optionalFileGroups><group name="G" type="SelectExactlyOne"><plugins order="Explicit">`)
	picks := make([]OptionChoice, 0, n)
	for i := 0; i < n; i++ {
		name := "p" + itoa(i)
		b.WriteString(plugin(name, "Recommended", "f"+itoa(i), "1"))
		picks = append(picks, OptionChoice{Name: name})
	}
	b.WriteString(`</plugins></group></optionalFileGroups></installStep></installSteps></config>`)
	cfg := mustParse(t, b.String())

	withinBudget(t, 10*time.Second, func() {
		res := evaluate(t, cfg, Request{Mode: ModeDefaults})
		assert.Equal(t, []string{"S/G/p" + itoa(n-1)}, selected(res))

		// MaxChoices caps a recording; replay the largest one allowed.
		res = evaluate(t, cfg, Request{Mode: ModeReplay, Choices: []StepChoice{{Name: "S", Groups: []GroupChoice{{Name: "G", Choices: picks[:MaxChoices-2]}}}}})
		assert.Len(t, selected(res), MaxChoices-2)
		assert.Empty(t, res.Unmatched)
	})
}
