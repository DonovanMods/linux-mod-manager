package fomod

// StepChoice records the answers for one install step. Its JSON form is
// Vortex's collection.json `choices.options` entry
// ({name, groups: [{name, choices: [{name, idx}]}]}), so a collection's
// recorded choices decode straight into []StepChoice, plus lmm's own
// optional `index` keys, which are omitted when unset so a Vortex document
// round-trips byte for byte.
//
// The yaml tags spell the same keys, so the profile document (#614's
// `fomod:` block) records a choice set exactly as a collection does.
//
// Names are matched exactly: never trimmed, never case-folded (a real
// collection records " Spicy Clean Hacking Screen" with its leading space,
// and a step named "").
type StepChoice struct {
	// Name is the step's name.
	Name string `json:"name" yaml:"name"`
	// Index, when set, matches only the step with this Ordinal among
	// same-named steps. Nil matches every step of that name, as Vortex does.
	Index *int `json:"index,omitempty" yaml:"index,omitempty"`
	// Groups are the answered groups. A group absent here is unanswered.
	Groups []GroupChoice `json:"groups" yaml:"groups"`
}

// GroupChoice records the answer for one group.
type GroupChoice struct {
	// Name is the group's name.
	Name string `json:"name" yaml:"name"`
	// Index, when set, matches only the group with this Ordinal among
	// same-named groups in the step. Nil matches every group of that name.
	Index *int `json:"index,omitempty" yaml:"index,omitempty"`
	// Choices are the picked options. The group's presence is the answer:
	// an empty (or nil) list means "pick nothing", not "no answer".
	Choices []OptionChoice `json:"choices" yaml:"choices"`
}

// OptionChoice records one picked option.
type OptionChoice struct {
	// Name is the option's name.
	Name string `json:"name" yaml:"name"`
	// Idx is Vortex's idx: the option's position in its group as the
	// installer presented it. It is carried for round-tripping and never
	// compared, exactly as Vortex never compares it.
	Idx int `json:"idx" yaml:"idx"`
	// Index, when set, matches only the option with this Ordinal among
	// same-named options in the group. Nil matches every option of that name.
	Index *int `json:"index,omitempty" yaml:"index,omitempty"`
}

// matches reports whether a recorded name and optional index select an
// item with the given name and ordinal.
func matches(name string, index *int, itemName string, itemOrdinal int) bool {
	return name == itemName && (index == nil || *index == itemOrdinal)
}

// GroupRef names a group in a result.
type GroupRef struct {
	// Step is the step's name.
	Step string
	// StepOrdinal is the step's Ordinal among same-named steps.
	StepOrdinal int
	// Group is the group's name.
	Group string
	// GroupOrdinal is the group's Ordinal among same-named groups.
	GroupOrdinal int
}

// UnmatchedReason says why a recorded choice selected nothing.
type UnmatchedReason int

// The reasons a recorded choice can fail to match.
const (
	// NoSuchStep: no step has the recorded name (and index).
	NoSuchStep UnmatchedReason = iota
	// NoSuchGroup: the step exists, but no group in it matches.
	NoSuchGroup
	// NoSuchOption: the group exists, but no option in it matches.
	NoSuchOption
	// StepNotVisible: the step exists but was hidden under the flags these
	// choices produced, so its answers were not applied.
	StepNotVisible
)

var unmatchedReasonNames = [...]string{"no such step", "no such group", "no such option", "step not visible"}

// String describes the reason.
func (r UnmatchedReason) String() string {
	if int(r) >= 0 && int(r) < len(unmatchedReasonNames) {
		return unmatchedReasonNames[r]
	}
	return "UnmatchedReason(?)"
}

// Unmatched is a recorded choice that selected nothing. Vortex skips these
// silently; lmm reports them, because a stale choice means the installer
// changed under the recording.
type Unmatched struct {
	// Step is the recorded step name.
	Step string
	// StepIndex is the recorded step index, if any.
	StepIndex *int
	// Group is the recorded group name; empty for NoSuchStep and StepNotVisible.
	Group string
	// GroupIndex is the recorded group index, if any.
	GroupIndex *int
	// Option is the recorded option name; set only for NoSuchOption.
	Option string
	// OptionIndex is the recorded option index, if any.
	OptionIndex *int
	// Reason says which level failed to match.
	Reason UnmatchedReason
}

// Violation is a visible group whose final selection breaks its rule
// (e.g. two picks in a SelectExactlyOne group). Vortex's headless replay
// does not check this; lmm reports it and leaves the policy to the caller.
type Violation struct {
	// Step is the step's name.
	Step string
	// Group is the group's name.
	Group string
	// Type is the group's effective rule.
	Type GroupType
	// Selected is how many options ended up selected.
	Selected int
}
