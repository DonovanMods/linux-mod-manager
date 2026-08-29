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

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
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

	profile, err := s.NewProfileManager().Get(game.ID, profileName)
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
func (s *Service) ListProfileNames(gameID string) (*ProfileNames, error) {
	names, err := s.NewProfileManager().ListNames(gameID)
	if err != nil {
		return nil, err
	}
	return &ProfileNames{GameID: gameID, Profiles: names}, nil
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
// second call.
type GameSummary struct {
	domain.Game
	LinkMethod domain.LinkMethod `json:"link_method"`
	// Profiles are the game's profile names, in ProfileManager.List order.
	Profiles []string `json:"profiles"`
	// ModCount is how many mods are installed in the game's ACTIVE profile
	// (zero when it has no default profile), matching the "mods in active
	// profile" the status table footnotes.
	ModCount  int  `json:"mod_count"`
	IsDefault bool `json:"is_default"`
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
//   - CachePath is the game's resolved cache root (its own CachePath when
//     set, else the global cache dir); Game.CachePath distinguishes the two.
//   - ActiveProfile is empty when the game has no default profile - an
//     ordinary state, not an error - and the counts below are then zero.
//   - LastDeploy is nil for a profile that has never been deployed.
//   - ConversionFailures counts the active profile's pak-conversion failures
//     (#221 design §5): mods whose prebuilt .pak could not be converted into
//     the merged pak on the last sync and stay raw-deployed instead. Always
//     zero for a non-DeployCompile game.
type GameStatus struct {
	domain.Game
	LinkMethod          domain.LinkMethod `json:"link_method"`
	EffectiveLinkMethod domain.LinkMethod `json:"effective_link_method"`
	LinkMethodSource    string            `json:"link_method_source"`
	CachePath           string            `json:"cache_path"`
	Profiles            []ProfileSummary  `json:"profiles"`
	ActiveProfile       string            `json:"active_profile,omitempty"`
	InstalledModCount   int               `json:"installed_mod_count"`
	EnabledModCount     int               `json:"enabled_mod_count"`
	LastDeploy          *time.Time        `json:"last_deploy,omitempty"`
	ConversionFailures  int               `json:"conversion_failures"`
}

// Status summarizes every configured game, ordered by ID (ListGames').
//
// Deliberately errorless: every per-game lookup behind a summary row - the
// default-game setting, the profile list, the active profile's installed
// mods - is a best-effort read whose failure degrades that ONE row's counts
// rather than the whole report, exactly as the pre-extraction CLI's own
// `profiles, _ := pm.List(...)` reads did. A caller that needs a game's
// state to be authoritative asks GameStatus, which does report errors.
func (s *Service) Status(ctx context.Context) *StatusReport {
	games := s.ListGames()
	defaultGame, _ := s.DefaultGame(ctx)
	pm := s.NewProfileManager()

	report := &StatusReport{Games: make([]GameSummary, 0, len(games))}
	for _, game := range games {
		profiles, _ := pm.List(game.ID)
		names := make([]string, len(profiles))
		for i, p := range profiles {
			names[i] = p.Name
		}

		var modCount int
		if active, err := pm.GetDefault(game.ID); err == nil {
			mods, _ := s.GetInstalledMods(ctx, game.ID, active.Name)
			modCount = len(mods)
		}

		report.Games = append(report.Games, GameSummary{
			Game:       *game,
			LinkMethod: s.getGameLinkMethod(game),
			Profiles:   names,
			ModCount:   modCount,
			IsDefault:  game.ID == defaultGame,
		})
	}
	return report
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
	profiles, err := pm.List(game.ID)
	if err != nil {
		return nil, err
	}

	linkMethod := s.getGameLinkMethod(game)
	status := &GameStatus{
		Game:                *game,
		LinkMethod:          linkMethod,
		EffectiveLinkMethod: linkMethod,
		LinkMethodSource:    "global",
		CachePath:           s.GetGameCachePath(game),
		Profiles:            make([]ProfileSummary, len(profiles)),
	}
	if game.LinkMethodExplicit {
		status.LinkMethodSource = "game"
	}
	for i, p := range profiles {
		status.Profiles[i] = ProfileSummary{Name: p.Name, ModCount: len(p.Mods), IsDefault: p.IsDefault}
	}

	active, err := pm.GetDefault(game.ID)
	if err != nil {
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
}

// SearchOptions narrows a Search.
//
// SourceID empty means "every source configured for the game" (the
// aggregate path, whose per-source failures become Warnings rather than
// errors); naming one restricts the search to it and makes any failure the
// call's own error. Category/Tags are forwarded verbatim - support varies by
// source, and a source that ignores them simply returns unfiltered results.
// PageSize is what each source is asked for; 0 lets every source apply its
// own default. Search always requests page 0 - callers that page use
// searchAllSources/SearchMods directly.
type SearchOptions struct {
	SourceID string
	Category string
	Tags     []string
	PageSize int
}

// SearchReport is everything `lmm search` renders: the hits, the per-source
// failures that did not stop the search, and the two counts a frontend needs
// to say something honest about an empty result.
//
//   - TotalResults is how many hits the sources returned; core never
//     truncates, so it always equals len(Mods) on THIS type. A frontend
//     that caps its own displayed list (e.g. the CLI's --limit) compares
//     its shown count against this field, which stays the untruncated
//     total (final review, Minor #2 / #301).
//   - AttemptedCount is how many of the game's sources actually had a search
//     attempted (capability-less sources are skipped silently). Zero means
//     NONE of them can search at all - indistinguishable from a genuine
//     empty result unless a caller checks it (#58 item 3). It is -1 for a
//     single named source: that path resolves to exactly one attempted
//     source or fails outright, so it has no such case to distinguish.
//   - Warnings stay structured (SourceID + error), never pre-formatted
//     lines: rendering them is the frontend's job.
type SearchReport struct {
	GameID         string          `json:"game_id"`
	Query          string          `json:"query"`
	Mods           []SearchHit     `json:"mods"`
	Warnings       []SourceWarning `json:"warnings"`
	TotalResults   int             `json:"total_results"`
	AttemptedCount int             `json:"attempted_count"`
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
	report := &SearchReport{GameID: game.ID, Query: query, AttemptedCount: -1}

	var found []domain.Mod
	if opts.SourceID == "" {
		agg, err := s.searchAllSources(ctx, game.ID, query, opts.Category, opts.Tags, 0, opts.PageSize)
		if err != nil {
			return nil, err
		}
		found = agg.Mods
		report.Warnings = agg.Warnings
		report.AttemptedCount = agg.AttemptedCount
	} else {
		result, err := s.SearchMods(ctx, opts.SourceID, game.ID, query, opts.Category, opts.Tags, 0, opts.PageSize)
		if err != nil {
			return nil, err
		}
		found = result.Mods
	}

	report.TotalResults = len(found)
	report.Mods = make([]SearchHit, len(found))
	if len(found) == 0 {
		return report, nil
	}

	installed, _ := s.GetInstalledMods(ctx, game.ID, profileName)
	installedKeys := make(map[string]bool, len(installed))
	for _, im := range installed {
		installedKeys[domain.ModKey(im.SourceID, im.ID)] = true
	}
	for i, mod := range found {
		report.Mods[i] = SearchHit{Mod: mod, Installed: installedKeys[domain.ModKey(mod.SourceID, mod.ID)]}
	}
	return report, nil
}

// GameListEntry is one row of `lmm game list`: a configured game plus
// whether it is the default one. An explicit boolean rather than a marker
// baked into the ID, so a consumer never string-matches to find out.
type GameListEntry struct {
	domain.Game
	Default bool `json:"default"`
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
		entries[i] = GameListEntry{Game: *game, Default: game.ID == defaultGame}
	}
	return entries, nil
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
