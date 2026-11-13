package domain

import (
	"maps"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
)

// FomodStepChoice, FomodGroupChoice and FomodOptionChoice name
// internal/fomod's choice types for a frontend, which builds answers
// (InstallOptions.Fomod and its siblings) but may not import the evaluator
// (#614; cmd/lmm's and internal/serve's import allow-lists).
type (
	// FomodStepChoice is fomod.StepChoice: one step's answers.
	FomodStepChoice = fomod.StepChoice
	// FomodGroupChoice is fomod.GroupChoice: one group's answer.
	FomodGroupChoice = fomod.GroupChoice
	// FomodOptionChoice is fomod.OptionChoice: one picked option.
	FomodOptionChoice = fomod.OptionChoice
)

// FomodChoices is what was chosen in a mod's FOMOD installer (#614): the
// recorded answers a profile carries beside Disabled and Category, so an
// export, an import, a duplicate, a copy and a collection all reproduce the
// same install.
//
// Steps are internal/fomod's choice types, whose JSON form is Vortex's
// collection.json `choices.options` block, so a collection's choices decode
// into it and re-encode byte for byte. ConfigSHA256 is the sha256 of the
// fomod/ModuleConfig.xml the choices were made against: an update whose
// installer changed is found by it, not by the version.
//
// The zero value - no ConfigSHA256 and no Steps - means "no recorded
// choices", and is what every reference written before this field existed
// reads as; both encodings omit it (IsZero).
type FomodChoices struct {
	// ConfigSHA256 is the lower-case hex sha256 of the ModuleConfig.xml
	// these choices answer.
	ConfigSHA256 string `yaml:"config_sha256" json:"config_sha256"`
	// Steps are the answers, step by step (internal/fomod.StepChoice).
	Steps []fomod.StepChoice `yaml:"steps" json:"steps"`
	// FileStates are the fileDependency outcomes the installer consulted
	// when these choices were installed: each tested path (lower-case,
	// '/'-separated) and the state it was in - Missing, Inactive or Active
	// (#614). A conditional install or an option type can depend on them,
	// so they are part of what the installed tree is, and of its cache
	// variant. Omitted when the installer tests no files.
	FileStates map[string]string `yaml:"file_states,omitempty" json:"file_states,omitempty"`
}

// IsZero reports that nothing is recorded. yaml.v3's omitempty and
// encoding/json/v2's omitzero both ask it, so an unrecorded reference
// writes no `fomod` key at all.
func (c FomodChoices) IsZero() bool {
	return c.ConfigSHA256 == "" && len(c.Steps) == 0 && len(c.FileStates) == 0
}

// Equal reports whether c and o record the same choices. A nil list and an
// empty one are the same record: a document read back gives [] where the
// value it was written from may have held nil.
func (c FomodChoices) Equal(o FomodChoices) bool {
	return c.ConfigSHA256 == o.ConfigSHA256 && slices.EqualFunc(c.Steps, o.Steps, stepChoiceEqual) &&
		maps.Equal(c.FileStates, o.FileStates)
}

func stepChoiceEqual(a, b fomod.StepChoice) bool {
	return a.Name == b.Name && intPtrEqual(a.Index, b.Index) && slices.EqualFunc(a.Groups, b.Groups, groupChoiceEqual)
}

func groupChoiceEqual(a, b fomod.GroupChoice) bool {
	return a.Name == b.Name && intPtrEqual(a.Index, b.Index) && slices.EqualFunc(a.Choices, b.Choices, optionChoiceEqual)
}

func optionChoiceEqual(a, b fomod.OptionChoice) bool {
	return a.Name == b.Name && a.Idx == b.Idx && intPtrEqual(a.Index, b.Index)
}

func intPtrEqual(a, b *int) bool {
	return (a == nil) == (b == nil) && (a == nil || *a == *b)
}
