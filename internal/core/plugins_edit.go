package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// The plugin edits (#617, design docs/plans/2026-11-10-collections-
// design.md §5 "Frontends"): `lmm plugins enable|disable|move|sort` and
// `lmm plugins rules ...`, and the web UI's Plugins tab, as one Plan/Apply
// pair. Every edit changes the profile's plugins: block - the user's
// intent - and, for the active profile, Apply then writes the game's
// Plugins.txt from it, exactly as a deploy would (syncPluginList).
//
// Only lmm's own plugins are the profile's to order or switch: the game's
// implicit plugins always load first, and an external plugin (installed by
// hand, or by the game's Creations menu) keeps the state and the place the
// game gave it (owner ruling, #616). An edit naming one is refused.
//
// A move and a sort record the EFFECTIVE order in the profile
// (materializedOrder): the list the user sees, which a sort reproduces
// exactly, so recording it never moves a plugin by itself. An enable or a
// disable touches the order no further than it must.

// PluginOpEnable, PluginOpDisable, PluginOpMove, PluginOpSort,
// PluginOpRuleAdd, PluginOpRuleRemove, PluginOpGroupAdd and
// PluginOpGroupRemove are PluginEdit.Op's values.
const (
	// PluginOpEnable activates PluginEdit.Plugins.
	PluginOpEnable = "enable"
	// PluginOpDisable deactivates PluginEdit.Plugins.
	PluginOpDisable = "disable"
	// PluginOpMove moves PluginEdit.Plugin before or after another plugin,
	// or to the first or last place.
	PluginOpMove = "move"
	// PluginOpSort records the effective order in the profile and writes
	// the game's list.
	PluginOpSort = "sort"
	// PluginOpRuleAdd adds PluginEdit.Rule to the plugin's rule.
	PluginOpRuleAdd = "rule_add"
	// PluginOpRuleRemove removes PluginEdit.Rule's parts from the
	// plugin's rule, or the whole rule when it names none.
	PluginOpRuleRemove = "rule_remove"
	// PluginOpGroupAdd adds PluginEdit.Group, or the groups it loads
	// after.
	PluginOpGroupAdd = "group_add"
	// PluginOpGroupRemove removes the group PluginEdit.Group names.
	PluginOpGroupRemove = "group_remove"
)

// PluginMoveFirst and PluginMoveLast are PluginEdit.To's values.
const (
	PluginMoveFirst = "first"
	PluginMoveLast  = "last"
)

// ErrPluginNotFound is the refusal for an edit naming a plugin the profile
// neither deploys nor lists.
var ErrPluginNotFound = errors.New("plugin not found")

// ErrPluginNotOwned is the refusal for an edit naming a plugin that is not
// lmm's to change: one of the game's own plugins, or an external one.
var ErrPluginNotOwned = errors.New("not a plugin lmm deploys")

// ErrPluginRuleNotFound is the refusal for removing a rule, a group, or a
// part of one, that the profile does not have.
var ErrPluginRuleNotFound = errors.New("no such plugin rule")

// ErrInvalidPluginEdit is the refusal for an edit that cannot mean
// anything: a move with no destination, a rule with nothing in it.
var ErrInvalidPluginEdit = errors.New("invalid plugin edit")

// PluginEdit is one change to a profile's plugins: block.
type PluginEdit struct {
	// Op is the edit (PluginOp*).
	Op string `json:"op"`
	// Plugins are the plugins an enable or a disable switches.
	Plugins []string `json:"plugins,omitempty"`
	// Plugin is the plugin a move moves; Before, After or To (PluginMoveFirst
	// or PluginMoveLast) - exactly one - is where.
	Plugin string `json:"plugin,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	To     string `json:"to,omitempty"`
	// Rule is a rule_add's rule, merged into the plugin's own; for a
	// rule_remove, the after and req entries to remove (none, and no
	// ClearGroup, removes the whole rule).
	Rule domain.PluginRule `json:"rule,omitzero"`
	// ClearGroup takes a rule_remove's plugin out of its group.
	ClearGroup bool `json:"clear_group,omitzero"`
	// Group is a group_add's group (merged into one of the same name), or
	// the group a group_remove removes.
	Group domain.PluginGroup `json:"group,omitzero"`
}

// PluginEditPlan is what PlanPluginEdit worked out: `lmm plugins ...
// --dry-run`'s document.
type PluginEditPlan struct {
	GameID  string     `json:"game_id"`
	Profile string     `json:"profile"`
	Edit    PluginEdit `json:"edit"`
	// Active reports that Profile is the game's active profile: Apply
	// writes the game's Plugins.txt as well as the profile.
	Active bool `json:"active"`
	// Changes are what the edit changes, a sentence each.
	Changes []string `json:"changes"`
	// Notes are what the user should know: a rule naming a plugin that is
	// not deployed, a move a rule or a master overrides.
	Notes []string `json:"notes,omitempty"`
	// NoChanges reports that the profile already says this; Apply still
	// writes the game's list for the active profile.
	NoChanges bool `json:"no_changes"`
	// Order is the effective order after the edit.
	Order *PluginOrderReport `json:"order"`

	// plugins is the block Apply writes; base the one the plan was worked
	// out from, which Apply requires unchanged (ErrStalePlan).
	plugins domain.ProfilePlugins
	base    domain.ProfilePlugins
}

// PluginEditResult is what ApplyPluginEdit did: `lmm plugins ...`'s
// document.
type PluginEditResult struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Changed reports that the profile was written.
	Changed bool `json:"changed"`
	// Changes are the plan's.
	Changes []string `json:"changes"`
	// List is what writing the game's plugin list did; nil for a profile
	// that is not the active one.
	List *PluginListResult `json:"list,omitempty"`
	// Order is the effective order now.
	Order *PluginOrderReport `json:"order"`
}

// PluginRulesReport is `lmm plugins rules list`'s document: a profile's
// plugin rules.
type PluginRulesReport struct {
	GameID  string             `json:"game_id"`
	Profile string             `json:"profile"`
	Rules   domain.PluginRules `json:"rules"`
}

// ProfilePluginRules reports profileName's plugin rules (#617). It
// refuses a game whose adapter manages no plugin list (ErrNoPluginList).
func (s *Service) ProfilePluginRules(ctx context.Context, game *domain.Game, profileName string) (*PluginRulesReport, error) {
	if _, ok := s.pluginRules(game); !ok {
		return nil, fmt.Errorf("%s: %w", game.ID, ErrNoPluginList)
	}
	profile, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	return &PluginRulesReport{GameID: game.ID, Profile: profileName, Rules: profile.Plugins.Clone().Rules}, nil
}

// PlanPluginEdit works out what edit does to profileName's plugins (#617).
// It writes nothing. Beyond the refusals of PluginOrder, it refuses an
// edit naming a plugin the profile neither deploys nor lists
// (ErrPluginNotFound), one that is not lmm's (ErrPluginNotOwned), a rule
// or group that is not there (ErrPluginRuleNotFound), an edit that cannot
// mean anything (ErrInvalidPluginEdit), and one that would leave the
// order with a cycle (*PluginOrderCycleError).
func (s *Service) PlanPluginEdit(ctx context.Context, game *domain.Game, profileName string, edit PluginEdit) (*PluginEditPlan, error) {
	before, err := s.pluginOrder(ctx, game, profileName, nil)
	if err != nil {
		return nil, err
	}
	profile, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	base := profile.Plugins.Clone()
	if edit.Op == PluginOpSort {
		if !before.Active {
			return nil, fmt.Errorf("%w: %s is not the active profile, so its plugins are not deployed and have no effective order to sort - switch to it first", ErrInvalidPluginEdit, profileName)
		}
		if err := cycleOf(before); err != nil {
			return nil, err
		}
	}
	e := pluginEditor{report: before, next: base.Clone()}
	if err := e.apply(edit); err != nil {
		return nil, err
	}
	plan := &PluginEditPlan{
		GameID: game.ID, Profile: profileName, Edit: edit, Active: before.Active,
		Changes: e.changes, Notes: e.notes, plugins: e.next, base: base,
	}
	if plan.Changes == nil {
		plan.Changes = []string{}
	}
	plan.NoChanges = e.next.Equal(base)
	after, err := s.pluginOrder(ctx, game, profileName, &plan.plugins)
	if err != nil {
		return nil, err
	}
	plan.Order = after
	if before.Sorted {
		if err := cycleOf(after); err != nil {
			return nil, err
		}
	}
	if edit.Op == PluginOpMove {
		plan.Notes = append(plan.Notes, moveHeld(edit, after)...)
	}
	return plan, nil
}

// cycleOf is the cycle that stopped report's sort, or nil.
func cycleOf(report *PluginOrderReport) error {
	if report.Sorted {
		return nil
	}
	for _, f := range report.Findings {
		if f.Code == PluginFindingCycle {
			return &PluginOrderCycleError{Edges: f.Cycle}
		}
	}
	return nil
}

// moveHeld is a note for a move the effective order does not honour: a
// rule or a master holds the plugin elsewhere.
func moveHeld(edit PluginEdit, after *PluginOrderReport) []string {
	pos := map[string]int{}
	var entry *PluginOrderEntry
	for i := range after.Plugins {
		pos[pluginKey(after.Plugins[i].Name)] = i
		if pluginKey(after.Plugins[i].Name) == pluginKey(edit.Plugin) {
			entry = &after.Plugins[i]
		}
	}
	if entry == nil {
		return nil
	}
	at := pos[pluginKey(edit.Plugin)]
	held := false
	switch {
	case edit.Before != "":
		other, ok := pos[pluginKey(edit.Before)]
		held = ok && at > other
	case edit.After != "":
		other, ok := pos[pluginKey(edit.After)]
		held = ok && at < other
	case edit.To == PluginMoveFirst:
		held = slices.ContainsFunc(after.Plugins[:at], func(p PluginOrderEntry) bool { return p.Managed })
	case edit.To == PluginMoveLast:
		held = slices.ContainsFunc(after.Plugins[at+1:], func(p PluginOrderEntry) bool { return p.Managed })
	}
	if !held {
		return nil
	}
	why := make([]string, 0, len(entry.Constraints))
	for _, c := range entry.Constraints {
		why = append(why, c.describe())
	}
	note := fmt.Sprintf("%s cannot load there: the profile records the move, but its place in the load order is held", entry.Name)
	if len(why) > 0 {
		note += " (" + strings.Join(why, "; ") + ")"
	}
	return []string{note}
}

// ApplyPluginEdit writes plan's plugins: block to the profile and, for the
// active profile, the game's plugin list (#617). It refuses a plan whose
// profile's plugins changed since it was made (ErrStalePlan). A list the
// sync could not write is a finding on the result, never an error: the
// profile was written by then.
func (s *Service) ApplyPluginEdit(ctx context.Context, game *domain.Game, plan *PluginEditPlan) (*PluginEditResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("%w: no plan", ErrInvalidPluginEdit)
	}
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	pm := s.NewProfileManager()
	profile, err := config.LoadProfile(s.configDir, game.ID, plan.Profile)
	if err != nil {
		return nil, err
	}
	if !profile.Plugins.Equal(plan.base) {
		return nil, fmt.Errorf("%w: profile %s's plugins changed since the plan was made", ErrStalePlan, plan.Profile)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res := &PluginEditResult{GameID: game.ID, Profile: plan.Profile, Changes: slices.Clone(plan.Changes)}
	if !plan.NoChanges {
		profile.Plugins = plan.plugins.Clone()
		if err := pm.save(profile); err != nil {
			return nil, err
		}
		res.Changed = true
	}
	// The profile is written; the list is derived from it, and is written
	// whatever the caller's context does now (Ruling 16).
	ctx = context.WithoutCancel(ctx)
	res.List = s.syncPluginList(ctx, game, plan.Profile)
	if res.Order, err = s.pluginOrder(ctx, game, plan.Profile, nil); err != nil {
		return nil, err
	}
	return res, nil
}

// pluginEditor applies one edit to a plugins: block, against the
// effective order it was read with.
type pluginEditor struct {
	report  *PluginOrderReport
	next    domain.ProfilePlugins
	changes []string
	notes   []string
}

func (e *pluginEditor) apply(edit PluginEdit) error {
	switch edit.Op {
	case PluginOpEnable, PluginOpDisable:
		return e.setEnabled(edit.Plugins, edit.Op == PluginOpEnable)
	case PluginOpMove:
		return e.move(edit)
	case PluginOpSort:
		return e.sort()
	case PluginOpRuleAdd:
		return e.ruleAdd(edit.Rule)
	case PluginOpRuleRemove:
		return e.ruleRemove(edit.Rule, edit.ClearGroup)
	case PluginOpGroupAdd:
		return e.groupAdd(edit.Group)
	case PluginOpGroupRemove:
		return e.groupRemove(edit.Group.Name)
	default:
		return fmt.Errorf("%w: unknown op %q", ErrInvalidPluginEdit, edit.Op)
	}
}

// entry is the report's entry for name, if it places one.
func (e *pluginEditor) entry(name string) (PluginOrderEntry, bool) {
	for _, p := range e.report.Plugins {
		if pluginKey(p.Name) == pluginKey(name) {
			return p, true
		}
	}
	return PluginOrderEntry{}, false
}

// orderIndex is name's first place in the block's order, or -1.
func (e *pluginEditor) orderIndex(name string) int {
	return slices.IndexFunc(e.next.Order, func(p domain.PluginEntry) bool { return pluginKey(p.Name) == pluginKey(name) })
}

// owned checks that name is one of the profile's plugins, lmm's to
// change, and answers its spelling.
func (e *pluginEditor) owned(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("%w: a plugin name is empty", ErrInvalidPluginEdit)
	}
	if p, ok := e.entry(name); ok {
		switch p.Placement {
		case PluginPlacementImplicit:
			return "", fmt.Errorf("%s is one of the game's own plugins, which always load first: %w", p.Name, ErrPluginNotOwned)
		case PluginPlacementExternal:
			return "", fmt.Errorf("%s was not deployed by lmm (installed by hand, or by the game's Creations menu); lmm leaves its place and state to the game: %w", p.Name, ErrPluginNotOwned)
		}
		return p.Name, nil
	}
	if i := e.orderIndex(name); i >= 0 {
		return e.next.Order[i].Name, nil
	}
	if !e.report.Active {
		return "", fmt.Errorf("%s is not in profile %s's plugin order (only the active profile's deployed plugins are known): %w", name, e.report.Profile, ErrPluginNotFound)
	}
	return "", fmt.Errorf("%s is neither deployed by profile %s nor in its plugin order: %w", name, e.report.Profile, ErrPluginNotFound)
}

// setEnabled switches names on or off. A plugin the order does not list
// yet gets an entry, and so does every unlisted plugin of lmm's before it
// - in the effective order, at the end - so recording it moves nothing.
func (e *pluginEditor) setEnabled(names []string, on bool) error {
	if len(names) == 0 {
		return fmt.Errorf("%w: name at least one plugin", ErrInvalidPluginEdit)
	}
	state := "disabled"
	if on {
		state = "enabled"
	}
	for _, name := range names {
		spelled, err := e.owned(name)
		if err != nil {
			return err
		}
		if e.orderIndex(spelled) < 0 {
			e.listUnlisted()
		}
		changed := false
		for i := range e.next.Order {
			if pluginKey(e.next.Order[i].Name) == pluginKey(spelled) && e.next.Order[i].Enabled != on {
				e.next.Order[i].Enabled = on
				changed = true
			}
		}
		if changed {
			e.changes = append(e.changes, fmt.Sprintf("%s: %s", spelled, state))
		} else {
			e.notes = append(e.notes, fmt.Sprintf("%s is already %s", spelled, state))
		}
	}
	return nil
}

// listUnlisted appends every placed plugin of lmm's the order does not
// list yet, in the effective order.
func (e *pluginEditor) listUnlisted() {
	for _, p := range e.report.Plugins {
		if p.Managed && e.orderIndex(p.Name) < 0 {
			e.next.Order = append(e.next.Order, domain.PluginEntry{Name: p.Name, Enabled: p.Active})
		}
	}
}

// materializedOrder is the effective order of lmm's plugins as an order
// block: the placed ones in their load order, each with its state, and
// every other entry the order has (a plugin not deployed now, an external
// one the user listed) after the entry it followed - so recording it
// changes no position.
func materializedOrder(report *PluginOrderReport, order []domain.PluginEntry) []domain.PluginEntry {
	if !report.Active {
		return dedupeOrder(order)
	}
	var placed []pluginNode
	isPlaced := map[string]bool{}
	for _, p := range report.Plugins {
		if p.Managed {
			placed = append(placed, pluginNode{name: p.Name, active: p.Active})
			isPlaced[pluginKey(p.Name)] = true
		}
	}
	var others []pluginNode
	var current []listEntry
	for _, o := range dedupeOrder(order) {
		current = append(current, listEntry{name: o.Name, active: o.Enabled})
		if !isPlaced[pluginKey(o.Name)] {
			others = append(others, pluginNode{name: o.Name, active: o.Enabled})
		}
	}
	ranks := mergedRanks(placed, others, current)
	all := append(slices.Clone(placed), others...)
	slices.SortStableFunc(all, func(a, b pluginNode) int { return ranks[pluginKey(a.name)] - ranks[pluginKey(b.name)] })
	out := make([]domain.PluginEntry, len(all))
	for i, n := range all {
		out[i] = domain.PluginEntry{Name: n.name, Enabled: n.active}
	}
	return out
}

// dedupeOrder is order with each plugin's first entry only - the one
// every reader decides it by.
func dedupeOrder(order []domain.PluginEntry) []domain.PluginEntry {
	seen := map[string]bool{}
	out := make([]domain.PluginEntry, 0, len(order))
	for _, o := range order {
		if !seen[pluginKey(o.Name)] {
			seen[pluginKey(o.Name)] = true
			out = append(out, o)
		}
	}
	return out
}

// move moves edit.Plugin in the materialized order.
func (e *pluginEditor) move(edit PluginEdit) error {
	dests := 0
	for _, d := range []string{edit.Before, edit.After, edit.To} {
		if d != "" {
			dests++
		}
	}
	if dests != 1 {
		return fmt.Errorf("%w: a move needs exactly one of before, after, or to (first or last)", ErrInvalidPluginEdit)
	}
	if edit.To != "" && edit.To != PluginMoveFirst && edit.To != PluginMoveLast {
		return fmt.Errorf("%w: to is %q; it must be %q or %q", ErrInvalidPluginEdit, edit.To, PluginMoveFirst, PluginMoveLast)
	}
	name, err := e.owned(edit.Plugin)
	if err != nil {
		return err
	}
	order := materializedOrder(e.report, e.next.Order)
	from := slices.IndexFunc(order, func(p domain.PluginEntry) bool { return pluginKey(p.Name) == pluginKey(name) })
	if from < 0 {
		return fmt.Errorf("%s: %w", name, ErrPluginNotFound)
	}
	moving := order[from]
	rest := slices.Delete(slices.Clone(order), from, from+1)
	var at int
	var change string
	switch {
	case edit.To == PluginMoveFirst:
		at, change = 0, fmt.Sprintf("%s: moved to the top of the order", name)
	case edit.To == PluginMoveLast:
		at, change = len(rest), fmt.Sprintf("%s: moved to the end of the order", name)
	default:
		anchor := firstNonEmpty(edit.Before, edit.After)
		spelled, err := e.owned(anchor)
		if err != nil {
			return err
		}
		if pluginKey(spelled) == pluginKey(name) {
			return fmt.Errorf("%w: %s cannot move relative to itself", ErrInvalidPluginEdit, name)
		}
		at = slices.IndexFunc(rest, func(p domain.PluginEntry) bool { return pluginKey(p.Name) == pluginKey(spelled) })
		if at < 0 {
			return fmt.Errorf("%s: %w", spelled, ErrPluginNotFound)
		}
		if edit.After != "" {
			at++
			change = fmt.Sprintf("%s: moved after %s", name, spelled)
		} else {
			change = fmt.Sprintf("%s: moved before %s", name, spelled)
		}
	}
	moved := slices.Insert(rest, at, moving)
	if slices.Equal(moved, order) {
		e.notes = append(e.notes, fmt.Sprintf("%s is already there", name))
		return nil
	}
	e.next.Order = moved
	e.changes = append(e.changes, change)
	return nil
}

// sort records the effective order in the profile.
func (e *pluginEditor) sort() error {
	order := materializedOrder(e.report, e.next.Order)
	if slices.Equal(order, e.next.Order) {
		return nil
	}
	listed := map[string]bool{}
	for _, o := range e.next.Order {
		listed[pluginKey(o.Name)] = true
	}
	for _, o := range order {
		if !listed[pluginKey(o.Name)] {
			e.changes = append(e.changes, fmt.Sprintf("%s: added to the profile's order where it loads", o.Name))
		}
	}
	// The plugins the order already listed, in their new sequence.
	var was, now []string
	for _, o := range dedupeOrder(e.next.Order) {
		was = append(was, pluginKey(o.Name))
	}
	for _, o := range order {
		if listed[pluginKey(o.Name)] {
			now = append(now, pluginKey(o.Name))
		}
	}
	if !slices.Equal(was, now) {
		e.changes = append(e.changes, "the profile's order now records where rules and masters place its plugins")
	}
	if len(e.changes) == 0 {
		e.changes = append(e.changes, "the profile's order drops its repeated entries")
	}
	e.next.Order = order
	return nil
}

// containsFold reports whether list holds name, case-insensitively.
func containsFold(list []string, name string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return pluginKey(s) == pluginKey(name) })
}

// ruleIndex is the index of name's rule, or -1.
func (e *pluginEditor) ruleIndex(name string) int {
	return slices.IndexFunc(e.next.Rules.Plugins, func(r domain.PluginRule) bool { return pluginKey(r.Name) == pluginKey(name) })
}

// groupIndex is the index of the group named name, or -1.
func (e *pluginEditor) groupIndex(name string) int {
	return slices.IndexFunc(e.next.Rules.Groups, func(g domain.PluginGroup) bool { return g.Name == name })
}

// known reports whether the report places name or the order lists it.
func (e *pluginEditor) known(name string) bool {
	_, placed := e.entry(name)
	return placed || e.orderIndex(name) >= 0
}

// ruleAdd merges rule into its plugin's rule.
func (e *pluginEditor) ruleAdd(rule domain.PluginRule) error {
	name := strings.TrimSpace(rule.Name)
	if name == "" {
		return fmt.Errorf("%w: a rule needs the plugin it is about", ErrInvalidPluginEdit)
	}
	if rule.Group == "" && len(rule.After) == 0 && len(rule.Req) == 0 {
		return fmt.Errorf("%w: a rule for %s needs a group, an after or a req", ErrInvalidPluginEdit, name)
	}
	for _, other := range slices.Concat(rule.After, rule.Req) {
		if strings.TrimSpace(other) == "" {
			return fmt.Errorf("%w: a rule names an empty plugin", ErrInvalidPluginEdit)
		}
		if pluginKey(other) == pluginKey(name) {
			return fmt.Errorf("%w: %s cannot load after itself", ErrInvalidPluginEdit, name)
		}
	}
	if rule.Group != "" && rule.Group != domain.PluginGroupDefault && e.groupIndex(rule.Group) < 0 {
		return fmt.Errorf("%w: there is no group %q - add it first (lmm plugins rules group add %q --after default)", ErrPluginRuleNotFound, rule.Group, rule.Group)
	}
	i := e.ruleIndex(name)
	if i < 0 {
		e.next.Rules.Plugins = append(e.next.Rules.Plugins, domain.PluginRule{Name: name})
		i = len(e.next.Rules.Plugins) - 1
	}
	r := &e.next.Rules.Plugins[i]
	if rule.Group != "" && r.Group != rule.Group {
		r.Group = rule.Group
		e.changes = append(e.changes, fmt.Sprintf("%s: in group %s", r.Name, rule.Group))
	}
	for _, a := range rule.After {
		if !containsFold(r.After, a) {
			r.After = append(r.After, a)
			e.changes = append(e.changes, fmt.Sprintf("%s: loads after %s", r.Name, a))
		}
	}
	for _, q := range rule.Req {
		if !containsFold(r.Req, q) {
			r.Req = append(r.Req, q)
			e.changes = append(e.changes, fmt.Sprintf("%s: requires %s", r.Name, q))
		}
	}
	if e.report.Active {
		for _, n := range slices.Concat([]string{name}, rule.After, rule.Req) {
			if !e.known(n) {
				e.notes = append(e.notes, fmt.Sprintf("%s is not deployed: the rule applies once it is", n))
			}
		}
	}
	return nil
}

// ruleRemove removes rule's parts from its plugin's rule, or the rule.
func (e *pluginEditor) ruleRemove(rule domain.PluginRule, clearGroup bool) error {
	i := e.ruleIndex(rule.Name)
	if i < 0 {
		return fmt.Errorf("%w: profile %s has no rule for %s", ErrPluginRuleNotFound, e.report.Profile, rule.Name)
	}
	r := &e.next.Rules.Plugins[i]
	if !clearGroup && len(rule.After) == 0 && len(rule.Req) == 0 {
		e.changes = append(e.changes, fmt.Sprintf("%s: rule removed", r.Name))
		e.next.Rules.Plugins = slices.Delete(e.next.Rules.Plugins, i, i+1)
		return nil
	}
	remove := func(list []string, name, what string) ([]string, error) {
		j := slices.IndexFunc(list, func(s string) bool { return pluginKey(s) == pluginKey(name) })
		if j < 0 {
			return nil, fmt.Errorf("%w: %s's rule has no %s %s", ErrPluginRuleNotFound, r.Name, what, name)
		}
		e.changes = append(e.changes, fmt.Sprintf("%s: no longer %s %s", r.Name, map[string]string{"after": "loads after", "req": "requires"}[what], list[j]))
		return slices.Delete(list, j, j+1), nil
	}
	var err error
	for _, a := range rule.After {
		if r.After, err = remove(r.After, a, "after"); err != nil {
			return err
		}
	}
	for _, q := range rule.Req {
		if r.Req, err = remove(r.Req, q, "req"); err != nil {
			return err
		}
	}
	if clearGroup {
		if r.Group == "" {
			return fmt.Errorf("%w: %s's rule puts it in no group", ErrPluginRuleNotFound, r.Name)
		}
		e.changes = append(e.changes, fmt.Sprintf("%s: out of group %s", r.Name, r.Group))
		r.Group = ""
	}
	if r.Group == "" && len(r.After) == 0 && len(r.Req) == 0 {
		e.next.Rules.Plugins = slices.Delete(e.next.Rules.Plugins, i, i+1)
	}
	return nil
}

// groupAdd adds group, or merges the groups it loads after into the one
// of its name.
func (e *pluginEditor) groupAdd(group domain.PluginGroup) error {
	name := strings.TrimSpace(group.Name)
	if name == "" {
		return fmt.Errorf("%w: a group needs a name", ErrInvalidPluginEdit)
	}
	if name == domain.PluginGroupDefault {
		return fmt.Errorf("%w: %q is the group every plugin is in unless a rule says otherwise; it needs no adding", ErrInvalidPluginEdit, name)
	}
	for _, a := range group.After {
		if a == name {
			return fmt.Errorf("%w: group %s cannot load after itself", ErrInvalidPluginEdit, name)
		}
		if a != domain.PluginGroupDefault && e.groupIndex(a) < 0 {
			return fmt.Errorf("%w: there is no group %q to load after", ErrPluginRuleNotFound, a)
		}
	}
	i := e.groupIndex(name)
	if i < 0 {
		e.next.Rules.Groups = append(e.next.Rules.Groups, domain.PluginGroup{Name: name})
		i = len(e.next.Rules.Groups) - 1
		if len(group.After) == 0 {
			e.changes = append(e.changes, fmt.Sprintf("group %s: added", name))
		}
	}
	g := &e.next.Rules.Groups[i]
	for _, a := range group.After {
		if !slices.Contains(g.After, a) {
			g.After = append(g.After, a)
			e.changes = append(e.changes, fmt.Sprintf("group %s: loads after group %s", name, a))
		}
	}
	return nil
}

// groupRemove removes the group named name, which nothing may still use.
func (e *pluginEditor) groupRemove(name string) error {
	i := e.groupIndex(name)
	if i < 0 {
		return fmt.Errorf("%w: profile %s has no group %q", ErrPluginRuleNotFound, e.report.Profile, name)
	}
	var users []string
	for _, r := range e.next.Rules.Plugins {
		if r.Group == name {
			users = append(users, r.Name)
		}
	}
	for _, g := range e.next.Rules.Groups {
		if slices.Contains(g.After, name) {
			users = append(users, "group "+g.Name)
		}
	}
	if len(users) > 0 {
		return fmt.Errorf("%w: group %s is still used by %s - remove those first", ErrInvalidPluginEdit, name, strings.Join(users, ", "))
	}
	e.next.Rules.Groups = slices.Delete(e.next.Rules.Groups, i, i+1)
	e.changes = append(e.changes, fmt.Sprintf("group %s: removed", name))
	return nil
}
