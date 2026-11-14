package core

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The file load-order report (#620): `lmm load-order list`'s document and
// the web UI's load-order tab - the effective order of a profile's files
// for each of the game's file load orders, and what placed each one.

// ErrNoFileLoadOrder is the refusal for a game whose adapter has no file
// load order, or not the one asked for: only Cyberpunk 2077 (adapter:
// cyberpunk2077) has one.
var ErrNoFileLoadOrder = errors.New("this game has no file load order: only a game on the cyberpunk2077 adapter has one (lmm game edit <game> --adapter cyberpunk2077)")

// FileLoadOrderEntry is one file of a FileLoadOrderReport.
type FileLoadOrderEntry struct {
	// Position is its place in the order, from 1.
	Position int `json:"position"`
	// Name is the file's name, as spelled on disk (or in the list, for an
	// external one).
	Name string `json:"name"`
	// Managed reports that lmm deploys it.
	Managed bool `json:"managed"`
	// SourceID, ModID and ModName are the mod that deploys it, when lmm
	// does.
	SourceID string `json:"source_id,omitempty"`
	ModID    string `json:"mod_id,omitempty"`
	ModName  string `json:"mod_name,omitempty"`
	// Placement is what decided its place (LoadOrderPlaced*).
	Placement string `json:"placement"`
}

// FileLoadOrderReport is one file load order of a profile.
type FileLoadOrderReport struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// ID is the load order's id, and Label its display name.
	ID    string `json:"id"`
	Label string `json:"label"`
	// ListFile is the list file the order is written to.
	ListFile string `json:"list_file,omitempty"`
	// FirstWins reports that a file earlier in the order wins a conflict
	// with a later one.
	FirstWins bool `json:"first_wins"`
	// Active reports that Profile is the game's active profile. Only its
	// files are deployed, so only its effective order can be worked out:
	// for another profile Files is empty and NotDeployed is its whole
	// recorded order.
	Active bool `json:"active"`
	// Recorded reports that the profile orders this list, so lmm writes
	// it at every deploy. When false the game uses its own order, and
	// Files shows that order for lmm's files; moving one records it.
	Recorded bool `json:"recorded"`
	// Files is the order, first loads first.
	Files []FileLoadOrderEntry `json:"files"`
	// NotDeployed are the profile's entries whose file is not deployed:
	// kept, so re-enabling its mod puts it back where it was.
	NotDeployed []string `json:"not_deployed,omitempty"`
	// Findings are the problems the deploy would report, errors first.
	Findings []LoadOrderFinding `json:"findings,omitempty"`
}

// FileLoadOrdersReport is `lmm load-order list`'s document: every file load
// order of a profile.
type FileLoadOrdersReport struct {
	GameID  string                `json:"game_id"`
	Profile string                `json:"profile"`
	Orders  []FileLoadOrderReport `json:"orders"`
}

// FileLoadOrders reports profileName's file load orders (#620). It writes
// nothing. It refuses a game whose adapter has none (ErrNoFileLoadOrder)
// and a profile that does not exist.
func (s *Service) FileLoadOrders(ctx context.Context, game *domain.Game, profileName string) (*FileLoadOrdersReport, error) {
	specs := s.loadOrderSpecs(game)
	if len(specs) == 0 {
		return nil, fmt.Errorf("%s: %w", game.ID, ErrNoFileLoadOrder)
	}
	out := &FileLoadOrdersReport{GameID: game.ID, Profile: profileName, Orders: []FileLoadOrderReport{}}
	for _, spec := range specs {
		r, err := s.fileLoadOrder(ctx, game, profileName, spec, nil)
		if err != nil {
			return nil, err
		}
		out.Orders = append(out.Orders, *r)
	}
	return out, nil
}

// FileLoadOrder reports one of profileName's file load orders, by id.
func (s *Service) FileLoadOrder(ctx context.Context, game *domain.Game, profileName, id string) (*FileLoadOrderReport, error) {
	spec, err := s.resolveLoadOrderSpec(game, id)
	if err != nil {
		return nil, err
	}
	return s.fileLoadOrder(ctx, game, profileName, spec, nil)
}

// resolveLoadOrderSpec is game's load order id; "" names the game's only
// one.
func (s *Service) resolveLoadOrderSpec(game *domain.Game, id string) (adapter.FileLoadOrderSpec, error) {
	specs := s.loadOrderSpecs(game)
	if id == "" && len(specs) == 1 {
		return specs[0], nil
	}
	for _, spec := range specs {
		if spec.ID == id {
			return spec, nil
		}
	}
	if len(specs) == 0 {
		return adapter.FileLoadOrderSpec{}, fmt.Errorf("%s: %w", game.ID, ErrNoFileLoadOrder)
	}
	if id == "" {
		return adapter.FileLoadOrderSpec{}, fmt.Errorf("%w: %s has more than one load order; name one", ErrInvalidLoadOrderEdit, game.ID)
	}
	return adapter.FileLoadOrderSpec{}, fmt.Errorf("%s has no load order %q: %w", game.ID, id, ErrNoFileLoadOrder)
}

// fileLoadOrder is FileLoadOrder for spec, with order, when non-nil,
// standing in for the profile's recorded order - a plan's proposal.
func (s *Service) fileLoadOrder(ctx context.Context, game *domain.Game, profileName string, spec adapter.FileLoadOrderSpec, order []string) (*FileLoadOrderReport, error) {
	profile, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	if order == nil {
		order = slices.Clone(profile.LoadOrders[spec.ID])
		if order == nil {
			order = []string{}
		}
	}
	report := &FileLoadOrderReport{
		GameID: game.ID, Profile: profileName, ID: spec.ID, Label: spec.Label, FirstWins: spec.FirstWins,
		Recorded: len(order) > 0, Files: []FileLoadOrderEntry{},
	}
	if _, recordedOnly, err := s.profileScope(ctx, game.ID, profileName); err != nil {
		return nil, err
	} else if recordedOnly {
		report.NotDeployed = dedupeNames(order)
		return report, nil
	}
	report.Active = true
	c, err := s.computeLoadOrder(ctx, game, profileName, spec, order, true)
	if err != nil {
		return nil, err
	}
	res := c.result()
	report.ListFile = res.ListFile
	report.Findings = res.Findings

	names := map[string]string{}
	if mods, err := s.GetInstalledMods(ctx, game.ID, profileName); err == nil {
		for _, m := range mods {
			names[domain.ModKey(m.SourceID, m.ID)] = m.Name
		}
	}
	for _, name := range c.entries {
		key := loadOrderKey(name)
		e := FileLoadOrderEntry{Position: len(report.Files) + 1, Name: name, Placement: c.placement[key]}
		if idx, ok := c.managed[key]; ok {
			e.Managed = true
			if idx >= 0 && idx < len(profile.Mods) {
				ref := profile.Mods[idx]
				e.SourceID, e.ModID = ref.SourceID, ref.ModID
				e.ModName = names[domain.ModKey(ref.SourceID, ref.ModID)]
			}
		}
		report.Files = append(report.Files, e)
	}
	for _, name := range dedupeNames(order) {
		if _, ok := c.managed[loadOrderKey(name)]; !ok {
			report.NotDeployed = append(report.NotDeployed, name)
		}
	}
	return report, nil
}

// dedupeNames is names without a repeat (compared case-insensitively),
// each at its first place.
func dedupeNames(names []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		if !seen[loadOrderKey(n)] {
			seen[loadOrderKey(n)] = true
			out = append(out, n)
		}
	}
	return out
}
