package fomod

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withinBudget runs f and fails the test if it has not returned within d.
// A regression to the old super-linear code fails here in seconds instead
// of hanging CI (the goroutine is abandoned).
func withinBudget(t *testing.T, d time.Duration, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("did not finish within %v", d)
	}
}

// Review item 1: an installIfUsable file list over a large dependency
// pattern re-evaluated the pattern once per file (60k x 60k in the repro).
func TestEvaluate_TypeResolutionIsMemoisedPerCollect(t *testing.T) {
	const n = 40000
	deps := make([]Condition, n)
	for i := range deps {
		deps[i] = Condition{Kind: ConditionVersion, Element: "gameDependency", Version: "1"}
	}
	files := make([]FileItem, n)
	for i := range files {
		files[i] = FileItem{Source: "f" + itoa(i), InstallIfUsable: true}
	}
	cfg := &Config{Steps: []Step{{Name: "S", Groups: []Group{{Name: "G", Type: SelectAny, Plugins: []Plugin{{
		Name:  "p",
		Files: files,
		Type: TypeDescriptor{Default: Optional, Patterns: []TypePattern{{
			When: Condition{Kind: ConditionComposite, Operator: OperatorAnd, Conditions: deps},
			Type: Optional,
		}}},
	}}}}}}}
	var res *Result
	withinBudget(t, 10*time.Second, func() {
		var err error
		res, err = Evaluate(cfg, Request{Mode: ModeReplay, Choices: []StepChoice{{Name: "S", Groups: []GroupChoice{{Name: "G"}}}}})
		require.NoError(t, err)
	})
	assert.Equal(t, []string{"gameDependency (version 1) is not checked: treated as satisfied"}, res.Warnings)
	assert.Len(t, res.MissingSources, n, "every installIfUsable file was considered")
}

func TestEvaluate_CompositesShortCircuit(t *testing.T) {
	e := &evaluator{flags: map[string]flagValue{}, warned: map[warnKey]bool{}}
	and := Condition{Kind: ConditionComposite, Operator: OperatorAnd, Conditions: []Condition{
		{Kind: ConditionFlag, Flag: "x", Value: "1"}, // false
		{Kind: ConditionVersion, Element: "gameDependency", Version: "1"},
	}}
	assert.False(t, e.test(&and))
	or := Condition{Kind: ConditionComposite, Operator: OperatorOr, Conditions: []Condition{
		{Kind: ConditionFlag, Flag: "x", Value: ""}, // true: unset
		{Kind: ConditionVersion, Element: "skseDependency", Version: "2"},
	}}
	assert.True(t, e.test(&or))
	assert.Empty(t, e.warnings, "neither second operand was reached")
}

func TestEvaluate_ConditionBudget(t *testing.T) {
	e := &evaluator{flags: map[string]flagValue{}, warned: map[warnKey]bool{}, limit: 3}
	c := Condition{Kind: ConditionComposite, Operator: OperatorAnd, Conditions: []Condition{
		{Kind: ConditionFlag, Flag: "a"}, {Kind: ConditionFlag, Flag: "b"}, {Kind: ConditionFlag, Flag: "c"},
	}}
	e.test(&c)
	assert.ErrorIs(t, e.err, ErrTooLarge)
}

// Review item 2: node count was uncapped.
func TestParse_NodeCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("<config><moduleDependencies>")
	for b.Len() < MaxConfigSize-64 {
		b.WriteString("<a/>")
	}
	b.WriteString("</moduleDependencies></config>")
	withinBudget(t, 10*time.Second, func() {
		_, err := Parse([]byte(b.String()))
		assert.ErrorIs(t, err, ErrTooLarge)
	})

	// Just under the cap still parses.
	ok := "<config><moduleDependencies>" + strings.Repeat("<a/>", MaxNodes-2) + "</moduleDependencies></config>"
	_, err := Parse([]byte(ok))
	assert.NoError(t, err)
	over := "<config><moduleDependencies>" + strings.Repeat("<a/>", MaxNodes-1) + "</moduleDependencies></config>"
	_, err = Parse([]byte(over))
	assert.ErrorIs(t, err, ErrTooLarge)
}

// Review item 3: Op.Source is the member exactly as given.
func TestResolve_SourceKeepsTheMemberSpelling(t *testing.T) {
	cfg := mustParse(t, `<config><requiredInstallFiles><file source="Data\A.esp" destination="A.esp"/><folder source="tex" destination="t"/></requiredInstallFiles></config>`)
	res := evaluate(t, cfg, Request{Members: []string{`.\Data\A.esp`, `tex\\b.dds`}})
	assert.Equal(t, map[string]string{"A.esp": `.\Data\A.esp`, "t/b.dds": `tex\\b.dds`}, copies(res))
}

// Review items 4 and 5: unsafe members are dropped and reported; one
// climbing member does not fail the evaluation.
func TestResolve_UnsafeMembersAreDroppedAndReported(t *testing.T) {
	cfg := mustParse(t, `<config><requiredInstallFiles><folder source="" destination=""/><file source="x\..\..\etc\passwd" destination="p"/></requiredInstallFiles></config>`)
	members := []string{"good.esp", "x/../../etc/passwd", "/abs/path", `\abs2`, "C:/Windows/x.dll", "nul\x00name", "ok/dir/f.txt"}
	res, err := Evaluate(cfg, Request{Members: members})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"good.esp": "good.esp", "ok/dir/f.txt": "ok/dir/f.txt"}, copies(res))
	assert.Equal(t, []string{"x/../../etc/passwd", "/abs/path", `\abs2`, "C:/Windows/x.dll", "nul\x00name"}, res.UnsafeMembers)
	assert.Equal(t, []MissingSource{{Source: `x\..\..\etc\passwd`, Reason: SourceNotFound}}, res.MissingSources)
}

// Review item 6: drive letters, UNC prefixes and NULs in destinations.
func TestCleanDest_RefusesWindowsAbsoluteAndNUL(t *testing.T) {
	for _, d := range []string{`C:\Windows\x.dll`, "c:x", `\\server\share\x`, "//server/share", "Data/a\x00b", `D:`} {
		_, err := cleanDest(d)
		assert.ErrorIs(t, err, ErrUnsafePath, "%q", d)
	}
	for d, want := range map[string]string{`\Data\x`: "Data/x", "Data/C:x": "Data/C:x", "a/../b": "b"} {
		got, err := cleanDest(d)
		require.NoError(t, err, d)
		assert.Equal(t, want, got)
	}
}

// Review item 7: a file op and an op under it (or a directory op on the
// same path) cannot both be performed.
func TestResolve_StructuralConflicts(t *testing.T) {
	tests := []struct {
		name      string
		items     string
		wantDests []string
	}{
		{
			"a file beats the lower-priority files under it",
			`<file source="a.txt" destination="a" priority="5"/><file source="b.txt" destination="a/b"/><file source="c.txt" destination="A/b/c"/>`,
			[]string{"a"},
		},
		{
			"files under a path beat a lower-priority file there",
			`<file source="a.txt" destination="a"/><file source="b.txt" destination="a/b" priority="5"/>`,
			[]string{"a/b"},
		},
		{
			"a tie goes to the larger source path",
			`<file source="z.txt" destination="a"/><file source="b.txt" destination="a/b"/>`,
			[]string{"a"},
		},
		{
			"a directory op and a file op on one path",
			`<file source="b.txt" destination="a\"/><file source="c.txt" destination="a" priority="1"/>`,
			[]string{"a"},
		},
		{
			"a directory op beats a lower-priority file",
			`<file source="b.txt" destination="a\" priority="3"/><file source="c.txt" destination="a" priority="1"/>`,
			[]string{"a"},
		},
		{
			"a directory with files under it is not a conflict",
			`<file source="b.txt" destination="a\"/><file source="c.txt" destination="a/c"/>`,
			[]string{"a", "a/c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, `<config><requiredInstallFiles>`+tt.items+`</requiredInstallFiles></config>`)
			res := evaluate(t, cfg, Request{Members: []string{"a.txt", "b.txt", "c.txt", "z.txt"}})
			var dests []string
			for _, op := range res.Ops {
				dests = append(dests, op.Destination)
			}
			assert.Equal(t, tt.wantDests, dests)
		})
	}
	// The kinds in the directory-op cases.
	cfg := mustParse(t, `<config><requiredInstallFiles><file source="b.txt" destination="a\" priority="3"/><file source="c.txt" destination="a" priority="1"/></requiredInstallFiles></config>`)
	res := evaluate(t, cfg, Request{Members: []string{"b.txt", "c.txt"}})
	require.Len(t, res.Ops, 1)
	assert.Equal(t, OpMkdir, res.Ops[0].Kind)
	assert.NotEmpty(t, res.Warnings)
}

// Review item 8: two candidates at one depth are ambiguous, never resolved
// by iteration order.
func TestLocate_CaseVariantsAreAmbiguous(t *testing.T) {
	for _, members := range [][]string{
		{"fomod/ModuleConfig.xml", "FOMOD/moduleconfig.xml"},
		{"A/fomod/ModuleConfig.xml", "a/fomod/ModuleConfig.xml"},
		{"W/fomod/ModuleConfig.xml", "W/Fomod/ModuleConfig.xml"},
	} {
		_, err := Locate("mod.zip", members)
		assert.ErrorIs(t, err, ErrAmbiguous, "%v", members)
	}
	// The same member listed twice is not two candidates.
	in, err := Locate("mod.zip", []string{"fomod/ModuleConfig.xml", `fomod\ModuleConfig.xml`})
	require.NoError(t, err)
	assert.Equal(t, "fomod/ModuleConfig.xml", in.ModuleConfig)
}

// Review item 9: the recorded choice set is capped.
func TestEvaluate_ChoiceCap(t *testing.T) {
	cfg := mustParse(t, oneGroup("SelectAny", plugin("a", "Optional")))
	picks := make([]OptionChoice, MaxChoices)
	for i := range picks {
		picks[i] = OptionChoice{Name: "a"}
	}
	// Steps and groups count too: 1 + 1 + MaxChoices entries is one over.
	_, err := Evaluate(cfg, Request{Choices: []StepChoice{{Name: "S", Groups: []GroupChoice{{Name: "G", Choices: picks}}}}})
	assert.ErrorIs(t, err, ErrTooLarge)
	_, err = Evaluate(cfg, Request{Choices: []StepChoice{{Name: "S", Groups: []GroupChoice{{Name: "G", Choices: picks[:MaxChoices-2]}}}}})
	assert.NoError(t, err)
}

func TestResolve_StructuralConflictScanStaysLinear(t *testing.T) {
	// Each destination is the previous one plus "!", which sorts before
	// '/': a scan from "x" to "x/" would walk every later key, for every key.
	var items strings.Builder
	members := make([]string, 0, 20000)
	dest := "x"
	for i := 0; i < 20000; i++ {
		src := "s" + itoa(i)
		members = append(members, src)
		items.WriteString(`<file source="` + src + `" destination="` + dest + `"/>`)
		dest += "!"
		if len(dest) > 200 {
			dest = "y" + itoa(i)
		}
	}
	cfg := mustParse(t, `<config><requiredInstallFiles>`+items.String()+`</requiredInstallFiles></config>`)
	withinBudget(t, 10*time.Second, func() {
		res, err := Evaluate(cfg, Request{Members: members})
		require.NoError(t, err)
		assert.Len(t, res.Ops, 20000)
	})
}
