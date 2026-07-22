package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/source/custom"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// coreProvider adapts *core.Service to the read-only DataProvider boundary.
//
// profileMu guards profile (Task 6 item b): SetProfile (called from the
// Bubble Tea Update goroutine when a profile-switch action completes - see
// app.go's actionDoneMsg handler / rebindProfile) writes it, while a
// still-in-flight Search call (its own independent goroutine, NOT gated by
// the action single-flight guard - see Search/installedModKeys) may read it
// concurrently. Every read goes through currentProfile(); every write
// through SetProfile - never read/write p.profile directly elsewhere in
// this file. resolvedHooks takes the already-resolved profile as a
// parameter (rather than re-reading p.profile itself) precisely so the
// caching layer below never needs to nest a second lock inside profileMu's
// critical section (sync.RWMutex is not reentrant).
//
// hooksMu guards the resolvedHooks/hookRunner result cache (Task 6 item c):
// both used to re-read+parse their backing config YAML from disk on EVERY
// action call (5a review Minor, "now hot with 5b's frequent actions") -
// each is now computed at most once per coreProvider instance and reused.
// A SEPARATE mutex from profileMu, deliberately, for the same
// non-reentrancy reason noted above; it is read from flow goroutines (any
// ActionProvider method may run on Bubble Tea's flow goroutine - see
// actions.go's buildAction) so it needs the same mutex discipline as
// profileMu, just not the SAME mutex.
type coreProvider struct {
	svc  *core.Service
	game *domain.Game

	profileMu sync.RWMutex
	profile   string

	hooksMu sync.Mutex
	// cachedHooks is profile-specific (a profile's hooks.yaml overrides can
	// differ from another's - see resolvedHooks), so SetProfile invalidates
	// it on every rebind, even a same-name one (cheap, and correctness
	// doesn't depend on detecting a genuine change). That invalidation is
	// keyed on profile SWITCHES only, not on disk edits: a user editing the
	// ACTIVE profile's hooks.yaml overrides while the TUI keeps running
	// that same profile still gets the stale, already-cached ResolvedHooks
	// for the rest of the session - the CLI has no such staleness, since it
	// re-reads and re-resolves hooks fresh on every invocation. Switching
	// away and back to the profile (or restarting the TUI) is what picks up
	// the edit.
	cachedHooks *core.ResolvedHooks
	// cachedRunner is NOT profile- or game-specific (HookTimeout is a
	// single global config.yaml setting - see hookRunner), so it is cached
	// for this coreProvider's whole lifetime once computed and SetProfile
	// never touches it. That also means it has no invalidation path at all:
	// a user editing config.yaml's hook_timeout while the TUI is running
	// keeps getting whatever timeout was in effect at the first hook-running
	// action of the session for every action after it - only restarting the
	// TUI re-reads config.yaml (the CLI, by contrast, re-reads it fresh on
	// every invocation via its own getHookRunner).
	cachedRunner *core.HookRunner
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
	profile := p.currentProfile()
	mods, err := p.svc.GetInstalledMods(p.game.ID, profile)
	if err != nil {
		return Summary{}, nil, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, profile, err)
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
		ProfileName: profile,
		Installed:   len(mods),
		Enabled:     enabled,
		Updates:     -1, // unknown: on-demand checks are wired (CheckUpdates, Phase 5b); a persistent, always-visible summary-strip COUNT is Phase 6
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
	profile := p.currentProfile()
	installed, err := p.svc.GetInstalledMods(p.game.ID, profile)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, profile, err)
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

// currentProfile returns the session's current active profile, guarded by
// profileMu (Task 6 item b) - the single read path every method on this
// type must use instead of touching p.profile directly.
func (p *coreProvider) currentProfile() string {
	p.profileMu.RLock()
	defer p.profileMu.RUnlock()
	return p.profile
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
//
// Task 6 item b: SetProfile runs on the Bubble Tea Update goroutine while a
// still-in-flight Search call (a different goroutine - see
// currentProfile's doc comment) may be reading p.profile concurrently;
// profileMu makes that safe.
//
// Task 6 item c: a profile switch can change which profile's hooks.yaml
// override applies (see resolvedHooks), so the cached ResolvedHooks is
// invalidated here too - cachedRunner is NOT (never profile-specific - see
// the struct's doc comment).
func (p *coreProvider) SetProfile(name string) {
	p.profileMu.Lock()
	p.profile = name
	p.profileMu.Unlock()

	p.hooksMu.Lock()
	p.cachedHooks = nil
	p.hooksMu.Unlock()
}

func (p *coreProvider) Profiles(_ context.Context) ([]ProfileItem, error) {
	activeProfile := p.currentProfile()
	profiles, err := p.svc.NewProfileManager().List(p.game.ID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles for %s: %w", p.game.ID, err)
	}

	items := make([]ProfileItem, 0, len(profiles))
	for _, profile := range profiles {
		items = append(items, ProfileItem{
			Name:     profile.Name,
			Active:   profile.Name == activeProfile,
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
//
// Task 6 item c: the underlying config read (config.Load) is neither game-
// nor profile-specific - HookTimeout is a single global setting - so the
// constructed *core.HookRunner is cached for this coreProvider's whole
// lifetime once computed (see the struct's doc comment on cachedRunner).
func (p *coreProvider) hookRunner() *core.HookRunner {
	p.hooksMu.Lock()
	defer p.hooksMu.Unlock()
	if p.cachedRunner != nil {
		return p.cachedRunner
	}

	cfg, err := config.Load(p.svc.ConfigDir())
	timeout := 60 * time.Second // default, matching cmd/lmm/hooks.go
	if err == nil && cfg.HookTimeout > 0 {
		timeout = time.Duration(cfg.HookTimeout) * time.Second
	}
	p.cachedRunner = core.NewHookRunner(timeout)
	return p.cachedRunner
}

// resolvedHooks resolves activeProfile's hooks, mirroring
// cmd/lmm/hooks.go's getResolvedHooks (minus its --no-hooks short-circuit -
// see hookRunner's doc comment). Takes the already-resolved profile as a
// parameter, rather than reading p.profile itself, so every caller reads
// p.profile exactly once via currentProfile() (Task 6 item b) - see the
// struct's doc comment.
//
// Task 6 item c: unlike hookRunner, this result genuinely varies per
// profile (a profile's hooks.yaml overrides can differ from another's), so
// it is cached only until SetProfile rebinds the session (see that
// method's doc comment and cachedHooks' own).
func (p *coreProvider) resolvedHooks(activeProfile string) *core.ResolvedHooks {
	p.hooksMu.Lock()
	defer p.hooksMu.Unlock()
	if p.cachedHooks != nil {
		return p.cachedHooks
	}

	var profile *domain.Profile
	if activeProfile != "" {
		if pr, err := config.LoadProfile(p.svc.ConfigDir(), p.game.ID, activeProfile); err == nil {
			profile = pr
		}
	}
	p.cachedHooks = core.ResolveHooks(p.game, profile)
	return p.cachedHooks
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
	result, err := p.svc.EnableMod(ctx, p.game, p.currentProfile(), item.Source, item.ID)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("enabling %s: %w", item.Name, err)
	}
	if !result.Changed {
		return ActionOutcome{Message: fmt.Sprintf("%q is already enabled", item.Name)}, nil
	}
	return ActionOutcome{Message: fmt.Sprintf("Enabled %q", item.Name), Warnings: mergeDiagnostics(nil, result.Notes)}, nil
}

func (p *coreProvider) DisableMod(ctx context.Context, item ModItem) (ActionOutcome, error) {
	result, err := p.svc.DisableMod(ctx, p.game, p.currentProfile(), item.Source, item.ID)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("disabling %s: %w", item.Name, err)
	}
	if !result.Changed {
		return ActionOutcome{Message: fmt.Sprintf("%q is already disabled", item.Name)}, nil
	}
	return ActionOutcome{Message: fmt.Sprintf("Disabled %q", item.Name), Warnings: mergeDiagnostics(nil, result.Notes)}, nil
}

// UninstallMod runs the same hook configuration cmd/lmm/uninstall.go's
// doUninstall passes to core.UninstallMod (KeepCache=false, Force=false -
// see hookRunner's doc comment for why hooks are never disabled here).
func (p *coreProvider) UninstallMod(ctx context.Context, item ModItem) (ActionOutcome, error) {
	profile := p.currentProfile()
	opts := core.UninstallOptions{
		KeepCache:   false,
		Hooks:       p.resolvedHooks(profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(),
		Force:       false,
	}
	result, err := p.svc.UninstallMod(ctx, p.game, profile, item.Source, item.ID, opts)
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
	profile := p.currentProfile()
	opts := core.DeployOptions{
		Hooks:       p.resolvedHooks(profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(),
		Force:       false,
	}
	result, err := p.svc.DeployProfile(ctx, p.game, profile, opts, nil)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("deploying profile %s: %w", profile, err)
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
// comment - so a fresh plan is always current) and applies it, streaming
// download/install progress for any ToInstall entries through progress
// (nil-safe). Phase 5b Task 4 LIFTED the NeedsDownloads refusal 5a's TUI
// used to enforce here (no install/download path existed yet then) - the
// plan's ToInstall entries now download and install exactly like the CLI's
// own doProfileSwitch would, via svc.ApplyProfileSwitch's own install loop.
// AlreadyActive plans are reported without calling ApplyProfileSwitch at
// all, mirroring cmd/lmm/profile.go's doProfileSwitch, which returns before
// ever calling it in that case.
//
// Preview/apply drift (Task 6 item e, documented honestly rather than
// "fixed" - no behavior change): the confirmation modal the user actually
// sees (mutations.go's switchSelectedProfile/resolvePlanResult) is built
// from a SEPARATE, EARLIER PlanProfileSwitch call - the one that decided
// whether to show a modal at all and what its detail lines say. THIS
// method's own re-plan above is a second, independent PlanProfileSwitch
// call, made at confirm time. Anything that changes the diff between those
// two calls (a manual install/uninstall from a shell, another profile
// mutation, a source's catalog changing underfoot) means the plan actually
// executed here can differ from what the modal showed - e.g. a mod the
// modal listed under ToEnable might have been uninstalled in the interim
// and now falls out of the plan entirely, or a NEW mod could appear. This
// mirrors PlanProfileSwitch's own doc comment ("speculatively... and
// discard the result without consequence") taken to its logical
// conclusion: speculative plans are cheap precisely because they're
// disposable, not because they're pinned to what gets applied later.
//
// Fix wave 2 (review finding): core.SwitchResult never records a per-mod
// install failure anywhere (SwitchInstallError's doc comment in flows.go:
// "these are NOT accumulated into any SwitchResult slice" - core.DeployPhase
// only recorded UpsertMod's own SwitchInstallNote into result.Notes). Left
// alone, a NeedsDownloads switch whose install loop hits a
// fetch/get-files/no-files/file-selection/deploy/save failure for one mod
// (SwitchInstallError) or a download failure (SwitchDownloadFailed) would
// silently report "Switched to X" with zero warnings, even though the CLI
// prints "Error: %s" for exactly these phases unconditionally
// (cmd/lmm/profile.go). installFailures below is this method's OWN
// accumulator - built from a progress observer that runs regardless of
// whether the CALLER passed a non-nil progress (unlike
// deployProgressAdapter, which no-ops entirely when progress is nil - a
// caller applying a switch with progress=nil, e.g. a "fire and check the
// outcome" caller, must still see the failure in Warnings).
func (p *coreProvider) ApplyProfileSwitch(ctx context.Context, profileName string, progress func(ActionProgress)) (ActionOutcome, error) {
	plan, err := p.svc.PlanProfileSwitch(ctx, p.game, profileName)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("planning switch to %s: %w", profileName, err)
	}
	if plan.AlreadyActive {
		return ActionOutcome{Message: fmt.Sprintf("Already on profile %q", profileName)}, nil
	}

	// installFailures and the observer below are local to this call: a
	// plain, unsynchronized slice is race-safe here because there is
	// exactly one writer and one reader, both on the same goroutine, never
	// overlapping in time. The writer is onProgress, invoked synchronously
	// by p.svc.ApplyProfileSwitch's own emit() calls (flows.go) from
	// whatever goroutine is executing THIS call - which, per
	// actions.go/buildAction's doc comment, is the single flow goroutine
	// Bubble Tea spins up to run a confirmed pendingAction's do(); nothing
	// else ever calls a coreProvider method concurrently with it (the
	// Model's single-flight guard blocks a second action while one is
	// running). The reader is the "Warnings: mergeDiagnostics(...)" line
	// below, which cannot execute until p.svc.ApplyProfileSwitch has
	// returned - i.e. until onProgress can no longer be called at all. So
	// the write phase (during the blocking call) and the read phase (after
	// it returns) never overlap, on the same goroutine besides.
	var installFailures []string
	onProgress := func(evt core.DeployProgress) {
		switch evt.Phase {
		case core.SwitchInstallError, core.SwitchDownloadFailed:
			installFailures = append(installFailures, fmt.Sprintf("%s:%s: %s", evt.SourceID, evt.ModID, evt.Detail))
		}
		if progress == nil {
			return
		}
		if line, ok := switchProgressLine(evt); ok {
			progress(line)
		}
	}

	result, err := p.svc.ApplyProfileSwitch(ctx, p.game, plan, onProgress)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("switching to %s: %w", profileName, err)
	}
	return ActionOutcome{
		Message:  fmt.Sprintf("Switched to %q", profileName),
		Warnings: mergeDiagnostics(installFailures, result.Notes),
	}, nil
}

// deployProgressAdapter wraps a nil-safe ActionProgress callback into a
// func(core.DeployProgress), applying compose to translate each event into
// a display line - nil in (progress nil) yields nil out, so
// ApplyInstall/ApplyUpdate/ApplyProfileSwitch never allocate a wrapper
// closure they'd have to call at every one of their own emit() sites just
// to no-op.
func deployProgressAdapter(progress func(ActionProgress), compose func(core.DeployProgress) (ActionProgress, bool)) func(core.DeployProgress) {
	if progress == nil {
		return nil
	}
	return func(p core.DeployProgress) {
		if line, ok := compose(p); ok {
			progress(line)
		}
	}
}

// switchProgressLine composes an ActionProgress from one core.DeployProgress
// event during ApplyProfileSwitch's install loop (the phases relevant to a
// TUI status line - see core.DeployPhase's Switch* constants for the full
// set this deliberately narrows).
//
// Fix wave 2 (review finding, item 2) added SwitchInstallError/
// SwitchDownloadFailed/SwitchFallbackUsed: the CLI (cmd/lmm/profile.go)
// prints "    Error: %s" for the first two and an unconditional (NOT
// --verbose-gated) "    Warning: stored file IDs not found, using primary"
// for the third, so all three are user-visible live output there - dropping
// them here via the default case left the TUI with no live sign of a
// failing/fallback-using install while the switch was still running (the
// completed outcome's Warnings, see coreProvider.ApplyProfileSwitch, is the
// only other place a failure surfaces, and used to be silently empty too).
// SourceID/ModID identify the mod for all three - see DeployProgress.
// SourceID's own doc comment: it's set for every ApplyProfileSwitch
// install-loop event from SwitchInstallingMod onward, including these,
// unlike ModName, which SwitchInstallError may fire before its mod is even
// fetched (fetch-failure is one of its listed reasons).
func switchProgressLine(p core.DeployProgress) (ActionProgress, bool) {
	switch p.Phase {
	case core.SwitchDownloading:
		return ActionProgress{Line: fmt.Sprintf("Switching: downloading %s:%s %.0f%%", p.SourceID, p.ModID, p.Percent), Percent: p.Percent}, true
	case core.SwitchInstallingMod:
		return ActionProgress{Line: fmt.Sprintf("Switching: installing %s:%s (%d/%d)", p.SourceID, p.ModID, p.Index, p.Total), Percent: -1}, true
	case core.SwitchEnabled, core.SwitchDisabled, core.SwitchInstalled:
		return ActionProgress{Line: fmt.Sprintf("Switching: %s (%d/%d)", p.ModName, p.Index, p.Total), Percent: -1}, true
	case core.SwitchInstallError, core.SwitchDownloadFailed:
		return ActionProgress{Line: fmt.Sprintf("Switching: %s:%s failed - %s", p.SourceID, p.ModID, p.Detail), Percent: -1}, true
	case core.SwitchFallbackUsed:
		return ActionProgress{Line: fmt.Sprintf("Switching: %s:%s - stored file IDs not found, using primary", p.SourceID, p.ModID), Percent: -1}, true
	default:
		return ActionProgress{}, false
	}
}

// installProgressLine composes an ActionProgress from one core.DeployProgress
// event during ApplyInstall, for both the STRICT (primary-only) and BATCH
// (dependency-inclusive) paths - see core.DeployPhase's Install* constants.
// modName is the primary mod's display name, used for the STRICT-path-only
// phases (InstallDownloading/InstallExtracting/InstallDeploying), which have
// no ModName of their own since they're always about the primary.
func installProgressLine(modName string, p core.DeployProgress) (ActionProgress, bool) {
	switch p.Phase {
	case core.InstallDownloading:
		return ActionProgress{Line: fmt.Sprintf("Installing %s: %.0f%%", modName, p.Percent), Percent: p.Percent}, true
	case core.InstallDepDownloading:
		return ActionProgress{Line: fmt.Sprintf("Installing %s: %.0f%%", p.ModName, p.Percent), Percent: p.Percent}, true
	case core.InstallExtracting:
		return ActionProgress{Line: fmt.Sprintf("Installing %s: extracting", modName), Percent: -1}, true
	case core.InstallDeploying:
		return ActionProgress{Line: fmt.Sprintf("Installing %s: deploying", modName), Percent: -1}, true
	case core.InstallDepInstalling:
		return ActionProgress{Line: fmt.Sprintf("Installing %s (%d/%d)", p.ModName, p.Index, p.Total), Percent: -1}, true
	default:
		return ActionProgress{}, false
	}
}

// updateProgressLine composes an ActionProgress from one core.DeployProgress
// event during ApplyUpdate - only UpdateDownloading carries anything worth
// a status line (see core.DeployPhase's Update* constants).
func updateProgressLine(modName string, p core.DeployProgress) (ActionProgress, bool) {
	if p.Phase == core.UpdateDownloading {
		return ActionProgress{Line: fmt.Sprintf("Updating %s: %.0f%%", modName, p.Percent), Percent: p.Percent}, true
	}
	return ActionProgress{}, false
}

// mapNetworkError classifies err from a network-touching ActionProvider call
// into the design's §7 + auth error contract: domain.ErrAuthRequired becomes
// the auth-hint wording the TUI search path already renders (app.go's
// searchAuthRequired case: "Authentication required for %s." / "Run 'lmm
// auth login %s' in a shell, then search again.") - collapsed to one line
// here (these are one-line status/error text, not a multi-line view) and
// reworded "try again" since none of these callers are a search.
// source.ErrNotSupported becomes a clean one-line capability-gap notice
// mirroring cmd/lmm/search.go's capabilityGapNotice, naming sourceID plus
// capability (what the source can't do) and fallback (the correct CLI
// command for the ACTUAL action the caller was performing - see the review
// finding this fixes below). Everything else is wrapped with %w under
// action, a short present-participle label (e.g. "planning install of
// SkyUI").
//
// mapNetworkError is deliberately unexported and only called through the
// per-action wrappers below (mapInstallNetworkError/mapUpdateNetworkError):
// a single shared "does not support this; use lmm install..." message for
// every call site used to suggest the install-path fallback even when the
// actual failure was an updates-capability gap surfaced through ApplyUpdate's
// CheckUpdates re-check (Phase 5b Task 4 review finding) - clearly wrong
// advice. Each wrapper supplies capability/fallback text matching what its
// own callers are actually trying to do, so the notice can never point at
// the wrong CLI command again.
func mapNetworkError(action, sourceID, capability, fallback string, err error) error {
	switch {
	case errors.Is(err, source.ErrNotSupported):
		return fmt.Errorf("source %q does not support %s; %s", sourceID, capability, fallback)
	case errors.Is(err, domain.ErrAuthRequired):
		return fmt.Errorf("Authentication required for %s. Run 'lmm auth login %s' in a shell, then try again.", sourceID, sourceID)
	default:
		return fmt.Errorf("%s: %w", action, err)
	}
}

// mapInstallNetworkError is mapNetworkError for the install-path callers
// (PlanInstall, ApplyInstall's re-plan and apply steps): a capability gap
// here means the source can't be planned/installed against, and the correct
// CLI fallback is the CLI's own single-mod install command.
func mapInstallNetworkError(action, sourceID string, err error) error {
	return mapNetworkError(action, sourceID, "installing",
		fmt.Sprintf("use 'lmm install --source %s --id <mod-id>' from a shell", sourceID), err)
}

// mapUpdateNetworkError is mapNetworkError for the update-path callers
// (ApplyUpdate's CheckUpdates re-check and apply steps): a capability gap
// here means the source can't report or apply updates - naming "installing"
// or suggesting 'lmm install' would be wrong advice for this gap (the Task 4
// review finding this fixes), so this names updates and points at the CLI's
// own update command instead.
func mapUpdateNetworkError(action, sourceID string, err error) error {
	return mapNetworkError(action, sourceID, "checking for updates", "run 'lmm update' from a shell instead", err)
}

// fileDisplayLabel renders a domain.DownloadableFile as a short display
// string for InstallPlanView.Files: its declared Name, falling back to
// FileName - simpler than cmd/lmm/install.go's own displayFileLabel (which
// internal/tui cannot import - see customSourceType's doc comment for why
// CLI-only helpers are duplicated rather than shared), but sufficient for
// the TUI's one-line-per-file plan display.
func fileDisplayLabel(f domain.DownloadableFile) string {
	if f.Name != "" {
		return f.Name
	}
	return f.FileName
}

// installPlanView maps a core.InstallPlan to its TUI render model.
// Conflicts render as "path (owned by <mod-id>)" (InstallPlanView.Conflicts'
// documented format); MissingDependencies render as domain.ModKey(sourceID,
// modID), mirroring cmd/lmm/install.go's showInstallPlan warning line.
func installPlanView(plan *core.InstallPlan) InstallPlanView {
	view := InstallPlanView{
		Name:         plan.Mod.Name,
		Version:      plan.Mod.Version,
		Source:       plan.SourceID,
		SizeLabel:    installSizeLabel(plan.TotalDownloadBytes),
		CycleWarning: plan.CycleDetected,
		Reinstall:    plan.Replaces != nil,
	}
	for _, f := range plan.Files {
		view.Files = append(view.Files, fileDisplayLabel(f))
	}
	for _, dep := range plan.Dependencies {
		view.Dependencies = append(view.Dependencies, fmt.Sprintf("%s v%s", dep.Name, dep.Version))
	}
	for _, c := range plan.Conflicts {
		view.Conflicts = append(view.Conflicts, fmt.Sprintf("%s (owned by %s)", c.RelativePath, c.CurrentModID))
	}
	for _, md := range plan.MissingDependencies {
		view.MissingDependencies = append(view.MissingDependencies, domain.ModKey(md.SourceID, md.ModID))
	}
	return view
}

// PlanInstall computes what installing item would do, mapped from
// svc.PlanInstall - the install-modal analog of PlanProfileSwitch.
// showArchived is always false (the TUI has no --show-archived equivalent
// yet - matching the CLI's own non-interactive default when that flag is
// omitted).
func (p *coreProvider) PlanInstall(ctx context.Context, item ModItem) (InstallPlanView, error) {
	plan, err := p.svc.PlanInstall(ctx, p.game, p.currentProfile(), item.Source, item.ID, false)
	if err != nil {
		return InstallPlanView{}, mapInstallNetworkError(fmt.Sprintf("planning install of %s", item.Name), item.Source, err)
	}
	return installPlanView(plan), nil
}

// ApplyInstall re-plans (mirroring ApplyProfileSwitch's own re-plan-at-apply
// precedent) and applies with the SAME hook configuration cmd/lmm/install.go's
// doInstall passes (Force=false, SkipVerify=false - the CLI's own
// --force/--skip-verify defaults), installing plan.Files exactly as planned:
// unlike the CLI, the TUI has no interactive/--file file-selection step, so
// PlanInstall's own non-interactive default (the primary-or-first file - see
// InstallPlan.Files' doc comment) is always what gets installed.
//
// Deliberately diverges from the CLI on conflicts (C1 review finding): the
// TUI has only a single upfront confirm modal, not a second blocking
// prompt mid-flight, so ConfirmConflicts below always returns true
// (auto-proceeds) rather than aborting - but it never silently hides an
// overwrite either, folding each conflicting file into Outcome.Warnings as
// "overwrote: <path> (owned by <mod-id>)", mirroring the BATCH path's own
// non-blocking "N file(s) conflict" warning philosophy (applyInstallBatchMod
// in internal/core/flows.go) rather than the CLI's blocking one.
func (p *coreProvider) ApplyInstall(ctx context.Context, item ModItem, progress func(ActionProgress)) (ActionOutcome, error) {
	profile := p.currentProfile()
	plan, err := p.svc.PlanInstall(ctx, p.game, profile, item.Source, item.ID, false)
	if err != nil {
		return ActionOutcome{}, mapInstallNetworkError(fmt.Sprintf("planning install of %s", item.Name), item.Source, err)
	}

	var conflictWarnings []string
	opts := core.InstallOptions{
		SkipVerify:  false,
		Hooks:       p.resolvedHooks(profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(),
		Force:       false,
		ConfirmConflicts: func(conflicts []core.Conflict) bool {
			for _, c := range conflicts {
				conflictWarnings = append(conflictWarnings, fmt.Sprintf("overwrote: %s (owned by %s)", c.RelativePath, c.CurrentModID))
			}
			return true
		},
	}

	adapter := deployProgressAdapter(progress, func(p core.DeployProgress) (ActionProgress, bool) {
		return installProgressLine(item.Name, p)
	})
	result, err := p.svc.ApplyInstall(ctx, p.game, plan, opts, adapter)
	if err != nil {
		return ActionOutcome{}, mapInstallNetworkError(fmt.Sprintf("installing %s", item.Name), item.Source, err)
	}

	// Warnings = result Warnings + Notes (mergeDiagnostics' documented
	// order), plus - the BATCH path only, when plan.Dependencies is
	// non-empty - result.Skipped's "<name>: <reason>" entries (I1 review
	// finding: bare Failed names carried no reason at all; Skipped already
	// pairs each failure with why - see InstallResult's doc comment), plus
	// any conflict-overwrite disclosures recorded above.
	warnings := mergeDiagnostics(result.Warnings, result.Notes)
	warnings = append(warnings, result.Skipped...)
	warnings = append(warnings, conflictWarnings...)

	// I1 review finding: a BATCH-path install (plan.Dependencies non-empty)
	// never fails on a primary's failure - ApplyInstall returns nil error
	// with the primary named in result.Failed instead (see InstallResult's
	// doc comment) - so unconditionally claiming "Installed %q" here was a
	// false success whenever the PRIMARY was the one that failed. A STRICT
	// (no-deps) primary failure is already fatal above (err != nil), so this
	// branch is only reachable via the BATCH path.
	message := fmt.Sprintf("Installed %q", item.Name)
	if slices.Contains(result.Failed, item.Name) {
		message = fmt.Sprintf("Installed %d of %d mod(s)", len(result.Installed), len(plan.Dependencies)+1)
	}
	return ActionOutcome{
		Message:  message,
		Warnings: warnings,
	}, nil
}

// CheckUpdates reports available updates for every checkable installed mod
// (pinned/local mods are already filtered by core.Updater.CheckUpdates -
// not re-filtered here). A per-source failure there is a partial-results
// situation (Updater.CheckUpdates' own doc comment): whatever updates DID
// resolve still populate Updates, and the failure itself becomes a single
// Warning - mirroring cmd/lmm/update.go's doUpdate, which does the exact
// same "Warning: %v\n, then continue showing partial updates" for this
// error, except for domain.ErrAuthRequired, which doUpdate special-cases
// via authPromptError(updateSource) - a single resolved --source flag value
// that isn't always the ACTUAL failing source when checking multiple mods
// across sources. The TUI has no such flag to name either, so its own
// ErrAuthRequired Warning names no specific source - every individual
// per-source failure is still legible inside the underlying joined
// "source %s: %w" text this doesn't discard.
func (p *coreProvider) CheckUpdates(ctx context.Context) (UpdatesView, error) {
	profile := p.currentProfile()
	installed, err := p.svc.GetInstalledMods(p.game.ID, profile)
	if err != nil {
		return UpdatesView{}, fmt.Errorf("loading installed mods for %s/%s: %w", p.game.ID, profile, err)
	}

	updates, checkErr := p.svc.NewUpdater().CheckUpdates(ctx, p.game, installed)
	var view UpdatesView
	for _, u := range updates {
		view.Updates = append(view.Updates, UpdateItem{
			Source: u.InstalledMod.SourceID, ID: u.InstalledMod.ID, Name: u.InstalledMod.Name,
			FromVersion: u.InstalledMod.Version, ToVersion: u.NewVersion,
		})
	}
	if checkErr != nil {
		if errors.Is(checkErr, domain.ErrAuthRequired) {
			view.Warnings = append(view.Warnings, fmt.Sprintf(
				"Authentication required for one or more sources. Run 'lmm auth login <source>' in a shell, then try again (%v)", checkErr))
		} else {
			view.Warnings = append(view.Warnings, checkErr.Error())
		}
	}
	return view, nil
}

// ApplyUpdate applies u with the SAME hook configuration cmd/lmm/update.go's
// applyUpdate passes (Force=false, its default). u is re-checked via
// CheckUpdates for just this one mod first - mirroring
// cmd/lmm/update.go's applySingleUpdate, which does the same before calling
// applyUpdate - rather than reconstructing a bare domain.Update from u's own
// fields: UpdateItem carries no FileIDReplacements (see its doc comment),
// and a real update may need that superseded-file-ID mapping to install
// correctly; only a fresh CheckUpdates call can supply it.
func (p *coreProvider) ApplyUpdate(ctx context.Context, u UpdateItem, progress func(ActionProgress)) (ActionOutcome, error) {
	profile := p.currentProfile()
	mod, err := p.svc.GetInstalledMod(u.Source, u.ID, p.game.ID, profile)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("getting installed mod %s: %w", u.Name, err)
	}

	updates, err := p.svc.NewUpdater().CheckUpdates(ctx, p.game, []domain.InstalledMod{*mod})
	if err != nil {
		return ActionOutcome{}, mapUpdateNetworkError(fmt.Sprintf("checking update for %s", u.Name), u.Source, err)
	}
	if len(updates) == 0 {
		return ActionOutcome{Message: fmt.Sprintf("%q is already up to date", u.Name)}, nil
	}
	upd := updates[0]

	opts := core.UpdateOptions{
		Hooks:       p.resolvedHooks(profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(),
		Force:       false,
	}

	adapter := deployProgressAdapter(progress, func(p core.DeployProgress) (ActionProgress, bool) {
		return updateProgressLine(u.Name, p)
	})
	result, err := p.svc.ApplyUpdate(ctx, p.game, profile, upd, opts, adapter)
	if err != nil {
		return ActionOutcome{}, mapUpdateNetworkError(fmt.Sprintf("updating %s", u.Name), u.Source, err)
	}
	return ActionOutcome{
		Message:  fmt.Sprintf("Updated %q to %s", u.Name, upd.NewVersion),
		Warnings: mergeDiagnostics(result.Warnings, result.Notes),
	}, nil
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
