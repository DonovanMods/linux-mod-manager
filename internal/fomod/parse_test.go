package fomod

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustParse(t *testing.T, doc string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(doc))
	require.NoError(t, err)
	return cfg
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	require.NoError(t, err)
	return data
}

func stepNames(cfg *Config) []string {
	var out []string
	for _, s := range cfg.Steps {
		out = append(out, s.Name)
	}
	return out
}

func TestParse_FullFixture(t *testing.T) {
	// The fixture declares encoding="utf-16" but is UTF-8: the declaration
	// must be ignored. Re-encode it as UTF-16 LE too: same result.
	raw := readFixture(t, "full.xml")
	for name, data := range map[string][]byte{
		"utf-8 bytes, utf-16 declaration": raw,
		"utf-16 LE bytes":                 utf16Bytes(string(raw), false, true),
	} {
		t.Run(name, func(t *testing.T) {
			cfg, err := Parse(data)
			require.NoError(t, err)

			assert.Equal(t, "Example Overhaul", cfg.ModuleName)
			assert.Equal(t, `fomod\images\banner.png`, cfg.ModuleImage)
			require.NotNil(t, cfg.Prerequisites)
			assert.Equal(t, ConditionComposite, cfg.Prerequisites.Kind)
			require.Len(t, cfg.Prerequisites.Conditions, 1)
			assert.Equal(t, Condition{Kind: ConditionFile, File: "skyrim.esm", State: FileActive}, cfg.Prerequisites.Conditions[0])

			require.Len(t, cfg.RequiredFiles, 2)
			assert.Equal(t, FileItem{Source: "00 Core", Destination: "", HasDestination: true, Folder: true}, cfg.RequiredFiles[0])
			assert.Equal(t, FileItem{Source: `00 Core\Docs\Readme.txt`, Destination: `Docs\Example Readme.txt`, HasDestination: true}, cfg.RequiredFiles[1])

			assert.Equal(t, []string{"Main", "Lighting Options"}, stepNames(cfg))
			main := cfg.Steps[0]
			assert.Nil(t, main.Visible)
			require.Len(t, main.Groups, 2)
			tex := main.Groups[0]
			assert.Equal(t, "Texture Resolution", tex.Name)
			assert.Equal(t, SelectExactlyOne, tex.Type)
			require.Len(t, tex.Plugins, 2)
			p2k := tex.Plugins[0]
			assert.Equal(t, "2K", p2k.Name)
			assert.Equal(t, "Balanced.", p2k.Description)
			assert.Equal(t, `fomod\images\2k.png`, p2k.Image)
			assert.Equal(t, []Flag{{Name: "res", Value: "2k"}}, p2k.Flags)
			assert.Equal(t, TypeDescriptor{Default: Recommended}, p2k.Type)

			dg := main.Groups[1].Plugins[0]
			assert.Equal(t, NotUsable, dg.Type.Default)
			require.Len(t, dg.Type.Patterns, 2)
			assert.Equal(t, Recommended, dg.Type.Patterns[0].Type)
			assert.Equal(t, CouldBeUsable, dg.Type.Patterns[1].Type)

			light := cfg.Steps[1]
			require.NotNil(t, light.Visible)
			assert.Equal(t, Condition{Kind: ConditionComposite, Operator: OperatorAnd, Conditions: []Condition{
				{Kind: ConditionFlag, Flag: "lighting", Value: "On"},
			}}, *light.Visible)

			require.Len(t, cfg.ConditionalInstalls, 2)
			assert.Equal(t, OperatorOr, cfg.ConditionalInstalls[1].When.Operator)
			assert.Equal(t, Condition{Kind: ConditionVersion, Element: "gameDependency", Version: "1.6.1170"}, cfg.ConditionalInstalls[1].When.Conditions[1])
			assert.Equal(t, int32(5), cfg.ConditionalInstalls[1].Files[0].Priority)
			assert.Empty(t, cfg.Warnings)
		})
	}
}

func TestParse_Order(t *testing.T) {
	doc := func(stepsOrder string) string {
		return `<config><installSteps` + stepsOrder + `>
			<installStep name="b"><optionalFileGroups/></installStep>
			<installStep name="C"><optionalFileGroups/></installStep>
			<installStep name="a"><optionalFileGroups/></installStep>
			<installStep name="B"><optionalFileGroups/></installStep>
		</installSteps></config>`
	}
	tests := []struct {
		name, attr string
		want       []string
	}{
		// A missing order is the XSD's default, Ascending: Vortex validates
		// with schema defaults applied, so it sorts. Case-insensitive, then
		// lower-case first, as .NET's culture-aware compare orders them.
		{"missing means ascending", ``, []string{"a", "b", "B", "C"}},
		{"ascending", ` order="Ascending"`, []string{"a", "b", "B", "C"}},
		{"descending", ` order="Descending"`, []string{"C", "B", "b", "a"}},
		{"explicit", ` order="Explicit"`, []string{"b", "C", "a", "B"}},
		{"case-insensitive value", ` order="explicit"`, []string{"b", "C", "a", "B"}},
		{"unknown value is explicit", ` order="Random"`, []string{"b", "C", "a", "B"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stepNames(mustParse(t, doc(tt.attr))))
		})
	}

	// Groups and plugins sort the same way.
	cfg := mustParse(t, `<config><installSteps order="Explicit"><installStep name="s">
		<optionalFileGroups>
			<group name="z" type="SelectAny"><plugins>
				<plugin name="y"><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
				<plugin name="x"><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
			</plugins></group>
			<group name="m" type="SelectAny"><plugins order="Explicit">
				<plugin name="y"><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
				<plugin name="x"><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
			</plugins></group>
		</optionalFileGroups></installStep></installSteps></config>`)
	groups := cfg.Steps[0].Groups
	assert.Equal(t, "m", groups[0].Name)
	assert.Equal(t, []string{"y", "x"}, []string{groups[0].Plugins[0].Name, groups[0].Plugins[1].Name})
	assert.Equal(t, []string{"x", "y"}, []string{groups[1].Plugins[0].Name, groups[1].Plugins[1].Name})
}

func TestParse_OrdinalsCountSameNamedSiblingsInDocumentOrder(t *testing.T) {
	cfg := mustParse(t, `<config><installSteps>
		<installStep name="Same"><optionalFileGroups order="Explicit">
			<group name="G" type="SelectAny"><plugins order="Explicit">
				<plugin name="P"><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
				<plugin name="Q"><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
				<plugin name="P"><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
			</plugins></group>
			<group name="G" type="SelectAny"><plugins/></group>
		</optionalFileGroups></installStep>
		<installStep name="Other"><optionalFileGroups/></installStep>
		<installStep name="Same"><optionalFileGroups/></installStep>
	</installSteps></config>`)
	// Ascending by default: Other, Same(0), Same(1). Ordinals are document order.
	require.Equal(t, []string{"Other", "Same", "Same"}, stepNames(cfg))
	assert.Equal(t, 0, cfg.Steps[0].Ordinal)
	assert.Equal(t, 0, cfg.Steps[1].Ordinal)
	assert.Equal(t, 1, cfg.Steps[2].Ordinal)
	g := cfg.Steps[1].Groups
	assert.Equal(t, []int{0, 1}, []int{g[0].Ordinal, g[1].Ordinal})
	p := g[0].Plugins
	assert.Equal(t, []int{0, 0, 1}, []int{p[0].Ordinal, p[1].Ordinal, p[2].Ordinal})
}

func TestParse_NamesAreNotTrimmed(t *testing.T) {
	cfg := mustParse(t, `<config><installSteps><installStep name=""><optionalFileGroups>
		<group name=" Spaced Group " type="SelectAny"><plugins>
			<plugin name=" Spicy Clean Hacking Screen"><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
		</plugins></group></optionalFileGroups></installStep></installSteps></config>`)
	assert.Equal(t, "", cfg.Steps[0].Name)
	assert.Equal(t, " Spaced Group ", cfg.Steps[0].Groups[0].Name)
	assert.Equal(t, " Spicy Clean Hacking Screen", cfg.Steps[0].Groups[0].Plugins[0].Name)
}

func TestParse_FileItems(t *testing.T) {
	cfg := mustParse(t, `<config><requiredInstallFiles>
		<file source="a.esp"/>
		<file source="b.esp" destination=""/>
		<folder source="dir" destination="Data\dir" alwaysInstall="TRUE" installIfUsable="1" priority="-3"/>
		<file source="big.esp" priority="99999999999"/>
		<file source="small.esp" priority="-99999999999"/>
		<file source="dec.esp" priority="2.7"/>
		<file source="bad.esp" priority="high"/>
		<file source="nobool.esp" alwaysInstall="yes"/>
	</requiredInstallFiles></config>`)
	f := cfg.RequiredFiles
	require.Len(t, f, 8)
	assert.Equal(t, FileItem{Source: "a.esp"}, f[0], "absent destination")
	assert.Equal(t, FileItem{Source: "b.esp", HasDestination: true}, f[1], "empty destination")
	assert.Equal(t, FileItem{Source: "dir", Destination: `Data\dir`, HasDestination: true, Folder: true, AlwaysInstall: true, InstallIfUsable: true, Priority: -3}, f[2])
	assert.Equal(t, int32(2147483647), f[3].Priority, "clamped like Vortex")
	assert.Equal(t, int32(-2147483648), f[4].Priority)
	assert.Equal(t, int32(2), f[5].Priority, "decimal truncates")
	assert.Equal(t, int32(0), f[6].Priority)
	assert.False(t, f[7].AlwaysInstall)
	assert.Len(t, cfg.Warnings, 2, "the bad priority and the bad boolean: %v", cfg.Warnings)
}

func TestParse_Conditions(t *testing.T) {
	cfg := mustParse(t, `<config><moduleDependencies operator="or">
		<fileDependency file="Data\Foo.ESP" state="Inactive"/>
		<flagDependency flag="f" value=""/>
		<skseDependency version="2.0.0"/>
		<fommDependency version="0.13"/>
		<dependencies>
			<fileDependency file="bar.esm" state="Missing"/>
		</dependencies>
		<fileDependency file="odd.esp" state="Sideways"/>
		<futureDependency x="1"/>
	</moduleDependencies></config>`)
	c := cfg.Prerequisites
	require.NotNil(t, c)
	assert.Equal(t, OperatorOr, c.Operator)
	require.Len(t, c.Conditions, 7)
	assert.Equal(t, Condition{Kind: ConditionFile, File: "data/foo.esp", State: FileInactive}, c.Conditions[0])
	assert.Equal(t, Condition{Kind: ConditionFlag, Flag: "f"}, c.Conditions[1])
	assert.Equal(t, Condition{Kind: ConditionVersion, Element: "skseDependency", Version: "2.0.0"}, c.Conditions[2])
	assert.Equal(t, Condition{Kind: ConditionVersion, Element: "fommDependency", Version: "0.13"}, c.Conditions[3])
	assert.Equal(t, Condition{Kind: ConditionComposite, Operator: OperatorAnd, Conditions: []Condition{
		{Kind: ConditionFile, File: "bar.esm", State: FileMissing},
	}}, c.Conditions[4])
	assert.Equal(t, ConditionUnknown, c.Conditions[5].Kind)
	assert.Equal(t, Condition{Kind: ConditionUnknown, Element: "futureDependency"}, c.Conditions[6])
	assert.Len(t, cfg.Warnings, 2, "%v", cfg.Warnings)
}

func TestParse_Tolerance(t *testing.T) {
	// Unknown group and plugin types, a missing typeDescriptor, unquoted
	// attributes, an HTML entity and a mismatched closing tag all parse.
	cfg := mustParse(t, `<config><moduleName>A&nbsp;B</moduleName><installSteps><installStep name=s><optionalFileGroups>
		<group name="G" type="selectexactlyone"><plugins>
			<plugin name="a"><typeDescriptor><type name="required"/></typeDescriptor></plugin>
			<plugin name="b"><typeDescriptor><type name="Mandatory"/></typeDescriptor></plugin>
			<plugin name="c"></plugin>
		</plugins></group>
		<group name="H" type="PickSome"><plugins/></group>
	</optionalFileGroups></installStep></installSteps></config>`)
	assert.Equal(t, "A&nbsp;B", cfg.ModuleName)
	g := cfg.Steps[0].Groups
	assert.Equal(t, SelectExactlyOne, g[0].Type)
	assert.Equal(t, Required, g[0].Plugins[0].Type.Default)
	assert.Equal(t, Optional, g[0].Plugins[1].Type.Default)
	assert.Equal(t, Optional, g[0].Plugins[2].Type.Default)
	assert.Equal(t, SelectAny, g[1].Type)
	assert.Len(t, cfg.Warnings, 3, "%v", cfg.Warnings)
}

func TestParse_VersionOneLayout(t *testing.T) {
	// ModConfig 1.0 put optionalFileGroups directly under config: one unnamed step.
	cfg := mustParse(t, `<config><optionalFileGroups><group name="G" type="SelectAny"><plugins>
		<plugin name="a"><typeDescriptor><type name="Optional"/></typeDescriptor></plugin>
	</plugins></group></optionalFileGroups></config>`)
	require.Len(t, cfg.Steps, 1)
	assert.Equal(t, "", cfg.Steps[0].Name)
	assert.Equal(t, "G", cfg.Steps[0].Groups[0].Name)
}

func TestParse_NoCustomEntityExpansion(t *testing.T) {
	cfg := mustParse(t, `<?xml version="1.0"?>
<!DOCTYPE config [<!ENTITY boom "EXPANDED"><!ENTITY lol "&boom;&boom;&boom;">]>
<config><moduleName>&lol;</moduleName></config>`)
	assert.NotContains(t, cfg.ModuleName, "EXPANDED")
}

func TestParse_Refusals(t *testing.T) {
	deep := "<config>" + strings.Repeat("<moduleDependencies>", MaxDepth) + strings.Repeat("</moduleDependencies>", MaxDepth) + "</config>"
	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"too large", []byte("<config>" + strings.Repeat(" ", MaxConfigSize) + "</config>"), ErrTooLarge},
		{"too deep", []byte(deep), ErrTooDeep},
		{"wrong root", []byte(`<fomod/>`), ErrNotModuleConfig},
		{"empty", nil, ErrNotModuleConfig},
		{"not xml", []byte("PK\x03\x04 binary"), ErrNotModuleConfig},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.in)
			assert.ErrorIs(t, err, tt.want)
		})
	}

	// Malformed XML inside a valid root is a parse error, not a panic.
	_, err := Parse([]byte(`<config><installSteps><installStep name="a"`))
	assert.Error(t, err)
}
