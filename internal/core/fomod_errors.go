package core

import (
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
)

// The reasons a FomodQuestion is asked.
const (
	// FomodReasonUnanswered: no choices are recorded for the mod, or the
	// answers given so far leave a visible group unanswered (an installer's
	// later steps appear only once earlier ones are answered).
	FomodReasonUnanswered = "unanswered"
	// FomodReasonStale: the recorded choices no longer fit the installer -
	// a choice selects nothing, a group's rule is broken, or a group the
	// recording never saw is visible now. The installer changed under the
	// recording.
	FomodReasonStale = "stale"
	// FomodReasonReconfigure: the user asked to change a FOMOD mod's
	// choices (`lmm mod fomod`, the web UI's "Reconfigure installer…",
	// #615). The recorded choices still fit; they are the preselection.
	FomodReasonReconfigure = "reconfigure"
)

// RefusalFomodChoicesRequired is the refusal code (ItemFailure.Refusal,
// InstalledRef.Refusal) of an item whose FOMOD installer needs answers
// (#614).
const RefusalFomodChoicesRequired = "fomod_choices_required"

// FomodChoicesRequiredError refuses an install whose FOMOD installer needs
// answers lmm does not have (#614): an archive that turned out, once
// downloaded and extracted, to be an installer with choices the profile
// records none of - or records ones that no longer fit.
//
// It is ANSWERABLE: an install-shaped Apply collects it in its acquire
// phase (answerableRefusal) and refuses with nothing written, keeping the
// downloads (retained_download.go), so the frontend asks and re-runs the
// same Apply with the answers in its Options (Fomod, keyed by
// domain.ModKey). Visibility is stateful - a step can depend on earlier
// answers - so the re-run may ask again, about the steps the answers made
// visible.
//
// Details() is the question document: one FomodQuestion per mod, keyed by
// source and mod id. Several mods' questions in one Apply are merged into
// one error.
type FomodChoicesRequiredError struct {
	// Mods are the questions, one per mod, in item order.
	Mods []FomodQuestion `json:"mods"`
}

// Error names the mods and what the user does next.
func (e *FomodChoicesRequiredError) Error() string {
	names := make([]string, 0, len(e.Mods))
	stale := false
	for _, q := range e.Mods {
		name := q.Name
		if name == "" {
			name = q.SourceID + ":" + q.ModID
		}
		names = append(names, name)
		stale = stale || q.Reason == FomodReasonStale
	}
	verb, what := "has", "an installer (FOMOD) whose choices need answering"
	if len(names) > 1 {
		verb, what = "have", "installers (FOMOD) whose choices need answering"
	}
	msg := fmt.Sprintf("%s %s %s", joinNames(names), verb, what)
	if stale {
		msg += " (the recorded choices no longer fit the installer)"
	}
	return msg + "; answer them and run the same command again"
}

// Details implements the --json error envelope's extension point: the
// question document.
func (e *FomodChoicesRequiredError) Details() any { return e }

// FomodQuestion is one mod's installer, as a frontend asks it: every
// visible step, group and option with its resolved type and current
// selection, and what the recorded choices got wrong.
type FomodQuestion struct {
	// SourceID and ModID identify the mod; the answer goes under
	// domain.ModKey(SourceID, ModID).
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	// Name is the mod's display name.
	Name string `json:"name"`
	// ConfigSHA256 is the sha256 of the ModuleConfig.xml asked about; an
	// answer records it.
	ConfigSHA256 string `json:"config_sha256"`
	// ModuleName is the installer's own title.
	ModuleName string `json:"module_name"`
	// InstallerRoot is the archive directory holding fomod/ ("" at the
	// archive root, or one wrapper directory with a trailing '/'): option
	// images are relative to it.
	InstallerRoot string `json:"installer_root"`
	// Reason is FomodReasonUnanswered, FomodReasonStale or
	// FomodReasonReconfigure.
	Reason string `json:"reason"`
	// Steps are the visible steps, in walk order.
	Steps []FomodQuestionStep `json:"steps"`
	// Choices is the current selection in canonical form - the defaults,
	// or the recorded choices as far as they still apply. A frontend edits
	// it and sends it back as the answer.
	Choices []fomod.StepChoice `json:"choices"`
	// Unanswered are the visible groups no answer covers yet.
	Unanswered []FomodGroupRef `json:"unanswered,omitempty"`
	// Unmatched are recorded choices that selected nothing.
	Unmatched []FomodUnmatchedChoice `json:"unmatched,omitempty"`
	// Violations are groups whose recorded selection breaks their rule.
	Violations []FomodRuleViolation `json:"violations,omitempty"`
}

// FomodQuestionStep is one visible installer step.
type FomodQuestionStep struct {
	// Name is the step's name, untrimmed.
	Name string `json:"name"`
	// Ordinal counts earlier same-named steps (an answer's index, when
	// names repeat).
	Ordinal int `json:"ordinal"`
	// Groups are the step's option groups.
	Groups []FomodQuestionGroup `json:"groups"`
}

// FomodQuestionGroup is one group of options.
type FomodQuestionGroup struct {
	// Name is the group's name, untrimmed.
	Name string `json:"name"`
	// Ordinal counts earlier same-named groups in the step.
	Ordinal int `json:"ordinal"`
	// Type is the group's effective rule: SelectAny, SelectAll,
	// SelectExactlyOne, SelectAtMostOne or SelectAtLeastOne.
	Type string `json:"type"`
	// Answered says whether the choices answered this group.
	Answered bool `json:"answered"`
	// Options are the group's options.
	Options []FomodQuestionOption `json:"options"`
}

// FomodQuestionOption is one option.
type FomodQuestionOption struct {
	// Name is the option's name, untrimmed.
	Name string `json:"name"`
	// Ordinal counts earlier same-named options in the group.
	Ordinal int `json:"ordinal"`
	// Description is the option's description text.
	Description string `json:"description,omitempty"`
	// Image is the option's image, relative to InstallerRoot, as written.
	Image string `json:"image,omitempty"`
	// Type is the option's resolved type: Required, Recommended, Optional,
	// NotUsable or CouldBeUsable.
	Type string `json:"type"`
	// Selected is the option's current selection.
	Selected bool `json:"selected"`
}

// FomodGroupRef names a group.
type FomodGroupRef struct {
	// Step and Group are the names.
	Step  string `json:"step"`
	Group string `json:"group"`
}

// FomodUnmatchedChoice is a recorded choice that selected nothing.
type FomodUnmatchedChoice struct {
	// Step, Group and Option are the recorded names, as far as they go.
	Step   string `json:"step"`
	Group  string `json:"group,omitempty"`
	Option string `json:"option,omitempty"`
	// Reason is which level failed to match: "no such step", "no such
	// group", "no such option" or "step not visible".
	Reason string `json:"reason"`
}

// FomodRuleViolation is a group whose selection breaks its rule.
type FomodRuleViolation struct {
	// Step and Group name the group.
	Step  string `json:"step"`
	Group string `json:"group"`
	// Type is the group's rule.
	Type string `json:"type"`
	// Selected is how many options are selected.
	Selected int `json:"selected"`
}

// FomodInstallError refuses a FOMOD install no answer fixes (#614): a
// scripted installer or an OMOD (never executed), an archive with more than
// one installer, an installer that cannot be read or whose prerequisites
// fail, or recorded choices the archive cannot reproduce (a source that
// matches nothing, an unsafe member).
type FomodInstallError struct {
	// SourceID, ModID and Name identify the mod.
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name"`
	// Reason is the refusal, as a sentence fragment.
	Reason string `json:"reason"`
	err    error
}

// Error states the refusal.
func (e *FomodInstallError) Error() string {
	name := e.Name
	if name == "" {
		name = e.SourceID + ":" + e.ModID
	}
	return fmt.Sprintf("cannot install %s: %s", name, e.Reason)
}

// Unwrap exposes internal/fomod's own error, when there is one.
func (e *FomodInstallError) Unwrap() error { return e.err }

// Details implements the --json error envelope's extension point.
func (e *FomodInstallError) Details() any { return e }

// mergeFomodQuestions folds every *FomodChoicesRequiredError among refusals
// into one, in item order: ONE question document per Apply. ok is false
// when there is none.
func mergeFomodQuestions(refusals []AcquireRefusal) (*FomodChoicesRequiredError, bool) {
	var merged *FomodChoicesRequiredError
	for _, r := range refusals {
		var q *FomodChoicesRequiredError
		if !asFomodQuestion(r.Err, &q) {
			continue
		}
		if merged == nil {
			merged = &FomodChoicesRequiredError{}
		}
		merged.Mods = append(merged.Mods, q.Mods...)
	}
	return merged, merged != nil
}

// asFomodQuestion is errors.As for *FomodChoicesRequiredError.
func asFomodQuestion(err error, target **FomodChoicesRequiredError) bool {
	return errors.As(err, target)
}

// ReasonFomodReanswer is why a flow that cannot ask - the update batch,
// automatic updates, a deploy's self-heal - skips a FOMOD mod whose
// recorded choices no longer fit its installer (#614): the user answers
// them again, and the mod installs then.
const ReasonFomodReanswer = "installer choices need re-answering"
