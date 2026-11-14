package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// The plugins report (#617, design docs/plans/2026-11-10-collections-
// design.md §5 "Frontends"): the effective plugin order `lmm plugins list`
// prints and the web UI's Plugins tab renders - the order lmm writes to the
// game's Plugins.txt, worked out exactly as the deploy works it out
// (computePluginListWith), with what each plugin is, what it needs, and
// why it sits where it does.

// ErrNoPluginList is the refusal for a game whose adapter manages no plugin
// list: only a Bethesda game (adapter: bethesda) has one.
var ErrNoPluginList = errors.New("this game has no plugin list: only a game on the bethesda adapter has one (lmm game edit <game> --adapter bethesda)")

// PluginPlacementImplicit, PluginPlacementExternal, PluginPlacementOrder,
// PluginPlacementNew, PluginPlacementRule and PluginPlacementMaster are
// PluginOrderEntry.Placement's values: what decided the plugin's place.
const (
	// PluginPlacementImplicit: one of the game's own plugins, which always
	// load first and are never listed.
	PluginPlacementImplicit = "implicit"
	// PluginPlacementExternal: a plugin lmm did not deploy (installed by
	// hand, or by the game's Creations menu), kept where the list had it.
	PluginPlacementExternal = "external"
	// PluginPlacementOrder: the profile's plugin order puts it here.
	PluginPlacementOrder = "order"
	// PluginPlacementNew: the profile's order does not list it yet, so it
	// loads last, after its masters.
	PluginPlacementNew = "new"
	// PluginPlacementRule: a profile rule (after, req or a group) holds it
	// after a plugin the order alone would put it before.
	PluginPlacementRule = "rule"
	// PluginPlacementMaster: one of its masters holds it after a plugin
	// the profile's order alone would put it before.
	PluginPlacementMaster = "master"
)

// PluginOrderEntry is one plugin of a PluginOrderReport.
type PluginOrderEntry struct {
	// Position is its place in the load order, from 1, the game's own
	// plugins included.
	Position int `json:"position"`
	// Name is the plugin's file name.
	Name string `json:"name"`
	// Active is whether the game loads it.
	Active bool `json:"active"`
	// Master, Light and Medium are its header's flags (with the
	// extension's: an .esm is a master, an .esl light).
	Master bool `json:"master"`
	Light  bool `json:"light"`
	Medium bool `json:"medium"`
	// Masters are the masters its header names; MissingMasters those of
	// them that are not active, when it is.
	Masters        []string `json:"masters,omitempty"`
	MissingMasters []string `json:"missing_masters,omitempty"`
	// Managed reports that lmm deploys it. False for the game's own
	// plugins and for an external one.
	Managed bool `json:"managed"`
	// SourceID, ModID and ModName are the mod that deploys it, when lmm
	// does.
	SourceID string `json:"source_id,omitempty"`
	ModID    string `json:"mod_id,omitempty"`
	ModName  string `json:"mod_name,omitempty"`
	// Placement is what decided its place (PluginPlacement*).
	Placement string `json:"placement"`
	// Constraints are the edges that hold it after a plugin the profile's
	// order alone would have put it before (PluginPlacementRule and
	// PluginPlacementMaster).
	Constraints []PluginOrderEdge `json:"constraints,omitempty"`
}

// PluginOrderReport is `lmm plugins list`'s document and the web UI's
// Plugins tab: the effective load order of a profile's plugins.
type PluginOrderReport struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Active reports that Profile is the game's active profile. Only its
	// plugins are deployed, so only its effective order can be worked out:
	// for another profile Plugins is empty and NotDeployed is its whole
	// recorded order.
	Active bool `json:"active"`
	// ListFile is the game's Plugins.txt, when its prefix resolves.
	ListFile string `json:"list_file,omitempty"`
	// Sorted is false when a cycle stopped the sort: Plugins then follow
	// the profile's order with no constraint applied, and Findings carry
	// the cycle.
	Sorted bool `json:"sorted"`
	// Plugins is the load order, first loads first.
	Plugins []PluginOrderEntry `json:"plugins"`
	// NotDeployed are the profile's order entries whose plugin is not
	// deployed: kept, so re-enabling its mod puts it back where it was.
	NotDeployed []domain.PluginEntry `json:"not_deployed,omitempty"`
	// Rules are the profile's plugin rules.
	Rules domain.PluginRules `json:"rules"`
	// Findings are the problems the deploy would report, errors first.
	Findings []PluginFinding `json:"findings,omitempty"`
}

// PluginOrder reports profileName's effective plugin order (#617). It
// writes nothing. It refuses a game whose adapter manages no plugin list
// (ErrNoPluginList) and a profile that does not exist.
func (s *Service) PluginOrder(ctx context.Context, game *domain.Game, profileName string) (*PluginOrderReport, error) {
	return s.pluginOrder(ctx, game, profileName, nil)
}

// pluginOrder is PluginOrder with plugins, when set, standing in for the
// profile's plugins: block - a plan's proposal.
func (s *Service) pluginOrder(ctx context.Context, game *domain.Game, profileName string, plugins *domain.ProfilePlugins) (*PluginOrderReport, error) {
	spec, ok := s.pluginRules(game)
	if !ok {
		return nil, fmt.Errorf("%s: %w", game.ID, ErrNoPluginList)
	}
	profile, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil {
		return nil, err
	}
	block := profile.Plugins
	if plugins != nil {
		block = *plugins
	}
	report := &PluginOrderReport{GameID: game.ID, Profile: profileName, Rules: block.Clone().Rules, Plugins: []PluginOrderEntry{}}
	if _, recordedOnly, err := s.profileScope(ctx, game.ID, profileName); err != nil {
		return nil, err
	} else if recordedOnly {
		report.Sorted = true
		report.NotDeployed = slices.Clone(block.Order)
		return report, nil
	}
	report.Active = true
	c, err := s.computePluginListWith(ctx, game, profileName, spec, pluginComputeOpts{plugins: &block, report: true})
	if err != nil {
		return nil, err
	}
	res := c.result()
	report.ListFile = res.ListFile
	report.Findings = res.Findings
	report.Sorted = c.cycle == nil
	report.Plugins = s.pluginOrderEntries(ctx, game, profileName, c, spec)
	placed := map[string]bool{}
	for _, e := range report.Plugins {
		placed[pluginKey(e.Name)] = true
	}
	seen := map[string]bool{}
	for _, e := range block.Order {
		key := pluginKey(e.Name)
		if placed[key] || seen[key] || c.managed[key] {
			continue
		}
		seen[key] = true
		report.NotDeployed = append(report.NotDeployed, e)
	}
	return report, nil
}

// pluginOrderEntries is c's effective order as report entries: the game's
// own plugins first, then every plugin c placed.
func (s *Service) pluginOrderEntries(ctx context.Context, game *domain.Game, profileName string, c *pluginComputation, spec adapter.PluginSpec) []PluginOrderEntry {
	names := map[string]string{}
	if mods, err := s.GetInstalledMods(ctx, game.ID, profileName); err == nil {
		for _, m := range mods {
			names[domain.ModKey(m.SourceID, m.ID)] = m.Name
		}
	}
	active := map[string]bool{}
	for _, name := range c.implicit {
		active[pluginKey(name)] = true
	}
	for k := range c.implicitlyActive {
		active[k] = true
	}
	for _, n := range c.sorted {
		if n.active {
			active[pluginKey(n.name)] = true
		}
	}
	missing := func(isActive bool, masters []string) []string {
		if !isActive {
			return nil
		}
		var out []string
		for _, m := range masters {
			if !active[pluginKey(m)] {
				out = append(out, m)
			}
		}
		return out
	}
	binding := map[string][]PluginOrderEdge{}
	if c.cycle == nil && len(c.managedReq.nodes) > 0 {
		binding = bindingEdges(c.managedReq)
	}
	listed := map[string]bool{}
	for _, e := range c.profile.Plugins.Order {
		listed[pluginKey(e.Name)] = true
	}

	out := make([]PluginOrderEntry, 0, len(c.implicit)+len(c.sorted))
	for _, name := range c.implicit {
		f := c.dirs.files[pluginKey(name)]
		h, _ := readHeaderAt(filepath.Join(f.dir, f.name), spec)
		class := ClassifyPlugin(f.name, h, spec)
		out = append(out, PluginOrderEntry{
			Position: len(out) + 1, Name: name, Active: true,
			Master: class.Master, Light: class.Light, Medium: class.Medium,
			Masters: h.Masters, Placement: PluginPlacementImplicit,
		})
	}
	for _, n := range c.sorted {
		key := pluginKey(n.name)
		e := PluginOrderEntry{
			Position: len(out) + 1, Name: n.name, Active: n.active,
			Master: n.class.Master, Light: n.class.Light, Medium: n.class.Medium,
			Masters: n.masters, MissingMasters: missing(n.active, n.masters),
			Managed: n.managed,
		}
		switch {
		case !n.managed:
			e.Placement = PluginPlacementExternal
		default:
			if n.modIndex >= 0 && n.modIndex < len(c.profile.Mods) {
				ref := c.profile.Mods[n.modIndex]
				e.SourceID, e.ModID = ref.SourceID, ref.ModID
				e.ModName = names[domain.ModKey(ref.SourceID, ref.ModID)]
			}
			e.Constraints = binding[key]
			e.Placement = placementOf(listed[key], e.Constraints)
		}
		out = append(out, e)
	}
	return out
}

// placementOf is a managed plugin's PluginOrderEntry.Placement: a rule
// that holds it first, then a hard edge, then the profile's order.
func placementOf(listed bool, constraints []PluginOrderEdge) string {
	hard := false
	for _, e := range constraints {
		switch e.Reason {
		case PluginEdgeAfter, PluginEdgeReq, PluginEdgeGroup:
			return PluginPlacementRule
		default:
			hard = true
		}
	}
	switch {
	case hard:
		return PluginPlacementMaster
	case listed:
		return PluginPlacementOrder
	default:
		return PluginPlacementNew
	}
}
