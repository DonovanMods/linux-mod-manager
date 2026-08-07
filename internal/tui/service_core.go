package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/core"
	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/source"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// coreProvider adapts *core.Service to the read-only DataProvider boundary.
//
// gameMu guards game (Task 8): SetGame (called from the Bubble Tea Update
// goroutine when a game-switch picker choice resolves - see mutations.go's
// resolveGameSwitch / actions.go's rebindGame) writes it, while a
// still-in-flight Search call may read it concurrently - the exact same
// race profileMu (below) already guards against for p.profile, see its own
// doc comment for the full reasoning (TestCoreProviderProfileFieldRaceGuard
// mirrors this for p.game too). Every read goes through currentGame();
// every write through SetGame - never read/write p.game directly elsewhere
// in this file. A SEPARATE mutex from profileMu and hooksMu, deliberately,
// for the same non-reentrancy reason those two are already separate from
// each other (sync.RWMutex/sync.Mutex are not reentrant) - SetGame takes
// all three sequentially (gameMu, then profileMu, then hooksMu), NEVER
// nested, so no critical section here ever blocks waiting on itself.
// resolvedHooks/hookContext take the already-snapshotted *domain.Game as a
// parameter - never calling currentGame() themselves - precisely so
// gameMu.RLock is never acquired inside hooksMu's critical section (the
// same snapshot-then-lock discipline resolvedHooks has always applied to
// the profile, see its doc comment). Relatedly (Task 8 review's TOCTOU
// finding): any method that uses the game more than once per logical
// operation snapshots it ONCE up front (`game := p.currentGame()`,
// mirroring the existing `profile := p.currentProfile()` convention) so a
// concurrent SetGame can never split one operation across two games.
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
	svc *core.Service

	gameMu sync.RWMutex
	game   *domain.Game

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
	game := p.currentGame()
	profile := p.currentProfile()
	mods, err := p.svc.GetInstalledMods(game.ID, profile)
	if err != nil {
		return Summary{}, nil, fmt.Errorf("loading installed mods for %s/%s: %w", game.ID, profile, err)
	}

	// Deterministic load order (Task 4): the SAME order DeployProfile/
	// PlanProfileSwitch already use (Task 1's core.OrderByProfile), so the
	// list a user reorders with J/K on this screen IS the order that will
	// actually deploy - reordering anything else would be reordering a
	// display fiction. Nil-safe on an unreadable profile.yaml, mirroring
	// core.DeployProfile's own "don't abort the caller's operation, stay
	// deterministic anyway" precedent (flows.go).
	profileYAML, _ := config.LoadProfile(p.svc.ConfigDir(), game.ID, profile)
	mods = core.OrderByProfile(profileYAML, mods)

	// refsByKey joins the profile YAML's lock state onto the DB-sourced
	// mods list below by (sourceID, modID) - domain.ModKey (#97). Built
	// once, up front, rather than re-scanning profileYAML.Mods per item via
	// Profile.FindRef: same nil-safe "an unreadable profile.yaml leaves
	// every mod unlocked" degradation OrderByProfile above already accepts
	// (profileYAML may be nil here - see its own doc comment).
	refsByKey := make(map[string]domain.ModReference, len(mods))
	if profileYAML != nil {
		for _, ref := range profileYAML.Mods {
			refsByKey[domain.ModKey(ref.SourceID, ref.ModID)] = ref
		}
	}

	enabled := 0
	for _, mod := range mods {
		if mod.Enabled {
			enabled++
		}
	}

	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		item := ModItem{
			ID:              mod.ID,
			Name:            mod.Name,
			Author:          mod.Author,
			Version:         mod.Version,
			Source:          mod.SourceID,
			Status:          installedModStatus(mod),
			UpdatePolicy:    policyToString(mod.UpdatePolicy),
			PreviousVersion: mod.PreviousVersion,
			ConvertPaks:     mod.ConvertPaks,
			CompileGame:     game.DeployMode == domain.DeployCompile,
			GameConvertPaks: game.ConvertPaks,
			HasPakSource:    p.svc.ModHasPakMergeSource(&mod),
			// This row came from the installed-mods list, so its
			// install-state fields above (Version/UpdatePolicy, plus
			// Locked/LockedVersion set below) are genuine local install
			// state - see ModItem.InstalledRow's own doc comment for why
			// modDetailsFromItem gates on this instead of Status.
			InstalledRow: true,
			Profile:      profile,
		}
		// ModItem.LockedVersion is only ever populated alongside Locked
		// (see that field's own doc comment) - an unlocked ref's Version is
		// the installed-version record, not a lock target, so it's left
		// out here even when a ref exists.
		if ref, ok := refsByKey[domain.ModKey(mod.SourceID, mod.ID)]; ok && ref.Locked {
			item.Locked = true
			item.LockedVersion = ref.Version
		}
		items = append(items, item)
	}

	// #106a: recomputed on EVERY Overview call (every loadData refresh, not
	// just after a deploy action) - unlike Updates, which needs an explicit
	// user-triggered check, this is a plain, cheap DB read (an index-prefix
	// scan ordered and limited to one row, same shape as Conflicts' own
	// "ride every ordinary load" reasoning - see DataProvider.Conflicts' doc
	// comment), so there is no staleness/preservation logic to write here,
	// unlike dataLoadedMsg's Updates-preservation special case (app.go).
	lastDeploy, err := p.svc.GetLastDeployTime(game.ID, profile)
	if err != nil {
		return Summary{}, nil, fmt.Errorf("loading last deploy time for %s/%s: %w", game.ID, profile, err)
	}

	return Summary{
		GameName:    game.Name,
		ProfileName: profile,
		Installed:   len(mods),
		Enabled:     enabled,
		// Updates and Conflicts are BOTH always the -1 "unknown" sentinel
		// straight out of Overview, but for different reasons - neither is a
		// missing feature (#106b/#106c closeout: both features have long
		// since shipped):
		//   - Updates genuinely IS unknown here: it requires an explicit,
		//     user-triggered check (there is no cheap "is there an update"
		//     read), so Overview has nothing to report until
		//     resolveCheckUpdatesResult (mutations.go) sets m.summary.Updates
		//     to the real, already-in-hand count after a check completes.
		//   - Conflicts is NOT actually unknown by the time a caller sees it:
		//     Model.loadData (app.go) always fetches DataProvider.Conflicts()
		//     immediately after this call and overwrites summary.Conflicts
		//     with the real, current count on every ordinary refresh (a
		//     plain, cheap read, unlike Updates). This -1 here is only ever
		//     the momentary value between "Overview returned" and "loadData
		//     finished computing the live count" - callers that skip that
		//     step (rare, e.g. a test constructing Summary directly) are the
		//     only ones that would ever observe it as -1.
		Updates:    -1,
		Conflicts:  -1,
		LastDeploy: lastDeploy,
	}, items, nil
}

func (p *coreProvider) Sources() []string {
	game := p.currentGame()
	sources := make([]string, 0, len(game.SourceIDs))
	for id := range game.SourceIDs {
		sources = append(sources, id)
	}
	sort.Strings(sources)
	return sources
}

// SourceInfos returns the Sources screen's rows, sorted by ID (Task 4,
// #75): with all == false, only the active game's configured+registered
// sources (core.Service.SourcesForGame - the same intersection Sources()
// above computes by hand, but with full display columns and already sorted,
// so no separate sort.Slice call is needed for that branch); with all ==
// true, every registered source (svc.ListSources(), whose map-order
// iteration DOES need the explicit sort below - mirrors cmd/lmm/auth.go's
// ListSources-sorting note), each marked InUse when it belongs to the
// active game.
func (p *coreProvider) SourceInfos(all bool) []SourceInfo {
	game := p.currentGame()

	var srcs []source.ModSource
	var inUseIDs map[string]bool
	if all {
		srcs = p.svc.ListSources()
		// SourcesForGame only errors on an unknown game, which can't happen
		// here: game came from currentGame(), itself always a *domain.Game
		// the service already resolved (see NewCoreProvider's doc comment).
		// A failure would mean no source counts as in-use rather than a
		// crash - the safer degradation for a read-only display column.
		if scoped, err := p.svc.SourcesForGame(game.ID); err == nil {
			inUseIDs = make(map[string]bool, len(scoped))
			for _, s := range scoped {
				inUseIDs[s.ID()] = true
			}
		}
	} else if scoped, err := p.svc.SourcesForGame(game.ID); err == nil {
		srcs = scoped
	}

	infos := make([]SourceInfo, 0, len(srcs))
	for _, src := range srcs {
		infos = append(infos, SourceInfo{
			ID:           src.ID(),
			Name:         src.Name(),
			Type:         source.TypeLabelOf(src),
			Auth:         sourceAuthState(src),
			Capabilities: sourceCapabilitySummary(source.CapabilitiesOf(src)),
			InUse:        inUseIDs[src.ID()],
		})
	}
	if all {
		sort.Slice(infos, func(i, j int) bool { return infos[i].ID < infos[j].ID })
	}
	return infos
}

// sourceAuthState reports a source's authentication status for display.
// Mirrors cmd/lmm/source.go's authState. CANONICAL NOTE on this file's
// duplicated display helpers: cmd/lmm is package main, which internal/tui
// cannot import, so small CLI display helpers are mirrored here by hand
// rather than shared; other comments in this file reference this note.
// (Type labels themselves are no longer duplicated - both packages call
// source.TypeLabelOf.)
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
// sourceAuthState's comment on why it's duplicated rather than imported).
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
	add(c.Versions, "versions")
	return out
}

// Search queries the given source, or every one of the game's configured
// sources when sourceID is "" (the all-sources sentinel), and marks results
// already installed in the active profile. pageSize <= 0 falls back to
// SearchPageSize (defensive - see that constant's doc comment); every real
// caller (startSearch) derives a positive size via Model.searchFetchSize.
func (p *coreProvider) Search(ctx context.Context, sourceID, query string, page, pageSize int) (SearchPage, error) {
	if pageSize <= 0 {
		pageSize = SearchPageSize
	}

	if sourceID == "" {
		agg, err := p.svc.SearchAllSources(ctx, p.currentGame().ID, query, "", nil, page, pageSize)
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
			Results:        p.modsToItems(agg.Mods, installedKeys),
			Query:          query,
			Source:         sourceID,
			Page:           page,
			PageSize:       pageSize,
			TotalCount:     agg.TotalCount,
			Warnings:       warnings,
			Exhausted:      agg.Exhausted,
			AttemptedCount: agg.AttemptedCount,
		}, nil
	}

	result, err := p.svc.SearchMods(ctx, sourceID, p.currentGame().ID, query, "", nil, page, pageSize)
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
		PageSize:   pageSize,
		TotalCount: result.TotalCount,
	}, nil
}

// installedModKeys returns the set of domain.ModKey(sourceID, modID) values
// installed in the active profile, used to mark search results as installed.
func (p *coreProvider) installedModKeys() (map[string]bool, error) {
	game := p.currentGame()
	profile := p.currentProfile()
	installed, err := p.svc.GetInstalledMods(game.ID, profile)
	if err != nil {
		return nil, fmt.Errorf("loading installed mods for %s/%s: %w", game.ID, profile, err)
	}
	keys := make(map[string]bool, len(installed))
	for _, mod := range installed {
		keys[domain.ModKey(mod.SourceID, mod.ID)] = true
	}
	return keys, nil
}

// modsToItems maps source search results to renderable rows, marking each
// as installed via domain.ModKey(sourceID, modID) against installedKeys.
// Version here is always the SOURCE's own version for this mod - the
// latest upstream, not necessarily what's installed - so an "installed"
// status row deliberately leaves InstalledRow/Profile/UpdatePolicy/Locked/
// LockedVersion at their zero values rather than approximating genuine
// install state from data this function never fetched (see
// ModItem.InstalledRow's own doc comment; #86 review).
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

// currentGame returns the session's current active game, guarded by gameMu
// (Task 8, mirroring currentProfile below) - the single read path every
// method on this type must use instead of touching p.game directly.
func (p *coreProvider) currentGame() *domain.Game {
	p.gameMu.RLock()
	defer p.gameMu.RUnlock()
	return p.game
}

// ListGames enumerates every game configured for the underlying service
// (Task 8's 'g' binding - see mutations.go's openGameSwitcher), sorted by
// Name - the same "Go map iteration order is randomized" concern
// SourceInfos already documents, since svc.ListGames ranges over its own
// internal map.
func (p *coreProvider) ListGames() ([]GameInfo, error) {
	games := p.svc.ListGames()
	current := p.currentGame().ID

	infos := make([]GameInfo, 0, len(games))
	for _, g := range games {
		infos = append(infos, GameInfo{ID: g.ID, Name: g.Name, Active: g.ID == current})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos, nil
}

// resolveDefaultProfile mirrors cmd/lmm/root.go's resolveProfile fallback
// semantics (SetGame's own doc comment): the target game's default profile
// when one resolves cleanly, "default" on any error (most commonly
// domain.ErrProfileNotFound for a freshly-added game with no profiles yet -
// but any other resolution failure falls back the same way, matching the
// brief's own "on error fall back to 'default'" wording precisely rather
// than special-casing just the not-found error).
func resolveDefaultProfile(pm *core.ProfileManager, gameID string) string {
	profile, err := pm.GetDefault(gameID)
	if err != nil {
		return "default"
	}
	return profile.Name
}

// SetGame rebinds the session to a different configured game: id must name
// a game svc.GetGame resolves (an unknown id is refused, and the current
// binding is left completely untouched - no partial rebind). Implements
// actions.go's optional gameRebinder hook, mirroring SetProfile's own
// "plain method, not part of DataProvider/ActionProvider" shape - see that
// method's doc comment - except a game switch also picks a FRESH profile
// (resolveDefaultProfile above), since the profile bound at construction
// time (or from whatever game was previously active) has no reason to exist
// under the new game at all.
//
// Lock ordering (gameMu's own doc comment on the struct): gameMu, then
// profileMu, then hooksMu, taken and released SEQUENTIALLY - never nested -
// exactly like SetProfile already takes profileMu then hooksMu below.
// cachedHooks is invalidated for the same reason SetProfile invalidates it
// (a different game's hooks.yaml/games.yaml Hooks entirely supersede the
// old game's); cachedRunner deliberately survives (mirrors SetProfile - see
// the struct's own doc comment: HookTimeout is a single global config.yaml
// setting, not game- or profile-specific).
func (p *coreProvider) SetGame(id string) error {
	game, err := p.svc.GetGame(id)
	if err != nil {
		return fmt.Errorf("switching to game %s: %w", id, err)
	}
	profileName := resolveDefaultProfile(p.svc.NewProfileManager(), id)

	p.gameMu.Lock()
	p.game = game
	p.gameMu.Unlock()

	p.profileMu.Lock()
	p.profile = profileName
	p.profileMu.Unlock()

	p.hooksMu.Lock()
	p.cachedHooks = nil
	p.hooksMu.Unlock()

	return nil
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
	game := p.currentGame()
	activeProfile := p.currentProfile()
	profiles, err := p.svc.NewProfileManager().List(game.ID)
	if err != nil {
		return nil, fmt.Errorf("listing profiles for %s: %w", game.ID, err)
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

// DeployedFiles lists the relative paths sourceID/modID has deployed in the
// active profile. p.svc.GetDeployedFilesForMod's own query already ORDER
// BYs relative_path (internal/storage/db/files.go), so no defensive
// sort.Strings is needed here - unlike Sources()/SourceInfos(), which sort
// because their underlying iteration order (a Go map) is genuinely
// unordered.
func (p *coreProvider) DeployedFiles(sourceID, modID string) ([]string, error) {
	paths, err := p.svc.GetDeployedFilesForMod(p.currentGame().ID, p.currentProfile(), sourceID, modID)
	if err != nil {
		return nil, fmt.Errorf("loading deployed files for %s:%s: %w", sourceID, modID, err)
	}
	return paths, nil
}

// Conflicts lists every file conflict the active profile currently has
// (Task 3), delegating directly to svc.GetProfileConflicts and mapping each
// core.ProfileConflict to its TUI render model - Owner/Winner/AlsoIn take
// each ConflictModRef's Name (already falls back to Key when empty - see
// that type's own doc comment), so no separate fallback is needed here.
func (p *coreProvider) Conflicts(ctx context.Context) ([]ConflictItem, error) {
	game := p.currentGame()
	profile := p.currentProfile()
	conflicts, err := p.svc.GetProfileConflicts(ctx, game, profile)
	if err != nil {
		return nil, fmt.Errorf("getting conflicts for %s/%s: %w", game.ID, profile, err)
	}

	items := make([]ConflictItem, 0, len(conflicts))
	for _, c := range conflicts {
		alsoIn := make([]string, 0, len(c.AlsoIn))
		for _, ref := range c.AlsoIn {
			alsoIn = append(alsoIn, ref.Name)
		}
		items = append(items, ConflictItem{
			Path:   c.Path,
			Owner:  c.Owner.Name,
			Winner: c.LoadOrderWinner.Name,
			AlsoIn: alsoIn,
			Stale:  c.Stale,
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

// policyToString renders a domain.UpdatePolicy as ModItem.UpdatePolicy's
// documented "notify"/"auto"/"pin" wire strings - the inverse of
// parseUpdatePolicy below. Deliberately NOT shared with cmd/lmm/update.go's
// own policyToString (whose "pinned" spelling differs and which internal/tui
// cannot import - see sourceAuthState's doc comment for why CLI-only
// helpers are duplicated rather than shared): the TUI's ActionProvider
// contract fixes "pin" as the wire string (see SetUpdatePolicy's doc
// comment), so this must NOT reuse the CLI's "pinned".
func policyToString(policy domain.UpdatePolicy) string {
	switch policy {
	case domain.UpdateAuto:
		return "auto"
	case domain.UpdatePinned:
		return "pin"
	default:
		return "notify"
	}
}

// parseUpdatePolicy maps SetUpdatePolicy's policy argument to its
// domain.UpdatePolicy constant, the inverse of policyToString - an unknown
// string (anything but "notify"/"auto"/"pin") is rejected rather than
// silently defaulting to UpdateNotify, so a caller typo never quietly pins
// nothing.
func parseUpdatePolicy(policy string) (domain.UpdatePolicy, error) {
	switch policy {
	case "notify":
		return domain.UpdateNotify, nil
	case "auto":
		return domain.UpdateAuto, nil
	case "pin":
		return domain.UpdatePinned, nil
	default:
		return 0, fmt.Errorf("unknown policy %q", policy)
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

// resolvedHooks resolves activeProfile's hooks for game, mirroring
// cmd/lmm/hooks.go's getResolvedHooks (minus its --no-hooks short-circuit -
// see hookRunner's doc comment). Takes BOTH the already-resolved profile
// AND the already-snapshotted game as parameters, rather than reading
// p.profile/p.game itself, so every caller reads each exactly once via
// currentProfile()/currentGame() (Task 6 item b / Task 8 review) - and,
// just as importantly, so this method never takes gameMu.RLock nested
// inside hooksMu's critical section: taking a second lock inside the first
// is exactly the nesting the struct's lock-ordering doc comment forbids
// (see gameMu's doc comment - the same reasoning already kept profileMu
// out of here since Task 6).
//
// Task 6 item c: unlike hookRunner, this result genuinely varies per
// profile (a profile's hooks.yaml overrides can differ from another's), so
// it is cached only until SetProfile/SetGame rebinds the session (see
// those methods' doc comments and cachedHooks' own).
func (p *coreProvider) resolvedHooks(game *domain.Game, activeProfile string) *core.ResolvedHooks {
	p.hooksMu.Lock()
	defer p.hooksMu.Unlock()
	if p.cachedHooks != nil {
		return p.cachedHooks
	}

	var profile *domain.Profile
	if activeProfile != "" {
		if pr, err := config.LoadProfile(p.svc.ConfigDir(), game.ID, activeProfile); err == nil {
			profile = pr
		}
	}
	p.cachedHooks = core.ResolveHooks(game, profile)
	return p.cachedHooks
}

// hookContext mirrors cmd/lmm/hooks.go's makeHookContext. Takes the
// already-snapshotted game as a parameter (rather than reading
// currentGame() itself, let alone three times - the Task 8 review's TOCTOU
// finding) so all three fields are guaranteed to describe the SAME game
// even if a concurrent SetGame lands mid-call.
func (p *coreProvider) hookContext(game *domain.Game) core.HookContext {
	return core.HookContext{
		GameID:   game.ID,
		GamePath: game.InstallPath,
		ModPath:  game.ModPath,
	}
}

func (p *coreProvider) EnableMod(ctx context.Context, item ModItem) (ActionOutcome, error) {
	result, err := p.svc.EnableMod(ctx, p.currentGame(), p.currentProfile(), item.Source, item.ID)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("enabling %s: %w", item.Name, err)
	}
	if !result.Changed {
		return ActionOutcome{Message: fmt.Sprintf("%q is already enabled", item.Name)}, nil
	}
	// #197 postsmoke fix: fold in result.Warnings (a merged-pak sync
	// failure lands there now, not result.Notes).
	return ActionOutcome{Message: fmt.Sprintf("Enabled %q", item.Name), Warnings: mergeDiagnostics(result.Warnings, result.Notes)}, nil
}

func (p *coreProvider) DisableMod(ctx context.Context, item ModItem) (ActionOutcome, error) {
	result, err := p.svc.DisableMod(ctx, p.currentGame(), p.currentProfile(), item.Source, item.ID)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("disabling %s: %w", item.Name, err)
	}
	if !result.Changed {
		return ActionOutcome{Message: fmt.Sprintf("%q is already disabled", item.Name)}, nil
	}
	// #197 postsmoke fix: fold in result.Warnings (a merged-pak sync
	// failure lands there now, not result.Notes).
	return ActionOutcome{Message: fmt.Sprintf("Disabled %q", item.Name), Warnings: mergeDiagnostics(result.Warnings, result.Notes)}, nil
}

// UninstallMod runs the same hook configuration cmd/lmm/uninstall.go's
// doUninstall passes to core.UninstallMod (KeepCache=false, Force=false -
// see hookRunner's doc comment for why hooks are never disabled here).
func (p *coreProvider) UninstallMod(ctx context.Context, item ModItem) (ActionOutcome, error) {
	game := p.currentGame()
	profile := p.currentProfile()
	opts := core.UninstallOptions{
		KeepCache:   false,
		Hooks:       p.resolvedHooks(game, profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(game),
		Force:       false,
	}
	result, err := p.svc.UninstallMod(ctx, game, profile, item.Source, item.ID, opts)
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
	game := p.currentGame()
	profile := p.currentProfile()
	opts := core.DeployOptions{
		Hooks:       p.resolvedHooks(game, profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(game),
		Force:       false,
	}
	result, err := p.svc.DeployProfile(ctx, game, profile, opts, nil)
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

// prefixSkipped maps each core.PurgeResult.Skipped entry ("<name>: <reason>"
// - see that field's own doc comment) to "Skipped <name>: <reason>" for
// ActionOutcome.Warnings. Unlike DeployResult.Skipped, which
// coreProvider.DeployProfile appends bare (its message already leads with
// "Deployed N, M failed"), PurgeProfile's outcome message never mentions a
// skip count at all - so each entry needs its own "Skipped " lead-in to read
// as a diagnostic rather than an unlabeled fragment on the status line.
func prefixSkipped(skipped []string) []string {
	if len(skipped) == 0 {
		return nil
	}
	out := make([]string, 0, len(skipped))
	for _, s := range skipped {
		out = append(out, "Skipped "+s)
	}
	return out
}

// purgeProgressLine composes an ActionProgress from one core.DeployProgress
// event during PurgeProfile - only the three phases task-7-brief.md calls
// out for the TUI status line: DeployPurging's header (Total mods about to
// purge), and each mod's own PurgeModPurged/PurgeModSkipped. Every other
// phase PurgeProfile can emit (DeployBeforeAllForced, PurgeWarning,
// PurgeNote, PurgeComplete) is deliberately NOT streamed - those already
// reach the user through the completed outcome's Warnings (see
// coreProvider.PurgeProfile below), and streaming them too would just
// duplicate the same text on the status line a moment earlier.
func purgeProgressLine(p core.DeployProgress) (ActionProgress, bool) {
	switch p.Phase {
	case core.DeployPurging:
		return ActionProgress{Line: fmt.Sprintf("purging %d mod(s)…", p.Total), Percent: -1}, true
	case core.PurgeModPurged:
		return ActionProgress{Line: fmt.Sprintf("✓ %s (%d/%d)", p.ModName, p.Index, p.Total), Percent: -1}, true
	case core.PurgeModSkipped:
		return ActionProgress{Line: "skipped " + p.ModName, Percent: -1}, true
	default:
		return ActionProgress{}, false
	}
}

// PurgeProfile undeploys every mod currently installed in the active
// profile - the TUI's 'X' binding (Task 7, mutations.go's
// purgeProfilePrompt), wired onto core.PurgeProfile (#61's extraction built
// specifically for this consumer). mods is re-fetched here via
// GetInstalledMods rather than reused from whatever list the confirmation
// modal was built from - the same documented plan-drift stance as
// ApplyProfileSwitch's own re-plan-at-apply precedent (see that method's
// doc comment): the set actually purged can differ from what the modal
// showed if something changed between the keypress and the confirm. An
// empty list short-circuits to a plain "no mods installed" outcome with no
// core call, no hooks, and no progress ticks at all - mirroring the CLI's
// own "No mods installed" short-circuit (cmd/lmm/purge.go's doPurge) and
// core.PurgeProfile's own documented empty-mods no-op.
//
// Uninstall is always false (the TUI has no --uninstall equivalent - purged
// mods keep their DB record, matching a plain `lmm purge`) and Force is
// always false (matching every other coreProvider mutation's hook
// defaults - see hookRunner's doc comment), same as DeployProfile/
// UninstallMod above.
//
// On error, coreProvider follows the SAME convention every other mutation
// in this file already does (DeployProfile/UninstallMod/ApplyInstall/
// ApplyUpdate/ApplyProfileSwitch): the wrapped error is returned with an
// empty ActionOutcome{}, not core.PurgeProfile's own partial result -
// buildAction's actionFailedMsg (actions.go) only ever carries the error
// text, never an outcome, so a partial result's Warnings/Skipped would
// never reach the status line regardless of whether this method populated
// them. The only PurgeProfile error path reachable with Force=false is an
// uninstall.before_all hook failure, which core.PurgeProfile documents as
// returning before anything in the result is populated anyway.
func (p *coreProvider) PurgeProfile(ctx context.Context, progress func(ActionProgress)) (ActionOutcome, error) {
	game := p.currentGame()
	profile := p.currentProfile()
	mods, err := p.svc.GetInstalledMods(game.ID, profile)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("loading installed mods for %s/%s: %w", game.ID, profile, err)
	}
	if len(mods) == 0 {
		return ActionOutcome{Message: "no mods installed"}, nil
	}

	opts := core.PurgeOptions{
		Uninstall:   false,
		Hooks:       p.resolvedHooks(game, profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(game),
		Force:       false,
	}

	adapter := deployProgressAdapter(progress, purgeProgressLine)
	result, err := p.svc.PurgeProfile(ctx, game, profile, mods, opts, adapter)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("purging profile %s: %w", profile, err)
	}

	warnings := mergeDiagnostics(append(prefixSkipped(result.Skipped), result.Warnings...), result.Notes)
	return ActionOutcome{
		Message:  fmt.Sprintf("Purged %d mod(s)", result.Purged),
		Warnings: warnings,
	}, nil
}

// ReorderMods persists orderedKeys (Task 4: the FULL desired load order, one
// domain.ModKey("sourceID:modID") per installed mod - see
// ActionProvider.ReorderMods' doc comment) as profile.Mods, via
// ProfileManager.ReorderMods - a local YAML write.
//
// Each entry in orderedKeys becomes a domain.ModReference built one of two
// ways: a mod ALREADY listed in profile.Mods keeps its EXISTING ref's
// Version/FileIDs verbatim (a reorder must never silently reset either -
// those fields track what was actually installed/downloaded, which a pure
// load-order change has no business touching); a mod not yet listed (Task
// 1's "unlisted" case - present in the installed set but absent from the
// profile file) is synthesized from its current DB record instead, so it
// gains a ref carrying today's actual installed Version/FileIDs rather than
// an empty one.
func (p *coreProvider) ReorderMods(_ context.Context, orderedKeys []string) (ActionOutcome, error) {
	game := p.currentGame()
	profileName := p.currentProfile()
	pm := p.svc.NewProfileManager()

	profile, err := pm.Get(game.ID, profileName)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("loading profile %s: %w", profileName, err)
	}
	existing := make(map[string]domain.ModReference, len(profile.Mods))
	for _, ref := range profile.Mods {
		existing[domain.ModKey(ref.SourceID, ref.ModID)] = ref
	}

	installed, err := p.svc.GetInstalledMods(game.ID, profileName)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("loading installed mods for %s/%s: %w", game.ID, profileName, err)
	}
	installedByKey := make(map[string]domain.InstalledMod, len(installed))
	for _, mod := range installed {
		installedByKey[domain.ModKey(mod.SourceID, mod.ID)] = mod
	}

	// Permutation guard (Copilot PR #73 round 6, mirroring the prototype):
	// orderedKeys must name every installed mod exactly once — a missing or
	// duplicated key would silently drop or duplicate profile refs.
	if len(orderedKeys) != len(installed) {
		return ActionOutcome{}, fmt.Errorf("reorder must include every installed mod: got %d of %d", len(orderedKeys), len(installed))
	}
	seen := make(map[string]bool, len(orderedKeys))
	mods := make([]domain.ModReference, 0, len(orderedKeys))
	for _, key := range orderedKeys {
		if seen[key] {
			return ActionOutcome{}, fmt.Errorf("duplicate mod key: %s", key)
		}
		seen[key] = true
		if ref, ok := existing[key]; ok {
			mods = append(mods, ref)
			continue
		}
		im, ok := installedByKey[key]
		if !ok {
			return ActionOutcome{}, fmt.Errorf("mod not found: %s", key)
		}
		mods = append(mods, domain.ModReference{
			SourceID: im.SourceID,
			ModID:    im.ID,
			Version:  im.Version,
			FileIDs:  im.FileIDs,
		})
	}

	if err := p.svc.ReorderProfileMods(game.ID, profileName, mods); err != nil {
		return ActionOutcome{}, fmt.Errorf("reordering profile %s: %w", profileName, err)
	}
	return ActionOutcome{Message: "load order updated"}, nil
}

func (p *coreProvider) PlanProfileSwitch(ctx context.Context, profileName string) (SwitchPlanView, error) {
	plan, err := p.svc.PlanProfileSwitch(ctx, p.currentGame(), profileName)
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
	game := p.currentGame()
	plan, err := p.svc.PlanProfileSwitch(ctx, game, profileName)
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

	result, err := p.svc.ApplyProfileSwitch(ctx, game, plan, onProgress)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("switching to %s: %w", profileName, err)
	}
	// #197 postsmoke fix: fold in result.Warnings (a merged-pak sync
	// failure now lands there, not result.Notes - SwitchResult gained a
	// Warnings field for exactly this).
	return ActionOutcome{
		Message:  fmt.Sprintf("Switched to %q", profileName),
		Warnings: mergeDiagnostics(append(installFailures, result.Warnings...), result.Notes),
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
// SwitchDownloadFailed: the CLI (cmd/lmm/profile.go) prints "    Error: %s"
// for both, so they are user-visible live output there - dropping them here
// via the default case left the TUI with no live sign of a failing install
// while the switch was still running (the completed outcome's Warnings, see
// coreProvider.ApplyProfileSwitch, is the only other place a failure
// surfaces, and used to be silently empty too).
// SourceID/ModID identify the mod for both - see DeployProgress. SourceID's
// own doc comment: it's set for every ApplyProfileSwitch install-loop event
// from SwitchInstallingMod onward, including these, unlike ModName, which
// SwitchInstallError may fire before its mod is even fetched (fetch-failure
// is one of its listed reasons).
//
// #95 removed SwitchFallbackUsed (renderer-cleanup task B2): the emit site
// (ApplyProfileSwitch's install loop) now hard-fails via SwitchInstallError
// instead of falling back, so there is no longer a fallback line to render.
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
	case core.InstallCompiling:
		return ActionProgress{Line: fmt.Sprintf("Installing %s: retaining", modName), Percent: -1}, true
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
// finding this fixes below - or, when no CLI command would fare any better
// because it shares the same failing path, what the caller can already rely
// on locally instead; see GetModDetails' own fallback below for that case).
// Everything else is wrapped with %w under action, a short
// present-participle label (e.g. "planning install of SkyUI").
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
// internal/tui cannot import - see sourceAuthState's doc comment for why
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
// DependencyWarnings (#52 item 10) pass through verbatim - already
// "<sourceID:modID>: <error>", formatted for direct display by
// resolveInstallDependencies.
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
	view.DependencyWarnings = plan.DependencyWarnings
	return view
}

// PlanInstall computes what installing item would do, mapped from
// svc.PlanInstall - the install-modal analog of PlanProfileSwitch.
// showArchived is always false (the TUI has no --show-archived equivalent
// yet - matching the CLI's own non-interactive default when that flag is
// omitted).
func (p *coreProvider) PlanInstall(ctx context.Context, item ModItem) (InstallPlanView, error) {
	plan, err := p.svc.PlanInstall(ctx, p.currentGame(), p.currentProfile(), item.Source, item.ID, false)
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
	game := p.currentGame()
	profile := p.currentProfile()
	plan, err := p.svc.PlanInstall(ctx, game, profile, item.Source, item.ID, false)
	if err != nil {
		return ActionOutcome{}, mapInstallNetworkError(fmt.Sprintf("planning install of %s", item.Name), item.Source, err)
	}

	var conflictWarnings []string
	opts := core.InstallOptions{
		SkipVerify:  false,
		Hooks:       p.resolvedHooks(game, profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(game),
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
	result, err := p.svc.ApplyInstall(ctx, game, plan, opts, adapter)
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
//
// Each UpdateItem.Changelog is run through core.CleanChangelog (Phase 6b
// Task 7) - the FULL cleaned text, with no truncation (see that field's own
// doc comment: the TUI's changelog overlay handles overflow itself, unlike
// the CLI's 800/500-char truncation, which stays CLI-side).
// notCheckedMessage explains an empty update result for a single mod. Zero
// results has two very different causes: the mod was compared against its
// source and is current, or it was filtered out by core.UpdateCheckable and
// never compared at all. Reporting the second as "up to date" claims currency
// that was never established - a pinned mod may well have a newer version.
func notCheckedMessage(name string, mod domain.InstalledMod) string {
	switch {
	case mod.UpdatePolicy == domain.UpdatePinned:
		return fmt.Sprintf("%q is pinned — not checked (P to change)", name)
	case mod.SourceID == domain.SourceLocal:
		return fmt.Sprintf("%q is a local mod — no remote source to check", name)
	default:
		return fmt.Sprintf("%q is already up to date", name)
	}
}

// updateSkipWarning describes mods core.CheckUpdates filtered out, for
// UpdatesView.Warnings. Empty when nothing was skipped, so the common case adds
// no noise. Mirrors cmd/lmm/update.go's printSkipped; the counts come from the
// same core helper, so the two interfaces cannot disagree about what happened.
func updateSkipWarning(skips core.UpdateSkips) string {
	var parts []string
	if skips.Pinned > 0 {
		parts = append(parts, fmt.Sprintf("%d pinned mod%s (P to change)", skips.Pinned, tuiPlural(skips.Pinned)))
	}
	if skips.Local > 0 {
		parts = append(parts, fmt.Sprintf("%d local mod%s (no remote source)", skips.Local, tuiPlural(skips.Local)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Not checked: " + strings.Join(parts, ", ")
}

func tuiPlural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (p *coreProvider) CheckUpdates(ctx context.Context) (UpdatesView, error) {
	game := p.currentGame()
	profile := p.currentProfile()
	installed, err := p.svc.GetInstalledMods(game.ID, profile)
	if err != nil {
		return UpdatesView{}, fmt.Errorf("loading installed mods for %s/%s: %w", game.ID, profile, err)
	}

	updates, checkErr := p.svc.CheckGameUpdates(ctx, game, profile, installed)

	// #143: join the profile YAML's lock state onto the update rows - the
	// same projection (and the same nil-safe "an unreadable profile leaves
	// every mod unlocked" degradation) Overview makes for ModItem, so the
	// batch-apply modal can mark rows ApplyUpdate will refuse.
	lockedRefs := map[string]string{}
	if profileYAML, perr := config.LoadProfile(p.svc.ConfigDir(), game.ID, profile); perr == nil && profileYAML != nil {
		for _, ref := range profileYAML.Mods {
			if ref.Locked {
				lockedRefs[domain.ModKey(ref.SourceID, ref.ModID)] = ref.Version
			}
		}
	}

	var view UpdatesView
	for _, u := range updates {
		lockedVersion, isLocked := lockedRefs[domain.ModKey(u.InstalledMod.SourceID, u.InstalledMod.ID)]
		view.Updates = append(view.Updates, UpdateItem{
			Source: u.InstalledMod.SourceID, ID: u.InstalledMod.ID, Name: u.InstalledMod.Name,
			FromVersion: u.InstalledMod.Version, ToVersion: u.NewVersion,
			Changelog: core.CleanChangelog(u.Changelog),
			Locked:    isLocked, LockedVersion: lockedVersion,
			RecompileNeeded: u.RecompileNeeded,
			RecompileReason: u.RecompileReason,
		})
	}
	if skipped := updateSkipWarning(core.CountUpdateSkips(installed)); skipped != "" {
		view.Warnings = append(view.Warnings, skipped)
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
// CheckGameUpdates for just this one mod first - mirroring
// cmd/lmm/update.go's applySingleUpdate, which does the same before calling
// applyUpdate - rather than reconstructing a bare domain.Update from u's own
// fields: UpdateItem carries no FileIDReplacements (see its doc comment),
// and a real update may need that superseded-file-ID mapping to install
// correctly; only a fresh check call can supply it.
//
// #196/#197: a RecompileNeeded row (NewVersion == the mod's current
// version - a merged-pak staleness signal, not a real update) is routed to
// Service.ApplyMergedPakRegen instead, which has no hooks/options to
// configure. This check MUST happen before GetInstalledMod below: a
// RecompileNeeded row's u.Source/u.ID identify the SYNTHETIC merged-pak
// row (domain.SourceMerged/"merged-pak"), which has no real
// installed_mods DB row - GetInstalledMod would fail loud for it. (Caught
// while wiring this task: the original draft called GetInstalledMod
// unconditionally first, which broke exactly this case.)
func (p *coreProvider) ApplyUpdate(ctx context.Context, u UpdateItem, progress func(ActionProgress)) (ActionOutcome, error) {
	game := p.currentGame()
	profile := p.currentProfile()

	if u.RecompileNeeded {
		adapter := deployProgressAdapter(progress, func(p core.DeployProgress) (ActionProgress, bool) {
			return updateProgressLine(u.Name, p)
		})
		result, err := p.svc.ApplyMergedPakRegen(ctx, game, profile, adapter)
		if err != nil {
			return ActionOutcome{}, mapUpdateNetworkError(fmt.Sprintf("recompiling %s", u.Name), u.Source, err)
		}
		return ActionOutcome{
			Message:  fmt.Sprintf("Recompiled %q (base pak updated)", u.Name),
			Warnings: mergeDiagnostics(result.Warnings, result.Notes),
		}, nil
	}

	mod, err := p.svc.GetInstalledMod(u.Source, u.ID, game.ID, profile)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("getting installed mod %s: %w", u.Name, err)
	}

	updates, err := p.svc.CheckGameUpdates(ctx, game, profile, []domain.InstalledMod{*mod})
	if err != nil {
		return ActionOutcome{}, mapUpdateNetworkError(fmt.Sprintf("checking update for %s", u.Name), u.Source, err)
	}
	if len(updates) == 0 {
		return ActionOutcome{Message: notCheckedMessage(u.Name, *mod)}, nil
	}
	upd := updates[0]

	adapter := deployProgressAdapter(progress, func(p core.DeployProgress) (ActionProgress, bool) {
		return updateProgressLine(u.Name, p)
	})

	opts := core.UpdateOptions{
		Hooks:       p.resolvedHooks(game, profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(game),
		Force:       false,
	}

	result, err := p.svc.ApplyUpdate(ctx, game, profile, upd, opts, adapter)
	if err != nil {
		return ActionOutcome{}, mapUpdateNetworkError(fmt.Sprintf("updating %s", u.Name), u.Source, err)
	}
	return ActionOutcome{
		Message:  fmt.Sprintf("Updated %q to %s", u.Name, upd.NewVersion),
		Warnings: mergeDiagnostics(result.Warnings, result.Notes),
	}, nil
}

// rollbackProgressLine composes an ActionProgress from one core.DeployProgress
// event during ApplyRollback, mirroring updateProgressLine's shape - but
// ApplyRollback (Phase 6b Task 5) never downloads anything (the previous
// version's files already live in the cache - see its own doc comment), so
// it has no percentage-based phase worth surfacing on the status line the
// way updateProgressLine's UpdateDownloading case does. Every phase
// ApplyRollback actually emits (UpdateBeforeEachForced/UpdateWarning/
// UpdateNote, reused verbatim from ApplyUpdate - see RollbackResult's own
// doc comment) already reaches the caller through the completed
// RollbackResult's Warnings/Notes fields (mergeDiagnostics, Rollback below) -
// exactly mirroring how coreProvider.ApplyUpdate's own updateProgressLine
// leaves those same three phases unmapped, for the identical reason. Kept as
// an explicit named function (rather than a literal always-false closure) so
// a future phase addition has an obvious, precedented place to compose a
// line.
func rollbackProgressLine(core.DeployProgress) (ActionProgress, bool) {
	return ActionProgress{}, false
}

// Rollback rolls item back to its PreviousVersion via svc.ApplyRollback
// (Phase 6b Task 5), with the SAME hook configuration
// UninstallMod/DeployProfile/ApplyUpdate already use (Force=false - see
// hookRunner's doc comment) - RollbackOptions mirrors UpdateOptions' own
// hook plumbing exactly (see that type's doc comment in flows.go). The TUI
// itself refuses a mod with no PreviousVersion synchronously, before this is
// ever called (mutations.go's rollbackSelectedMod) - core.ApplyRollback
// repeats the same guard defense-in-depth, so a stale selection still fails
// cleanly here rather than rolling back the wrong thing.
func (p *coreProvider) Rollback(ctx context.Context, item ModItem, progress func(ActionProgress)) (ActionOutcome, error) {
	game := p.currentGame()
	profile := p.currentProfile()
	opts := core.RollbackOptions{
		Hooks:       p.resolvedHooks(game, profile),
		HookRunner:  p.hookRunner(),
		HookContext: p.hookContext(game),
		Force:       false,
	}

	adapter := deployProgressAdapter(progress, rollbackProgressLine)
	result, err := p.svc.ApplyRollback(ctx, game, profile, item.Source, item.ID, opts, adapter)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("rolling back %s: %w", item.Name, err)
	}
	return ActionOutcome{
		Message:  fmt.Sprintf("Rolled back %q to %s", result.ModName, result.ToVersion),
		Warnings: mergeDiagnostics(result.Warnings, result.Notes),
	}, nil
}

// SetUpdatePolicy validates and maps policy (see parseUpdatePolicy) and
// persists it via svc.SetModUpdatePolicy - a local DB write with no network
// call and no hooks, unlike every other mutation in this file.
func (p *coreProvider) SetUpdatePolicy(_ context.Context, item ModItem, policy string) (ActionOutcome, error) {
	mapped, err := parseUpdatePolicy(policy)
	if err != nil {
		return ActionOutcome{}, err
	}
	if err := p.svc.SetModUpdatePolicy(item.Source, item.ID, p.currentGame().ID, p.currentProfile(), mapped); err != nil {
		return ActionOutcome{}, fmt.Errorf("setting update policy for %s: %w", item.Name, err)
	}
	return ActionOutcome{Message: fmt.Sprintf("%s update policy: %s", item.Name, policy)}, nil
}

// SetConvertPaks persists item's #221 pak-to-exmod conversion flag via
// svc.SetModConvertPaks - a local DB write, no network call, no hooks
// (mirroring SetUpdatePolicy immediately above). The merged pak isn't
// regenerated here; the message says so, matching the CLI's `lmm mod
// convert` wording (cmd/lmm/mod.go's doModConvert) - convergence happens on
// the next deploy/merge sync. When the ACTIVE game's own convert_paks is
// false (domain.Game.ConvertPaks), the generic "(deploy to apply)" trailer
// is misleading - no deploy will convert this mod no matter what the
// per-mod flag says until the game flag is flipped back on (Copilot round 1
// on PR #222 fixed the identical wording trap in doModConvert; this mirrors
// that fix for the TUI's own status line).
func (p *coreProvider) SetConvertPaks(_ context.Context, item ModItem, enabled bool) (ActionOutcome, error) {
	game := p.currentGame()
	if err := p.svc.SetModConvertPaks(item.Source, item.ID, game.ID, p.currentProfile(), enabled); err != nil {
		return ActionOutcome{}, fmt.Errorf("setting pak conversion for %s: %w", item.Name, err)
	}
	state := "on"
	if !enabled {
		state = "off"
	}
	trailer := "(deploy to apply)"
	if !game.ConvertPaks {
		trailer = "(this game's convert_paks: false currently disables conversion for the whole game)"
	}
	return ActionOutcome{Message: fmt.Sprintf("%s pak conversion: %s %s", item.Name, state, trailer)}, nil
}

// SetLock locks item at version (""=the ref's current recorded version) via
// ProfileManager.SetModLock - a local profile YAML write, no network call,
// no hooks (mirroring SetUpdatePolicy's own "local write" shape above and
// ActionProvider.SetLock's doc comment: never touches the network or
// deploys - convergence happens on the next profile apply/switch).
func (p *coreProvider) SetLock(_ context.Context, item ModItem, version string) (ActionOutcome, error) {
	if err := p.svc.NewProfileManager().SetModLock(p.currentGame().ID, p.currentProfile(), item.Source, item.ID, version); err != nil {
		return ActionOutcome{}, fmt.Errorf("locking %s: %w", item.Name, err)
	}
	if version != "" {
		return ActionOutcome{Message: fmt.Sprintf("Locked %q at %s", item.Name, version)}, nil
	}
	return ActionOutcome{Message: fmt.Sprintf("Locked %q", item.Name)}, nil
}

// Unlock clears item's lock marker via ProfileManager.ClearModLock; the
// ref's Version record is left untouched (see ClearModLock's own doc
// comment). Local write, no network call, no hooks - mirroring SetLock
// immediately above.
func (p *coreProvider) Unlock(_ context.Context, item ModItem) (ActionOutcome, error) {
	if err := p.svc.NewProfileManager().ClearModLock(p.currentGame().ID, p.currentProfile(), item.Source, item.ID); err != nil {
		return ActionOutcome{}, fmt.Errorf("unlocking %s: %w", item.Name, err)
	}
	return ActionOutcome{Message: fmt.Sprintf("Unlocked %q", item.Name)}, nil
}

// AvailableVersions lists the distinct versions item's source reports - the
// lock picker's data source (#97). Fetches item's current mod record via
// svc.GetMod first (so the source-specific game-ID mapping GetMod performs
// is respected, exactly like ApplyUpdate's own GetMod-then-GetModFiles
// precedent - flows.go's ApplyUpdate) before delegating to
// svc.AvailableModVersions. A source with no version info to report (or any
// other network-path failure) is mapped through mapNetworkError, naming
// pin (P) as the fallback: an un-versioned source can still be held back
// from update checks even though its exact versions can't be listed.
func (p *coreProvider) AvailableVersions(ctx context.Context, item ModItem) ([]string, error) {
	action := fmt.Sprintf("listing versions for %s", item.Name)
	mod, err := p.svc.GetMod(ctx, item.Source, p.currentGame().ID, item.ID)
	if err != nil {
		return nil, mapNetworkError(action, item.Source, "version resolution", "pin it instead (P)", err)
	}
	versions, err := p.svc.AvailableModVersions(ctx, item.Source, mod)
	if err != nil {
		return nil, mapNetworkError(action, item.Source, "version resolution", "pin it instead (P)", err)
	}
	return versions, nil
}

// GetModDetails fetches item's mod via core.Service.ModDetail (Task 2),
// which joins the source-side fetch with whatever local install state the
// active profile has, then overlays that onto modDetailsFromItem's local
// seed - so a field the source doesn't report (or a fetch that fails) still
// leaves the row-derived values in place rather than blanking them. A
// network call for remote sources; mapped through mapNetworkError like
// AvailableVersions above. The fallback does NOT point at 'lmm mod show':
// that command now runs through this exact same core.Service.ModDetail path
// (Task 2's extraction), so on a genuine ErrNotSupported it would fail
// identically - pointing at it would be advice sending the user to an
// equally-doomed command (Copilot review finding on PR #233). Instead the
// fallback tells the user what they still have: the failure lands on
// resolveModDetailsFailed's degrade-in-place path (mutations.go), which
// leaves the seeded local fields - name/version/author/install state -
// visible; only the source-side enrichment (description) is missing.
func (p *coreProvider) GetModDetails(ctx context.Context, item ModItem) (ModDetails, error) {
	action := fmt.Sprintf("fetching details for %s", item.Name)
	game := p.currentGame()
	detail, err := p.svc.ModDetail(ctx, game, p.currentProfile(), item.Source, item.ID)
	if err != nil {
		return ModDetails{}, mapNetworkError(action, item.Source, "mod details",
			"the fields already shown are everything known locally", err)
	}

	out := modDetailsFromItem(item)
	mod := detail.Mod
	out.Name, out.Version, out.Author = mod.Name, mod.Version, mod.Author
	out.Summary, out.Category = mod.Summary, mod.Category
	out.SourceURL, out.PictureURL = mod.SourceURL, mod.PictureURL
	// Same shared cleaner the CLI's mod show and the update flow use, so all
	// three surfaces render a source's markup identically (#86).
	out.Description = core.CleanChangelog(mod.Description)
	if mod.Endorsements != nil {
		out.Endorsements, out.HasEndorsements = *mod.Endorsements, true
	}

	if detail.Installed != nil {
		out.Installed = &InstalledDetails{
			Version:       detail.Installed.Version,
			Profile:       detail.Installed.Profile,
			UpdatePolicy:  policyToString(detail.Installed.UpdatePolicy),
			Locked:        detail.Installed.Locked,
			LockedVersion: detail.Installed.LockedVersion,
			ConvertPaks:   detail.Installed.ConvertPaks,
		}
	} else {
		out.Installed = nil
	}
	return out, nil
}

// CreateProfile creates a new, empty profile via ProfileManager.Create - a
// local YAML write, no network call, no hooks (mirroring SetUpdatePolicy's
// own "local DB/config write" shape above). ProfileManager.Create already
// rejects a colliding name ("profile already exists: <name>") - see
// internal/core/profile.go - so no separate check is needed here.
func (p *coreProvider) CreateProfile(_ context.Context, name string) (ActionOutcome, error) {
	if _, err := p.svc.NewProfileManager().Create(p.currentGame().ID, name); err != nil {
		return ActionOutcome{}, fmt.Errorf("creating profile %s: %w", name, err)
	}
	return ActionOutcome{Message: fmt.Sprintf("Created profile: %s", name)}, nil
}

// DeleteProfile removes profile name via ProfileManager.Delete. Refuses to
// delete the currently active profile - defense-in-depth backing the TUI's
// own active-row guard (mutations.go's deleteSelectedProfile), in case a
// stale selection ever reaches this call with the session's actual current
// profile (see ActionProvider.DeleteProfile's doc comment).
func (p *coreProvider) DeleteProfile(_ context.Context, name string) (ActionOutcome, error) {
	if name == p.currentProfile() {
		return ActionOutcome{}, errors.New(errCannotDeleteActiveProfile)
	}
	if err := p.svc.NewProfileManager().Delete(p.currentGame().ID, name); err != nil {
		return ActionOutcome{}, fmt.Errorf("deleting profile %s: %w", name, err)
	}
	return ActionOutcome{Message: fmt.Sprintf("Deleted profile: %s", name)}, nil
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

// modRefLabel renders a domain.ModReference as "sourceID:modID vVersion" -
// matching the CLI's own profile-import preview list-line format
// (cmd/lmm/profile.go's doProfileImport, "    - %s:%s v%s\n"/"    ↓ %s:%s
// v%s\n") - shared by every ImportPlanView category below.
func modRefLabel(ref domain.ModReference) string {
	return fmt.Sprintf("%s:%s v%s", ref.SourceID, ref.ModID, ref.Version)
}

// importPlanView maps a core.ImportPlan to its TUI render model - the
// import-modal analog of switchPlanView/installPlanView.
func importPlanView(plan *core.ImportPlan) ImportPlanView {
	view := ImportPlanView{
		Name:   plan.Profile.Name,
		GameID: plan.Profile.GameID,
		Exists: plan.Exists,
	}
	for _, ref := range plan.Installed {
		view.Installed = append(view.Installed, modRefLabel(ref))
	}
	for _, ref := range plan.NeedsRedownload {
		view.NeedsDownload = append(view.NeedsDownload, modRefLabel(ref))
	}
	for _, ref := range plan.Missing {
		view.Missing = append(view.Missing, modRefLabel(ref))
	}
	return view
}

// PlanImport parses data and categorizes its mods against the session's
// current game/DB/cache state, mapped from svc.PlanImport - the import-modal
// analog of PlanProfileSwitch/PlanInstall.
func (p *coreProvider) PlanImport(ctx context.Context, data []byte) (ImportPlanView, error) {
	plan, err := p.svc.PlanImport(ctx, p.currentGame(), data)
	if err != nil {
		return ImportPlanView{}, fmt.Errorf("planning import: %w", err)
	}
	return importPlanView(plan), nil
}

// importProgressLine composes an ActionProgress from one core.DeployProgress
// event during ApplyImport, mirroring switchProgressLine/installProgressLine's
// own per-phase-mapping shape (see either's doc comment) - one line per
// Import* DeployPhase that has something worth a status line.
// ImportDownloadDone/ImportNote are deliberately left unmapped (default
// case): the former is just the CLI's blank-line separator after a download
// (nothing to show), and the latter's sole diagnostic already reaches the
// caller through the completed result's Notes field (mergeDiagnostics,
// ApplyImport below) - mirroring updateProgressLine/rollbackProgressLine's
// own "already covered by the result, not worth a live tick too" convention.
func importProgressLine(p core.DeployProgress) (ActionProgress, bool) {
	switch p.Phase {
	case core.ImportSaved:
		return ActionProgress{Line: fmt.Sprintf("Imported profile: %s", p.ModName), Percent: -1}, true
	case core.ImportInstalling:
		return ActionProgress{Line: fmt.Sprintf("Importing: downloading and installing %d mod(s)…", p.Total), Percent: -1}, true
	case core.ImportModInstalling:
		return ActionProgress{Line: fmt.Sprintf("Importing: installing %s:%s (%d/%d)", p.SourceID, p.ModID, p.Index, p.Total), Percent: -1}, true
	case core.ImportDownloading:
		return ActionProgress{Line: fmt.Sprintf("Importing: downloading %s:%s %.0f%%", p.SourceID, p.ModID, p.Percent), Percent: p.Percent}, true
	case core.ImportModFailed:
		return ActionProgress{Line: fmt.Sprintf("Importing: %s:%s failed - %s", p.SourceID, p.ModID, p.Detail), Percent: -1}, true
	case core.ImportModInstalled:
		return ActionProgress{Line: fmt.Sprintf("Importing: %s (%d/%d)", p.ModName, p.Index, p.Total), Percent: -1}, true
	default:
		return ActionProgress{}, false
	}
}

// ApplyImport re-plans data (mirroring ApplyInstall/ApplyProfileSwitch's own
// re-plan-at-apply precedent - see either's doc comment) and applies it with
// Force=true (the TUI's preview modal confirm IS the overwrite consent - see
// ActionProvider.ApplyImport's doc comment), NoInstall=false and
// ConfirmInstall=nil (proceed unconditionally - the same modal confirm
// already covers "yes, download and install these too", matching
// ConfirmConflicts' own "nil = proceed" convention in ApplyInstall above).
//
// ImportedProfile (ActionOutcome) is set to the just-saved profile's name
// ONLY when plan.Profile.GameID matches the session's CURRENTLY ACTIVE game
// (p.currentGame().ID) - Task 9's signal for offering a follow-up "switch to
// it now?" confirmation (mutations.go's resolveImportApplied, dispatched
// from app.go's actionDoneMsg handler). A cross-game import leaves this "":
// the imported profile was saved under ITS OWN declared game (see
// ProfileManager.ImportWithOptions - it saves by profile.GameID, not by
// whatever game this session happens to be bound to), so offering to switch
// the CURRENT session onto it would either silently fail (wrong game's
// profile directory) or need a game rebind first - out of scope for this
// offer, which only ever targets an already-reachable, same-game profile.
func (p *coreProvider) ApplyImport(ctx context.Context, data []byte, progress func(ActionProgress)) (ActionOutcome, error) {
	game := p.currentGame()
	plan, err := p.svc.PlanImport(ctx, game, data)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("planning import: %w", err)
	}

	opts := core.ProfileImportOptions{Force: true, NoInstall: false, ConfirmInstall: nil}
	adapter := deployProgressAdapter(progress, importProgressLine)
	result, err := p.svc.ApplyImport(ctx, game, plan, opts, adapter)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("importing profile: %w", err)
	}

	outcome := ActionOutcome{
		Message:  fmt.Sprintf("Imported profile %q (%d installed, %d failed, %d skipped)", result.ProfileName, result.Installed, result.Failed, result.Skipped),
		Warnings: mergeDiagnostics(result.Warnings, result.Notes),
	}
	if plan.Profile.GameID == game.ID {
		outcome.ImportedProfile = result.ProfileName
	}
	return outcome, nil
}

// ExportProfile writes profile name's exported YAML (ProfileManager.Export -
// the SAME bytes `lmm profile export` prints to stdout, see
// cmd/lmm/profile.go's doProfileExport; CLI/TUI parity's own
// interface-side carve-out lets the CLI keep printing to stdout while the
// TUI writes to a file, both over the identical pm.Export call) to path via
// a fresh os.OpenFile - O_EXCL means a path that already names an existing
// file refuses rather than silently overwriting it, surfacing as exactly
// "file exists: <path>" (task-10-brief.md's own wording, deliberately NOT
// wrapped with this method's usual "exporting %s: %w" convention, so the
// TUI's status line reads a plain, unambiguous refusal). A relative path
// resolves against the process's current working directory - os.OpenFile's
// own behavior, nothing special done here.
func (p *coreProvider) ExportProfile(_ context.Context, name, path string) (ActionOutcome, error) {
	data, err := p.svc.NewProfileManager().Export(p.currentGame().ID, name)
	if err != nil {
		return ActionOutcome{}, fmt.Errorf("exporting profile %s: %w", name, err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return ActionOutcome{}, fmt.Errorf("file exists: %s", path)
		}
		return ActionOutcome{}, fmt.Errorf("exporting profile %s: %w", name, err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return ActionOutcome{}, fmt.Errorf("exporting profile %s: %w", name, err)
	}

	return ActionOutcome{Message: fmt.Sprintf("exported %q to %s", name, path)}, nil
}

// Health runs the verify engine's LOCAL tier (disk/DB only - never the
// network) for the dashboard signal and the Health screen's initial content
// (#224 Task 8). Mirrors Conflicts' own "ride every ordinary loadData
// refresh" contract (DataProvider.Health's doc comment) rather than
// Updates/CheckUpdates' explicit-user-action gate: the Local tier is a
// plain, cheap DB/cache walk, not a network round trip.
func (p *coreProvider) Health(ctx context.Context) (HealthView, error) {
	game := p.currentGame()
	res, err := p.svc.Verify(ctx, game, p.currentProfile(), core.VerifyOptions{Tier: core.VerifyLocal}, nil)
	if err != nil {
		return HealthView{}, fmt.Errorf("checking health for %s/%s: %w", game.ID, p.currentProfile(), err)
	}
	return healthView(res, false), nil
}

// RunHealthCheck runs the verify engine on demand (#224 Task 8): full=true
// selects the Full tier (adds the network version pass), fix=true applies
// CLI --fix semantics. The two are independent knobs here - opts.Tier only
// moves off VerifyLocal when full is true, regardless of fix - matching the
// design's actual CLI parity point exactly: `lmm verify --fix` itself
// always runs the Full tier (cmd/lmm/verify.go never offers a --fix-without-
// network mode), so it is the Health screen's 'F' binding (ActionProvider.
// RunHealthCheck's own doc comment: "always full") that is responsible for
// invoking this with full=true whenever fix=true - not this method
// second-guessing its caller.
//
// Progress mapping (nil-safe, checked once per event like every other
// coreProvider streaming method's onProgress closure - e.g.
// ApplyProfileSwitch's onProgress above): VerifyEvProgress (a version-pass
// tick), VerifyEvFinding (a reported row), and VerifyEvRepairDetail/
// VerifyEvSyncWarning (both already-formatted sub-lines) each compose one
// status-line entry. VerifyEvBegin/VerifyEvVerbose carry nothing worth a
// line and fall through unmapped.
//
// A VerifyEvProgress tick fires at the TOP of every version-pass mod
// iteration, INCLUDING mods the pass then silently skips (local-source/
// manual/no-fileIDs - see versionPass' own doc comment in verify.go) - so
// the LAST tick a caller observes can linger on a skipped mod with no
// finding ever following it. Harmless for this status-line consumer (the
// next real tick, or the run's completion, simply supersedes it), but worth
// knowing if a future caller ever tries to correlate the final tick with a
// specific outcome.
func (p *coreProvider) RunHealthCheck(ctx context.Context, full, fix bool, progress func(ActionProgress)) (HealthView, error) {
	game := p.currentGame()
	profile := p.currentProfile()
	opts := core.VerifyOptions{Tier: core.VerifyLocal, Fix: fix}
	if full {
		opts.Tier = core.VerifyFull
	}

	res, err := p.svc.Verify(ctx, game, profile, opts, func(ev core.VerifyEvent) {
		if progress == nil {
			return
		}
		switch ev.Kind {
		case core.VerifyEvProgress:
			progress(ActionProgress{Line: fmt.Sprintf("checking versions %d/%d: %s", ev.Index, ev.Total, ev.ModName)})
		case core.VerifyEvFinding:
			// Reuses healthFindingSubject's ModName -> ModID -> FileID
			// fallback (app.go, #224 Copilot round 1) rather than ev.Finding.
			// ModName alone - a modless convergence finding (e.g. a dangling
			// cache-rooted symlink's stale_deployment row) carries no ModName
			// at all, which used to render a bare "stale_deployment: " line
			// (#224 Copilot round 2). core.VerifyFinding and HealthFinding
			// share the same ModID/ModName/FileID shape by design (see
			// HealthFinding's own doc comment), so this converts field-for-
			// field rather than duplicating the fallback logic.
			subject := healthFindingSubject(HealthFinding{ModID: ev.Finding.ModID, ModName: ev.Finding.ModName, FileID: ev.Finding.FileID})
			progress(ActionProgress{Line: fmt.Sprintf("%s: %s", ev.Finding.Status, subject)})
		case core.VerifyEvRepairDetail, core.VerifyEvSyncWarning:
			progress(ActionProgress{Line: ev.Detail})
		}
	})
	if err != nil {
		return HealthView{}, fmt.Errorf("checking health for %s/%s: %w", game.ID, profile, err)
	}
	return healthView(res, full), nil
}

// healthView maps a core.VerifyResult to the TUI's HealthView.
//
// 2026-08-07 smoke feedback (user override, #224): this used to drop
// quiet-ok rows (Status "ok" with an empty Note) as "nothing to show" - the
// same convention the CLI's own verify TABLE summary applies - but the
// CLI's row-by-row output (`lmm verify`) prints a `+ <name> - OK` line per
// checked file regardless, and the user rightly expects the Health screen
// to match that, not the summary. Every row the engine reports - the
// quiet-ok rows AND the lock-pending "ok" rows (ok status with a non-empty
// Note, e.g. "lock pending convergence...") - is now kept verbatim. Shared
// by Health (always full=false, the Local tier) and RunHealthCheck (full
// mirrors the caller's own tier choice).
func healthView(res *core.VerifyResult, full bool) HealthView {
	v := HealthView{Issues: res.Issues, Warnings: res.Warnings, Full: full, Checked: res.Checked, Findings: make([]HealthFinding, 0, len(res.Findings))}
	for _, f := range res.Findings {
		v.Findings = append(v.Findings, HealthFinding{ModID: f.ModID, ModName: f.ModName, FileID: f.FileID, Status: f.Status, Note: f.Note, Recorded: f.Recorded, Effective: f.Effective, Version: f.Version})
	}
	return v
}
