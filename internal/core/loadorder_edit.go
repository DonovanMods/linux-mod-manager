package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// The file load-order edits (#620): `lmm load-order move|reset` and the web
// UI's load-order tab, as one Plan/Apply pair. An edit changes the
// profile's load_orders: list - the user's intent - and, for the active
// profile, Apply then writes the game's list file from it, exactly as a
// deploy would (syncLoadOrders).
//
// A move records the EFFECTIVE order in the profile: the list the user
// sees, with the move applied, so recording it moves nothing else. A
// profile that ordered nothing starts ordering the list with its first
// move. A reset clears the profile's order: lmm hands the list back to the
// game (and the user) at once, as a purge would.
//
// Only lmm's own files are the profile's to move: a file lmm did not
// deploy keeps its place in the order the game loads it in (#620's
// ownership ruling). An edit naming one is refused.

// LoadOrderOpMove and LoadOrderOpReset are FileLoadOrderEdit.Op's values.
const (
	// LoadOrderOpMove moves FileLoadOrderEdit.File before or after another
	// file, or to the first or last place.
	LoadOrderOpMove = "move"
	// LoadOrderOpReset clears the profile's order: the game's own order
	// applies again.
	LoadOrderOpReset = "reset"
)

// LoadOrderMoveFirst and LoadOrderMoveLast are FileLoadOrderEdit.To's
// values.
const (
	LoadOrderMoveFirst = "first"
	LoadOrderMoveLast  = "last"
)

// ErrLoadOrderFileNotFound is the refusal for an edit naming a file the
// order does not have.
var ErrLoadOrderFileNotFound = errors.New("file not in the load order")

// ErrLoadOrderFileNotOwned is the refusal for an edit moving a file lmm
// did not deploy.
var ErrLoadOrderFileNotOwned = errors.New("not a file lmm deploys")

// ErrInvalidLoadOrderEdit is the refusal for an edit that cannot mean
// anything: a move with no destination, or more than one.
var ErrInvalidLoadOrderEdit = errors.New("invalid load-order edit")

// FileLoadOrderEdit is one change to a profile's file load order.
type FileLoadOrderEdit struct {
	// ID is the load order ("cyberpunk2077-archive"); empty names the
	// game's only one.
	ID string `json:"id,omitempty"`
	// Op is the edit (LoadOrderOp*).
	Op string `json:"op"`
	// File is the file a move moves; Before, After or To
	// (LoadOrderMoveFirst or LoadOrderMoveLast) - exactly one - is where.
	File   string `json:"file,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	To     string `json:"to,omitempty"`
}

// FileLoadOrderEditPlan is what PlanFileLoadOrderEdit worked out: `lmm
// load-order ... --dry-run`'s document.
type FileLoadOrderEditPlan struct {
	GameID  string            `json:"game_id"`
	Profile string            `json:"profile"`
	Edit    FileLoadOrderEdit `json:"edit"`
	// Active reports that Profile is the game's active profile: Apply
	// writes the game's list file as well as the profile.
	Active bool `json:"active"`
	// Changes are what the edit changes, a sentence each.
	Changes []string `json:"changes"`
	// NoChanges reports that the profile already says this.
	NoChanges bool `json:"no_changes"`
	// Order is the order after the edit.
	Order *FileLoadOrderReport `json:"order"`

	// order is the list Apply writes (nil for a reset); base the one the
	// plan was worked out from, which Apply requires unchanged.
	order, base []string
}

// FileLoadOrderEditResult is what ApplyFileLoadOrderEdit did: `lmm
// load-order ...`'s document.
type FileLoadOrderEditResult struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Changed reports that the profile was written.
	Changed bool `json:"changed"`
	// Changes are the plan's.
	Changes []string `json:"changes"`
	// List is what writing the game's list file did; nil for a profile
	// that is not the active one, or when there was nothing to write.
	List *FileLoadOrderResult `json:"list,omitempty"`
	// Order is the order now.
	Order *FileLoadOrderReport `json:"order"`
}

// PlanFileLoadOrderEdit works out what edit does to profileName's file
// load order (#620). It writes nothing. It refuses a game without that
// load order (ErrNoFileLoadOrder), an edit naming a file the order does
// not have (ErrLoadOrderFileNotFound), one moving a file lmm did not
// deploy (ErrLoadOrderFileNotOwned), and one that cannot mean anything
// (ErrInvalidLoadOrderEdit).
func (s *Service) PlanFileLoadOrderEdit(ctx context.Context, game *domain.Game, profileName string, edit FileLoadOrderEdit) (*FileLoadOrderEditPlan, error) {
	spec, err := s.resolveLoadOrderSpec(game, edit.ID)
	if err != nil {
		return nil, err
	}
	edit.ID = spec.ID
	before, err := s.fileLoadOrder(ctx, game, profileName, spec, nil)
	if err != nil {
		return nil, err
	}
	profile, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	base := slices.Clone(profile.LoadOrders[spec.ID])
	plan := &FileLoadOrderEditPlan{GameID: game.ID, Profile: profileName, Edit: edit, Active: before.Active, base: base}
	switch edit.Op {
	case LoadOrderOpReset:
		plan.NoChanges = len(base) == 0
		if !plan.NoChanges {
			plan.Changes = []string{fmt.Sprintf("clear the profile's %s order (%d file(s)): the game's own order applies again", spec.Label, len(dedupeNames(base)))}
		}
	case LoadOrderOpMove:
		next, change, err := moveInOrder(before, base, edit)
		if err != nil {
			return nil, err
		}
		plan.order = next
		plan.NoChanges = slices.Equal(next, base)
		if !plan.NoChanges {
			plan.Changes = []string{change}
			if len(base) == 0 {
				plan.Changes = append(plan.Changes, fmt.Sprintf("the profile now orders the %s list, so lmm writes %s at every deploy", spec.Label, spec.ListFile))
			}
		}
	default:
		return nil, fmt.Errorf("%w: unknown op %q (want %s or %s)", ErrInvalidLoadOrderEdit, edit.Op, LoadOrderOpMove, LoadOrderOpReset)
	}
	if plan.Changes == nil {
		plan.Changes = []string{}
	}
	proposal := plan.order
	if proposal == nil {
		proposal = []string{}
	}
	if plan.Order, err = s.fileLoadOrder(ctx, game, profileName, spec, proposal); err != nil {
		return nil, err
	}
	return plan, nil
}

// moveInOrder is base after edit's move, worked out against the order the
// user sees (report): that order's files of lmm's, with the move applied,
// and base's entries whose files are not deployed after the entry they
// followed in base. change describes the move.
func moveInOrder(report *FileLoadOrderReport, base []string, edit FileLoadOrderEdit) ([]string, string, error) {
	dests := 0
	for _, d := range []string{edit.Before, edit.After, edit.To} {
		if d != "" {
			dests++
		}
	}
	if edit.File == "" || dests != 1 {
		return nil, "", fmt.Errorf("%w: a move names one file and exactly one of before, after or to", ErrInvalidLoadOrderEdit)
	}
	if edit.To != "" && edit.To != LoadOrderMoveFirst && edit.To != LoadOrderMoveLast {
		return nil, "", fmt.Errorf("%w: to is %q or %q, not %q", ErrInvalidLoadOrderEdit, LoadOrderMoveFirst, LoadOrderMoveLast, edit.To)
	}
	if !report.Active {
		return nil, "", fmt.Errorf("%w: %s is not the active profile, so its files are not deployed and have no order to move in - switch to it first", ErrInvalidLoadOrderEdit, report.Profile)
	}
	find := func(name string) (FileLoadOrderEntry, error) {
		for _, e := range report.Files {
			if loadOrderKey(e.Name) == loadOrderKey(name) {
				if !e.Managed {
					return e, fmt.Errorf("%s: %w; it keeps its place among the files lmm did not deploy", e.Name, ErrLoadOrderFileNotOwned)
				}
				return e, nil
			}
		}
		return FileLoadOrderEntry{}, fmt.Errorf("%s: %w", name, ErrLoadOrderFileNotFound)
	}
	moving, err := find(edit.File)
	if err != nil {
		return nil, "", err
	}
	var managed []string
	for _, e := range report.Files {
		if e.Managed && loadOrderKey(e.Name) != loadOrderKey(moving.Name) {
			managed = append(managed, e.Name)
		}
	}
	var at int
	var change string
	switch edit.To {
	case LoadOrderMoveFirst:
		at, change = 0, fmt.Sprintf("move %s to the top", moving.Name)
	case LoadOrderMoveLast:
		at, change = len(managed), fmt.Sprintf("move %s to the end", moving.Name)
	default:
		other := edit.Before
		if other == "" {
			other = edit.After
		}
		anchor, err := find(other)
		if err != nil {
			return nil, "", err
		}
		if loadOrderKey(anchor.Name) == loadOrderKey(moving.Name) {
			return nil, "", fmt.Errorf("%w: %s cannot move relative to itself", ErrInvalidLoadOrderEdit, moving.Name)
		}
		at = slices.IndexFunc(managed, func(n string) bool { return loadOrderKey(n) == loadOrderKey(anchor.Name) })
		if edit.Before != "" {
			change = fmt.Sprintf("move %s before %s", moving.Name, anchor.Name)
		} else {
			at++
			change = fmt.Sprintf("move %s after %s", moving.Name, anchor.Name)
		}
	}
	managed = slices.Insert(managed, at, moving.Name)
	return withNotDeployed(managed, report.NotDeployed, base), change, nil
}

// withNotDeployed is order with the entries notDeployed (base's entries
// whose files are not deployed) put back after the entry each followed in
// base, so a mod disabled for now keeps its files' places.
func withNotDeployed(order, notDeployed, base []string) []string {
	if len(notDeployed) == 0 {
		return order
	}
	nodes := func(names []string) []pluginNode {
		out := make([]pluginNode, len(names))
		for i, n := range names {
			out[i] = pluginNode{name: n}
		}
		return out
	}
	current := make([]listEntry, len(base))
	for i, n := range base {
		current[i] = listEntry{name: n}
	}
	ranks := mergedRanks(nodes(order), nodes(notDeployed), current)
	out := append(slices.Clone(order), notDeployed...)
	slices.SortStableFunc(out, func(a, b string) int { return ranks[loadOrderKey(a)] - ranks[loadOrderKey(b)] })
	return out
}

// loadOrderEditAfterSave is a test seam: called between
// ApplyFileLoadOrderEdit's profile write and its list write. nil in
// production.
var loadOrderEditAfterSave func()

// ApplyFileLoadOrderEdit writes plan's order to the profile and, for the
// active profile, the game's list file (#620). It refuses a plan whose
// profile's order changed since it was made (ErrStalePlan). That is the
// whole freshness check: the plan's only write is the profile's order, so
// only a change to that order can make it stale; the deployed files and the
// list file are read again by the sync, which writes them as they are at
// Apply time, never as the plan saw them. A list the sync could not write
// is a finding on the result, never an error: the profile was written by
// then.
func (s *Service) ApplyFileLoadOrderEdit(ctx context.Context, game *domain.Game, plan *FileLoadOrderEditPlan) (*FileLoadOrderEditResult, error) {
	if plan == nil {
		return nil, fmt.Errorf("%w: no plan", ErrInvalidLoadOrderEdit)
	}
	spec, err := s.resolveLoadOrderSpec(game, plan.Edit.ID)
	if err != nil {
		return nil, err
	}
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	profile, err := config.LoadProfile(s.configDir, game.ID, plan.Profile)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(profile.LoadOrders[spec.ID], plan.base) {
		return nil, fmt.Errorf("%w: profile %s's %s order changed since the plan was made", ErrStalePlan, plan.Profile, spec.Label)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res := &FileLoadOrderEditResult{GameID: game.ID, Profile: plan.Profile, Changes: slices.Clone(plan.Changes)}
	if !plan.NoChanges {
		orders := profile.LoadOrders.Clone()
		if orders == nil {
			orders = domain.FileLoadOrders{}
		}
		if len(plan.order) == 0 {
			delete(orders, spec.ID)
		} else {
			orders[spec.ID] = slices.Clone(plan.order)
		}
		profile.LoadOrders = orders.Clone()
		if err := s.NewProfileManager().save(profile); err != nil {
			return nil, err
		}
		res.Changed = true
	}
	if loadOrderEditAfterSave != nil {
		loadOrderEditAfterSave()
	}
	// The profile is written; the list is derived from it, and is written
	// whatever the caller's context does now (Ruling 16).
	ctx = context.WithoutCancel(ctx)
	if _, recordedOnly, err := s.profileScope(ctx, game.ID, plan.Profile); err == nil && !recordedOnly {
		res.List = s.syncLoadOrder(ctx, game, plan.Profile, spec)
	}
	if res.Order, err = s.fileLoadOrder(ctx, game, plan.Profile, spec, nil); err != nil {
		return nil, err
	}
	return res, nil
}
