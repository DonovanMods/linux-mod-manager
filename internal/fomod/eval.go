package fomod

import (
	"fmt"
)

// Mode says how Evaluate treats a visible group the choice set does not answer.
type Mode int

// The evaluation modes.
const (
	// ModeReplay is Vortex's headless replay of recorded choices: an
	// unanswered group gets only its Required options, every option of a
	// SelectAll group, and the first option of a SelectExactlyOne group.
	// Recommended options are not preselected. A collection replay uses it.
	ModeReplay Mode = iota
	// ModeDefaults is Vortex's dialog preselection (lmm's --fomod-defaults,
	// and the preselection a question shows): an unanswered group also gets
	// its Recommended options. Answered groups are treated as in ModeReplay.
	ModeDefaults
)

// Request is one evaluation's input.
type Request struct {
	// Choices are the recorded answers; nil answers nothing.
	Choices []StepChoice
	// Mode is how unanswered groups are filled.
	Mode Mode
	// Files answers fileDependency conditions. Nil means every file is
	// Missing (not Vortex's "every fileDependency is false", under which
	// even a Missing test fails).
	Files FileStates
	// Members are the archive's file members relative to the installer
	// root (Installer.Rel), any separator, any case. Directory entries are
	// ignored. Sources are matched against them; nil matches nothing.
	Members []string
}

// Result is one evaluation's output: what to install, and everything a
// caller needs to decide whether to install it or to ask.
type Result struct {
	// Ops are the install operations, one per destination (conflicts already
	// resolved), sorted by destination case-insensitively.
	Ops []Op
	// Steps are the visible steps, in walk order, with each option's
	// resolved type and final selection: the raw material of a question.
	Steps []StepState
	// Choices is the selection actually made, in canonical form (every
	// visible group present, Index set only where a name repeats among its
	// siblings, Idx the option's position in its group). Recording it and
	// replaying it reproduces this result.
	Choices []StepChoice
	// Unanswered are visible groups the choice set does not answer and
	// whose selection the user could change (not SelectAll, and with an
	// option that is neither Required nor NotUsable).
	Unanswered []GroupRef
	// Unmatched are recorded choices that selected nothing.
	Unmatched []Unmatched
	// UnsafeMembers are Request.Members dropped before matching, as given:
	// a ".." segment, an absolute or drive-letter path, or a NUL. Dropping
	// one never fails the evaluation; whether it should block an install is
	// the caller's call.
	UnsafeMembers []string
	// Violations are visible groups whose selection breaks their rule.
	Violations []Violation
	// MissingSources are file and folder entries that match no member.
	MissingSources []MissingSource
	// Warnings are conditions lmm assumed (game and script-extender
	// versions, unknown dependency elements) and Vortex quirks applied.
	Warnings []string
}

// StepState is a visible step as the walk left it.
type StepState struct {
	// Name is the step's name.
	Name string
	// Ordinal is the step's Ordinal among same-named steps.
	Ordinal int
	// Groups are the step's groups.
	Groups []GroupState
}

// GroupState is a group as the walk left it.
type GroupState struct {
	// Name is the group's name.
	Name string
	// Ordinal is the group's Ordinal among same-named groups.
	Ordinal int
	// Type is the group's effective rule: a group with more than one
	// Required option is relaxed (SelectExactlyOne to SelectAtLeastOne,
	// SelectAtMostOne to SelectAny), as Vortex does.
	Type GroupType
	// Answered says whether the choice set answered this group.
	Answered bool
	// Options are the group's options.
	Options []OptionState
}

// OptionState is an option as the walk left it.
type OptionState struct {
	// Name is the option's name.
	Name string
	// Ordinal is the option's Ordinal among same-named options.
	Ordinal int
	// Description is the option's description.
	Description string
	// Image is the option's image path inside the archive, as written.
	Image string
	// Type is the option's resolved type when its step was walked. A
	// recorded choice of a NotUsable option makes it CouldBeUsable, as
	// Vortex does, so a recording can always be honoured.
	Type PluginType
	// Selected is the option's final selection.
	Selected bool
}

type optKey struct{ step, group, plugin int }

type flagValue struct {
	value string
	owner optKey
}

type evaluator struct {
	cfg   *Config
	files FileStates
	flags map[string]flagValue
	// owned lists the flag names each option set, so deselecting it costs
	// what it owns, not every flag.
	owned    map[optKey][]string
	selected map[optKey]bool
	// selectedIn indexes selected options by group, so clearing a group
	// costs its selection, not its size.
	selectedIn map[[2]int]map[int]bool
	override   map[optKey]PluginType
	groupType  map[[2]int]GroupType
	warned     map[warnKey]bool
	warnings   []string
	// used counts condition nodes visited; past limit (MaxConditionEvals
	// when zero) every test fails and err is set.
	used, limit int
	err         error
}

// warnKey dedupes warnings before any message is formatted: a condition
// evaluated thousands of times costs a map lookup, not a Sprintf.
type warnKey struct {
	kind             ConditionKind
	element, version string
}

// Evaluate walks cfg with a choice set the way Vortex's installer does,
// and resolves the outcome into install operations.
//
// Steps are walked in order. A step whose <visible> condition fails under
// the flags set so far is skipped. In a visible step each group is
// preselected (from the choices, or by Mode), then Required options are
// forced on and NotUsable ones off. Selecting an option sets its flags;
// deselecting it clears the flags it still owns. Install operations come
// from required files, selected options (plus unselected options' files
// marked alwaysInstall or installIfUsable, in every step), and the
// conditional installs whose conditions hold under the final flags.
//
// A failing <moduleDependencies> is a *PrerequisitesError. Evaluate also
// fails with ErrUnsafePath for a destination outside the mod root and
// ErrTooLarge past MaxOps; everything else (unanswered groups, unmatched
// choices, rule violations, missing sources) is reported on the Result for
// the caller to judge.
func Evaluate(cfg *Config, req Request) (*Result, error) {
	if cfg == nil {
		return nil, ErrNotModuleConfig
	}
	if n := countChoices(req.Choices); n > MaxChoices {
		return nil, fmt.Errorf("%w: %d recorded choices, more than %d", ErrTooLarge, n, MaxChoices)
	}
	e := &evaluator{
		cfg:        cfg,
		files:      req.Files,
		flags:      map[string]flagValue{},
		owned:      map[optKey][]string{},
		selected:   map[optKey]bool{},
		selectedIn: map[[2]int]map[int]bool{},
		override:   map[optKey]PluginType{},
		groupType:  map[[2]int]GroupType{},
		warned:     map[warnKey]bool{},
	}

	if cfg.Prerequisites != nil && !e.test(cfg.Prerequisites) {
		if e.err != nil {
			return nil, e.err
		}
		return nil, &PrerequisitesError{Unmet: e.unmet(cfg.Prerequisites)}
	}
	e.fixSteps()

	res := &Result{}
	visible := make([]bool, len(cfg.Steps))
	for si := range cfg.Steps {
		step := &cfg.Steps[si]
		if step.Visible != nil && !e.test(step.Visible) {
			continue
		}
		visible[si] = true
		presets := stepPresets(req.Choices, step)
		answered := make([]bool, len(step.Groups))
		for gi := range step.Groups {
			gp := groupPresets(presets, &step.Groups[gi])
			answered[gi] = len(gp) > 0
			e.preselect(si, gi, gp, answered[gi] || req.Mode == ModeReplay)
		}
		e.fixSelected(si)
		res.Steps = append(res.Steps, e.snapshot(si, answered))
		if e.err != nil {
			return nil, e.err
		}
	}

	r := newResolver(req.Members, e.warn)
	if err := e.collect(r); err != nil {
		return nil, err
	}
	if e.err != nil {
		return nil, e.err
	}
	res.Ops = r.result()
	res.MissingSources = r.missing
	res.UnsafeMembers = r.unsafe

	res.Choices = canonicalChoices(cfg, res.Steps)
	res.Unanswered, res.Violations = groupFindings(res.Steps)
	res.Unmatched = unmatched(cfg, req.Choices, visible)
	res.Warnings = e.warnings
	return res, nil
}

// fixSteps relaxes a group with more than one Required option (under the
// initial, flagless state), as Vortex does before walking.
func (e *evaluator) fixSteps() {
	for si := range e.cfg.Steps {
		for gi := range e.cfg.Steps[si].Groups {
			g := &e.cfg.Steps[si].Groups[gi]
			required := 0
			for pi := range g.Plugins {
				if e.typeOf(optKey{si, gi, pi}) == Required {
					required++
				}
			}
			t := g.Type
			switch {
			case required > 1 && t == SelectAtMostOne:
				t = SelectAny
			case required > 1 && t == SelectExactlyOne:
				t = SelectAtLeastOne
			}
			e.groupType[[2]int{si, gi}] = t
		}
	}
}

func (e *evaluator) plugin(k optKey) *Plugin {
	return &e.cfg.Steps[k.step].Groups[k.group].Plugins[k.plugin]
}

// typeOf resolves an option's type under the current flags: a forced
// override, else the first satisfied pattern, else the default.
func (e *evaluator) typeOf(k optKey) PluginType {
	if t, ok := e.override[k]; ok {
		return t
	}
	p := e.plugin(k)
	for i := range p.Type.Patterns {
		if e.test(&p.Type.Patterns[i].When) {
			return p.Type.Patterns[i].Type
		}
	}
	return p.Type.Default
}

func (e *evaluator) enable(k optKey) {
	e.owned[k] = e.owned[k][:0]
	e.selected[k] = true
	g := [2]int{k.step, k.group}
	if e.selectedIn[g] == nil {
		e.selectedIn[g] = map[int]bool{}
	}
	e.selectedIn[g][k.plugin] = true
	for _, f := range e.plugin(k).Flags {
		e.flags[f.Name] = flagValue{value: f.Value, owner: k}
		e.owned[k] = append(e.owned[k], f.Name)
	}
}

func (e *evaluator) disable(k optKey) {
	if !e.selected[k] {
		// An unselected option owns no flags: enable is the only setter.
		return
	}
	delete(e.selected, k)
	delete(e.selectedIn[[2]int{k.step, k.group}], k.plugin)
	for _, name := range e.owned[k] {
		if v, ok := e.flags[name]; ok && v.owner == k {
			delete(e.flags, name)
		}
	}
	delete(e.owned, k)
}

// clearGroup deselects every selected option in a group.
func (e *evaluator) clearGroup(si, gi int) {
	for pi := range e.selectedIn[[2]int{si, gi}] {
		e.disable(optKey{si, gi, pi})
	}
}

// preselect is Vortex's per-group preselection. presets are the recorded
// answers for this group (empty if unanswered); presetMode is true when
// Recommended must not be preselected (the group is answered, or the walk
// is a replay).
func (e *evaluator) preselect(si, gi int, presets []*GroupChoice, presetMode bool) {
	g := &e.cfg.Steps[si].Groups[gi]
	gtype := e.groupType[[2]int{si, gi}]
	answered := len(presets) > 0
	picks := indexPicks(presets)
	setFirst := gtype == SelectExactlyOne
	for pi := range g.Plugins {
		k := optKey{si, gi, pi}
		typ := e.typeOf(k)
		isPreset := picks.has(g.Plugins[pi].Name, g.Plugins[pi].Ordinal)
		if isPreset && typ == NotUsable {
			// A recorded pick of an unusable option is honoured.
			e.override[k] = CouldBeUsable
			typ = CouldBeUsable
		}
		if typ == Required || (!presetMode && typ == Recommended) || gtype == SelectAll || isPreset {
			if !presetMode && typ == Recommended && (gtype == SelectExactlyOne || gtype == SelectAtMostOne) {
				// Several recommended in a one-pick group: clear the group
				// so the preselection stays valid.
				e.clearGroup(si, gi)
			}
			setFirst = false
			if answered && !isPreset {
				e.disable(k)
			} else {
				e.enable(k)
			}
		}
	}
	if setFirst && len(g.Plugins) > 0 {
		e.enable(optKey{si, gi, 0})
	}
}

// fixSelected forces a step's Required options on and NotUsable ones off.
func (e *evaluator) fixSelected(si int) {
	for gi := range e.cfg.Steps[si].Groups {
		for pi := range e.cfg.Steps[si].Groups[gi].Plugins {
			k := optKey{si, gi, pi}
			switch e.typeOf(k) {
			case Required:
				e.enable(k)
			case NotUsable:
				e.disable(k)
			}
		}
	}
}

func (e *evaluator) snapshot(si int, answered []bool) StepState {
	step := &e.cfg.Steps[si]
	st := StepState{Name: step.Name, Ordinal: step.Ordinal}
	for gi := range step.Groups {
		g := &step.Groups[gi]
		gs := GroupState{Name: g.Name, Ordinal: g.Ordinal, Type: e.groupType[[2]int{si, gi}], Answered: answered[gi]}
		for pi := range g.Plugins {
			p := &g.Plugins[pi]
			k := optKey{si, gi, pi}
			gs.Options = append(gs.Options, OptionState{
				Name:        p.Name,
				Ordinal:     p.Ordinal,
				Description: p.Description,
				Image:       p.Image,
				Type:        e.typeOf(k),
				Selected:    e.selected[k],
			})
		}
		st.Groups = append(st.Groups, gs)
	}
	return st
}

// collect feeds every install item to the resolver in Vortex's layers.
func (e *evaluator) collect(r *resolver) error {
	const offset = 1_000_000_000
	for _, f := range e.cfg.RequiredFiles {
		if err := r.install(f, -offset); err != nil {
			return err
		}
	}
	for si := range e.cfg.Steps {
		for gi := range e.cfg.Steps[si].Groups {
			for pi := range e.cfg.Steps[si].Groups[gi].Plugins {
				k := optKey{si, gi, pi}
				sel := e.selected[k]
				// The option's type is resolved at most once here, however
				// many installIfUsable files it carries.
				usable, resolved := false, false
				for _, f := range e.plugin(k).Files {
					if !sel && !f.AlwaysInstall && f.InstallIfUsable && !resolved {
						usable, resolved = e.typeOf(k) != NotUsable, true
					}
					if sel || f.AlwaysInstall || (f.InstallIfUsable && usable) {
						if err := r.install(f, 0); err != nil {
							return err
						}
					}
				}
			}
		}
	}
	for i := range e.cfg.ConditionalInstalls {
		ci := &e.cfg.ConditionalInstalls[i]
		if !e.test(&ci.When) {
			continue
		}
		for _, f := range ci.Files {
			if err := r.install(f, offset); err != nil {
				return err
			}
		}
	}
	return nil
}

// warn records a resolver message once.
func (e *evaluator) warn(msg string) {
	e.warnOnce(warnKey{kind: -1, element: msg}, func() string { return msg })
}

// warnOnce records the message for key the first time key is seen; format
// runs only then.
func (e *evaluator) warnOnce(key warnKey, format func() string) {
	if e.warned[key] {
		return
	}
	e.warned[key] = true
	e.warnings = append(e.warnings, format())
}

// test evaluates a condition under the current flags. Composites short
// circuit (an And stops at its first false child, an Or at its first true
// one); Vortex evaluates every child, which changes nothing but which
// assumed dependencies are warned about. Every node visited counts toward
// MaxConditionEvals; past it, test fails and e.err is set.
func (e *evaluator) test(c *Condition) bool {
	limit := e.limit
	if limit == 0 {
		limit = MaxConditionEvals
	}
	if e.err != nil {
		return false
	}
	e.used++
	if e.used > limit {
		e.err = fmt.Errorf("%w: more than %d condition evaluations", ErrTooLarge, limit)
		return false
	}
	switch c.Kind {
	case ConditionComposite:
		and := c.Operator == OperatorAnd
		for i := range c.Conditions {
			if e.test(&c.Conditions[i]) != and {
				return !and
			}
		}
		return and
	case ConditionFile:
		state := FileMissing
		if e.files != nil {
			state = e.files.State(c.File)
		}
		return state == c.State
	case ConditionFlag:
		v, ok := e.flags[c.Flag]
		if c.Value == "" {
			return !ok || v.value == ""
		}
		return ok && v.value == c.Value
	case ConditionVersion:
		e.warnOnce(warnKey{kind: c.Kind, element: c.Element, version: c.Version}, func() string {
			return fmt.Sprintf("%s (version %s) is not checked: treated as satisfied", c.Element, c.Version)
		})
		return true
	default:
		e.warnOnce(warnKey{kind: c.Kind, element: c.Element}, func() string {
			return fmt.Sprintf("%s is not understood: treated as satisfied", c.Element)
		})
		return true
	}
}

// unmet describes the leaves that make a failing condition fail.
func (e *evaluator) unmet(c *Condition) []string {
	if e.test(c) {
		return nil
	}
	switch c.Kind {
	case ConditionComposite:
		var out []string
		for i := range c.Conditions {
			out = append(out, e.unmet(&c.Conditions[i])...)
		}
		return out
	case ConditionFile:
		return []string{fmt.Sprintf("file %q is not %s", c.File, c.State)}
	case ConditionFlag:
		return []string{fmt.Sprintf("flag %q is not %q", c.Flag, c.Value)}
	}
	return nil
}

func stepPresets(choices []StepChoice, step *Step) []*StepChoice {
	var out []*StepChoice
	for i := range choices {
		if matches(choices[i].Name, choices[i].Index, step.Name, step.Ordinal) {
			out = append(out, &choices[i])
		}
	}
	return out
}

func groupPresets(steps []*StepChoice, g *Group) []*GroupChoice {
	var out []*GroupChoice
	for _, s := range steps {
		for i := range s.Groups {
			if matches(s.Groups[i].Name, s.Groups[i].Index, g.Name, g.Ordinal) {
				out = append(out, &s.Groups[i])
			}
		}
	}
	return out
}

// canonicalChoices records the walk's selection.
func canonicalChoices(cfg *Config, steps []StepState) []StepChoice {
	stepNames := map[string]int{}
	for _, s := range cfg.Steps {
		stepNames[s.Name]++
	}
	var out []StepChoice
	for _, s := range steps {
		sc := StepChoice{Name: s.Name, Index: ordinalIfRepeated(stepNames[s.Name], s.Ordinal), Groups: []GroupChoice{}}
		groupNames := map[string]int{}
		for _, g := range s.Groups {
			groupNames[g.Name]++
		}
		for _, g := range s.Groups {
			gc := GroupChoice{Name: g.Name, Index: ordinalIfRepeated(groupNames[g.Name], g.Ordinal), Choices: []OptionChoice{}}
			optNames := map[string]int{}
			for _, o := range g.Options {
				optNames[o.Name]++
			}
			for idx, o := range g.Options {
				if o.Selected {
					gc.Choices = append(gc.Choices, OptionChoice{Name: o.Name, Idx: idx, Index: ordinalIfRepeated(optNames[o.Name], o.Ordinal)})
				}
			}
			sc.Groups = append(sc.Groups, gc)
		}
		out = append(out, sc)
	}
	return out
}

func ordinalIfRepeated(count, ordinal int) *int {
	if count < 2 {
		return nil
	}
	return &ordinal
}

// groupFindings lists the unanswered groups a user could answer and the
// groups whose selection breaks a rule the user could have kept.
func groupFindings(steps []StepState) ([]GroupRef, []Violation) {
	var unanswered []GroupRef
	var violations []Violation
	for _, s := range steps {
		for _, g := range s.Groups {
			free, usable, picked := false, 0, 0
			for _, o := range g.Options {
				if o.Type != Required && o.Type != NotUsable {
					free = true
				}
				if o.Type != NotUsable {
					usable++
				}
				if o.Selected {
					picked++
				}
			}
			if !g.Answered && free && g.Type != SelectAll {
				unanswered = append(unanswered, GroupRef{Step: s.Name, StepOrdinal: s.Ordinal, Group: g.Name, GroupOrdinal: g.Ordinal})
			}
			broken := false
			switch g.Type {
			case SelectExactlyOne:
				broken = picked > 1 || (picked == 0 && usable > 0)
			case SelectAtMostOne:
				broken = picked > 1
			case SelectAtLeastOne:
				broken = picked == 0 && usable > 0
			}
			if broken {
				violations = append(violations, Violation{Step: s.Name, Group: g.Name, Type: g.Type, Selected: picked})
			}
		}
	}
	return unanswered, violations
}

// unmatched lists the recorded choices that selected nothing.
func unmatched(cfg *Config, choices []StepChoice, visible []bool) []Unmatched {
	var out []Unmatched
	for _, sc := range choices {
		var steps []int
		anyVisible := false
		for si := range cfg.Steps {
			if matches(sc.Name, sc.Index, cfg.Steps[si].Name, cfg.Steps[si].Ordinal) {
				steps = append(steps, si)
				anyVisible = anyVisible || visible[si]
			}
		}
		if len(steps) == 0 {
			out = append(out, Unmatched{Step: sc.Name, StepIndex: sc.Index, Reason: NoSuchStep})
			continue
		}
		if !anyVisible {
			out = append(out, Unmatched{Step: sc.Name, StepIndex: sc.Index, Reason: StepNotVisible})
			continue
		}
		for _, gc := range sc.Groups {
			var groups []*Group
			for _, si := range steps {
				if !visible[si] {
					continue
				}
				for gi := range cfg.Steps[si].Groups {
					g := &cfg.Steps[si].Groups[gi]
					if matches(gc.Name, gc.Index, g.Name, g.Ordinal) {
						groups = append(groups, g)
					}
				}
			}
			if len(groups) == 0 {
				out = append(out, Unmatched{Step: sc.Name, StepIndex: sc.Index, Group: gc.Name, GroupIndex: gc.Index, Reason: NoSuchGroup})
				continue
			}
			ordinals := map[string]map[int]bool{} // option name -> ordinals present
			for _, g := range groups {
				for pi := range g.Plugins {
					p := &g.Plugins[pi]
					if ordinals[p.Name] == nil {
						ordinals[p.Name] = map[int]bool{}
					}
					ordinals[p.Name][p.Ordinal] = true
				}
			}
			for _, oc := range gc.Choices {
				present := ordinals[oc.Name]
				found := len(present) > 0 && (oc.Index == nil || present[*oc.Index])
				if !found {
					out = append(out, Unmatched{
						Step: sc.Name, StepIndex: sc.Index, Group: gc.Name, GroupIndex: gc.Index,
						Option: oc.Name, OptionIndex: oc.Index, Reason: NoSuchOption,
					})
				}
			}
		}
	}
	return out
}

// picks indexes a group's recorded picks by name: the ordinals a pick
// names, or "any" for a pick with no index.
type picks map[string]*pickSet

type pickSet struct {
	any      bool
	ordinals map[int]bool
}

func indexPicks(presets []*GroupChoice) picks {
	out := picks{}
	for _, gp := range presets {
		for _, c := range gp.Choices {
			ps := out[c.Name]
			if ps == nil {
				ps = &pickSet{ordinals: map[int]bool{}}
				out[c.Name] = ps
			}
			if c.Index == nil {
				ps.any = true
			} else {
				ps.ordinals[*c.Index] = true
			}
		}
	}
	return out
}

func (p picks) has(name string, ordinal int) bool {
	ps := p[name]
	return ps != nil && (ps.any || ps.ordinals[ordinal])
}

// countChoices counts a choice set's entries: steps, groups and picks.
func countChoices(steps []StepChoice) int {
	n := len(steps)
	for _, s := range steps {
		n += len(s.Groups)
		for _, g := range s.Groups {
			n += len(g.Choices)
		}
	}
	return n
}
