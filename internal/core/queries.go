package core

// queries.go holds core's read-only query types: the documents a frontend
// renders for `lmm list`, `lmm status`, `lmm search`, `lmm game list` and
// `lmm verify` (v2 Phase 3 Task 3, #301). Each one exists because the join
// behind it spans more than one store - the DB row, the profile YAML, the
// game config, the source registry - and frontends are thin adapters over
// core, so no frontend re-derives that join for itself (the same rule
// ModDetail was built under, #86). Every type carries snake_case json tags
// and a golden in testdata/json: these ARE the wire contract the CLI's
// --json switches to in Unit O, and `lmm serve` renders after that.
//
// Nothing here writes to stdout/stderr or reads stdin.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// ModListing is one row of a ModList: the installed mod itself plus the
// state that lives outside its DB row.
//
//   - Locked/LockedVersion come from the profile YAML ref (#97), not the DB.
//     LockedVersion is the ref's own version - which may differ from the
//     embedded InstalledMod.Version - and is only ever set when Locked is
//     true.
//   - ConvertPaks is nil when pak conversion does not apply to this mod at
//     all (not a merge-compile game, or no pak merge source), distinct from
//     a non-nil pointer to false meaning "applies, and is off" - the same
//     tri-state InstalledDetail.ConvertPaks uses. It deliberately shadows
//     the embedded InstalledMod.ConvertPaks bool: the pointer is the
//     answer a listing needs (it can say "not applicable"), and being the
//     shallower field it also wins the `convert_paks` JSON key, so the
//     wire shape carries the tri-state rather than the raw flag.
type ModListing struct {
	domain.InstalledMod
	Locked        bool   `json:"locked"`
	LockedVersion string `json:"locked_version,omitempty"`
	ConvertPaks   *bool  `json:"convert_paks,omitempty"`
}

// ModList is everything `lmm list` renders: the mods installed in one
// profile, in the profile's load order.
type ModList struct {
	GameID  string       `json:"game_id"`
	Profile string       `json:"profile"`
	Mods    []ModListing `json:"mods"`
}

// ListMods returns the mods installed in profileName, in the profile's load
// order - the order that decides merge precedence (later = merged later =
// wins), not the DB's installed_at order (#201). A mod installed but absent
// from the load order is still listed (never silently dropped), placed
// first: orderByProfile, not the deploy-only
// GetInstalledModsInProfileOrder, which deliberately omits it.
//
// A genuinely missing profile.yaml (domain.ErrProfileNotFound) is tolerated:
// a fresh profile with no YAML on disk yet simply has nothing locked and no
// load order. Any OTHER profile-load failure - including #172's fail-loud
// link_method validation - surfaces instead of silently degrading into a
// listing that shows no locks and treats every mod as absent from the load
// order (#203 release review).
func (s *Service) ListMods(ctx context.Context, game *domain.Game, profileName string) (*ModList, error) {
	mods, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	profile, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
	if err != nil && !errors.Is(err, domain.ErrProfileNotFound) {
		return nil, fmt.Errorf("loading profile: %w", err)
	}

	// One precomputed map rather than a FindRef scan per mod: this loops
	// over every installed mod.
	lockedByKey := map[string]domain.ModReference{}
	if profile != nil {
		for _, ref := range profile.Mods {
			if ref.Locked {
				lockedByKey[domain.ModKey(ref.SourceID, ref.ModID)] = ref
			}
		}
	}

	ordered := orderByProfile(profile, mods)
	list := &ModList{GameID: game.ID, Profile: profileName, Mods: make([]ModListing, len(ordered))}
	for i := range ordered {
		mod := ordered[i]
		row := ModListing{InstalledMod: mod}
		if ref, ok := lockedByKey[domain.ModKey(mod.SourceID, mod.ID)]; ok {
			row.Locked = true
			row.LockedVersion = ref.Version
		}
		if game.DeployMode == domain.DeployCompile && s.ModHasPakMergeSource(game, &mod) {
			v := mod.ConvertPaks
			row.ConvertPaks = &v
		}
		list.Mods[i] = row
	}
	return list, nil
}

// ProfileNames is everything `lmm list --profiles` renders: a game's profile
// names in ProfileManager.List order. A bare []string would carry the names
// with no statement of which game they belong to; the wrapper keeps the
// document self-describing, matching ModList's own game_id/profile stamp.
type ProfileNames struct {
	GameID   string   `json:"game_id"`
	Profiles []string `json:"profiles"`
}

// ListProfileNames returns gameID's profile names, in ProfileManager.List
// order. A profile whose YAML fails to load is still listed by name (the
// name comes from the directory entry, not the file's contents), so a
// malformed profile never disappears from the listing that would let a user
// find it.
func (s *Service) ListProfileNames(ctx context.Context, gameID string) (*ProfileNames, error) {
	names, err := s.NewProfileManager().ListNames(ctx, gameID)
	if err != nil {
		return nil, err
	}
	return &ProfileNames{GameID: gameID, Profiles: names}, nil
}

// ProfileListing is everything `lmm profile list` renders: one game's
// profiles, in ProfileManager.List order, each carrying the same
// ProfileSummary shape GameStatus.Profiles already uses (name, mod count,
// default marker) - a query scoped to profiles alone rather than to one
// game's whole status (#309).
type ProfileListing struct {
	GameID   string           `json:"game_id"`
	Profiles []ProfileSummary `json:"profiles"`
}

// ListProfiles returns gameID's profiles as ProfileSummary rows, in
// ProfileManager.List order - the document `lmm profile list --json`
// emits; the plain NAME/MODS/DEFAULT table is rebuilt from it
// byte-identically (#309).
func (s *Service) ListProfiles(ctx context.Context, gameID string) (*ProfileListing, error) {
	profiles, err := s.NewProfileManager().List(ctx, gameID)
	if err != nil {
		return nil, err
	}
	listing := &ProfileListing{GameID: gameID}
	for _, p := range profiles {
		listing.Profiles = append(listing.Profiles, ProfileSummary{Name: p.Name, ModCount: len(p.Mods), IsDefault: p.IsDefault})
	}
	return listing, nil
}

// ExportProfile returns gameID/profileName's portable domain.ExportedProfile
// value - the document `lmm profile export --json` emits; the plain path
// keeps writing ProfileManager.Export's YAML bytes unchanged. Carries
// exactly what the YAML export carries (config.ExportProfileValue is the
// shared building block, including the installed-mods FileIDs backfill
// (#309)).
func (s *Service) ExportProfile(ctx context.Context, gameID, profileName string) (*domain.ExportedProfile, error) {
	profile, err := s.NewProfileManager().loadForExport(ctx, gameID, profileName)
	if err != nil {
		return nil, err
	}
	return config.ExportProfileValue(profile), nil
}

// DefaultGame is `lmm game show-default --json`'s document (#309): Set is
// false when no default game is configured, in which case ID/Name are both
// empty. Name is empty when the configured ID no longer resolves via
// GetGame (e.g. games.yaml was edited since) - matching the plain path's
// own "Default game: <id>" (no name) fallback for that case.
type DefaultGame struct {
	Set  bool   `json:"set"`
	ID   string `json:"id,omitzero"`
	Name string `json:"name,omitzero"`
}

// DefaultGameInfo resolves the configured default game into the DefaultGame
// report `lmm game show-default --json` emits: built on top of the
// existing (*Service).DefaultGame (left unchanged - it has its own
// internal callers and direct tests) rather than replacing it, with the
// game's Name added when it still resolves (#309).
func (s *Service) DefaultGameInfo(ctx context.Context) (*DefaultGame, error) {
	id, err := s.DefaultGame(ctx)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return &DefaultGame{}, nil
	}
	info := &DefaultGame{Set: true, ID: id}
	if game, err := s.GetGame(id); err == nil {
		info.Name = game.Name
	}
	return info, nil
}

// GameSummary is one row of a StatusReport: a configured game plus the
// counts `lmm status` shows next to it.
//
// domain.Game is embedded (final review, Important #2 / #301): every wire
// type in this file that carries a whole game embeds it flat, matching
// GameListEntry, so a consumer never has to know which endpoint nests one
// under "game" and which doesn't. LinkMethod is the GAME-level resolution
// (game-explicit or global default), not the profile-effective one -
// GameStatus is where the profile-aware answer lives - and, as the shallower
// field, it correctly shadows the embedded Game.LinkMethod for both field
// access and the "link_method" JSON key (the same mechanism ModListing.
// ConvertPaks relies on). Game.LinkMethodExplicit says which of the two
// levels it came from, so a renderer can mark a per-game override without a
// second call. ConvertPaks shadows the embedded Game.ConvertPaks the same
// way, for the same reason (final review, Important #5 / #302): nil when
// the game is not DeployCompile (pak conversion does not apply at all),
// non-nil when it is - never an always-emitted false claiming "off" for a
// game the setting has no effect on.
type GameSummary struct {
	domain.Game
	LinkMethod domain.LinkMethod `json:"link_method"`
	// Profiles are the game's profile names, in ProfileManager.List order.
	Profiles []string `json:"profiles"`
	// ModCount is how many mods are installed in the game's ACTIVE profile
	// (zero when it has no default profile), matching the "mods in active
	// profile" the status table footnotes.
	ModCount    int   `json:"mod_count"`
	IsDefault   bool  `json:"is_default"`
	ConvertPaks *bool `json:"convert_paks,omitzero"`
}

// StatusReport is everything `lmm status` renders with no game named: every
// configured game, ordered by ID (#299 - not games.yaml's map order).
type StatusReport struct {
	Games []GameSummary `json:"games"`
}

// ProfileSummary is one row of a GameStatus's profile list.
type ProfileSummary struct {
	Name string `json:"name"`
	// ModCount counts the profile YAML's own refs (its load order), not the
	// DB's installed rows - the number `lmm status --game X` prints beside
	// each profile.
	ModCount  int  `json:"mod_count"`
	IsDefault bool `json:"is_default"`
}

// GameStatus is everything `lmm status --game <id>` renders for one game.
//
//   - domain.Game is embedded, like GameSummary above (final review,
//     Important #2 / #301) - not nested under "game".
//   - LinkMethod is the GAME-level resolution (game-explicit or global
//     default); EffectiveLinkMethod is what a deploy into the active profile
//     actually uses (profile > game > global, #155/#81), and
//     LinkMethodSource says which level won: "profile", "game" or "global".
//     With no profile override the two methods are equal. As the shallower
//     field, LinkMethod shadows the embedded Game.LinkMethod for both field
//     access and the "link_method" JSON key.
//   - ResolvedCachePath is the game's RESOLVED cache root: its own
//     Game.CachePath when the config sets one, else the global cache dir
//     (final review, Important #1 / #302: named distinctly, rather than
//     shadowing Game.CachePath under the same "cache_path" key, so a
//     consumer can see BOTH the configured per-game override, if any, and
//     what the resolver actually returned - the pre-#302 shadowing wire
//     shape made the override invisible whenever one was set).
//   - ActiveProfile is empty when the game has no default profile - an
//     ordinary state, not an error - and the counts below are then zero.
//   - LastDeploy is nil for a profile that has never been deployed.
//   - ConversionFailures counts the active profile's pak-conversion failures
//     (#221 design §5): mods whose prebuilt .pak could not be converted into
//     the merged pak on the last sync and stay raw-deployed instead. Always
//     zero for a non-DeployCompile game.
//   - ConvertPaks shadows the embedded Game.ConvertPaks (final review,
//     Important #5 / #302): nil when the game is not DeployCompile, matching
//     GameSummary's own field of the same name.
type GameStatus struct {
	domain.Game
	LinkMethod          domain.LinkMethod `json:"link_method"`
	EffectiveLinkMethod domain.LinkMethod `json:"effective_link_method"`
	LinkMethodSource    string            `json:"link_method_source"`
	ResolvedCachePath   string            `json:"resolved_cache_path"`
	Profiles            []ProfileSummary  `json:"profiles"`
	ActiveProfile       string            `json:"active_profile,omitempty"`
	InstalledModCount   int               `json:"installed_mod_count"`
	EnabledModCount     int               `json:"enabled_mod_count"`
	// ExternalCount is how many of InstalledModCount are EXTERNAL mods
	// (#269) - Steam Workshop items lmm tracks but never deploys - so a
	// readout can say "30 installed (30 tracked from Steam)" instead of
	// implying lmm deployed thirty mods it never touched. omitzero: a game
	// with no such mods emits no key at all.
	ExternalCount      int        `json:"external_count,omitzero"`
	LastDeploy         *time.Time `json:"last_deploy,omitempty"`
	ConversionFailures int        `json:"conversion_failures"`
	ConvertPaks        *bool      `json:"convert_paks,omitzero"`
}

// Status summarizes every configured game, ordered by ID (ListGames').
//
// Deliberately tolerant: every per-game lookup behind a summary row - the
// default-game setting, the profile list, the active profile's installed
// mods - is a best-effort read whose failure degrades that ONE row's counts
// rather than the whole report, exactly as the pre-extraction CLI's own
// `profiles, _ := pm.List(...)` reads did. A caller that needs a game's
// state to be authoritative asks GameStatus, which reports every error.
//
// A cancellation is the one read failure NOT degraded that way (v2 Phase 3
// Ruling 16 (C)): it would report "no profiles" for every remaining game,
// a summary indistinguishable from a truthful one, so the report is
// abandoned and ctx.Err() returned instead. That is the only error Status
// returns - the method was errorless before this ruling, and this is the
// error it gained.
func (s *Service) Status(ctx context.Context) (*StatusReport, error) {
	games := s.ListGames()
	defaultGame, _ := s.DefaultGame(ctx)
	pm := s.NewProfileManager()

	report := &StatusReport{Games: make([]GameSummary, 0, len(games))}
	for _, game := range games {
		profiles, err := pm.List(ctx, game.ID)
		if err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
		}
		names := make([]string, len(profiles))
		for i, p := range profiles {
			names[i] = p.Name
		}

		var modCount int
		if active, err := pm.GetDefault(ctx, game.ID); err == nil {
			mods, _ := s.GetInstalledMods(ctx, game.ID, active.Name)
			modCount = len(mods)
		} else if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}

		summary := GameSummary{
			Game:       *game,
			LinkMethod: s.getGameLinkMethod(game),
			Profiles:   names,
			ModCount:   modCount,
			IsDefault:  game.ID == defaultGame,
		}
		if game.DeployMode == domain.DeployCompile {
			v := game.ConvertPaks
			summary.ConvertPaks = &v
		}
		report.Games = append(report.Games, summary)
	}
	return report, nil
}

// GameStatus assembles one game's detail: its profiles, the link method that
// actually applies to its active profile, and that profile's mod counts,
// last deploy and pak-conversion failures.
//
// Unlike Status, this reports errors: it is the answer to a question about
// ONE game, so a failure to list its profiles, resolve its effective link
// method (#172's fail-loud invalid link_method) or read its last deploy is
// the answer being wrong, not one row of many being thin. A game with no
// default profile is not an error - ActiveProfile simply stays empty.
func (s *Service) GameStatus(ctx context.Context, game *domain.Game) (*GameStatus, error) {
	pm := s.NewProfileManager()
	profiles, err := pm.List(ctx, game.ID)
	if err != nil {
		return nil, err
	}

	linkMethod := s.getGameLinkMethod(game)
	status := &GameStatus{
		Game:                *game,
		LinkMethod:          linkMethod,
		EffectiveLinkMethod: linkMethod,
		LinkMethodSource:    "global",
		ResolvedCachePath:   s.GetGameCachePath(game),
		Profiles:            make([]ProfileSummary, len(profiles)),
	}
	if game.LinkMethodExplicit {
		status.LinkMethodSource = "game"
	}
	if game.DeployMode == domain.DeployCompile {
		v := game.ConvertPaks
		status.ConvertPaks = &v
	}
	for i, p := range profiles {
		status.Profiles[i] = ProfileSummary{Name: p.Name, ModCount: len(p.Mods), IsDefault: p.IsDefault}
	}

	active, err := pm.GetDefault(ctx, game.ID)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		// No default profile: an ordinary state (a freshly added game), and
		// the only thing it costs is the per-profile detail below.
		return status, nil
	}

	method, err := s.GetEffectiveLinkMethod(ctx, game, active.Name)
	if err != nil {
		return nil, err
	}
	status.EffectiveLinkMethod = method
	if active.LinkMethodExplicit {
		status.LinkMethodSource = "profile"
	}

	mods, _ := s.GetInstalledMods(ctx, game.ID, active.Name)
	status.ActiveProfile = active.Name
	status.InstalledModCount = len(mods)
	status.ExternalCount = countExternal(mods)
	for _, m := range mods {
		if m.Enabled {
			status.EnabledModCount++
		}
	}

	lastDeploy, err := s.getLastDeployTime(ctx, game.ID, active.Name)
	if err != nil {
		// Wording preserved verbatim from the pre-extraction CLI, whose text
		// and --json paths both surfaced this exact prefix.
		return nil, fmt.Errorf("status: last deploy time: %w", err)
	}
	status.LastDeploy = lastDeploy

	// #221 design §5: read straight from the merged pak's stored
	// fingerprint - the same source verify's own "conversion_failed" rows
	// use.
	if game.DeployMode == domain.DeployCompile {
		if outcomes, ok := s.mergedPakOutcomes(ctx, game, active.Name); ok {
			for _, entry := range outcomes {
				if !entry.Converted {
					status.ConversionFailures++
				}
			}
		}
	}

	return status, nil
}

// SearchHit is one search result plus whether it is already installed in
// the profile the search was scoped to. The check is source-aware: a mod ID
// is only unique within its source.
type SearchHit struct {
	domain.Mod
	Installed bool `json:"installed"`

	// External marks a hit whose source is the workshop-capable one (#269
	// W2). It is a DISPLAY fact, exactly as domain.ModReference's twin is:
	// a Steam Workshop item's Version is the 19-digit content id, which no
	// human-facing surface may print as a version (issue 269's approval
	// note), and a catalog document - unlike an installed row - has no
	// External of its own to branch on. With it, every search renderer runs
	// the hit through the same displayVersion/displayModVersion helper as
	// every other surface, and shows domain.Mod.UpdatedAt instead.
	//
	// Additive and omitzero, so a search over any other source is
	// byte-identical.
	External bool `json:"external,omitzero"`
}

// SearchOptions narrows a Search.
//
// SourceID empty means "every source configured for the game" (the
// aggregate path, whose per-source failures become Warnings rather than
// errors); naming one restricts the search to it and makes any failure the
// call's own error. Category/Tags are forwarded verbatim - support varies by
// source, and a source that ignores them simply returns unfiltered results.
// Page/PageSize are what each source is asked for (source.SearchQuery's own
// fields, forwarded verbatim to searchAllSources/SearchMods); PageSize 0
// lets every source apply its own default, and Page is meaningless without
// it (searchAllSources' own per-source cursor). The CLI's single-page call
// never sets either, matching their historical "always page 0" behavior.
// Limit caps how many hits SearchReport.Mods returns (0 or negative applies
// no cap, matching a non-positive PageSize's "no opinion" convention). It is
// also a TARGET, not just a ceiling (#109): a positive Limit alongside a
// positive PageSize on page 0 makes the aggregate path keep paging each
// source's own cursor until that many merged hits exist, every source is
// exhausted, or searchAllSources' max-pages guard trips - so a source whose
// server-side page cap sits below its share of the limit no longer decides
// how many results `--limit N` returns. Every other shape stays a single
// round; see searchAllSources' doc comment for why.
//
// SearchReport.TotalResults always reports the untruncated count regardless
// (final review, Important #3 / #302: the cap lives here, in core, so a
// caller applying its own --limit and `lmm serve` rendering the same call
// see the identical document, instead of a caller truncating core's result
// after the fact).
type SearchOptions struct {
	SourceID string
	Category string
	Tags     []string
	Page     int
	PageSize int
	Limit    int
}

// SearchReport is everything `lmm search` renders: the hits, the per-source
// failures that did not stop the search, and the two counts a frontend needs
// to say something honest about an empty result.
//
//   - Mods is the slice actually RETURNED: every hit found when
//     SearchOptions.Limit is unset, or the first Limit of them when it's
//     set - it may be a --limit-capped subset, not necessarily every hit
//     the sources found.
//   - TotalResults is the TOTAL NUMBER OF HITS the sources returned, BEFORE
//     any Limit cap - it can exceed len(Mods) when Limit truncated the
//     list, and equals len(Mods) whenever Limit didn't (final review,
//     Important #3 / #302: this doc previously claimed TotalResults always
//     equals len(Mods), which stopped being true the moment a cap was
//     possible on this type).
//   - AttemptedCount is how many of the game's sources actually had a search
//     attempted (capability-less sources are skipped silently). Zero means
//     NONE of them can search at all - indistinguishable from a genuine
//     empty result unless a caller checks it (#58 item 3). It is -1 for a
//     single named source: that path resolves to exactly one attempted
//     source or fails outright, so it has no such case to distinguish.
//   - Warnings stay structured (SourceID + error), never pre-formatted
//     lines: rendering them is the frontend's job.
//   - Page/PageSize echo SearchOptions.Page/PageSize verbatim (#331: the
//     search PAGE's own pagination controls need nothing else to confirm
//     which page a report answers). Both omitzero, so an unset one carries
//     no key at all rather than a zero that looks like a real page 0 of
//     size 0. Measured (#326, epic live review D-2): `lmm search` always
//     sets PageSize - --limit defaults to 10 and searchPageSize feeds it
//     straight through - so its document always carries page_size and never
//     page; `/api/v1/search` called with no page params sets NEITHER, so
//     its document carries neither. The two differ in exactly that way and
//     no other.
//   - HasMore reports whether the sources queried might have a page N+1:
//     AggregateSearchResult.Exhausted negated on the aggregate path,
//     sourceHasMore's own per-source heuristic on a named --source. Always
//     false when PageSize is unset - with no page size there is no paging
//     concept to report on (sourceHasMore's own "pageSize <= 0 has no
//     next-page concept at all", which Exhausted already folds in).
type SearchReport struct {
	GameID         string          `json:"game_id"`
	Query          string          `json:"query"`
	Mods           []SearchHit     `json:"mods"`
	Warnings       []SourceWarning `json:"warnings"`
	TotalResults   int             `json:"total_results"`
	AttemptedCount int             `json:"attempted_count"`
	Page           int             `json:"page,omitzero"`
	PageSize       int             `json:"page_size,omitzero"`
	HasMore        bool            `json:"has_more,omitzero"`
}

// Search runs query against the game's sources (or the one named in opts)
// and marks each hit that is already installed in profileName.
//
// Source failures are returned unwrapped so a caller can classify them
// (source.ErrNotSupported, domain.ErrAuthRequired) and word its own notice;
// in the aggregate path only an all-sources failure is an error at all.
//
// With no hits, the profile is never read: nothing can be marked installed,
// and a search that found nothing must not fail on an unreadable profile.
func (s *Service) Search(ctx context.Context, game *domain.Game, profileName, query string, opts SearchOptions) (*SearchReport, error) {
	report := &SearchReport{
		GameID: game.ID, Query: query, AttemptedCount: -1,
		Page: opts.Page, PageSize: opts.PageSize,
	}

	var found []domain.Mod
	if opts.SourceID == "" {
		agg, err := s.searchAllSources(ctx, game.ID, query, opts.Category, opts.Tags, opts.Page, opts.PageSize, opts.Limit)
		if err != nil {
			return nil, err
		}
		found = agg.Mods
		report.Warnings = agg.Warnings
		report.AttemptedCount = agg.AttemptedCount
		report.HasMore = !agg.Exhausted
	} else {
		result, err := s.SearchMods(ctx, opts.SourceID, game.ID, query, opts.Category, opts.Tags, opts.Page, opts.PageSize)
		if err != nil {
			return nil, err
		}
		found = result.Mods
		report.HasMore = sourceHasMore(result, opts.Page, opts.PageSize)
	}

	report.TotalResults = len(found)
	if len(found) == 0 {
		report.Mods = make([]SearchHit, 0)
		return report, nil
	}

	// The Limit cap applies to what's RETURNED, never to TotalResults above -
	// a non-positive Limit leaves the full list untouched, matching
	// SearchOptions.PageSize's own "no opinion" convention.
	visible := found
	if opts.Limit > 0 && len(visible) > opts.Limit {
		visible = visible[:opts.Limit]
	}

	installed, _ := s.GetInstalledMods(ctx, game.ID, profileName)
	installedKeys := make(map[string]bool, len(installed))
	for _, im := range installed {
		installedKeys[domain.ModKey(im.SourceID, im.ID)] = true
	}
	// One capability lookup per SOURCE, not per hit: an aggregate search
	// returns one page from each of the game's sources, and asking the
	// registry the same question thirty times over would be the same answer
	// thirty times.
	workshopSources := make(map[string]bool, 2)
	report.Mods = make([]SearchHit, len(visible))
	for i, mod := range visible {
		isExternal, known := workshopSources[mod.SourceID]
		if !known {
			isExternal = s.sourceIsWorkshop(mod.SourceID)
			workshopSources[mod.SourceID] = isExternal
		}
		report.Mods[i] = SearchHit{
			Mod:       mod,
			Installed: installedKeys[domain.ModKey(mod.SourceID, mod.ID)],
			External:  isExternal,
		}
	}
	return report, nil
}

// GameListEntry is one row of `lmm game list`: a configured game plus
// whether it is the default one. An explicit boolean rather than a marker
// baked into the ID, so a consumer never string-matches to find out.
// ConvertPaks shadows the embedded Game.ConvertPaks, matching GameSummary/
// GameStatus (final review, Important #5 / #302): nil when the game is not
// DeployCompile, rather than an always-emitted false claiming the setting is
// "off" for a game it has no effect on.
type GameListEntry struct {
	domain.Game
	Default     bool  `json:"default"`
	ConvertPaks *bool `json:"convert_paks,omitzero"`
}

// ListGameEntries returns every configured game, ordered by ID (ListGames'),
// with the default game marked. The error is the default-game lookup's:
// unlike Status, a listing whose "which one is default" answer failed is
// wrong rather than thin, and the setting is a single read for the whole
// list.
func (s *Service) ListGameEntries(ctx context.Context) ([]GameListEntry, error) {
	defaultGame, err := s.DefaultGame(ctx)
	if err != nil {
		return nil, err
	}
	games := s.ListGames()
	entries := make([]GameListEntry, len(games))
	for i, game := range games {
		entries[i] = newGameListEntry(game, defaultGame)
	}
	return entries, nil
}

// newGameListEntry builds one `lmm game list` row for game, marking it
// default when it is defaultGameID and attaching the ConvertPaks pointer
// only for a DeployCompile game (GameListEntry's own doc comment). Shared
// with AddGame (game_add.go), which answers with the identical row shape
// so a frontend can splice an add's response straight into its list.
func newGameListEntry(game *domain.Game, defaultGameID string) GameListEntry {
	entry := GameListEntry{Game: *game, Default: game.ID == defaultGameID}
	if game.DeployMode == domain.DeployCompile {
		v := game.ConvertPaks
		entry.ConvertPaks = &v
	}
	return entry
}

// VerifyReport is a VerifyResult plus the game/profile it describes - the
// one document `lmm verify` (and serve) renders, so no frontend re-stamps
// that identity onto the result itself.
type VerifyReport struct {
	GameID  string        `json:"game_id"`
	Profile string        `json:"profile"`
	Result  *VerifyResult `json:"result"`
}

// VerifyReport runs verify against profileName and wraps its result with the
// game/profile identity. sink receives the same progress events verify emits.
func (s *Service) VerifyReport(ctx context.Context, game *domain.Game, profileName string, opts VerifyOptions, sink EventSink) (*VerifyReport, error) {
	result, err := s.verifyGated(ctx, game, profileName, opts, sink)
	if err != nil {
		return nil, err
	}
	return &VerifyReport{GameID: game.ID, Profile: profileName, Result: result}, nil
}
