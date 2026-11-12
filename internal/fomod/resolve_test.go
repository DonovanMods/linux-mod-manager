package fomod

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copies maps destination -> source for every copy op.
func copies(res *Result) map[string]string {
	out := map[string]string{}
	for _, op := range res.Ops {
		if op.Kind == OpCopy {
			out[op.Destination] = op.Source
		}
	}
	return out
}

func TestResolve_FullFixture(t *testing.T) {
	cfg, err := Parse(readFixture(t, "full.xml"))
	require.NoError(t, err)
	members := []string{
		"fomod/ModuleConfig.xml",
		"00 Core/Example.esp",
		"00 Core/Docs/Readme.txt",
		"10 Textures 2K/a.dds",
		"10 Textures 2K/sub/b.dds",
		"11 Textures 4K/a.dds",
		"20 Patches/Example - Lighting.esp",
		"20 Patches/Example - Dawnguard.esp",
		"30 Conditional/Dark Lighting.ini",
		"30 Conditional/Example.ini",
	}
	res := evaluate(t, cfg, Request{Mode: ModeReplay, Members: members, Files: fileStates{"skyrim.esm": FileActive}, Choices: []StepChoice{
		{Name: "Main", Groups: []GroupChoice{
			{Name: "Texture Resolution", Choices: []OptionChoice{{Name: "2K"}}},
			{Name: "Patches", Choices: []OptionChoice{{Name: "Lighting Patch"}}},
		}},
		{Name: "Lighting Options", Groups: []GroupChoice{{Name: "Brightness", Choices: []OptionChoice{{Name: "Dark"}}}}},
	}})
	assert.Equal(t, map[string]string{
		"Example.esp":               "00 Core/Example.esp",
		"Docs/Readme.txt":           "00 Core/Docs/Readme.txt",
		"Docs/Example Readme.txt":   "00 Core/Docs/Readme.txt",
		"textures/a.dds":            "10 Textures 2K/a.dds",
		"textures/sub/b.dds":        "10 Textures 2K/sub/b.dds",
		"Example - Lighting.esp":    "20 Patches/Example - Lighting.esp",
		"SKSE/Plugins/Lighting.ini": "30 Conditional/Dark Lighting.ini",
		"Example.ini":               "30 Conditional/Example.ini",
	}, copies(res))
	assert.Empty(t, res.MissingSources)
	// Example.ini is installed because gameDependency is assumed satisfied.
	assert.Contains(t, res.Warnings, "gameDependency (version 1.6.1170) is not checked: treated as satisfied")

	// Ops are sorted by destination, case-insensitively, and carry their layer.
	for i := 1; i < len(res.Ops); i++ {
		assert.LessOrEqual(t, lowerKey(res.Ops[i-1].Destination), lowerKey(res.Ops[i].Destination))
	}
	for _, op := range res.Ops {
		switch op.Destination {
		case "Docs/Example Readme.txt", "Example.esp":
			assert.Equal(t, int64(-1e9), op.Priority, op.Destination)
		case "Example.ini":
			assert.Equal(t, int64(1e9+5), op.Priority)
		case "textures/a.dds":
			assert.Equal(t, int64(0), op.Priority)
		}
	}
}

func TestResolve_Layering(t *testing.T) {
	doc := func(required, option, conditional string) string {
		return `<config>
			<requiredInstallFiles>` + required + `</requiredInstallFiles>
			<installSteps><installStep name="S"><optionalFileGroups><group name="G" type="SelectAll"><plugins>
				<plugin name="p"><files>` + option + `</files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
			</plugins></group></optionalFileGroups></installStep></installSteps>
			<conditionalFileInstalls><patterns><pattern><dependencies/><files>` + conditional + `</files></pattern></patterns></conditionalFileInstalls>
		</config>`
	}
	members := []string{"req/x.esp", "opt/x.esp", "cond/x.esp", "a/x.esp", "b/x.esp", "dir/x.esp"}
	tests := []struct {
		name                   string
		required, option, cond string
		wantSource             string
	}{
		{
			"conditional beats selected beats required",
			`<file source="req\x.esp" destination="x.esp" priority="100"/>`,
			`<file source="opt\x.esp" destination="x.esp" priority="50"/>`,
			`<file source="cond\x.esp" destination="x.esp" priority="-50"/>`,
			"cond/x.esp",
		},
		{
			"selected beats required whatever the priorities",
			`<file source="req\x.esp" destination="x.esp" priority="999999"/>`,
			`<file source="opt\x.esp" destination="x.esp" priority="-999999"/>`,
			``,
			"opt/x.esp",
		},
		{
			"higher priority wins within a layer",
			``,
			`<file source="b\x.esp" destination="x.esp" priority="1"/><file source="a\x.esp" destination="x.esp" priority="2"/>`,
			``,
			"a/x.esp",
		},
		{
			"a tie goes to the larger source path, not to document order",
			``,
			`<file source="b\x.esp" destination="x.esp"/><file source="a\x.esp" destination="x.esp"/>`,
			``,
			"b/x.esp",
		},
		{
			"a tie compares source paths case-insensitively",
			``,
			`<file source="B\X.ESP" destination="x.esp"/><file source="a\x.esp" destination="x.esp"/>`,
			``,
			"b/x.esp",
		},
		{
			"destinations collide case-insensitively",
			``,
			`<file source="a\x.esp" destination="Data\X.esp" priority="1"/><file source="b\x.esp" destination="data\x.ESP"/>`,
			``,
			"a/x.esp",
		},
		{
			// Divergence from Vortex: Vortex installs a required <folder> at
			// its raw priority (0 here), which would tie with the selected
			// file and win on the larger source path ("req" > "opt").
			// lmm applies the -1e9 offset to folders too.
			"required folders are layered like files",
			`<folder source="req" destination=""/>`,
			`<file source="opt\x.esp" destination="x.esp"/>`,
			``,
			"opt/x.esp",
		},
		{
			// The same divergence for a conditional folder over a selected file.
			"conditional folders are layered like files",
			``,
			`<file source="opt\x.esp" destination="x.esp" priority="10"/>`,
			`<folder source="cond" destination=""/>`,
			"cond/x.esp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := evaluate(t, mustParse(t, doc(tt.required, tt.option, tt.cond)), Request{Mode: ModeReplay, Members: members})
			c := copies(res)
			var got []string
			for dest, src := range c {
				if lowerKey(dest) == "x.esp" || lowerKey(dest) == "data/x.esp" {
					got = append(got, src)
				}
			}
			require.Len(t, got, 1, "exactly one op per destination: %v", res.Ops)
			assert.Equal(t, tt.wantSource, lowerKey(got[0]))
		})
	}
}

func TestResolve_Destinations(t *testing.T) {
	members := []string{`Opt A/Data/Foo.esp`, "Opt A/textures/a.dds", "Opt A/textures/b.dds", "root.txt"}
	tests := []struct {
		name  string
		item  string
		want  map[string]string
		mkdir []string
		warn  bool
	}{
		{"empty destination is the base name at the root", `<file source="opt a\data\foo.esp" destination=""/>`, map[string]string{"foo.esp": "Opt A/Data/Foo.esp"}, nil, false},
		// Destinations keep the XML's spelling, as Vortex's do; only the
		// part a folder contributes comes from the archive member.
		{"absent destination is the source path", `<file source="Opt A\Data\Foo.esp"/>`, map[string]string{"Opt A/Data/Foo.esp": "Opt A/Data/Foo.esp"}, nil, false},
		{"leading separators and dot segments are dropped", `<file source="root.txt" destination="\.\Docs\\root.txt"/>`, map[string]string{"Docs/root.txt": "root.txt"}, nil, false},
		{"a folder with an empty destination lands at the root", `<folder source="Opt A\textures" destination=""/>`, map[string]string{"a.dds": "Opt A/textures/a.dds", "b.dds": "Opt A/textures/b.dds"}, nil, false},
		{"a folder with no destination keeps its path", `<folder source="opt a\textures\"/>`, map[string]string{"opt a/textures/a.dds": "Opt A/textures/a.dds", "opt a/textures/b.dds": "Opt A/textures/b.dds"}, nil, false},
		{"a folder into a directory", `<folder source="Opt A/textures" destination="Data\Textures\"/>`, map[string]string{"Data/Textures/a.dds": "Opt A/textures/a.dds", "Data/Textures/b.dds": "Opt A/textures/b.dds"}, nil, false},
		// Vortex: a file whose destination ends in a separator becomes a
		// directory creation and installs nothing. lmm does the same, and warns.
		{"a file into a trailing-separator destination only makes the directory", `<file source="root.txt" destination="Docs\"/>`, map[string]string{}, []string{"Docs"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := mustParse(t, `<config><requiredInstallFiles>`+tt.item+`</requiredInstallFiles></config>`)
			res := evaluate(t, cfg, Request{Mode: ModeReplay, Members: members})
			assert.Equal(t, tt.want, copies(res))
			var dirs []string
			for _, op := range res.Ops {
				if op.Kind == OpMkdir {
					dirs = append(dirs, op.Destination)
				}
			}
			assert.Equal(t, tt.mkdir, dirs)
			assert.Equal(t, tt.warn, len(res.Warnings) > 0, "%v", res.Warnings)
		})
	}
}

func TestResolve_UnsafeDestinationsAreRefused(t *testing.T) {
	for _, item := range []string{
		`<file source="a.txt" destination="..\..\.bashrc"/>`,
		`<file source="a.txt" destination="Data/../../x"/>`,
		`<folder source="d" destination="..\"/>`,
	} {
		cfg := mustParse(t, `<config><requiredInstallFiles>`+item+`</requiredInstallFiles></config>`)
		_, err := Evaluate(cfg, Request{Members: []string{"a.txt", "d/x"}})
		assert.ErrorIs(t, err, ErrUnsafePath, item)
	}
}

func TestResolve_MissingSources(t *testing.T) {
	cfg := mustParse(t, `<config><requiredInstallFiles>
		<file source="gone.esp"/>
		<file source="Dir"/>
		<folder source="NoDir"/>
		<file source="present.esp"/>
	</requiredInstallFiles></config>`)
	res := evaluate(t, cfg, Request{Members: []string{"present.esp", "dir/inner.esp", "dir/"}})
	assert.Equal(t, []MissingSource{
		{Source: "gone.esp", Reason: SourceNotFound},
		{Source: "Dir", Reason: SourceIsDirectory},
		{Source: "NoDir", Folder: true, Reason: SourceNotFound},
	}, res.MissingSources)
	assert.Equal(t, map[string]string{"present.esp": "present.esp"}, copies(res))
}

func TestResolve_UnselectedOptionFiles(t *testing.T) {
	doc := `<config><installSteps order="Explicit">
		<installStep name="S"><optionalFileGroups order="Explicit"><group name="G" type="SelectAny"><plugins order="Explicit">
			<plugin name="always"><files><file source="always.txt" alwaysInstall="true"/><file source="plain.txt"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
			<plugin name="usable"><files><file source="usable.txt" installIfUsable="true"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
			<plugin name="unusable"><files><file source="unusable.txt" installIfUsable="true"/></files><typeDescriptor><type name="NotUsable"/></typeDescriptor></plugin>
		</plugins></group></optionalFileGroups></installStep>
		<installStep name="Hidden"><visible><flagDependency flag="x" value="1"/></visible><optionalFileGroups><group name="H" type="SelectAny"><plugins>
			<plugin name="h"><files><file source="hidden.txt" alwaysInstall="true"/></files><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
		</plugins></group></optionalFileGroups></installStep>
	</installSteps></config>`
	members := []string{"always.txt", "plain.txt", "usable.txt", "unusable.txt", "hidden.txt"}
	res := evaluate(t, mustParse(t, doc), Request{Mode: ModeReplay, Members: members, Choices: []StepChoice{
		{Name: "S", Groups: []GroupChoice{{Name: "G", Choices: []OptionChoice{}}}},
	}})
	// Vortex walks every step's options for alwaysInstall/installIfUsable,
	// hidden steps included.
	assert.Equal(t, map[string]string{"always.txt": "always.txt", "usable.txt": "usable.txt", "hidden.txt": "hidden.txt"}, copies(res))
}

func TestResolve_MemberMatching(t *testing.T) {
	cfg := mustParse(t, `<config><requiredInstallFiles><file source="CORE\Sub\A.ESP" destination="a.esp"/><folder source="core\sub" destination="all"/></requiredInstallFiles></config>`)
	res := evaluate(t, cfg, Request{Members: []string{`Core\Sub\a.esp`, "Core/Sub/", "core/sub/b.esp"}})
	assert.Equal(t, map[string]string{
		"a.esp":     "Core/Sub/a.esp",
		"all/a.esp": "Core/Sub/a.esp",
		"all/b.esp": "core/sub/b.esp",
	}, copies(res))
}

func TestResolve_OpCap(t *testing.T) {
	cfg := mustParse(t, `<config><requiredInstallFiles><folder source="" destination="a"/><folder source="" destination="b"/></requiredInstallFiles></config>`)
	members := make([]string, MaxOps/2+1)
	for i := range members {
		members[i] = "f" + string(rune('a'+i%26)) + "/" + itoa(i)
	}
	_, err := Evaluate(cfg, Request{Members: members})
	assert.ErrorIs(t, err, ErrTooLarge)
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for ; i > 0; i /= 10 {
		b = append([]byte{byte('0' + i%10)}, b...)
	}
	return string(b)
}

func TestResolve_HostileMemberNamesCannotEscape(t *testing.T) {
	// Members come from an archive listing; core's extractor refuses ".."
	// members, but the resolver must not rely on that.
	for _, item := range []string{
		`<folder source="" destination=""/>`,
		`<folder source="x" destination="Data"/>`,
		`<file source="x\.." destination=""/>`,
	} {
		cfg := mustParse(t, `<config><requiredInstallFiles>`+item+`</requiredInstallFiles></config>`)
		res, err := Evaluate(cfg, Request{Members: []string{"x/../../../evil", "x/.."}})
		if err != nil {
			assert.ErrorIs(t, err, ErrUnsafePath, item)
			continue
		}
		for _, op := range res.Ops {
			assert.True(t, isContained(op.Destination), "%s: %q", item, op.Destination)
			assert.NotContains(t, "/"+op.Destination+"/", "/../", item)
		}
	}
}
