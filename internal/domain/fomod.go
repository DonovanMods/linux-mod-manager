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

// FomodFiles is a mod's FOMOD installer choices recorded PER FILE (#634),
// keyed by the source's file id: the form a mod takes when two or more of
// its pinned files each carry their own installer (a collection that pins
// a mod's main file and a patch file, each a FOMOD). Each value is one
// installer's choices, exactly as FomodChoices records them for a mod with
// one installer.
type FomodFiles map[string]FomodChoices

// IsZero reports that no file's choices are recorded; both encodings omit
// it then.
func (f FomodFiles) IsZero() bool {
	for _, c := range f {
		if !c.IsZero() {
			return false
		}
	}
	return true
}

// Equal reports whether f and o record the same choices for the same
// files. A file recorded with zero choices is the same as one not recorded.
func (f FomodFiles) Equal(o FomodFiles) bool {
	for id, c := range f {
		if !c.Equal(o[id]) {
			return false
		}
	}
	for id, c := range o {
		if _, ok := f[id]; !ok && !c.IsZero() {
			return false
		}
	}
	return true
}

// Clone is a copy of f whose map can be changed without changing f's; nil
// for a zero f.
func (f FomodFiles) Clone() FomodFiles {
	if f.IsZero() {
		return nil
	}
	out := make(FomodFiles, len(f))
	for id, c := range f {
		if !c.IsZero() {
			out[id] = c
		}
	}
	return out
}

// FomodRecord is everything a mod's reference records about its FOMOD
// installers (#634): one installer's choices (Choices, the profile's
// `fomod:` block), or one per file (Files, its `fomod_files:` block) for a
// mod with several. A flow that carries "the recorded choices" - a
// rollback's previous ones, a profile write's - carries this, so both forms
// travel together. Files, when set, is the record: an install never writes
// both.
type FomodRecord struct {
	// Choices is the single installer's choices.
	Choices FomodChoices
	// Files is the per-file form.
	Files FomodFiles
}

// IsZero reports that nothing is recorded in either form.
func (r FomodRecord) IsZero() bool { return r.Choices.IsZero() && r.Files.IsZero() }

// PerFile reports that the record is the per-file form.
func (r FomodRecord) PerFile() bool { return !r.Files.IsZero() }

// Equal reports whether r and o record the same choices.
func (r FomodRecord) Equal(o FomodRecord) bool {
	return r.Choices.Equal(o.Choices) && r.Files.Equal(o.Files)
}

// FomodFileKey is the answer key for ONE file's installer of a mod with
// several (#634): domain.ModKey, then '#', then the file id - for example
// "nexusmods:12604#37471". An Apply's Options.Fomod takes it beside the
// plain mod key, which still answers a mod whose installer is one file.
func FomodFileKey(sourceID, modID, fileID string) string {
	return ModKey(sourceID, modID) + "#" + fileID
}
