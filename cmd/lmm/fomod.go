package main

// fomod.go answers FOMOD installer questions (#615, design
// docs/plans/2026-11-10-collections-design.md §4 "Plan/Apply without
// callbacks"). Core never calls back into the CLI: an install-shaped Apply
// that meets an installer it has no answers for refuses with
// *core.FomodChoicesRequiredError (or, for a batch, reports it on its
// result), having written nothing, and the command re-runs the same Apply
// with the answers in its Options.Fomod. fomodAnswerer is the one place
// that turns a question document into answers, for every command that can
// raise one:
//
//   - --fomod-choices <file.json>: answers from a file, in the profile's
//     `fomod:` shape or Vortex's bare `choices` array, keyed by mod
//     (source:id) when several mods ask - or, for a mod whose files each
//     carry their own installer, by file (source:id#file-id, #634).
//   - --fomod-defaults: the installer's own preselection (required and
//     recommended options, the first of an exactly-one group).
//   - neither, at a terminal: an interactive walk, one step at a time.
//   - neither, and no terminal (or --json): the question itself is the
//     error - the --json envelope carries the document, the text error
//     prints a summary - and the command exits non-zero. lmm never picks
//     defaults silently.
//
// THE WALK. Visibility is stateful: a step can depend on flags earlier
// answers set, and the question lists only the steps visible under its
// preselection. So the walk goes step by step; while the user keeps a
// step's preselection, the later steps it shows are still the right ones
// and the walk goes on locally. The first step the user changes ends the
// round: the answers so far go back to core, which asks again about
// whatever those answers made visible - the walk resumes there.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The FOMOD flags every command that can raise a question shares.
var (
	fomodChoicesPath string
	fomodDefaults    bool
)

// addFomodFlags registers --fomod-choices and --fomod-defaults on cmd.
func addFomodFlags(cmd *cobra.Command) {
	cmd.Flags().StringVar(&fomodChoicesPath, "fomod-choices", "", "answer FOMOD installer questions from a JSON file (the profile's fomod: shape or Vortex's choices array; key it by source:id when several mods ask, or by source:id#file-id for one file's installer)")
	cmd.Flags().BoolVar(&fomodDefaults, "fomod-defaults", false, "answer FOMOD installer questions with the installer's own preselection")
}

// fomodStdin and fomodIsTerminal are the walk's input and its terminal
// check; tests replace them.
var (
	fomodStdin      io.Reader = os.Stdin
	fomodIsTerminal           = func() bool { return term.IsTerminal(int(os.Stdin.Fd())) }
)

// maxFomodRounds bounds the Apply re-runs one command makes for FOMOD
// answers: a round per changed step, so a real installer stays far below it.
const maxFomodRounds = 64

// fomodAnswerer answers FOMOD questions for one command run.
type fomodAnswerer struct {
	service *core.Service
	// fromFile are --fomod-choices' answers by mod key; single is its
	// unkeyed form, for a question about one mod.
	fromFile map[string]domain.FomodChoices
	single   *domain.FomodChoices
	defaults bool
	// interactive is a terminal walk (stdin is a TTY and not --json).
	interactive bool
	in          *bufio.Reader
	out         io.Writer

	answers map[string]domain.FomodChoices
	// walked counts the steps of each mod's question the walk has settled.
	walked map[string]int
	// sent fingerprints the non-interactive answers already sent, so one
	// that comes back is not sent forever.
	sent   map[string]string
	rounds int
}

// newFomodAnswerer reads the FOMOD flags. A --fomod-choices file that
// cannot be read or parsed fails here, before anything is downloaded.
func newFomodAnswerer(service *core.Service) (*fomodAnswerer, error) {
	f := &fomodAnswerer{
		service:     service,
		defaults:    fomodDefaults,
		interactive: !jsonOutput && fomodIsTerminal(),
		in:          bufio.NewReader(fomodStdin),
		out:         os.Stdout,
	}
	if fomodChoicesPath != "" {
		data, err := os.ReadFile(fomodChoicesPath)
		if err != nil {
			return nil, fmt.Errorf("reading --fomod-choices: %w", err)
		}
		f.fromFile, f.single, err = parseFomodChoicesFile(data)
		if err != nil {
			return nil, fmt.Errorf("--fomod-choices %s: %w", fomodChoicesPath, err)
		}
	}
	return f, nil
}

// options is the answers so far, for an Apply's Options.Fomod; nil before
// any.
func (f *fomodAnswerer) options() map[string]domain.FomodChoices {
	if f == nil || len(f.answers) == 0 {
		return nil
	}
	out := make(map[string]domain.FomodChoices, len(f.answers))
	for k, v := range f.answers {
		out[k] = v
	}
	return out
}

// answer turns a question into answers for the re-run, or returns the
// question as the error when nothing can answer it (see the file comment).
func (f *fomodAnswerer) answer(q *core.FomodChoicesRequiredError) (map[string]domain.FomodChoices, error) {
	if f.rounds++; f.rounds > maxFomodRounds {
		return nil, fmt.Errorf("the installer kept asking after %d rounds of answers: %w", maxFomodRounds, q)
	}
	if f.answers == nil {
		f.answers = map[string]domain.FomodChoices{}
		f.walked = map[string]int{}
		f.sent = map[string]string{}
	}
	if f.single != nil && len(q.Mods) > 1 {
		return nil, fmt.Errorf("--fomod-choices answers one installer, but %d ask; key its answers by mod (source:id), or by file (source:id#file-id) for a mod with several installers: %w", len(q.Mods), q)
	}
	for _, m := range q.Mods {
		// #634: one file's installer of a mod with several is answered
		// under its own file key.
		key := m.Key()
		if a, ok := f.fileAnswer(key); ok && f.sendOnce(key, a) {
			a.ConfigSHA256 = m.ConfigSHA256
			f.answers[key] = a
			continue
		}
		if f.defaults {
			// The installer's own preselection, not the question's: on a
			// reconfigure or a stale recording the two differ.
			steps := m.Choices
			if len(m.Defaults) > 0 {
				steps = m.Defaults
			}
			a := domain.FomodChoices{ConfigSHA256: m.ConfigSHA256, Steps: steps}
			if f.sendOnce(key, a) {
				f.answers[key] = a
				continue
			}
		}
		if !f.interactive {
			return nil, q
		}
		a, err := f.walk(m)
		if err != nil {
			return nil, err
		}
		f.answers[key] = a
	}
	return f.options(), nil
}

// fileAnswer is --fomod-choices' answer for key.
func (f *fomodAnswerer) fileAnswer(key string) (domain.FomodChoices, bool) {
	if f.single != nil {
		return *f.single, true
	}
	a, ok := f.fromFile[key]
	return a, ok
}

// sendOnce reports whether a has not been sent for key yet, and records
// it: an answer core asks about again did not settle the question, and
// sending it again would ask again.
func (f *fomodAnswerer) sendOnce(key string, a domain.FomodChoices) bool {
	fp, _ := json.Marshal(a.Steps)
	if f.sent[key] == string(fp) {
		return false
	}
	f.sent[key] = string(fp)
	return true
}

// parseFomodChoicesFile reads --fomod-choices: a bare array of steps
// (Vortex's choices.options), an object in the profile's fomod: shape
// ({config_sha256, steps}), or an object keyed by mod (source:id) whose
// values are either.
func parseFomodChoicesFile(data []byte) (map[string]domain.FomodChoices, *domain.FomodChoices, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil, errors.New("the file is empty")
	}
	one := func(raw []byte) (domain.FomodChoices, error) {
		raw = bytes.TrimSpace(raw)
		var c domain.FomodChoices
		if len(raw) > 0 && raw[0] == '[' {
			err := json.Unmarshal(raw, &c.Steps)
			return c, err
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		err := dec.Decode(&c)
		return c, err
	}
	if data[0] == '[' {
		c, err := one(data)
		return nil, &c, err
	}
	var keyed map[string]json.RawMessage
	if err := json.Unmarshal(data, &keyed); err != nil {
		return nil, nil, err
	}
	if _, ok := keyed["steps"]; ok {
		c, err := one(data)
		return nil, &c, err
	}
	out := make(map[string]domain.FomodChoices, len(keyed))
	for k, raw := range keyed {
		if !strings.Contains(k, ":") {
			return nil, nil, fmt.Errorf("%q is not a mod key (source:id)", k)
		}
		c, err := one(raw)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = c
	}
	return out, nil, nil
}

// walk asks one mod's question at the terminal (see the file comment) and
// returns the answers to send: every step settled so far.
func (f *fomodAnswerer) walk(m core.FomodQuestion) (domain.FomodChoices, error) {
	key := m.Key()
	start := f.walked[key]
	if start >= len(m.Steps) {
		// The whole walk was sent and core still asks: walk it again.
		start = 0
	}
	base := questionChoices(m)
	f.printQuestionHeader(m, start)
	for i := start; i < len(m.Steps); i++ {
		changed, err := f.askStep(m, i, base[i])
		if err != nil {
			return domain.FomodChoices{}, err
		}
		if changed {
			// Later steps may now show or hide: core says which.
			f.walked[key] = i + 1
			return domain.FomodChoices{ConfigSHA256: m.ConfigSHA256, Steps: base[:i+1]}, nil
		}
	}
	f.walked[key] = len(m.Steps)
	return domain.FomodChoices{ConfigSHA256: m.ConfigSHA256, Steps: base}, nil
}

// questionChoices is a question's preselection as answers, one entry per
// visible step and group - the canonical choices core sent, where they
// line up with the steps, so a step's own index is kept.
func questionChoices(m core.FomodQuestion) []domain.FomodStepChoice {
	out := make([]domain.FomodStepChoice, len(m.Steps))
	for i, st := range m.Steps {
		sc := domain.FomodStepChoice{Name: st.Name, Groups: make([]domain.FomodGroupChoice, len(st.Groups))}
		if i < len(m.Choices) && m.Choices[i].Name == st.Name {
			sc.Index = m.Choices[i].Index
		}
		for j, g := range st.Groups {
			gc := domain.FomodGroupChoice{Name: g.Name}
			if i < len(m.Choices) && j < len(m.Choices[i].Groups) && m.Choices[i].Groups[j].Name == g.Name {
				gc.Index = m.Choices[i].Groups[j].Index
			} else if repeatedGroupName(st.Groups, g.Name) {
				gc.Index = new(g.Ordinal)
			}
			gc.Choices = optionChoices(g, selectedOf(g))
			sc.Groups[j] = gc
		}
		out[i] = sc
	}
	return out
}

func repeatedGroupName(groups []core.FomodQuestionGroup, name string) bool {
	n := 0
	for _, g := range groups {
		if g.Name == name {
			n++
		}
	}
	return n > 1
}

// selectedOf is a group's current selection.
func selectedOf(g core.FomodQuestionGroup) []bool {
	sel := make([]bool, len(g.Options))
	for i, o := range g.Options {
		sel[i] = o.Selected
	}
	return sel
}

// optionChoices is a selection in canonical form: the option's position as
// Idx, and its ordinal as Index only where its name repeats in the group.
func optionChoices(g core.FomodQuestionGroup, sel []bool) []domain.FomodOptionChoice {
	names := map[string]int{}
	for _, o := range g.Options {
		names[o.Name]++
	}
	out := []domain.FomodOptionChoice{}
	for i, o := range g.Options {
		if !sel[i] {
			continue
		}
		c := domain.FomodOptionChoice{Name: o.Name, Idx: i}
		if names[o.Name] > 1 {
			c.Index = new(o.Ordinal)
		}
		out = append(out, c)
	}
	return out
}

// printQuestionHeader introduces a mod's question, and what a stale
// recording got wrong.
func (f *fomodAnswerer) printQuestionHeader(m core.FomodQuestion, start int) {
	name := shown(m.Name)
	if name == "" {
		name = shown(domain.ModKey(m.SourceID, m.ModID))
	}
	name += fileSuffix(m)
	title := shown(m.ModuleName)
	if title == "" {
		title = "FOMOD installer"
	}
	if start > 0 {
		_, _ = fmt.Fprintf(f.out, "\n%s: %s - continued\n", name, title)
		return
	}
	_, _ = fmt.Fprintf(f.out, "\n%s: %s\n", name, title)
	switch m.Reason {
	case core.FomodReasonStale:
		_, _ = fmt.Fprintln(f.out, "The recorded installer choices no longer fit this installer; review them:")
	case core.FomodReasonReconfigure:
		_, _ = fmt.Fprintln(f.out, "Change the installer choices (your recorded ones are preselected):")
	}
	for _, u := range m.Unmatched {
		_, _ = fmt.Fprintf(f.out, "  - recorded choice %s no longer matches (%s)\n", fomodPath(u.Step, u.Group, u.Option), u.Reason)
	}
	for _, v := range m.Violations {
		_, _ = fmt.Fprintf(f.out, "  - %s breaks its rule (%s, %d selected)\n", fomodPath(v.Step, v.Group, ""), groupRule(v.Type), v.Selected)
	}
}

// fileSuffix names the file whose installer m is, for a mod whose files
// each carry one (#634): " - <file name>", or "" for a mod's only
// installer.
func fileSuffix(m core.FomodQuestion) string {
	switch {
	case m.FileName != "":
		return " - " + shown(m.FileName)
	case m.FileID != "":
		return " - file " + shown(m.FileID)
	}
	return ""
}

func fomodPath(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, fmt.Sprintf("%q", shown(p)))
		}
	}
	return strings.Join(kept, " > ")
}

// askStep shows one step and asks each of its groups that has a choice,
// writing the picks into sc. changed reports a pick other than the
// preselection.
func (f *fomodAnswerer) askStep(m core.FomodQuestion, i int, sc domain.FomodStepChoice) (changed bool, err error) {
	st := m.Steps[i]
	_, _ = fmt.Fprintf(f.out, "\nStep %d of %d: %s\n", i+1, len(m.Steps), displayName(st.Name))
	for j, g := range st.Groups {
		sel := selectedOf(g)
		if groupIsFree(g) {
			picked, err := f.askGroup(m, g)
			if err != nil {
				return false, err
			}
			if !slices.Equal(picked, sel) {
				changed = true
			}
			sel = picked
		} else {
			f.printGroup(m, g, sel)
			_, _ = fmt.Fprintln(f.out, "  (nothing to choose here)")
		}
		sc.Groups[j].Choices = optionChoices(g, sel)
	}
	return changed, nil
}

// groupIsFree reports a group the user can change: not SelectAll, with an
// option that is neither Required nor NotUsable.
func groupIsFree(g core.FomodQuestionGroup) bool {
	if g.Type == "SelectAll" {
		return false
	}
	for _, o := range g.Options {
		if o.Type != "Required" && o.Type != "NotUsable" {
			return true
		}
	}
	return false
}

// groupRule words a group type.
func groupRule(t string) string {
	switch t {
	case "SelectExactlyOne":
		return "choose exactly one"
	case "SelectAtMostOne":
		return "choose at most one"
	case "SelectAtLeastOne":
		return "choose at least one"
	case "SelectAll":
		return "all are installed"
	default:
		return "choose any"
	}
}

// displayName is an installer-supplied name as the terminal shows it:
// shown, or "(unnamed)" for a blank one.
func displayName(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(unnamed)"
	}
	return shown(s)
}

// shown makes one line of installer-supplied text safe to print. A
// ModuleConfig.xml is untrusted, and encoding/xml passes C1 controls and
// bidi overrides straight through, so every name a prompt or summary shows
// goes through domain.SanitizeLine - at display time only: the answers
// sent back carry the raw names, which is what the installer matches.
func shown(s string) string { return domain.SanitizeLine(s) }

// printGroup lists a group's options with their selection, type,
// description and image.
func (f *fomodAnswerer) printGroup(m core.FomodQuestion, g core.FomodQuestionGroup, sel []bool) {
	_, _ = fmt.Fprintf(f.out, "  %s - %s\n", displayName(g.Name), groupRule(g.Type))
	for i, o := range g.Options {
		mark := " "
		if sel[i] {
			mark = "x"
		}
		note := ""
		switch o.Type {
		case "Required":
			note = " (required)"
		case "Recommended":
			note = " (recommended)"
		case "NotUsable":
			note = " (not usable)"
		case "CouldBeUsable":
			note = " (may not work)"
		}
		_, _ = fmt.Fprintf(f.out, "    %d) [%s] %s%s\n", i+1, mark, displayName(o.Name), note)
		for _, line := range strings.Split(strings.TrimSpace(domain.SanitizeText(o.Description)), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				_, _ = fmt.Fprintf(f.out, "           %s\n", line)
			}
		}
		if o.Image != "" && f.service != nil {
			if img, err := f.service.FomodImage(m.SourceID, m.ModID, m.ConfigSHA256, o.Image); err == nil {
				_, _ = fmt.Fprintf(f.out, "           image: %s\n", img.Path)
			}
		}
	}
}

// askGroup prompts for one group's selection until it keeps the group's
// rule. Enter keeps the current selection.
func (f *fomodAnswerer) askGroup(m core.FomodQuestion, g core.FomodQuestionGroup) ([]bool, error) {
	current := selectedOf(g)
	f.printGroup(m, g, current)
	for {
		_, _ = fmt.Fprintf(f.out, "  Choose %s [Enter keeps %s, q cancels]: ", selectionHint(g), selectionLabel(current))
		line, err := f.in.ReadString('\n')
		if err != nil && (!errors.Is(err, io.EOF) || strings.TrimSpace(line) == "") {
			return nil, promptReadErrorAs(err, ErrCancelled)
		}
		line = strings.TrimSpace(line)
		var picked []bool
		switch strings.ToLower(line) {
		case "":
			picked = slices.Clone(current)
		case "q":
			return nil, ErrCancelled
		case "none", "-":
			picked = make([]bool, len(g.Options))
		default:
			nums, perr := parseRangeSelection(line, len(g.Options))
			if perr != nil {
				_, _ = fmt.Fprintf(f.out, "  Invalid selection: %v\n", perr)
				continue
			}
			picked = make([]bool, len(g.Options))
			for _, n := range nums {
				picked[n-1] = true
			}
		}
		if problem := groupSelectionProblem(g, picked); problem != "" {
			_, _ = fmt.Fprintf(f.out, "  %s\n", problem)
			continue
		}
		return picked, nil
	}
}

// selectionHint words what a group accepts.
func selectionHint(g core.FomodQuestionGroup) string {
	switch g.Type {
	case "SelectExactlyOne":
		return "one number"
	case "SelectAtMostOne":
		return "one number, or none"
	default:
		return "numbers (e.g. 1,3), or none"
	}
}

// selectionLabel is a selection as the numbers a user would type.
func selectionLabel(sel []bool) string {
	var nums []string
	for i, s := range sel {
		if s {
			nums = append(nums, fmt.Sprint(i+1))
		}
	}
	if len(nums) == 0 {
		return "none"
	}
	return strings.Join(nums, ",")
}

// groupSelectionProblem checks a selection against the group's rule and
// its options' types; "" when it is valid. Required options must stay
// selected and NotUsable ones unselected.
func groupSelectionProblem(g core.FomodQuestionGroup, sel []bool) string {
	picked, usable := 0, 0
	for i, o := range g.Options {
		switch {
		case o.Type == "Required" && !sel[i]:
			return fmt.Sprintf("%q is required and stays selected.", shown(o.Name))
		case o.Type == "NotUsable" && sel[i] && !o.Selected:
			return fmt.Sprintf("%q is not usable here.", shown(o.Name))
		}
		if o.Type != "NotUsable" {
			usable++
		}
		if sel[i] {
			picked++
		}
	}
	switch g.Type {
	case "SelectExactlyOne":
		if picked != 1 && usable > 0 {
			return "Choose exactly one."
		}
	case "SelectAtMostOne":
		if picked > 1 {
			return "Choose at most one."
		}
	case "SelectAtLeastOne":
		if picked == 0 && usable > 0 {
			return "Choose at least one."
		}
	}
	return ""
}

// reportFomodQuestion prints a FOMOD question under the error line when
// nothing answered it (no terminal, no flag): what was asked, and how to
// answer it. --json carries the whole document in the envelope's details
// instead.
func reportFomodQuestion(err error) {
	var q *core.FomodChoicesRequiredError
	if !errors.As(err, &q) {
		return
	}
	printFomodQuestionSummary(os.Stderr, q)
}

// printFomodQuestionSummary lists each mod's steps, groups and options.
func printFomodQuestionSummary(w io.Writer, q *core.FomodChoicesRequiredError) {
	for _, m := range q.Mods {
		_, _ = fmt.Fprintf(w, "\n%s%s (%s): %s\n", displayName(m.Name), fileSuffix(m), shown(m.Key()), displayName(m.ModuleName))
		for _, st := range m.Steps {
			_, _ = fmt.Fprintf(w, "  Step %q\n", shown(st.Name))
			for _, g := range st.Groups {
				_, _ = fmt.Fprintf(w, "    Group %q - %s:", shown(g.Name), groupRule(g.Type))
				for _, o := range g.Options {
					mark := ""
					if o.Selected {
						mark = "*"
					}
					_, _ = fmt.Fprintf(w, " %q%s", shown(o.Name), mark)
				}
				_, _ = fmt.Fprintln(w)
			}
		}
	}
	_, _ = fmt.Fprintln(w, "\n(* marks the installer's preselection.) Answer with --fomod-choices <file.json> or --fomod-defaults,")
	_, _ = fmt.Fprintln(w, "or run the command in a terminal to choose interactively; --json prints the full question document.")
}

// applyAnsweringFomod runs apply, answering the FOMOD questions it raises
// and running it again with the answers, until it stops asking. A question
// comes back either as the Apply's error (a strict install, an update, an
// archive import) or, when asked is set, on its result (a batch install or a
// profile import, which refuse with nothing written and report it there).
//
// A question nothing can answer ends the loop: as the error for an
// error-shaped question, and as a nil error with the question still on the
// result for a result-shaped one - the caller renders that result and exits
// non-zero (fomodUnanswered).
func applyAnsweringFomod[R any](fa *fomodAnswerer, apply func(answers map[string]domain.FomodChoices) (R, error), asked func(R) *core.FomodChoicesRequiredError) (R, error) {
	for {
		res, err := apply(fa.options())
		var q *core.FomodChoicesRequiredError
		resultShaped := false
		switch {
		case errors.As(err, &q):
		case err != nil:
			return res, err
		case asked != nil:
			if q = asked(res); q == nil {
				return res, nil
			}
			resultShaped = true
		default:
			return res, nil
		}
		if _, aerr := fa.answer(q); aerr != nil {
			if resultShaped && aerr == error(q) {
				return res, nil
			}
			return res, aerr
		}
	}
}

// fomodUnanswered is the error a command returns after rendering a result
// that still carries a FOMOD question nothing answered: the result (or its
// --json document) already said so, and the exit status must not claim
// success.
func fomodUnanswered(q *core.FomodChoicesRequiredError) error {
	if q == nil {
		return nil
	}
	if !jsonOutput {
		printFomodQuestionSummary(os.Stdout, q)
	}
	return fmt.Errorf("FOMOD installer choices are needed: %w", ErrReported)
}
