package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/internal/tui/prototype"
)

// SearchPageSize is DataProvider.Search's defensive fallback page size, used
// by every implementation whenever a caller passes pageSize <= 0 (a stray
// direct call, or a test that doesn't care about sizing), and the floor of
// m.searchFetchSize()'s derived clamp range (app.go) - a floor-height
// terminal's search still returns this many results rather than shrinking
// further. Originally mirrored the CLI picker's own fixed displayPageSize
// (cmd/lmm/install.go) as the TUI's ONLY page size; #111 Tier 1 replaced
// that fixed use with a per-session size derived from the visible pane (see
// searchFetchSize), leaving this constant with the floor/fallback role only.
const SearchPageSize = 10

// Summary is the dashboard header data.
type Summary struct {
	GameName    string
	ProfileName string
	Installed   int
	Enabled     int
	Updates     int // -1 = unknown (no update check has run)
	Conflicts   int // -1 = unknown
	// LastDeploy is the timestamp of the active profile's most recent deploy
	// (#106a's dashboard "Last deploy" row), or nil when the profile has
	// never been deployed (a truly-unknown value surfaces as an error from
	// the provider, never as nil here). Unlike Updates/Conflicts' "-1 =
	// unknown" int sentinel, nil is the natural "no value" for a *time.Time
	// and needs no separate sentinel constant.
	// coreProvider.Overview populates this from core.Service.
	// GetLastDeployTime, where nil specifically means "this profile has
	// never been deployed" (a normal state, not an error - see that method's
	// doc comment); prototypeProvider.Overview instead sets a canned recent
	// time so --prototype mode has something to show (see
	// prototype.Stats.LastDeploy's doc comment) - the alt game's minimal
	// demo set (Data.AltMods) leaves it nil, same as its Updates/Conflicts
	// sentinels. Rendering is lastDeployLabel's job (app.go): it takes the
	// current time as an explicit parameter (Model.now) rather than calling
	// time.Now() itself, so the label is recomputed fresh on every View()
	// call instead of going stale between loadData refreshes.
	LastDeploy *time.Time
}

// ModItem is one renderable mod row. ID, together with Source, fully
// addresses the mod for core mutations keyed on (sourceID, modID) - see
// ActionProvider.
type ModItem struct {
	ID              string
	Name            string
	Author          string
	Version         string
	Source          string
	Status          string
	Summary         string
	Downloads       int64
	Endorsements    int64
	HasEndorsements bool
	// UpdatePolicy is the mod's current update-check policy - "notify",
	// "auto", or "pin" (see ActionProvider.SetUpdatePolicy's doc comment for
	// what each means) - populated by coreProvider's Overview mapping
	// (stringified from domain.InstalledMod.UpdatePolicy) and
	// prototypeProvider's canned Mod.UpdatePolicy field. Empty for a
	// Search-derived ModItem (search results aren't installed, so they have
	// no policy of their own) - only Overview/the Installed Mods screen ever
	// populates it.
	UpdatePolicy string
	// PreviousVersion is the version this mod would roll back to via '<' on
	// Installed Mods (Task 6, mutations.go's rollbackSelectedMod) - "" means
	// no previous version is available (the mod has never been updated, or
	// has already been rolled back once), which the handler refuses
	// synchronously rather than opening a modal. coreProvider populates this
	// from domain.InstalledMod.PreviousVersion (service_core.go's Overview
	// mapping); prototypeProvider from the canned Mod.PreviousVersion field
	// (see that type's doc comment). Empty for a Search-derived ModItem,
	// mirroring UpdatePolicy's own "only Overview populates it" convention
	// above.
	PreviousVersion string
}

// SourceInfo is one renderable source-registry row, mirroring the columns of
// `lmm source list` (cmd/lmm/source.go) minus its Error column: the Sources
// screen only lists sources that are actually REGISTERED with the service.
// Source-definition load failures (a malformed YAML file, an ID collision)
// never produce a registered source, so they have no row here and remain a
// CLI-only diagnostic (`lmm source list` / `lmm source validate`).
type SourceInfo struct {
	ID           string
	Name         string
	Type         string // "built-in", "directory", "manifest", or "api"
	Auth         string // "yes", "no", or "n/a" (source has no auth capability)
	Capabilities string // compact list, e.g. "search,updates"
}

// ProfileItem is one renderable profile row.
type ProfileItem struct {
	Name     string
	Active   bool
	ModCount int
}

// ConflictItem is one renderable file-conflict row for the Conflicts screen
// (Task 3): Owner/Winner/AlsoIn carry display NAMES only, mirroring
// core.ConflictModRef's own Name field (which already falls back to Key
// when a mod has no installed record supplying a name - see that type's doc
// comment) - callers never need the raw source:mod key here, only what to
// show. Stale mirrors core.ProfileConflict.Stale: true means the DB's
// recorded owner disagrees with the load-order winner (the profile was
// reordered, or deploy order was once nondeterministic, since the last
// deploy) - a redeploy would change who wins.
type ConflictItem struct {
	Path   string
	Owner  string
	Winner string
	AlsoIn []string
	Stale  bool
}

// GameInfo is one renderable configured-game row for the in-TUI game
// switcher (Task 8's 'g' binding - see mutations.go's openGameSwitcher).
// Mirrors ProfileItem's shape: just enough to render a picker option and
// mark which one is currently bound to the session.
type GameInfo struct {
	ID, Name string
	Active   bool
}

// SearchPage is one page of search results for one source/query.
type SearchPage struct {
	Results    []ModItem
	Query      string
	Source     string
	Page       int // 0-based
	PageSize   int
	TotalCount int // 0 if the source doesn't report totals
	// Warnings holds per-source failures in all-sources mode, already
	// formatted for display (e.g. "<sourceID>: <err>"). Empty for
	// single-source searches.
	Warnings []string
	// Exhausted mirrors core.AggregateSearchResult.Exhausted (#58 item 1):
	// only meaningful for all-sources searches (Source == ""), where
	// TotalCount is summed across sources with independent pagination
	// cursors and therefore cannot bound a single global PageSize the way a
	// single source's TotalCount does - see roundExhausted's doc comment
	// (search.go) for how this replaces that unsafe math. Always false (its
	// zero value) for single-source searches, which keep using the
	// pre-existing TotalCount/PageSize math instead.
	Exhausted bool
	// AttemptedCount mirrors core.AggregateSearchResult.AttemptedCount (#58
	// item 3): only meaningful for all-sources searches (Source == "").
	// Zero real sources supporting search renders as a distinct "no source
	// supports search" notice instead of the ordinary zero-results copy -
	// see searchView's zero-results branch.
	AttemptedCount int
}

// DataProvider is the narrow, read-only boundary between the TUI and app
// data. Implementations must be safe to call from a Bubble Tea command
// goroutine.
type DataProvider interface {
	// Overview returns the dashboard summary and installed-mod rows from a
	// single underlying fetch.
	Overview(ctx context.Context) (Summary, []ModItem, error)
	Profiles(ctx context.Context) ([]ProfileItem, error)
	// Sources lists the game's configured real source IDs, sorted. The TUI
	// prepends the all-sources sentinel ("") ahead of these (see
	// newSearchModel); the CLI instead defaults to an aggregate search
	// across all of them when --source is omitted (see doSearch in
	// cmd/lmm/search.go).
	Sources() []string
	// SourceInfos lists every source registered with the service (built-in
	// and user-defined), sorted by ID, for the read-only Sources screen. See
	// SourceInfo's doc comment for how this differs from Sources.
	SourceInfos() []SourceInfo
	// Search queries one source, or every one of the game's configured
	// sources when source is "" (the documented all-sources sentinel).
	// pageSize is the number of results to fetch for this page (#111 Tier
	// 1's window-sized fetch - see Model.searchFetchSize in app.go
	// for how the TUI derives it); implementations fall back to
	// SearchPageSize when pageSize <= 0, a defensive default for callers
	// that don't derive a real one (see SearchPageSize's own doc comment).
	Search(ctx context.Context, source, query string, page, pageSize int) (SearchPage, error)
	// DeployedFiles lists the relative paths a specific installed mod has
	// deployed into the game directory, sorted, for the read-only files
	// overlay (Task 4). An empty slice with a nil error means the mod is
	// known but has nothing currently deployed (e.g. disabled).
	DeployedFiles(sourceID, modID string) ([]string, error)
	// ListGames lists every game configured for this session's underlying
	// app data, sorted by Name, for the in-TUI game switcher (Task 8's 'g'
	// binding - see mutations.go's openGameSwitcher). Exactly one entry has
	// Active set: the game this session is currently bound to.
	ListGames() ([]GameInfo, error)
	// Conflicts lists every file conflict the active profile currently has
	// (Task 3), sorted by Path - mirroring core.GetProfileConflicts' own
	// contract, which every implementation of this method is expected to
	// delegate to (directly, for coreProvider, or via canned data for
	// prototypeProvider). Fetched alongside Overview/Profiles in the same
	// loadData refresh cycle (app.go), not gated behind an explicit user
	// action the way Updates/CheckUpdates is: detection is local-only (DB
	// reads plus a directory walk of each enabled mod's cache - no network),
	// so it rides every ordinary load. The walks scale with installed-mod
	// count and cache size; if refreshes ever feel slow on very large mod
	// sets, memoizing per (mod, version) manifest is the obvious lever.
	Conflicts(ctx context.Context) ([]ConflictItem, error)
}

// prototypeProvider serves the static demo data set. It must never touch
// disk, network, DB, or APIs.
type prototypeProvider struct {
	data prototype.Data
	// altActive is Task 8's game-switch flag: false (the zero value) means
	// the session is bound to data.Game/data.InstalledMods, true means it's
	// bound to data.AltGame/data.AltMods - see currentGameID/Overview/
	// SetGame below and ListGames/SetGame in actions_provider.go.
	altActive bool
}

// NewPrototypeProvider returns the side-effect-free demo DataProvider used
// by --prototype mode and tests. The returned value also implements
// ActionProvider (see actions_provider.go's prototypeProvider methods): a
// caller that needs both roles for one demo session should type-assert the
// single returned value (`provider.(ActionProvider)`) rather than calling
// this constructor twice, since each call seeds an independent in-memory
// dataset - two calls would silently diverge instead of sharing state.
func NewPrototypeProvider() DataProvider {
	return newPrototypeProviderConcrete()
}

// newPrototypeProviderConcrete is NewPrototypeProvider's body, returning the
// concrete type instead of the DataProvider interface - needed by
// NewPrototypeModel (app.go) to read activeGame().Name for Options.GameName
// (#58 item 5) without a defensive type-assertion back out of the interface
// NewPrototypeProvider deliberately returns.
func newPrototypeProviderConcrete() *prototypeProvider {
	return &prototypeProvider{data: prototype.Load()}
}

// activeGame returns the canned Game this session is currently bound to
// (see altActive's doc comment).
func (p *prototypeProvider) activeGame() prototype.Game {
	if p.altActive {
		return p.data.AltGame
	}
	return p.data.Game
}

// activeMods returns the canned installed-mods slice this session is
// currently bound to (see altActive's doc comment). Every prototypeProvider
// method that reads OR mutates installed mods must go through this (or
// setActiveMods below) instead of touching p.data.InstalledMods directly -
// Copilot PR #69 caught SetUpdatePolicy/PurgeProfile (and an audit found
// every other mutation) addressing the primary slice regardless of which
// game was active, silently mutating the WRONG game's dataset after a
// --prototype game switch. In-place element writes through the returned
// slice value share the backing array and are visible in later reads;
// append/remove must write the new header back via setActiveMods.
func (p *prototypeProvider) activeMods() []prototype.Mod {
	if p.altActive {
		return p.data.AltMods
	}
	return p.data.InstalledMods
}

// setActiveMods replaces the ACTIVE game's installed-mods slice - the
// write-back half of activeMods (see its doc comment), needed by the
// operations that grow or shrink the list (UninstallMod, ApplyInstall,
// materializeNeedsDownloads) rather than mutating elements in place.
func (p *prototypeProvider) setActiveMods(mods []prototype.Mod) {
	if p.altActive {
		p.data.AltMods = mods
		return
	}
	p.data.InstalledMods = mods
}

// adjustStats applies installed/enabled deltas to the PRIMARY game's canned
// Stats counters - a deliberate no-op while the alt game is active: the alt
// game has no canned Stats at all (its Overview derives Installed/Enabled
// live from AltMods - see Overview's doc comment), so mutating the shared
// Stats there would silently corrupt the PRIMARY game's counters (the
// Copilot PR #69 PurgeProfile finding, generalized to every mutation).
func (p *prototypeProvider) adjustStats(installedDelta, enabledDelta int) {
	if p.altActive {
		return
	}
	p.data.Stats.Installed += installedDelta
	p.data.Stats.Enabled += enabledDelta
}

func (p *prototypeProvider) Overview(_ context.Context) (Summary, []ModItem, error) {
	mods := p.activeMods()
	if !p.altActive {
		// The primary game keeps its own canned Stats (installed/enabled
		// counts don't necessarily match InstalledMods' own length/status
		// mix - see prototype.Stats' doc comment) - Updates/Conflicts are
		// deliberately fictional there. AltMods has no such canned Stats
		// (it's a minimal 1-2 mod demo set, see Data.AltMods' doc comment),
		// so the alt branch below derives Installed/Enabled directly from
		// it and leaves Updates/Conflicts at the "unknown" sentinel, same
		// as coreProvider.Overview's real convention.
		lastDeploy := p.data.Stats.LastDeploy
		return Summary{
			GameName:    p.activeGame().Name,
			ProfileName: p.data.Profile.Name,
			Installed:   p.data.Stats.Installed,
			Enabled:     p.data.Stats.Enabled,
			Updates:     p.data.Stats.Updates,
			Conflicts:   p.data.Stats.Conflicts,
			LastDeploy:  &lastDeploy,
		}, modItems(mods), nil
	}

	enabled := 0
	for _, mod := range mods {
		if mod.Status != "disabled" {
			enabled++
		}
	}
	return Summary{
		GameName:    p.activeGame().Name,
		ProfileName: p.data.Profile.Name,
		Installed:   len(mods),
		Enabled:     enabled,
		Updates:     -1,
		Conflicts:   -1,
	}, modItems(mods), nil
}

// ListGames returns the two canned games (see Data.AltGame's doc comment),
// sorted by Name like coreProvider.ListGames - deterministic even though a
// 2-entry list has nothing to actually jitter.
func (p *prototypeProvider) ListGames() ([]GameInfo, error) {
	games := []GameInfo{
		{ID: p.data.Game.ID, Name: p.data.Game.Name, Active: !p.altActive},
		{ID: p.data.AltGame.ID, Name: p.data.AltGame.Name, Active: p.altActive},
	}
	sort.Slice(games, func(i, j int) bool { return games[i].Name < games[j].Name })
	return games, nil
}

// SetGame implements actions.go's optional gameRebinder hook: id must name
// one of the two canned games (Data.Game/Data.AltGame), flipping altActive
// to match. Unlike coreProvider.SetGame (service_core.go), which always
// WRITES p.game/p.profile even on a same-id rebind (cheap, no correctness
// concern there), this is written as a plain "which one" switch rather than
// a toggle specifically so a REPEAT call with the SAME id is a no-op: Model.
// rebindGame (actions.go) calls SetGame on both m.provider and m.actions,
// and --prototype mode wires both from the SAME prototypeProvider instance
// (NewPrototypeModel) - a toggle-based flip would double-apply and switch
// back to the ORIGINAL game on the second call. Mirrors why prototypeProvider
// deliberately does not implement SetProfile at all (profileRebinder's own
// doc comment) - this hook can't opt out the same way (Task 8 wants the
// switcher to visibly work in --prototype demo mode), so it's made
// idempotent instead.
func (p *prototypeProvider) SetGame(id string) error {
	switch id {
	case p.data.Game.ID:
		p.altActive = false
	case p.data.AltGame.ID:
		p.altActive = true
	default:
		return fmt.Errorf("game not found: %s", id)
	}
	return nil
}

func (p *prototypeProvider) Sources() []string {
	return []string{"nexusmods"}
}

func (p *prototypeProvider) SourceInfos() []SourceInfo {
	return []SourceInfo{
		{ID: "curseforge", Name: "CurseForge", Type: "built-in", Auth: "n/a", Capabilities: "search,updates"},
		{ID: "local-mods", Name: "Local Mods", Type: "directory", Auth: "n/a", Capabilities: "search,updates"},
		{ID: "nexusmods", Name: "Nexus Mods", Type: "built-in", Auth: "yes", Capabilities: "search,deps,updates,auth"},
	}
}

// prototypeAllSourcesWarning is a canned per-source failure (#58 item 4),
// surfaced ONLY in all-sources mode (source == "") so --prototype demo mode
// actually exercises searchWarningLine's rendering path - before this, no
// prototype search ever populated Warnings at all, leaving that line of the
// UI completely untested by the one path (--prototype) meant to demo every
// search state.
const prototypeAllSourcesWarning = "curseforge: connection refused"

func (p *prototypeProvider) Search(_ context.Context, source, query string, page, pageSize int) (SearchPage, error) {
	if pageSize <= 0 {
		// Defensive fallback (see SearchPageSize's doc comment) - every real
		// call path (startSearch) always derives a positive size, so this
		// only fires for a stray direct call or a test that doesn't care.
		pageSize = SearchPageSize
	}

	all := modItems(p.data.SearchResults)
	matched := make([]ModItem, 0, len(all))
	for _, item := range all {
		if strings.Contains(strings.ToLower(item.Name), strings.ToLower(query)) {
			matched = append(matched, item)
		}
	}

	// Paginate the canned set by the given size (#111 Tier 1) - previously
	// this always returned every match on a single "page 0" regardless of
	// what PageSize claimed, which happened to look right only because
	// SearchPageSize (10) exceeded len(SearchResults) (5). start/end are
	// clamped to len(matched) so an out-of-range page (or a pageSize larger
	// than what's left) never slices past the end; page is clamped to >= 0
	// for the same defensive reason pageSize is normalized above - no
	// caller passes a negative page today, but a negative start would panic
	// the slice below rather than degrade.
	page = max(page, 0)
	start := min(page*pageSize, len(matched))
	end := min(start+pageSize, len(matched))

	result := SearchPage{
		Results:    matched[start:end],
		Query:      query,
		Source:     source,
		Page:       page,
		PageSize:   pageSize,
		TotalCount: len(matched),
	}
	if source == "" {
		result.Warnings = []string{prototypeAllSourcesWarning}
		// Exhausted/AttemptedCount (#58 fix-round: whole-branch-review
		// finding) must mirror what a genuinely-exhausted, genuinely-
		// attempted real aggregate looks like, or --prototype's demo
		// contradicts the very honest-search behavior this batch added:
		//   - Exhausted is true exactly when this page reached the end of
		//     the canned set (end == len(matched)) - #111 Tier 1 made this
		//     a real computation instead of an unconditional true, now that
		//     the canned set can genuinely span more than one round when
		//     pageSize is small; roundExhausted's aggregate branch (search.go,
		//     `!Exhausted`) - and, downstream of it, searchFooterLine's "all N
		//     shown" wording (app.go) - depend on this being honest, not just
		//     true-by-convention.
		//   - AttemptedCount is the canned SEARCHABLE source count, derived
		//     from SourceInfos() (not hardcoded) - all three of its entries
		//     advertise "search" in their Capabilities string, matching
		//     prototypeAllSourcesWarning's premise that one of them
		//     (curseforge) was attempted and failed. Leaving this at its
		//     zero value would make EVERY zero-match demo search
		//     indistinguishable from "no source supports searching" (#58
		//     item 3's exact dishonesty), even though the canned sources
		//     plainly do support it.
		result.Exhausted = end == len(matched)
		result.AttemptedCount = len(p.SourceInfos())
	}
	return result, nil
}

// DeployedFiles returns 2-3 plausible canned rows derived from item's name
// (falling back to the raw modID when it isn't one of the ACTIVE game's
// installed mods or the canned SearchResults entries - see
// findInstalledIndex/findSearchResult in actions_provider.go):
// deterministic, no randomness, and never errors, matching this type's
// "never touch disk/network" contract. Sorted, matching the interface's
// documented contract (coreProvider gets this for free from its query's
// ORDER BY - see that type's own DeployedFiles).
func (p *prototypeProvider) DeployedFiles(sourceID, modID string) ([]string, error) {
	name := modID
	if idx := p.findInstalledIndex(sourceID, modID); idx >= 0 {
		name = p.activeMods()[idx].Name
	} else if idx := p.findSearchResult(sourceID, modID); idx >= 0 {
		name = p.data.SearchResults[idx].Name
	}
	files := []string{
		fmt.Sprintf("%s.esp", name),
		fmt.Sprintf("Data/%s.bsa", name),
		fmt.Sprintf("textures/%s/main.dds", modID),
	}
	sort.Strings(files)
	return files, nil
}

// Conflicts returns the PRIMARY game's canned conflict set (see
// prototype.Data.Conflicts' doc comment); the alt game (see altActive's own
// doc comment) has no canned conflicts at all - its minimal 1-2 mod demo set
// has no interesting conflict scenario to show, mirroring AltMods leaving
// Dependencies/Conflicts/etc. unset elsewhere.
func (p *prototypeProvider) Conflicts(_ context.Context) ([]ConflictItem, error) {
	if p.altActive {
		return nil, nil
	}
	items := make([]ConflictItem, 0, len(p.data.Conflicts))
	for _, c := range p.data.Conflicts {
		items = append(items, ConflictItem{
			Path:   c.Path,
			Owner:  c.Owner,
			Winner: c.Winner,
			AlsoIn: append([]string(nil), c.AlsoIn...),
			Stale:  c.Stale,
		})
	}
	return items, nil
}

func (p *prototypeProvider) Profiles(_ context.Context) ([]ProfileItem, error) {
	items := make([]ProfileItem, 0, len(p.data.Profiles))
	for _, profile := range p.data.Profiles {
		items = append(items, ProfileItem{
			Name:     profile.Name,
			Active:   profile.Active,
			ModCount: profile.ModCount,
		})
	}
	return items, nil
}

func modItems(mods []prototype.Mod) []ModItem {
	items := make([]ModItem, 0, len(mods))
	for _, mod := range mods {
		items = append(items, ModItem{
			ID:              mod.ID,
			Name:            mod.Name,
			Author:          mod.Author,
			Version:         mod.Version,
			Source:          mod.Source,
			Status:          mod.Status,
			Summary:         mod.Summary,
			Downloads:       mod.Downloads,
			Endorsements:    mod.Endorsements,
			HasEndorsements: mod.HasEndorsements,
			UpdatePolicy:    mod.UpdatePolicy,
			PreviousVersion: mod.PreviousVersion,
		})
	}
	return items
}
