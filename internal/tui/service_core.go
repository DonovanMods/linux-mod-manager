package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// coreProvider adapts *core.Service to the read-only DataProvider boundary.
type coreProvider struct {
	svc     *core.Service
	game    *domain.Game
	profile string
}

// NewCoreProvider returns a DataProvider backed by the real app service for
// one game/profile pair.
func NewCoreProvider(svc *core.Service, game *domain.Game, profileName string) DataProvider {
	return &coreProvider{svc: svc, game: game, profile: profileName}
}

// NewCoreActions returns an ActionProvider backed by the real app service,
// for the same (svc, game, profileName) triple NewCoreProvider takes. The
// two constructors are independent (coreProvider carries no in-memory-only
// state - every mutation goes through svc's DB/filesystem, so two separate
// instances always observe the same underlying truth), so a caller (Task
// 6/7's cmd/lmm/tui.go) can call both with the game/profile it already
// resolved once, without re-deriving anything.
func NewCoreActions(svc *core.Service, game *domain.Game, profileName string) ActionProvider {
	return &coreProvider{svc: svc, game: game, profile: profileName}
}

func (p *coreProvider) Overview(_ context.Context) (Summary, []ModItem, error) {
	mods, err := p.svc.GetInstalledMods(p.game.ID, p.profile)
	if err != nil {
		return Summary{}, nil, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, p.profile, err)
	}

	enabled := 0
	for _, mod := range mods {
		if mod.Enabled {
			enabled++
		}
	}

	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		items = append(items, ModItem{
			ID:      mod.ID,
			Name:    mod.Name,
			Author:  mod.Author,
			Version: mod.Version,
			Source:  mod.SourceID,
			Status:  installedModStatus(mod),
		})
	}

	return Summary{
		GameName:    p.game.Name,
		ProfileName: p.profile,
		Installed:   len(mods),
		Enabled:     enabled,
		Updates:     -1, // unknown: update checks are a Phase 6 workflow
		Conflicts:   -1, // unknown: conflict detection is a Phase 6 workflow
	}, items, nil
}

func (p *coreProvider) Sources() []string {
	sources := make([]string, 0, len(p.game.SourceIDs))
	for id := range p.game.SourceIDs {
		sources = append(sources, id)
	}
	sort.Strings(sources)
	return sources
}

// SourceInfos returns every source registered with the underlying service,
// sorted by ID. Sorting is required, not cosmetic: registry iteration order
// is Go map order, which is intentionally randomized, and an unsorted list
// would jitter row order between renders (mirrors cmd/lmm/auth.go's
// ListSources-sorting note).
func (p *coreProvider) SourceInfos() []SourceInfo {
	srcs := p.svc.ListSources()
	infos := make([]SourceInfo, 0, len(srcs))
	for _, src := range srcs {
		infos = append(infos, SourceInfo{
			ID:           src.ID(),
			Name:         src.Name(),
			Type:         customSourceType(src),
			Auth:         sourceAuthState(src),
			Capabilities: sourceCapabilitySummary(source.CapabilitiesOf(src)),
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	return infos
}

// customSourceType classifies a registered source for display. It mirrors
// cmd/lmm/source.go's isCustomSource switch; that helper isn't reused
// directly since cmd/lmm is a `package main` and not importable here, so the
// classification is kept in sync by hand — extend this switch alongside
// isCustomSource if a new custom source type ships.
func customSourceType(src source.ModSource) string {
	switch src.(type) {
	case *custom.Directory:
		return "directory"
	case *custom.Manifest:
		return "manifest"
	case *custom.API:
		return "api"
	default:
		return "built-in"
	}
}

// sourceAuthState reports a source's authentication status for display.
// Mirrors cmd/lmm/source.go's authState (see customSourceType's comment on
// why it's duplicated rather than imported).
func sourceAuthState(src source.ModSource) string {
	if !source.CapabilitiesOf(src).Auth {
		return "n/a"
	}
	if a, ok := src.(interface{ IsAuthenticated() bool }); ok {
		if a.IsAuthenticated() {
			return "yes"
		}
		return "no"
	}
	return "yes"
}

// sourceCapabilitySummary renders capabilities as a compact list, e.g.
// "search,updates". Mirrors cmd/lmm/source.go's capabilitySummary (see
// customSourceType's comment on why it's duplicated rather than imported).
func sourceCapabilitySummary(c source.Capabilities) string {
	out := ""
	add := func(enabled bool, name string) {
		if !enabled {
			return
		}
		if out != "" {
			out += ","
		}
		out += name
	}
	add(c.Search, "search")
	add(c.Dependencies, "deps")
	add(c.Updates, "updates")
	add(c.Auth, "auth")
	return out
}

// Search queries the given source, or every one of the game's configured
// sources when sourceID is "" (the all-sources sentinel), and marks results
// already installed in the active profile.
func (p *coreProvider) Search(ctx context.Context, sourceID, query string, page int) (SearchPage, error) {
	if sourceID == "" {
		agg, err := p.svc.SearchAllSources(ctx, p.game.ID, query, "", nil, page, SearchPageSize)
		if err != nil {
			return SearchPage{}, fmt.Errorf("searching all sources for %q: %w", query, err)
		}

		installedKeys, err := p.installedModKeys()
		if err != nil {
			return SearchPage{}, err
		}

		warnings := make([]string, 0, len(agg.Warnings))
		for _, w := range agg.Warnings {
			warnings = append(warnings, fmt.Sprintf("%s: %v", w.SourceID, w.Err))
		}

		return SearchPage{
			Results:    p.modsToItems(agg.Mods, installedKeys),
			Query:      query,
			Source:     sourceID,
			Page:       page,
			PageSize:   SearchPageSize,
			TotalCount: agg.TotalCount,
			Warnings:   warnings,
		}, nil
	}

	result, err := p.svc.SearchMods(ctx, sourceID, p.game.ID, query, "", nil, page, SearchPageSize)
	if err != nil {
		return SearchPage{}, fmt.Errorf("searching %s for %q: %w", sourceID, query, err)
	}

	installedKeys, err := p.installedModKeys()
	if err != nil {
		return SearchPage{}, err
	}

	return SearchPage{
		Results:    p.modsToItems(result.Mods, installedKeys),
		Query:      query,
		Source:     sourceID,
		Page:       page,
		PageSize:   SearchPageSize,
		TotalCount: result.TotalCount,
	}, nil
}

// installedModKeys returns the set of domain.ModKey(sourceID, modID) values
// installed in the active profile, used to mark search results as installed.
func (p *coreProvider) installedModKeys() (map[string]bool, error) {
	installed, err := p.svc.GetInstalledMods(p.game.ID, p.profile)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, p.profile, err)
	}
	keys := make(map[string]bool, len(installed))
	for _, mod := range installed {
		keys[domain.ModKey(mod.SourceID, mod.ID)] = true
	}
	return keys, nil
}

// modsToItems maps source search results to renderable rows, marking each
// as installed via domain.ModKey(sourceID, modID) against installedKeys.
func (p *coreProvider) modsToItems(mods []domain.Mod, installedKeys map[string]bool) []ModItem {
	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		status := "available"
		if installedKeys[domain.ModKey(mod.SourceID, mod.ID)] {
			status = "installed"
		}
		item := ModItem{
			ID:        mod.ID,
			Name:      mod.Name,
			Author:    mod.Author,
			Version:   mod.Version,
			Source:    mod.SourceID,
			Status:    status,
			Summary:   mod.Summary,
			Downloads: mod.Downloads,
		}
		if mod.Endorsements != nil {
			item.Endorsements = *mod.Endorsements
			item.HasEndorsements = true
		}
		items = append(items, item)
	}
	return items
}

// SetProfile rebinds the session to a new active profile: after a
// successful TUI-driven switch, core.Service.ApplyProfileSwitch has already
// persisted the new default profile (see its own doc comment in flows.go),
// but THIS instance's p.profile - fixed at NewCoreProvider/NewCoreActions
// construction time and read by every method on this type - would
// otherwise never find out, leaving Profiles()/Overview() starring the OLD
// profile and every mutation still targeting it. Implements app.go's
// optional profileRebinder hook via a plain method, not part of
// DataProvider/ActionProvider (both stay frozen at their documented
// contracts); see rebindProfile there for why both the Provider and Actions
// instances (cmd/lmm/tui.go wires two SEPARATE *coreProvider values, one
// per constructor) must each be rebound independently.
func (p *coreProvider) SetProfile(name string) {
	p.profile = name
}

func (p *coreProvider) Profiles(_ context.Context) ([]ProfileItem, error) {
	profiles, err := p.svc.NewProfileManager().List(p.game.ID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles for %s: %w", p.game.ID, err)
	}

	items := make([]ProfileItem, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, ProfileItem{
			Name:     profile.Name,
			Active:   profile.Name == p.profile,
			ModCount: len(profile.Mods),
		})
	}
	return items, nil
}

func installedModStatus(mod domain.InstalledMod) string {
	switch {
	case mod.Enabled && mod.Deployed:
		return "deployed"
	case mod.Enabled:
		return "enabled"
	default:
		return "disabled"
	}
}

// --- ActionProvider ---

// hookRunner returns a HookRunner using the game/profile's configured hook
// timeout, mirroring cmd/lmm/hooks.go's getHookRunner. The TUI has no
// --no-hooks equivalent yet (out of scope for this task), so - unlike the
// CLI helper, which returns nil when --no-hooks is set - this never returns
// nil: TUI-triggered mutations always run hooks, same as a default CLI
// invocation.
func (p *coreProvider) hookRunner() *core.HookRunner {
	cfg, err := config.Load(p.svc.ConfigDir())
	timeout := 60 * time.Second // default, matching cmd/lmm/hooks.go
	if err == nil && cfg.HookTimeout > 0 {
		timeout = time.Duration(cfg.HookTimeout) * time.Second
	}
	return core.NewHookRunner(timeout)
}

// resolvedHooks resolves game/profile hooks, mirroring cmd/lmm/hooks.go's
// getResolvedHooks (minus its --no-hooks short-circuit - see hookRunner's
// doc comment).
func (p *coreProvider) resolvedHooks() *core.ResolvedHooks {
	var profile *domain.Profile
	if p.profile != "" {
		if pr, err := config.LoadProfile(p.svc.ConfigDir(), p.game.ID, p.profile); err == nil {
			profile = pr
		}
	}
	return core.ResolveHooks(p.game, profile)
}

// hookContext mirrors cmd/lmm/hooks.go's makeHookContext.
func (p *coreProvider) hookContext() core.HookContext {
	return core.HookContext{
		GameID:   p.game.ID,
		GamePath: p.game.InstallPath,
		ModPath:  p.game.ModPath,
	}
}

func (p *coreProvider) EnableMod(ctx context.Context, item ModItem) (ActionOutcome, error) {
	changed, err := p.svc.EnableMod(ctx, p.game, p.profile, item.Source, item.ID)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("enabling %s: %w", item.Name, err)
	}
	if !changed {
		return ActionOutcome{Message: fmt.Sprintf("%q is already enabled", item.Name)}, nil
	}
	return ActionOutcome{Message: fmt.Sprintf("Enabled %q", item.Name)}, nil
}

func (p *coreProvider) DisableMod(ctx context.Context, item ModItem) (ActionOutcome, error) {
	changed, err := p.svc.DisableMod(ctx, p.game, p.profile, item.Source, item.ID)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("disabling %s: %w", item.Name, err)
	}
	if !changed {
		return ActionOutcome{Message: fmt.Sprintf("%q is already disabled", item.Name)}, nil
	}
	return ActionOutcome{Message: fmt.Sprintf("Disabled %q", item.Name)}, nil
}

// UninstallMod runs the same hook configuration cmd/lmm/uninstall.go's
// doUninstall passes to core.UninstallMod (KeepCache=false, Force=false -
// see hookRunner's doc comment for why hooks are never disabled here).
func (p *coreProvider) UninstallMod(ctx context.Context, item ModItem) (ActionOutcome, error) {
	opts := core.UninstallOptions{
		KeepCache:   false,
		Hooks:       p.resolvedHooks(),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(),
		Force:       false,
	}
	result, err := p.svc.UninstallMod(ctx, p.game, p.profile, item.Source, item.ID, opts)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("uninstalling %s: %w", item.Name, err)
	}
	return ActionOutcome{
		Message:  fmt.Sprintf("Uninstalled %q", item.Name),
		Warnings: mergeDiagnostics(result.Warnings, result.Notes),
	}, nil
}

// DeployProfile deploys with default options (no purge, no link-method
// override - matching a plain `lmm deploy` with no flags) and the same hook
// configuration cmd/lmm/deploy.go passes. progress is nil: 5a shows a
// static "working" state while this call is in flight; 5b streams
// core.DeployProgress events.
func (p *coreProvider) DeployProfile(ctx context.Context) (ActionOutcome, error) {
	opts := core.DeployOptions{
		Hooks:       p.resolvedHooks(),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(),
		Force:       false,
	}
	result, err := p.svc.DeployProfile(ctx, p.game, p.profile, opts, nil)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("deploying profile %s: %w", p.profile, err)
	}
	msg := fmt.Sprintf("Deployed %d mod(s)", result.Deployed)
	if len(result.Skipped) > 0 {
		msg += fmt.Sprintf(", %d failed", len(result.Skipped))
	}
	// result.Skipped carries one "<mod name>: <reason>" entry per mod that
	// didn't deploy (see DeployResult.Skipped's doc comment); appended after
	// the flow Warnings/Notes mergeDiagnostics already composed so the
	// status line's warning suffix can explain WHY a mod failed, not just
	// that one did. Appending an empty result.Skipped to a nil merge leaves
	// Warnings nil, matching every other DataProvider method's "no
	// diagnostics" convention.
	return ActionOutcome{
		Message:  msg,
		Warnings: append(mergeDiagnostics(result.Warnings, result.Notes), result.Skipped...),
	}, nil
}

func (p *coreProvider) PlanProfileSwitch(ctx context.Context, profileName string) (SwitchPlanView, error) {
	plan, err := p.svc.PlanProfileSwitch(ctx, p.game, profileName)
	if err != nil {
		return SwitchPlanView{}, fmt.Errorf("planning switch to %s: %w", profileName, err)
	}
	return switchPlanView(plan), nil
}

// ApplyProfileSwitch re-plans (PlanProfileSwitch is pure/cheap - see its doc
// comment - so a fresh plan is always current) and, unless the fresh plan
// needs downloads, applies it. A plan with NeedsDownloads entries is
// refused outright (no mutation): 5a's TUI has no install/download path
// yet (that ships in 5b), so honoring such a plan here would silently do
// less than the CLI equivalent. AlreadyActive plans are reported without
// calling ApplyProfileSwitch at all, mirroring cmd/lmm/profile.go's
// doProfileSwitch, which returns before ever calling it in that case.
// TODO(Phase 5b Task 4, part B): progress is accepted (interface parity
// with the pump - Part A) but not yet threaded to svc.ApplyProfileSwitch,
// and the NeedsDownloads refusal below is not yet lifted; both land
// together in this task's second RED/GREEN pair.
func (p *coreProvider) ApplyProfileSwitch(ctx context.Context, profileName string, progress func(ActionProgress)) (ActionOutcome, error) {
	_ = progress
	plan, err := p.svc.PlanProfileSwitch(ctx, p.game, profileName)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("planning switch to %s: %w", profileName, err)
	}
	if plan.AlreadyActive {
		return ActionOutcome{Message: fmt.Sprintf("Already on profile %q", profileName)}, nil
	}
	if len(plan.ToInstall) > 0 {
		return ActionOutcome{}, errProfileNeedsDownloads
	}

	result, err := p.svc.ApplyProfileSwitch(ctx, p.game, plan, nil)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("switching to %s: %w", profileName, err)
	}
	return ActionOutcome{
		Message:  fmt.Sprintf("Switched to %q", profileName),
		Warnings: mergeDiagnostics(nil, result.Notes),
	}, nil
}

// TODO(Phase 5b Task 4, part B RED): stubs only - fleshed out in the GREEN
// commit alongside prototypeProvider's own real implementations and the
// NeedsDownloads refusal lift above.

func (p *coreProvider) PlanInstall(_ context.Context, _ ModItem) (InstallPlanView, error) {
	return InstallPlanView{}, errors.New("not implemented")
}

func (p *coreProvider) ApplyInstall(_ context.Context, _ ModItem, _ func(ActionProgress)) (ActionOutcome, error) {
	return ActionOutcome{}, errors.New("not implemented")
}

func (p *coreProvider) CheckUpdates(_ context.Context) (UpdatesView, error) {
	return UpdatesView{}, errors.New("not implemented")
}

func (p *coreProvider) ApplyUpdate(_ context.Context, _ UpdateItem, _ func(ActionProgress)) (ActionOutcome, error) {
	return ActionOutcome{}, errors.New("not implemented")
}

// switchPlanView maps a core.SwitchPlan to its TUI render model, using the
// same display strings cmd/lmm/profile.go's doProfileSwitch plan printout
// uses: ToEnable/ToDisable entries are addressed by Name (the CLI's "  + %s
// (%s)\n"/"  - %s (%s)\n" lines also show the ID, but SwitchPlanView's
// Enable/Disable fields are documented as plain mod names). ToInstall
// entries have no Name yet (they haven't been fetched from source), so
// NeedsDownloads uses the CLI's own "%s:%s v%s" ref format ("  ↓ %s:%s
// v%s\n") verbatim - the only display data actually available at plan time.
func switchPlanView(plan *core.SwitchPlan) SwitchPlanView {
	view := SwitchPlanView{
		From:          plan.From,
		To:            plan.To,
		NoChanges:     plan.NoChanges,
		AlreadyActive: plan.AlreadyActive,
	}
	for _, im := range plan.ToEnable {
		view.Enable = append(view.Enable, im.Name)
	}
	for _, im := range plan.ToDisable {
		view.Disable = append(view.Disable, im.Name)
	}
	for _, ref := range plan.ToInstall {
		view.NeedsDownloads = append(view.NeedsDownloads, fmt.Sprintf("%s:%s v%s", ref.SourceID, ref.ModID, ref.Version))
	}
	return view
}
