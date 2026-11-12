package fomod

import "errors"

// Limits on untrusted input. A ModuleConfig.xml is author-supplied and
// arrives inside a downloaded archive, so every dimension is capped.
const (
	// MaxConfigSize is the largest ModuleConfig.xml Parse accepts, in bytes.
	// Real installers run to a few hundred KiB.
	MaxConfigSize = 8 << 20
	// MaxDepth is the deepest element nesting Parse accepts. Composite
	// dependencies nest, but real installers stay far below this.
	MaxDepth = 100
	// MaxOps is the most install operations one Evaluate produces. A
	// <folder> expands to every member under it, so a hostile config could
	// otherwise multiply a large archive many times over.
	MaxOps = 1 << 20
	// MaxNodes is the most XML elements Parse accepts. Byte and depth caps
	// alone still let 8 MiB of empty elements decode into millions of
	// nodes and gigabytes of allocation; the largest real installers hold a
	// few tens of thousands of elements, so this leaves ample headroom.
	MaxNodes = 100_000
	// MaxChoices is the most recorded entries (steps, groups and picks
	// together) Evaluate accepts in Request.Choices.
	MaxChoices = 10_000
	// MaxConditionEvals bounds the condition nodes one Evaluate visits. The
	// walk visits each node a handful of times, so a config under MaxNodes
	// stays far below it; it is a backstop, not a working limit.
	MaxConditionEvals = 10_000_000
)

var (
	// ErrScripted refuses an installer lmm will not run: a C# scripted
	// FOMOD (fomod/script.cs with no ModuleConfig.xml) or an OMOD. Both
	// execute author code; lmm never executes anything from an archive.
	ErrScripted = errors.New("fomod: scripted installers (fomod/script.cs, OMOD) are not supported")
	// ErrAmbiguous means an archive holds more than one wrapper directory
	// with its own fomod/ModuleConfig.xml, and none at the root.
	ErrAmbiguous = errors.New("fomod: archive holds more than one installer")
	// ErrNotModuleConfig means the document is not a ModuleConfig: empty,
	// not XML, or rooted at something other than <config>.
	ErrNotModuleConfig = errors.New("fomod: not a ModuleConfig.xml document")
	// ErrTooLarge means an input exceeds a cap: MaxConfigSize or MaxNodes
	// for a document, MaxChoices for a choice set, MaxOps or
	// MaxConditionEvals for an evaluation.
	ErrTooLarge = errors.New("fomod: installer exceeds a size limit")
	// ErrTooDeep means the document nests deeper than MaxDepth.
	ErrTooDeep = errors.New("fomod: ModuleConfig.xml nests too deeply")
	// ErrUnsafePath means an install destination climbs out of the mod
	// root ("..").
	ErrUnsafePath = errors.New("fomod: install destination escapes the mod root")
)

// PrerequisitesError reports a <moduleDependencies> block that is not
// satisfied: the installer refuses to run at all (Vortex's "Installer
// prerequisites not fulfilled").
type PrerequisitesError struct {
	// Unmet describes each failing condition, e.g. `file "skyrim.esm" is not Active`.
	Unmet []string
}

// Error implements error.
func (e *PrerequisitesError) Error() string {
	msg := "fomod: installer prerequisites not met"
	for i, u := range e.Unmet {
		if i == 0 {
			msg += ": "
		} else {
			msg += "; "
		}
		msg += u
	}
	return msg
}

// Config is a parsed fomod/ModuleConfig.xml. Steps, groups and plugins are
// in presentation order (their `order` attributes applied).
type Config struct {
	// ModuleName is the installer's title.
	ModuleName string
	// ModuleImage is the banner image's path inside the archive, as written.
	ModuleImage string
	// Prerequisites is <moduleDependencies>; nil when absent.
	Prerequisites *Condition
	// RequiredFiles is <requiredInstallFiles>, installed unconditionally.
	RequiredFiles []FileItem
	// Steps are the install steps, in presentation order.
	Steps []Step
	// ConditionalInstalls is <conditionalFileInstalls>, evaluated against
	// the final flag state.
	ConditionalInstalls []ConditionalInstall
	// Warnings lists irregularities Parse tolerated (an unknown group type,
	// an unparseable priority, ...), each with the value it fell back to.
	Warnings []string
}

// Step is one <installStep>.
type Step struct {
	// Name is the step's name attribute, untrimmed.
	Name string
	// Ordinal counts earlier steps with the same name, in document order:
	// lmm's disambiguator for non-unique names (see StepChoice.Index).
	Ordinal int
	// Visible is the step's <visible> condition; nil means always visible.
	Visible *Condition
	// Groups are the step's option groups, in presentation order.
	Groups []Group
}

// Group is one <group> of options.
type Group struct {
	// Name is the group's name attribute, untrimmed.
	Name string
	// Ordinal counts earlier same-named groups in the step, in document order.
	Ordinal int
	// Type is the group's selection rule as written.
	Type GroupType
	// Plugins are the group's options, in presentation order.
	Plugins []Plugin
}

// Plugin is one selectable option (<plugin>) in a group.
type Plugin struct {
	// Name is the option's name attribute, untrimmed.
	Name string
	// Ordinal counts earlier same-named options in the group, in document order.
	Ordinal int
	// Description is the option's description text.
	Description string
	// Image is the option's image path inside the archive, as written.
	Image string
	// Files are installed when the option is selected (and, per file,
	// sometimes when it is not: see FileItem.AlwaysInstall).
	Files []FileItem
	// Flags are set while the option is selected.
	Flags []Flag
	// Type decides whether the option is required, recommended, usable...
	Type TypeDescriptor
}

// Flag is one <flag name="...">value</flag> a selected option sets.
type Flag struct {
	// Name is the flag's name; flag names are case-sensitive.
	Name string
	// Value is the flag's value; compared case-sensitively.
	Value string
}

// TypeDescriptor is a plugin's <typeDescriptor>: a static type, or a
// default plus patterns whose first satisfied condition decides.
type TypeDescriptor struct {
	// Default is the static type, or the dependencyType's defaultType.
	Default PluginType
	// Patterns are tried in order; the first satisfied one's Type wins.
	Patterns []TypePattern
}

// TypePattern is one dependencyType pattern.
type TypePattern struct {
	// When is the pattern's condition.
	When Condition
	// Type applies when When is satisfied.
	Type PluginType
}

// ConditionalInstall is one <conditionalFileInstalls> pattern.
type ConditionalInstall struct {
	// When is evaluated against the final flag state.
	When Condition
	// Files are installed when When is satisfied.
	Files []FileItem
}

// FileItem is one <file> or <folder> entry.
type FileItem struct {
	// Source is the archive path, relative to the installer root, as written
	// (backslashes and case included).
	Source string
	// Destination is the install path, relative to the mod root, as written.
	Destination string
	// HasDestination is false when the attribute is absent, which means
	// "same as Source"; an empty Destination that is present means the
	// source's base name at the root (for a folder: its contents at the root).
	HasDestination bool
	// Folder is true for a <folder> entry.
	Folder bool
	// AlwaysInstall installs the item even when its option is not selected.
	AlwaysInstall bool
	// InstallIfUsable installs the item when its option is not selected but
	// is not NotUsable.
	InstallIfUsable bool
	// Priority orders conflicting items within a layer (clamped to int32,
	// as Vortex does).
	Priority int32
}

// GroupType is a group's selection rule.
type GroupType int

// The group selection rules.
const (
	// SelectAny allows any number of options.
	SelectAny GroupType = iota
	// SelectAll selects every option.
	SelectAll
	// SelectExactlyOne requires exactly one option.
	SelectExactlyOne
	// SelectAtMostOne allows zero or one option.
	SelectAtMostOne
	// SelectAtLeastOne requires one or more options.
	SelectAtLeastOne
)

var groupTypeNames = [...]string{"SelectAny", "SelectAll", "SelectExactlyOne", "SelectAtMostOne", "SelectAtLeastOne"}

// String returns the rule's ModuleConfig spelling.
func (g GroupType) String() string {
	if int(g) >= 0 && int(g) < len(groupTypeNames) {
		return groupTypeNames[g]
	}
	return "GroupType(?)"
}

// PluginType is an option's resolved type.
type PluginType int

// The option types.
const (
	// Optional is unselected by default and freely selectable.
	Optional PluginType = iota
	// Required is always selected and cannot be deselected.
	Required
	// Recommended is preselected by defaults.
	Recommended
	// NotUsable is always deselected (unless a recorded choice insists).
	NotUsable
	// CouldBeUsable is selectable, with a warning.
	CouldBeUsable
)

var pluginTypeNames = [...]string{"Optional", "Required", "Recommended", "NotUsable", "CouldBeUsable"}

// String returns the type's ModuleConfig spelling.
func (p PluginType) String() string {
	if int(p) >= 0 && int(p) < len(pluginTypeNames) {
		return pluginTypeNames[p]
	}
	return "PluginType(?)"
}

// ConditionKind discriminates a Condition.
type ConditionKind int

// The condition kinds.
const (
	// ConditionComposite combines Conditions with Operator.
	ConditionComposite ConditionKind = iota
	// ConditionFile tests File's state through FileStates.
	ConditionFile
	// ConditionFlag tests a flag's value.
	ConditionFlag
	// ConditionVersion is a gameDependency, fommDependency or script-extender
	// dependency: lmm cannot check it, so it is satisfied with a warning.
	ConditionVersion
	// ConditionUnknown is an element (or fileDependency state) lmm does not
	// understand: satisfied with a warning.
	ConditionUnknown
)

// Operator combines a composite condition's children.
type Operator int

// The composite operators.
const (
	// OperatorAnd needs every child satisfied (an empty And is satisfied).
	OperatorAnd Operator = iota
	// OperatorOr needs one child satisfied (an empty Or is not).
	OperatorOr
)

// Condition is a compositeDependency tree node.
type Condition struct {
	// Kind says which fields below apply.
	Kind ConditionKind
	// Operator combines Conditions (ConditionComposite).
	Operator Operator
	// Conditions are the composite's children.
	Conditions []Condition
	// File is the tested path, lower-case and '/'-separated (ConditionFile).
	File string
	// State is the state File must be in (ConditionFile).
	State FileState
	// Flag is the tested flag's name (ConditionFlag).
	Flag string
	// Value is the value Flag must hold; empty means "unset" (ConditionFlag).
	Value string
	// Element is the XML element name (ConditionVersion, ConditionUnknown).
	Element string
	// Version is the required version as written (ConditionVersion).
	Version string
}

// FileState is a file's state for a fileDependency.
type FileState int

// The file states.
const (
	// FileMissing means no installed mod and not the game provides the file.
	FileMissing FileState = iota
	// FileInactive means the file is present but not active.
	FileInactive
	// FileActive means the file is present and active.
	FileActive
)

var fileStateNames = [...]string{"Missing", "Inactive", "Active"}

// String returns the state's ModuleConfig spelling.
func (s FileState) String() string {
	if int(s) >= 0 && int(s) < len(fileStateNames) {
		return fileStateNames[s]
	}
	return "FileState(?)"
}

// FileStates answers fileDependency conditions. The caller (core) decides
// what present and active mean for a game; this package only asks.
type FileStates interface {
	// State reports path's state. path is lower-case, '/'-separated and
	// relative to the game's data root, as the ModuleConfig names it.
	State(path string) FileState
}
