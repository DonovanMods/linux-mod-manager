package fomod

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
)

// The XML shapes, decoded with encoding/xml and then converted. Every
// attribute that has a schema default is a pointer, so "absent" can be told
// from "empty".

type configXML struct {
	ModuleName struct {
		Text string `xml:",chardata"`
	} `xml:"moduleName"`
	ModuleImage struct {
		Path string `xml:"path,attr"`
	} `xml:"moduleImage"`
	ModuleDependencies      *dependencyXML `xml:"moduleDependencies"`
	RequiredInstallFiles    *fileListXML   `xml:"requiredInstallFiles"`
	InstallSteps            *stepListXML   `xml:"installSteps"`
	OptionalFileGroups      *groupListXML  `xml:"optionalFileGroups"` // ModConfig 1.0
	ConditionalFileInstalls *struct {
		Patterns struct {
			Pattern []struct {
				Dependencies *dependencyXML `xml:"dependencies"`
				Files        *fileListXML   `xml:"files"`
			} `xml:"pattern"`
		} `xml:"patterns"`
	} `xml:"conditionalFileInstalls"`
}

type stepListXML struct {
	Order *string   `xml:"order,attr"`
	Steps []stepXML `xml:"installStep"`
}

type stepXML struct {
	Name    string         `xml:"name,attr"`
	Visible *dependencyXML `xml:"visible"`
	Groups  *groupListXML  `xml:"optionalFileGroups"`
}

type groupListXML struct {
	Order  *string    `xml:"order,attr"`
	Groups []groupXML `xml:"group"`
}

type groupXML struct {
	Name    string `xml:"name,attr"`
	Type    string `xml:"type,attr"`
	Plugins *struct {
		Order   *string     `xml:"order,attr"`
		Plugins []pluginXML `xml:"plugin"`
	} `xml:"plugins"`
}

type pluginXML struct {
	Name        string `xml:"name,attr"`
	Description string `xml:"description"`
	Image       struct {
		Path string `xml:"path,attr"`
	} `xml:"image"`
	Files *fileListXML `xml:"files"`
	Flags *struct {
		Flag []struct {
			Name  string `xml:"name,attr"`
			Value string `xml:",chardata"`
		} `xml:"flag"`
	} `xml:"conditionFlags"`
	TypeDescriptor *struct {
		Type *struct {
			Name string `xml:"name,attr"`
		} `xml:"type"`
		DependencyType *struct {
			DefaultType struct {
				Name string `xml:"name,attr"`
			} `xml:"defaultType"`
			Patterns struct {
				Pattern []struct {
					Dependencies *dependencyXML `xml:"dependencies"`
					Type         struct {
						Name string `xml:"name,attr"`
					} `xml:"type"`
				} `xml:"pattern"`
			} `xml:"patterns"`
		} `xml:"dependencyType"`
	} `xml:"typeDescriptor"`
}

type fileListXML struct {
	Items []struct {
		XMLName         xml.Name
		Source          string  `xml:"source,attr"`
		Destination     *string `xml:"destination,attr"`
		AlwaysInstall   string  `xml:"alwaysInstall,attr"`
		InstallIfUsable string  `xml:"installIfUsable,attr"`
		Priority        string  `xml:"priority,attr"`
	} `xml:",any"`
}

// dependencyXML is every dependency element: a composite (any element
// holding children) or a leaf, told apart by its name.
type dependencyXML struct {
	XMLName  xml.Name
	Operator string          `xml:"operator,attr"`
	File     string          `xml:"file,attr"`
	State    string          `xml:"state,attr"`
	Flag     string          `xml:"flag,attr"`
	Value    string          `xml:"value,attr"`
	Version  string          `xml:"version,attr"`
	Children []dependencyXML `xml:",any"`
}

// Parse decodes a fomod/ModuleConfig.xml. It is tolerant the way real
// installers need: the encoding is sniffed from the bytes and the XML
// declaration ignored, there is no schema validation (encoding/xml in
// non-strict mode), enum values match case-insensitively, and an
// unrecognised value falls back to a default with a Config.Warnings entry.
// It is not tolerant of size: input over MaxConfigSize or nested deeper
// than MaxDepth is refused. No entity is expanded beyond XML's five
// predefined ones; a DTD's custom entities are left as literal text.
func Parse(data []byte) (*Config, error) {
	text, err := decodeText(data)
	if err != nil {
		return nil, err
	}
	text = stripDeclaration(text)
	if err := prescan(text); err != nil {
		return nil, err
	}

	var raw configXML
	dec := newDecoder(text)
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("fomod: parsing ModuleConfig.xml: %w", err)
	}

	p := &parser{}
	cfg := p.config(&raw)
	cfg.Warnings = p.warnings
	return cfg, nil
}

func newDecoder(text string) *xml.Decoder {
	dec := xml.NewDecoder(strings.NewReader(text))
	dec.Strict = false
	// The declaration was stripped, but a stray one later in the text must
	// not make the decoder reach for a charset: the text is already UTF-8.
	dec.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	return dec
}

// prescan walks the token stream once to enforce MaxDepth and check the
// root element, before the struct decode (whose recursion has no cap of
// its own beyond the standard library's).
func prescan(text string) error {
	dec := newDecoder(text)
	depth := 0
	sawRoot := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if !sawRoot {
				return fmt.Errorf("%w: %v", ErrNotModuleConfig, err)
			}
			return fmt.Errorf("fomod: parsing ModuleConfig.xml: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !sawRoot {
				if !strings.EqualFold(t.Name.Local, "config") {
					return fmt.Errorf("%w: root element is <%s>", ErrNotModuleConfig, t.Name.Local)
				}
				sawRoot = true
			}
			depth++
			if depth > MaxDepth {
				return ErrTooDeep
			}
		case xml.EndElement:
			depth--
		}
	}
	if !sawRoot {
		return ErrNotModuleConfig
	}
	return nil
}

type parser struct {
	warnings []string
}

func (p *parser) warnf(format string, args ...any) {
	p.warnings = append(p.warnings, fmt.Sprintf(format, args...))
}

func (p *parser) config(raw *configXML) *Config {
	cfg := &Config{
		ModuleName:  strings.TrimSpace(raw.ModuleName.Text),
		ModuleImage: raw.ModuleImage.Path,
	}
	if raw.ModuleDependencies != nil {
		c := p.composite(raw.ModuleDependencies)
		cfg.Prerequisites = &c
	}
	cfg.RequiredFiles = p.files(raw.RequiredInstallFiles)

	switch {
	case raw.InstallSteps != nil:
		for _, s := range raw.InstallSteps.Steps {
			step := Step{Name: s.Name, Groups: p.groups(s.Groups)}
			if s.Visible != nil {
				c := p.composite(s.Visible)
				step.Visible = &c
			}
			cfg.Steps = append(cfg.Steps, step)
		}
		assignOrdinals(cfg.Steps, func(s *Step) (string, *int) { return s.Name, &s.Ordinal })
		sortByOrder(p, raw.InstallSteps.Order, cfg.Steps, func(s Step) string { return s.Name })
	case raw.OptionalFileGroups != nil:
		cfg.Steps = []Step{{Groups: p.groups(raw.OptionalFileGroups)}}
	}

	if raw.ConditionalFileInstalls != nil {
		for _, pat := range raw.ConditionalFileInstalls.Patterns.Pattern {
			cfg.ConditionalInstalls = append(cfg.ConditionalInstalls, ConditionalInstall{
				When:  p.compositeOrEmpty(pat.Dependencies),
				Files: p.files(pat.Files),
			})
		}
	}
	return cfg
}

func (p *parser) groups(raw *groupListXML) []Group {
	if raw == nil {
		return nil
	}
	var out []Group
	for _, g := range raw.Groups {
		group := Group{Name: g.Name, Type: p.groupType(g.Type)}
		if g.Plugins != nil {
			for _, pl := range g.Plugins.Plugins {
				group.Plugins = append(group.Plugins, p.plugin(&pl))
			}
			assignOrdinals(group.Plugins, func(pl *Plugin) (string, *int) { return pl.Name, &pl.Ordinal })
			sortByOrder(p, g.Plugins.Order, group.Plugins, func(pl Plugin) string { return pl.Name })
		}
		out = append(out, group)
	}
	assignOrdinals(out, func(g *Group) (string, *int) { return g.Name, &g.Ordinal })
	sortByOrder(p, raw.Order, out, func(g Group) string { return g.Name })
	return out
}

func (p *parser) plugin(raw *pluginXML) Plugin {
	pl := Plugin{
		Name:        raw.Name,
		Description: strings.TrimSpace(raw.Description),
		Image:       raw.Image.Path,
		Files:       p.files(raw.Files),
	}
	if raw.Flags != nil {
		for _, f := range raw.Flags.Flag {
			pl.Flags = append(pl.Flags, Flag{Name: f.Name, Value: f.Value})
		}
	}
	td := raw.TypeDescriptor
	switch {
	case td != nil && td.Type != nil:
		pl.Type.Default = p.pluginType(td.Type.Name, raw.Name)
	case td != nil && td.DependencyType != nil:
		dt := td.DependencyType
		pl.Type.Default = p.pluginType(dt.DefaultType.Name, raw.Name)
		for _, pat := range dt.Patterns.Pattern {
			pl.Type.Patterns = append(pl.Type.Patterns, TypePattern{
				When: p.compositeOrEmpty(pat.Dependencies),
				Type: p.pluginType(pat.Type.Name, raw.Name),
			})
		}
	default:
		p.warnf("plugin %q has no typeDescriptor: treated as Optional", raw.Name)
	}
	return pl
}

func (p *parser) files(raw *fileListXML) []FileItem {
	if raw == nil {
		return nil
	}
	var out []FileItem
	for _, it := range raw.Items {
		var folder bool
		switch {
		case strings.EqualFold(it.XMLName.Local, "file"):
		case strings.EqualFold(it.XMLName.Local, "folder"):
			folder = true
		default:
			p.warnf("unknown file-list element <%s> ignored", it.XMLName.Local)
			continue
		}
		item := FileItem{
			Source:          it.Source,
			Folder:          folder,
			AlwaysInstall:   p.boolean(it.AlwaysInstall, "alwaysInstall"),
			InstallIfUsable: p.boolean(it.InstallIfUsable, "installIfUsable"),
			Priority:        p.priority(it.Priority),
		}
		if it.Destination != nil {
			item.Destination = *it.Destination
			item.HasDestination = true
		}
		out = append(out, item)
	}
	return out
}

// composite converts a compositeDependency element (moduleDependencies,
// visible, dependencies): its children, combined by its operator.
func (p *parser) composite(raw *dependencyXML) Condition {
	c := Condition{Kind: ConditionComposite, Operator: OperatorAnd}
	switch {
	case raw.Operator == "", strings.EqualFold(raw.Operator, "And"):
	case strings.EqualFold(raw.Operator, "Or"):
		c.Operator = OperatorOr
	default:
		p.warnf("unknown dependency operator %q: treated as And", raw.Operator)
	}
	for i := range raw.Children {
		c.Conditions = append(c.Conditions, p.dependency(&raw.Children[i]))
	}
	return c
}

func (p *parser) compositeOrEmpty(raw *dependencyXML) Condition {
	if raw == nil {
		return Condition{Kind: ConditionComposite, Operator: OperatorAnd}
	}
	return p.composite(raw)
}

func (p *parser) dependency(raw *dependencyXML) Condition {
	name := raw.XMLName.Local
	switch {
	case strings.EqualFold(name, "dependencies"):
		return p.composite(raw)
	case strings.EqualFold(name, "fileDependency"):
		state, ok := parseFileState(raw.State)
		if !ok {
			p.warnf("fileDependency on %q has unknown state %q: treated as satisfied", raw.File, raw.State)
			return Condition{Kind: ConditionUnknown, Element: name}
		}
		return Condition{Kind: ConditionFile, File: normalizeKey(raw.File), State: state}
	case strings.EqualFold(name, "flagDependency"):
		return Condition{Kind: ConditionFlag, Flag: raw.Flag, Value: raw.Value}
	case strings.HasSuffix(strings.ToLower(name), "dependency") && raw.Version != "":
		// gameDependency, fommDependency, and the script extenders
		// (foseDependency, nvseDependency, skseDependency, f4seDependency).
		return Condition{Kind: ConditionVersion, Element: name, Version: raw.Version}
	}
	p.warnf("unknown dependency <%s>: treated as satisfied", name)
	return Condition{Kind: ConditionUnknown, Element: name}
}

func parseFileState(s string) (FileState, bool) {
	for i, n := range fileStateNames {
		if strings.EqualFold(s, n) {
			return FileState(i), true
		}
	}
	return FileMissing, false
}

func (p *parser) groupType(s string) GroupType {
	for i, n := range groupTypeNames {
		if strings.EqualFold(s, n) {
			return GroupType(i)
		}
	}
	p.warnf("unknown group type %q: treated as SelectAny", s)
	return SelectAny
}

func (p *parser) pluginType(s, plugin string) PluginType {
	for i, n := range pluginTypeNames {
		if strings.EqualFold(s, n) {
			return PluginType(i)
		}
	}
	p.warnf("plugin %q has unknown type %q: treated as Optional", plugin, s)
	return Optional
}

func (p *parser) boolean(s, attr string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "0":
		return false
	case "true", "1":
		return true
	}
	p.warnf("%s=%q is not a boolean: treated as false", attr, s)
	return false
}

// priority parses a priority attribute the way Vortex does: a decimal,
// truncated, clamped to the int32 range.
func (p *parser) priority(s string) int32 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) {
		p.warnf("priority=%q is not a number: treated as 0", s)
		return 0
	}
	switch {
	case f >= math.MaxInt32:
		return math.MaxInt32
	case f <= math.MinInt32:
		return math.MinInt32
	}
	return int32(f)
}

// assignOrdinals numbers each item among its same-named predecessors, in
// the slice's current (document) order.
func assignOrdinals[T any](items []T, key func(*T) (string, *int)) {
	seen := map[string]int{}
	for i := range items {
		name, ord := key(&items[i])
		*ord = seen[name]
		seen[name]++
	}
}

// sortByOrder applies an `order` attribute. Absent means the schema's
// default, Ascending: Vortex validates against the XSD with its defaults
// applied, so a missing attribute sorts there too. An unrecognised value
// keeps document order, as Vortex's parser does.
func sortByOrder[T any](p *parser, order *string, items []T, name func(T) string) {
	mode := "ascending"
	if order != nil {
		mode = strings.ToLower(strings.TrimSpace(*order))
	}
	switch mode {
	case "ascending":
		sort.SliceStable(items, func(i, j int) bool { return compareNames(name(items[i]), name(items[j])) < 0 })
	case "descending":
		sort.SliceStable(items, func(i, j int) bool { return compareNames(name(items[i]), name(items[j])) > 0 })
	case "explicit":
	default:
		p.warnf("unknown order %q: document order kept", *order)
	}
}

// compareNames approximates .NET's culture-aware string comparison, which
// Vortex sorts with: case-insensitive first, then lower-case before
// upper-case, then by code point.
func compareNames(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	// Lower-case sorts before upper-case: invert the byte comparison.
	return strings.Compare(b, a)
}
