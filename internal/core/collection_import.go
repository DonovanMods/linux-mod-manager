// collection_import.go is the version-1 reader (#621; design §12, spec
// docs/collection-format.md): PlanCollectionImport / ApplyCollectionImport,
// `lmm profile import <file>` for a `.lmmc` container or a version-1 YAML
// document. A version-0 document keeps PlanImport / ApplyImport, unchanged.
//
// A collection import plans as a profile import, like the Workshop one
// (workshop_collection.go): the document is checked, sanitised and matched
// to a local game, turned into the profile it describes, and handed to
// PlanImport. So the staleness snapshot, the buckets and the two-phase
// apply (acquire_engine.go, #602) are the import flow lmm already has.
//
// What the format adds on top:
//
//   - the feature gate: a document requiring a feature outside this lmm's
//     supported set plans (so the user sees what is missing) and its Apply
//     refuses with *CollectionUnsupportedError, writing nothing;
//   - the game: matched by Steam app id, then by source game ids, unless
//     the caller names one;
//   - hooks: a document's hooks are code, so the profile is saved WITHOUT
//     them unless the caller asks (opts.WithHooks) for a document read from
//     a local file. A document fetched from a URL (L2, #622) never has its
//     hooks applied: the plan built for one keeps no hooked copy at all, so
//     there is nothing for an option to switch on;
//   - external items and the collection identity, recorded on the profile.
//
// Failures stay per item, as an ordinary import's do (owner ruling,
// 2026-11-12): a v1 document is also every profile backup now, and one mod
// gone from its source must not make the rest unimportable. Answerable
// refusals and the feature gate block before anything is written;
// ProfileImportOptions.AllOrNothing is left for the converted Nexus
// collections of C2 (#607).
package core

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/fomod"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// CollectionInput says where a collection document came from, which
// decides what of it lmm will apply.
type CollectionInput string

const (
	// CollectionFromFile is a document the user picked on this machine: a
	// file given to the CLI, or uploaded through the web UI. Its hooks are
	// applied on request (ProfileImportOptions.WithHooks).
	CollectionFromFile CollectionInput = "file"
	// CollectionFromURL is a document fetched from the network (L2, #622).
	// Its hooks are never applied.
	CollectionFromURL CollectionInput = "url"
	// CollectionFromSource is a document converted from a source's own
	// collection (#607: a NexusMods collection). It carries no hooks.
	CollectionFromSource CollectionInput = "source"
)

// CollectionImportRequest is what PlanCollectionImport plans.
type CollectionImportRequest struct {
	// Data is the document: a `.lmmc` container or a version-1 YAML
	// document.
	Data []byte
	// Input is where Data came from; empty means CollectionFromFile.
	Input CollectionInput
	// GameID names the game to import into (`--game`). Empty matches the
	// document's portable game identity against the configured games.
	GameID string
	// ProfileName names the profile to create (`--as`). Empty uses the
	// document's own name.
	ProfileName string
}

// CollectionImport is the collection half of a version-1 import plan
// (ImportPlan.Collection): what the document is, how it was matched to this
// machine, and everything in it the buckets do not show - the gate's
// verdict, the hooks, the overrides, the external items, the items whose
// source this lmm lacks.
type CollectionImport struct {
	// FormatVersion is the document's `lmm_collection:` version, and
	// Container whether it came in a `.lmmc` container.
	FormatVersion int  `json:"format_version"`
	Container     bool `json:"container,omitzero"`
	// Input is where the document came from: "file" or "url".
	Input CollectionInput `json:"input"`
	// ID and Version are the collection's identity, as the author wrote
	// them.
	ID      string `json:"id,omitempty"`
	Version int    `json:"version,omitzero"`
	// Name is the collection's display name (its title, or the profile
	// name), and ProfileName the profile the import creates.
	Name        string `json:"name"`
	ProfileName string `json:"profile_name"`
	// Author, Description and InstallInstructions are the author's.
	Author              string `json:"author,omitempty"`
	Description         string `json:"description,omitempty"`
	InstallInstructions string `json:"install_instructions,omitempty"`
	// Origin is where the collection came from, when someone else curated
	// it.
	Origin *domain.CollectionOrigin `json:"origin,omitempty"`
	// Game is the document's portable game identity; GameID the local game
	// it imports into, and MatchedBy how that game was chosen: "game" (the
	// caller named it), "steam_app_id", or "source:<id>".
	Game      domain.CollectionGame `json:"game"`
	GameID    string                `json:"game_id"`
	MatchedBy string                `json:"matched_by"`
	// Requires is the union of what the document declares and what its
	// content needs; Unsupported the part of it this lmm cannot import.
	// A non-empty Unsupported means Apply refuses.
	Requires    []string                       `json:"requires,omitempty"`
	Unsupported []UnsupportedCollectionFeature `json:"unsupported,omitempty"`
	// Items is every mod, in the document's order, with its display name;
	// an item whose source this lmm cannot reach for the game carries
	// Unavailable, the reason and its remedy.
	Items []CollectionImportItem `json:"items"`
	// External lists the items lmm cannot fetch: recorded on the profile,
	// for the user to get by hand.
	External []domain.CollectionExternalItem `json:"external,omitempty"`
	// Hooks lists every hook the document carries. HooksImportable is
	// whether they can be applied at all - from a local file, only with
	// --with-hooks; from a URL, never.
	Hooks           []ImportHook `json:"hooks,omitempty"`
	HooksImportable bool         `json:"hooks_importable"`
	// Overrides lists the config files the profile writes into the game
	// directory (contained to it at deploy, as every override is).
	Overrides []string `json:"overrides,omitempty"`
	// Warnings are what the reader skipped or could not check.
	Warnings []string `json:"warnings,omitempty"`
	// Plugins summarises the plugin list the import saves on the profile
	// (#611): absent when the document carries none.
	Plugins *CollectionPluginSummary `json:"plugins,omitempty"`
	// LoadOrders are the file load orders the collection carries (#620):
	// each order's files, as the profile records them, and whether lmm
	// will write it for this game.
	LoadOrders []CollectionLoadOrderSummary `json:"load_orders,omitempty"`
	// Source is the source collection this document was converted from
	// (#607): every item with what the import does with it, the features
	// the collection raised, the curator's tools and the conversion's
	// notes. Absent for a document read from a file or a URL.
	Source *ConvertedCollection `json:"source,omitempty"`
}

// CollectionPluginSummary is what a collection import does with the
// plugin list it carries (#611): the counts a plan shows, and whether lmm
// will apply it. The list itself is the plan's profile's (ImportPlan.
// Profile.Plugins).
type CollectionPluginSummary struct {
	// Plugins counts the load order's plugins, and Active the ones it
	// enables; Inactive names the others, in load order.
	Plugins  int      `json:"plugins"`
	Active   int      `json:"active"`
	Inactive []string `json:"inactive,omitempty"`
	// Rules counts the per-plugin rules, and Groups names the rule groups,
	// in the document's order.
	Rules  int      `json:"rules"`
	Groups []string `json:"groups,omitempty"`
	// Managed is whether lmm writes the game's plugin list (its adapter
	// manages one), so the order applies at every deploy. When false the
	// list is only recorded on the profile.
	Managed bool `json:"managed"`
}

// CollectionLoadOrderSummary is one file load order a collection import
// carries (#620): its files, first loaded first, and whether lmm writes it.
type CollectionLoadOrderSummary struct {
	// ID is the order's id; Label its display name, when the game's
	// adapter has the order.
	ID    string `json:"id"`
	Label string `json:"label,omitempty"`
	// Managed is whether the game's adapter writes this order, so it
	// applies at every deploy; FirstWins whether a file earlier in it wins
	// a conflict with a later one.
	Managed   bool `json:"managed"`
	FirstWins bool `json:"first_wins,omitzero"`
	// Files are the order's files, first loaded first.
	Files []CollectionLoadOrderFile `json:"files"`
}

// CollectionLoadOrderFile is one file of a collection's load order.
type CollectionLoadOrderFile struct {
	Name string `json:"name"`
	// Mod is the collection mod the source ties the file to, when it does
	// (a converted Nexus collection's hint): display only.
	Mod string `json:"mod,omitempty"`
}

// summariseLoadOrders is orders as the plan shows them, in id order; nil
// when the collection carries none.
func (s *Service) summariseLoadOrders(game *domain.Game, orders domain.FileLoadOrders) []CollectionLoadOrderSummary {
	var out []CollectionLoadOrderSummary
	for _, id := range orders.IDs() {
		sum := CollectionLoadOrderSummary{ID: id, Files: []CollectionLoadOrderFile{}}
		if spec, ok := s.loadOrderSpec(game, id); ok {
			sum.Label, sum.Managed, sum.FirstWins = spec.Label, true, spec.FirstWins
		}
		for _, name := range orders[id] {
			sum.Files = append(sum.Files, CollectionLoadOrderFile{Name: name})
		}
		out = append(out, sum)
	}
	return out
}

// summarisePlugins is p's CollectionPluginSummary, nil when p records
// nothing.
func summarisePlugins(p domain.ProfilePlugins, managed bool) *CollectionPluginSummary {
	if p.IsZero() {
		return nil
	}
	s := &CollectionPluginSummary{Plugins: len(p.Order), Rules: len(p.Rules.Plugins), Managed: managed}
	for _, e := range p.Order {
		if e.Enabled {
			s.Active++
		} else {
			s.Inactive = append(s.Inactive, e.Name)
		}
	}
	for _, g := range p.Rules.Groups {
		if !slices.Contains(s.Groups, g.Name) {
			s.Groups = append(s.Groups, g.Name)
		}
	}
	return s
}

// maxCycleEdgesShown caps the edges pluginRuleCycle's warning names.
const maxCycleEdgesShown = 8

// pluginRuleCycle is a plan warning naming the cycle p's own rules form
// over the plugins it lists, or "" when they form none (#611 review). It
// runs P3's sort (sortPlugins) over the plugins the order and the rules
// name, with no header facts: a cycle among the rules alone fails every
// deploy that has those plugins (*PluginOrderCycleError), so the plan says
// so before the import rather than after. A warning, not a refusal: the
// rules are the curator's, and the user can change them on the profile.
// Group edges never form a cycle (addGroupEdges skips one that would).
func pluginRuleCycle(p domain.ProfilePlugins) string {
	if len(p.Rules.Plugins) == 0 {
		return ""
	}
	var nodes []pluginNode
	seen := map[string]bool{}
	add := func(name string) {
		if !seen[pluginKey(name)] {
			seen[pluginKey(name)] = true
			nodes = append(nodes, pluginNode{name: name, modIndex: -1})
		}
	}
	for _, e := range p.Order {
		add(e.Name)
	}
	for _, r := range p.Rules.Plugins {
		add(r.Name)
	}
	_, err := sortPlugins(sortRequest{nodes: nodes, order: p.Order, rules: p.Rules})
	var cycle *PluginOrderCycleError
	if !errors.As(err, &cycle) {
		return ""
	}
	// A stranger's recipe can make the cycle as long as its plugin list:
	// the warning names the first edges and counts the rest, since it
	// travels in the plan JSON, its SSE frames, the web plan and the
	// terminal (#611 re-review).
	shown := min(len(cycle.Edges), maxCycleEdgesShown)
	parts := make([]string, shown)
	for i, edge := range cycle.Edges[:shown] {
		parts[i] = edge.describe()
	}
	edges := strings.Join(parts, "; ")
	if more := len(cycle.Edges) - shown; more > 0 {
		edges += fmt.Sprintf("; ... and %d more", more)
	}
	return "The collection's plugin rules form a cycle, so no load order satisfies them all: " + edges +
		". The import goes ahead, but every deploy will leave the game's plugin list as it was and report the cycle, until a rule in the profile's plugins: changes."
}

// CollectionImportResult is the ONE document `lmm profile import --json`
// writes for a collection: the collection beside the import's own result,
// as WorkshopCollectionImportResult does for a Workshop collection - the
// counts say what happened, the collection says to which items, and which
// of them need the user (external items, missing sources, withheld hooks).
type CollectionImportResult struct {
	Collection *CollectionImport    `json:"collection"`
	Result     *ProfileImportResult `json:"result"`
}

// collectionState is the profile state a collection import saves beside
// its version-0 document: the collection identity and the external items,
// both checked by sanitizeCollection.
type collectionState struct {
	collection *domain.CollectionInfo
	external   []domain.CollectionExternalItem
	// plugins is the document's plugin list (#616).
	plugins domain.ProfilePlugins
	// loadOrders are the document's file load orders (#620).
	loadOrders domain.FileLoadOrders
}

// applyTo sets c's state on profile; a nil c sets nothing.
func (c *collectionState) applyTo(profile *domain.Profile) {
	if c == nil {
		return
	}
	profile.Collection, profile.External = c.collection, c.external
	profile.Plugins = c.plugins.Clone()
	profile.LoadOrders = c.loadOrders.Clone()
}

// CollectionImportItem is one mod of a collection import plan.
type CollectionImportItem struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name,omitempty"`
	Version  string `json:"version,omitempty"`
	// Unavailable says why lmm cannot fetch the item here, and what to do
	// about it; empty when it can.
	Unavailable string `json:"unavailable,omitempty"`
	// InstallerChoices are the FOMOD installer choices the collection
	// records for the item (#615), one "Step > Group: picks" line per
	// answered group, for a plan to show; empty when it records none. They
	// are replayed as written; an installer that no longer accepts them is
	// asked about at install time.
	InstallerChoices []string `json:"installer_choices,omitempty"`
}

// CollectionGameError refuses a collection whose portable game identity
// matches none, or more than one, of the configured games. The caller's
// input decides it: name the game (`--game`).
type CollectionGameError struct {
	// Game is the document's game identity.
	Game domain.CollectionGame
	// Candidates names the matching games when there are several; empty
	// when none matched.
	Candidates []string
	// Mismatched names the games that share a source id with the
	// document but are, by their Steam app id, a different game.
	Mismatched []CollectionGameMismatch
}

// CollectionGameMismatch is a local game a collection's source id points
// at, refused because its Steam app id differs from the document's.
type CollectionGameMismatch struct {
	GameID     string
	SteamAppID string
	SourceID   string
}

// Error implements error.
func (e *CollectionGameError) Error() string {
	what := describeCollectionGame(e.Game)
	if len(e.Candidates) > 0 {
		return fmt.Sprintf("this collection is for %s, which matches more than one of your games (%s); choose one with --game",
			what, strings.Join(e.Candidates, ", "))
	}
	if len(e.Mismatched) > 0 {
		var games []string
		for _, m := range e.Mismatched {
			games = append(games, fmt.Sprintf("%s (Steam app %s, same %s id)", m.GameID, m.SteamAppID, m.SourceID))
		}
		return fmt.Sprintf("this collection is for %s, and your game %s is a different Steam app; if it is the right game, choose it with --game",
			what, strings.Join(games, ", "))
	}
	return fmt.Sprintf("this collection is for %s, which matches none of your games; add the game, or choose one with --game", what)
}

// IsCollectionGameUnresolved reports whether err is a collection import
// that needs the caller to name its game.
func IsCollectionGameUnresolved(err error) bool {
	var target *CollectionGameError
	return errors.As(err, &target)
}

// ErrInvalidCollection is a collection document the reader refused: over a
// size cap, or not a collection at all.
var ErrInvalidCollection = errors.New("invalid collection document")

// IsInvalidCollection reports whether err is a collection document the
// reader refused - the caller's input, not a failure.
func IsInvalidCollection(err error) bool {
	return errors.Is(err, ErrInvalidCollection) || errors.Is(err, errBadContainer) ||
		errors.Is(err, ErrCollectionFormatTooNew)
}

// Caps on a version-1 document, past the byte cap: a document from a
// stranger is untrusted input.
const (
	maxCollectionItems     = 10000
	maxCollectionExternal  = 1000
	maxCollectionOverrides = 256
	maxCollectionFeatures  = 64
	maxCollectionLine      = 1024
	maxCollectionText      = 64 << 10
)

// PlanCollectionImport reads a collection document and plans importing the
// profile it describes. Nothing is written and nothing is fetched. The plan
// is an ordinary ImportPlan with Collection attached; a document needing
// features this lmm lacks still plans, and its Apply refuses.
func (s *Service) PlanCollectionImport(ctx context.Context, req CollectionImportRequest) (*ImportPlan, error) {
	return s.planCollectionImport(ctx, req, "", nil)
}

// planCollectionImport is PlanCollectionImport. importURL is the address a
// URL import fetched Data from (#622), recorded as the profile's
// origin.import_url; it also lets Data be a version-0 profile export,
// which a file imports through PlanImport instead. warnings lead the
// plan's own.
func (s *Service) planCollectionImport(ctx context.Context, req CollectionImportRequest, importURL string, warnings []string) (*ImportPlan, error) {
	if req.Input == "" {
		req.Input = CollectionFromFile
	}
	doc, container, more, err := s.readCollectionData(req.Data, importURL != "")
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, more...)
	more, err = sanitizeCollection(doc)
	if err != nil {
		return nil, err
	}
	warnings = append(warnings, more...)

	game, matchedBy, err := s.matchImportGame(doc, req.GameID)
	if err != nil {
		return nil, err
	}
	if matchedBy == "game" {
		if w := s.gameMismatch(doc.Game, game); w != "" {
			warnings = append(warnings, w)
		}
		if doc.FormatVersion == 0 && doc.GameID != "" && doc.GameID != game.ID {
			warnings = append(warnings, fmt.Sprintf("this profile export was made for the game %q; importing into %s as asked", doc.GameID, game.ID))
		}
	}

	name, err := collectionImportName(doc, req.ProfileName)
	if err != nil {
		return nil, err
	}

	profile := &domain.Profile{Name: name, GameID: game.ID, External: doc.External, Plugins: doc.Plugins, LoadOrders: doc.LoadOrders.Clone()}
	for _, item := range doc.Items {
		profile.Mods = append(profile.Mods, item.Ref)
	}
	if len(doc.Overrides) > 0 {
		profile.Overrides = make(map[string][]byte, len(doc.Overrides))
		for path, content := range doc.Overrides {
			profile.Overrides[path] = []byte(content)
		}
	}
	info := doc.Info
	if importURL != "" {
		// #622: the address is the profile's to fetch again (#613), beside
		// whatever credit the document's own origin carries.
		var origin domain.CollectionOrigin
		if info.Origin != nil {
			origin = *info.Origin
		}
		origin.ImportURL = importURL
		info.Origin = &origin
	}
	if !info.IsZero() {
		profile.Collection = &info
	}
	// The hooks go into the document the plan saves only for a local file,
	// where the import gate (WithHooks) decides; a URL's document never
	// holds them, so no option can switch them on.
	hooks := listHooks(doc.Hooks, doc.HooksExplicit)
	if req.Input == CollectionFromFile {
		profile.Hooks, profile.HooksExplicit = doc.Hooks, doc.HooksExplicit
	}
	data, err := config.ExportProfile(profile)
	if err != nil {
		return nil, fmt.Errorf("building the collection's profile: %w", err)
	}

	plan, err := s.PlanImport(ctx, game, data)
	if err != nil {
		return nil, err
	}
	// The plan lists every hook the DOCUMENT carries, a URL's included.
	plan.Hooks = hooks
	plan.collectionState = &collectionState{collection: profile.Collection, external: profile.External, plugins: profile.Plugins, loadOrders: profile.LoadOrders.Clone()}
	// The preview shows the profile the import will save.
	plan.Profile.Collection, plan.Profile.External = redactedCollection(profile.Collection), profile.External
	plan.Profile.Plugins = profile.Plugins.Clone()
	plan.Profile.LoadOrders = profile.LoadOrders.Clone()

	requires := doc.Requires
	derived := deriveCollectionRequires(doc)
	c := &CollectionImport{
		FormatVersion:       doc.FormatVersion,
		Container:           container,
		Input:               req.Input,
		ID:                  info.ID,
		Version:             info.Version,
		Name:                firstNonEmpty(info.Title, doc.Name, name),
		ProfileName:         name,
		Author:              info.Author,
		Description:         info.Description,
		InstallInstructions: info.InstallInstructions,
		Origin:              info.Origin.Redacted(),
		Game:                doc.Game,
		GameID:              game.ID,
		MatchedBy:           matchedBy,
		Requires:            unionFeatures(requires, derived),
		Unsupported:         unsupportedFeatures(requires, derived, s.modTypeRoutes(game), s.loadOrderSupport(game)),
		External:            doc.External,
		Hooks:               hooks,
		HooksImportable:     req.Input == CollectionFromFile,
		Warnings:            warnings,
		Items:               make([]CollectionImportItem, 0, len(doc.Items)),
	}
	_, managed := s.pluginRules(game)
	c.Plugins = summarisePlugins(profile.Plugins, managed)
	c.LoadOrders = s.summariseLoadOrders(game, profile.LoadOrders)
	if w := pluginRuleCycle(profile.Plugins); w != "" {
		c.Warnings = append(c.Warnings, w)
	}

	for path := range doc.Overrides {
		c.Overrides = append(c.Overrides, path)
	}
	sort.Strings(c.Overrides)
	for _, item := range doc.Items {
		c.Items = append(c.Items, CollectionImportItem{
			SourceID:    item.Ref.SourceID,
			ModID:       item.Ref.ModID,
			Name:        item.Name,
			Version:     item.Ref.Version,
			Unavailable: s.unavailableReason(game, item.Ref.SourceID),
			// #615: what the collection's installer answers will install.
			InstallerChoices: installerRecordLines(item),
		})
	}
	plan.Collection = c
	return plan, nil
}

// ApplyCollectionImport executes a plan PlanCollectionImport (or
// PlanCollectionImportURL) produced. It refuses, writing nothing, when the
// document needs a feature this lmm cannot import
// (*CollectionUnsupportedError). opts.WithHooks is ignored for a document
// fetched from a URL, whose hooks are never applied. Otherwise it is
// ApplyImport over the collection's profile, whose import gates decide the
// hooks (opts.WithHooks) and the overrides (opts.AllowAnyOverrides), as
// they do for every document import.
func (s *Service) ApplyCollectionImport(ctx context.Context, game *domain.Game, plan *ImportPlan, opts ProfileImportOptions, sink EventSink) (*ProfileImportResult, error) {
	c := plan.Collection
	if c == nil {
		return &ProfileImportResult{}, errors.New("not a collection import plan")
	}
	original := plan
	if len(c.Unsupported) > 0 {
		return &ProfileImportResult{}, &CollectionUnsupportedError{Unsupported: c.Unsupported, Supported: supportedExcept(c.Unsupported)}
	}
	if !c.HooksImportable {
		// A URL's hooks are never applied (#622): the plan's profile holds
		// none, and --with-hooks is ignored rather than refused, so the
		// result counts every hook the document carried as withheld.
		opts.WithHooks = false
	}
	if c.Source != nil {
		// A converted source collection is all or nothing at acquire (owner
		// ruling, #602): a half-installed collection is the outcome that
		// looks like it worked. An account the source serves no downloads
		// to installs only what needs none - a mod another profile already
		// has - and saves the profile with every other item pending.
		opts.AllOrNothing = true
		if c.Source.ManualDownload {
			trimmed := *plan
			trimmed.NeedsRedownload, trimmed.Missing = nil, nil
			plan = &trimmed
		}
	}
	result, err := s.ApplyImport(ctx, game, plan, opts, sink)
	if c.Source != nil {
		if lapsed := s.lapsedAccount(c.Source.SourceID, err); lapsed != nil {
			return result, lapsed
		}
	}
	if result == nil || result.ProfileName == "" {
		return result, err
	}
	if c.Source != nil && err == nil {
		if plan != original {
			// The items a free account cannot download were held back
			// from the apply; they are pending, as a --no-install import
			// counts its pending mods.
			result.Skipped += len(original.NeedsRedownload) + len(original.Missing)
		}
		finishSourceCollection(original, c.Source, result, opts)
	}
	if n := len(c.External); n > 0 {
		result.Notes = append(result.Notes, fmt.Sprintf("%d item(s) of this collection are not fetched by lmm: get them by hand (the profile records them, with the curator's instructions).", n))
	}
	return result, err
}

// matchCollectionGame picks the local game a collection imports into: the
// one named (gameID), else the one game whose Steam app id matches, else
// the one game sharing a source game id with the document.
func (s *Service) matchCollectionGame(want domain.CollectionGame, gameID string) (*domain.Game, string, error) {
	if gameID != "" {
		game, err := s.GetGame(gameID)
		if err != nil {
			return nil, "", fmt.Errorf("%w: %s", err, gameID)
		}
		return game, "game", nil
	}
	games := s.ListGames()
	if want.SteamAppID != "" {
		var hits []*domain.Game
		for _, g := range games {
			if s.steamAppIDOf(g) == want.SteamAppID {
				hits = append(hits, g)
			}
		}
		if len(hits) == 1 {
			return hits[0], "steam_app_id", nil
		}
		if len(hits) > 1 {
			return nil, "", &CollectionGameError{Game: want, Candidates: gameIDs(hits)}
		}
	}
	var hits []*domain.Game
	var via string
	var mismatched []CollectionGameMismatch
	for _, g := range games {
		src := sharedSource(want.Sources, g.SourceIDs)
		if src == "" {
			continue
		}
		// A shared source id is not enough when the Steam app ids say
		// otherwise (#621 review S7): Fallout 4 and Fallout 4 VR share
		// nexusmods' fallout4, Skyrim SE and VR share
		// skyrimspecialedition. A game whose own app id is known and
		// differs is a different game.
		if want.SteamAppID != "" {
			if id := s.steamAppIDOf(g); id != "" && id != want.SteamAppID {
				mismatched = append(mismatched, CollectionGameMismatch{GameID: g.ID, SteamAppID: id, SourceID: src})
				continue
			}
		}
		hits = append(hits, g)
		via = src
	}
	if len(hits) == 1 {
		return hits[0], "source:" + via, nil
	}
	if len(hits) > 1 {
		return nil, "", &CollectionGameError{Game: want, Candidates: gameIDs(hits)}
	}
	return nil, "", &CollectionGameError{Game: want, Mismatched: mismatched}
}

// sharedSource is the first source (in name order) both maps give the same
// non-empty game id, or "".
func sharedSource(doc, local map[string]string) string {
	ids := make([]string, 0, len(doc))
	for src := range doc {
		ids = append(ids, src)
	}
	sort.Strings(ids)
	for _, src := range ids {
		if doc[src] != "" && local[src] == doc[src] {
			return src
		}
	}
	return ""
}

// gameMismatch is a warning when the game the caller named contradicts
// the document's own identity, or "".
func (s *Service) gameMismatch(want domain.CollectionGame, game *domain.Game) string {
	contradicts := false
	if want.SteamAppID != "" {
		if id := s.steamAppIDOf(game); id != "" && id != want.SteamAppID {
			contradicts = true
		}
	}
	for src, id := range want.Sources {
		if local := game.SourceIDs[src]; local != "" && id != "" && local != id {
			contradicts = true
		}
	}
	if contradicts {
		return fmt.Sprintf("this collection is for %s, but importing into %s as asked", describeCollectionGame(want), game.ID)
	}
	// Nothing contradicts, but nothing agrees either (#621 review N3): the
	// document names its game and the chosen game shares none of it.
	if want.SteamAppID == "" && len(want.Sources) == 0 {
		return ""
	}
	if want.SteamAppID != "" && s.steamAppIDOf(game) == want.SteamAppID {
		return ""
	}
	if sharedSource(want.Sources, game.SourceIDs) != "" {
		return ""
	}
	return fmt.Sprintf("this collection is for %s, which shares no Steam app id or source id with %s; importing into it as asked", describeCollectionGame(want), game.ID)
}

// unavailableReason is why lmm cannot fetch a mod from sourceID here -
// the source is not registered (a custom source this machine lacks, or one
// switched off) - with the remedy, or "" when it can.
func (s *Service) unavailableReason(_ *domain.Game, sourceID string) string {
	if _, err := s.GetSource(sourceID); err != nil {
		return fmt.Sprintf("lmm has no source %q here; add it, then re-import (a collection never carries a custom source's definition: ask its author for it)", sourceID)
	}
	return ""
}

// collectionImportName is the profile name an import creates: the one
// asked for, else the document's name, else (when that cannot name a
// profile file) a slug of its title or name.
func collectionImportName(doc *domain.CollectionDocument, asked string) (string, error) {
	if name := strings.TrimSpace(asked); name != "" {
		if !validProfileName(name) {
			return "", fmt.Errorf("%w: %q", domain.ErrInvalidProfileName, name)
		}
		return name, nil
	}
	if validProfileName(doc.Name) {
		return doc.Name, nil
	}
	if slug := slugify(firstNonEmpty(doc.Name, doc.Info.Title)); slug != "" {
		return slug, nil
	}
	return "", fmt.Errorf("%w: the document names no profile; choose one with --as", ErrInvalidCollection)
}

// validProfileName is config's profile-name rule: non-blank, no path
// separator, no "..".
func validProfileName(name string) bool {
	return strings.TrimSpace(name) != "" && !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..") &&
		len(name) <= maxProfileSlug*4
}

// sanitizeCollection enforces the caps and cleans every display string of
// a document from someone else (#594). Items with no source or mod id are
// dropped, with a warning.
func sanitizeCollection(doc *domain.CollectionDocument) (warnings []string, err error) {
	switch {
	case len(doc.Items) > maxCollectionItems:
		return nil, fmt.Errorf("%w: it lists %d mods, over the %d limit", ErrInvalidCollection, len(doc.Items), maxCollectionItems)
	case len(doc.External) > maxCollectionExternal:
		return nil, fmt.Errorf("%w: it lists %d external items, over the %d limit", ErrInvalidCollection, len(doc.External), maxCollectionExternal)
	case len(doc.Overrides) > maxCollectionOverrides:
		return nil, fmt.Errorf("%w: it carries %d overrides, over the %d limit", ErrInvalidCollection, len(doc.Overrides), maxCollectionOverrides)
	case len(doc.Requires) > maxCollectionFeatures:
		return nil, fmt.Errorf("%w: it requires %d features, over the %d limit", ErrInvalidCollection, len(doc.Requires), maxCollectionFeatures)
	}
	var tooLong error
	line := func(field string, s *string) {
		*s = domain.SanitizeLine(*s)
		if len(*s) > maxCollectionLine && tooLong == nil {
			tooLong = fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit", ErrInvalidCollection, field, len(*s), maxCollectionLine)
		}
	}
	text := func(field string, s *string) {
		*s = domain.SanitizeText(*s)
		if len(*s) > maxCollectionText && tooLong == nil {
			tooLong = fmt.Errorf("%w: %s is %d bytes, over the %d-byte limit", ErrInvalidCollection, field, len(*s), maxCollectionText)
		}
	}

	line("name", &doc.Name)
	line("game_id", &doc.GameID)
	line("id", &doc.Info.ID)
	line("title", &doc.Info.Title)
	line("author", &doc.Info.Author)
	text("description", &doc.Info.Description)
	text("install_instructions", &doc.Info.InstallInstructions)
	line("signer", &doc.Signer)
	line("game.name", &doc.Game.Name)
	line("game.steam_app_id", &doc.Game.SteamAppID)
	for src, id := range doc.Game.Sources {
		line("game.sources", &id)
		doc.Game.Sources[src] = id
	}
	for i := range doc.Game.Versions {
		line("game.versions", &doc.Game.Versions[i])
	}
	for i := range doc.Requires {
		line("requires", &doc.Requires[i])
	}
	if o := doc.Info.Origin; o != nil {
		// import_url is this machine's record of a URL import (#622), never
		// a document's to set: a document naming one could point a later
		// update anywhere.
		o.ImportURL = ""
		line("origin.source_id", &o.SourceID)
		line("origin.slug", &o.Slug)
		line("origin.url", &o.URL)
		line("origin.author", &o.Author)
	}
	for i := range doc.External {
		e := &doc.External[i]
		line("external.name", &e.Name)
		line("external.url", &e.URL)
		text("external.instructions", &e.Instructions)
	}
	// Hooks are commands lmm would run, and override keys and source ids
	// are names that reach the plan and the filesystem: none is rewritten
	// (a cleaned command is not the command that runs), so one carrying a
	// control, escape, or bidi/format character - which could make the plan
	// show something other than what it holds - refuses the document
	// (#621 review S1, S2).
	for _, h := range listHooks(doc.Hooks, doc.HooksExplicit) {
		if hasUnsafeRunes(h.Command) {
			return nil, fmt.Errorf("%w: the %s hook's command contains control or formatting characters, so the plan could not show it as it would run", ErrInvalidCollection, h.Hook)
		}
		if len(h.Command) > maxCollectionLine {
			return nil, fmt.Errorf("%w: the %s hook's command is %d bytes, over the %d-byte limit", ErrInvalidCollection, h.Hook, len(h.Command), maxCollectionLine)
		}
	}
	for path := range doc.Overrides {
		if hasUnsafeRunes(path) || len(path) > maxCollectionLine {
			return nil, fmt.Errorf("%w: an override path (%q) contains control or formatting characters, or is over the %d-byte limit", ErrInvalidCollection, path, maxCollectionLine)
		}
		// The deploy's own containment rule, applied before anything is
		// saved: an override the deploy would refuse never reaches the
		// profile, where it would fail every later deploy (S6).
		if err := checkOverridePath(path); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidCollection, err)
		}
	}
	for src := range doc.Game.Sources {
		if hasUnsafeRunes(src) || len(src) > maxCollectionLine {
			return nil, fmt.Errorf("%w: a game.sources key (%q) contains control or formatting characters, or is over the %d-byte limit", ErrInvalidCollection, src, maxCollectionLine)
		}
	}
	// A link is shown as one only when it is a plain web address (S3): any
	// other URL - javascript:, data:, file:, a relative one - is dropped
	// from the item and named, as text, in a warning.
	safeURL := func(what string, u *string) {
		if *u != "" && domain.SafeWebURL(*u) == "" {
			warnings = append(warnings, fmt.Sprintf("%s's link (%q) is not a web address and was dropped", what, *u))
			*u = ""
		}
	}
	if o := doc.Info.Origin; o != nil {
		safeURL("the collection's origin", &o.URL)
		if o.IsZero() {
			doc.Info.Origin = nil
		}
	}
	for i := range doc.External {
		safeURL(fmt.Sprintf("external item %q", doc.External[i].Name), &doc.External[i].URL)
	}

	items := doc.Items[:0]
	for _, item := range doc.Items {
		r := &item.Ref
		line("mods.source_id", &r.SourceID)
		line("mods.mod_id", &r.ModID)
		line("mods.version", &r.Version)
		line("mods.category", &r.Category)
		line("mods.name", &item.Name)
		for i := range r.FileIDs {
			line("mods.file_ids", &r.FileIDs[i])
		}
		for i := range item.Files {
			line("mods.files.id", &item.Files[i].ID)
			line("mods.files.md5", &item.Files[i].MD5)
			line("mods.files.sha256", &item.Files[i].SHA256)
		}
		if strings.TrimSpace(r.SourceID) == "" || strings.TrimSpace(r.ModID) == "" {
			warnings = append(warnings, fmt.Sprintf("a mods: entry (%q) names no source or mod id and was skipped", item.Name))
			continue
		}
		if err := checkCollectionFomod(r.Fomod); err != nil {
			return nil, fmt.Errorf("%w: %s:%s's fomod choices %v", ErrInvalidCollection, r.SourceID, r.ModID, err)
		}
		if err := checkCollectionFomodFiles(*r); err != nil {
			return nil, fmt.Errorf("%w: %s:%s's fomod_files %v", ErrInvalidCollection, r.SourceID, r.ModID, err)
		}
		if err := checkCollectionFomodOrder(*r); err != nil {
			return nil, fmt.Errorf("%w: %s:%s's fomod_order %v", ErrInvalidCollection, r.SourceID, r.ModID, err)
		}
		items = append(items, item)
	}
	doc.Items = items
	if err := checkCollectionPlugins(doc.Plugins); err != nil {
		return nil, fmt.Errorf("%w: its plugins: %v", ErrInvalidCollection, err)
	}
	if err := checkCollectionLoadOrders(doc.LoadOrders); err != nil {
		return nil, fmt.Errorf("%w: its load orders: %v", ErrInvalidCollection, err)
	}
	if doc.Signer != "" {
		warnings = append(warnings, fmt.Sprintf("the document names a signer (%s), which this version of lmm does not verify", doc.Signer))
	}
	return warnings, tooLong
}

// checkCollectionFomodOrder refuses a stranger's per-file layer-order hint
// (#634) lmm could not keep as written: one with no fomod_files to order,
// longer than fomod.MaxChoices, or naming a file that is empty, repeated,
// holds a control, escape or bidi/format character, runs over the line
// limit, or is not one the entry pins (when it pins any).
func checkCollectionFomodOrder(r domain.ModReference) error {
	if len(r.FomodOrder) == 0 {
		return nil
	}
	if r.FomodFiles.IsZero() {
		return errors.New("has no fomod_files to order")
	}
	if len(r.FomodOrder) > fomod.MaxChoices {
		return fmt.Errorf("lists %d files, over the %d limit", len(r.FomodOrder), fomod.MaxChoices)
	}
	for i, id := range r.FomodOrder {
		if id == "" || hasUnsafeRunes(id) || len(id) > maxCollectionLine || slices.Contains(r.FomodOrder[:i], id) {
			return fmt.Errorf("names a file (%q) that is empty, repeated, contains control or formatting characters, or is over the %d-byte limit", id, maxCollectionLine)
		}
		if len(r.FileIDs) > 0 && !slices.Contains(r.FileIDs, id) {
			return fmt.Errorf("names file %q, which the entry does not pin", id)
		}
	}
	return nil
}

// checkCollectionFomodFiles refuses a stranger's per-file FOMOD choices
// (#634) lmm could not replay as written: a file id that is not one the
// entry pins (when it pins any), or that holds a control, escape or
// bidi/format character or runs over the line limit - it is an answer key
// and a profile key, never rewritten - more files than fomod.MaxChoices,
// and any one file's choices that checkCollectionFomod refuses.
func checkCollectionFomodFiles(r domain.ModReference) error {
	if r.FomodFiles.IsZero() {
		return nil
	}
	if len(r.FomodFiles) > fomod.MaxChoices {
		return fmt.Errorf("record %d files, over the %d limit", len(r.FomodFiles), fomod.MaxChoices)
	}
	for id, c := range r.FomodFiles {
		if id == "" || hasUnsafeRunes(id) || len(id) > maxCollectionLine {
			return fmt.Errorf("name a file id (%q) that is empty, contains control or formatting characters, or is over the %d-byte limit", id, maxCollectionLine)
		}
		if len(r.FileIDs) > 0 && !slices.Contains(r.FileIDs, id) {
			return fmt.Errorf("record choices for file %q, which the entry does not pin", id)
		}
		if err := checkCollectionFomod(c); err != nil {
			return fmt.Errorf("for file %q %v", id, err)
		}
	}
	return nil
}

// checkCollectionFomod refuses a stranger's recorded FOMOD choices that
// lmm could not replay as written (#614). Every name is matched against
// the installer's, so none is rewritten: one holding a control, escape or
// bidi/format character, or over the line limit, refuses the document, as
// a hook command does. The config hash must be lmm's own form (64
// lower-case hex digits, or absent), a file state one of the three the
// evaluator gives, and the counts stay within what internal/fomod accepts:
// fomod.MaxChoices recorded entries (steps, groups and picks together),
// and as many file states.
func checkCollectionFomod(c domain.FomodChoices) error {
	if c.IsZero() {
		return nil
	}
	if c.ConfigSHA256 != "" && !isLowerHexSHA256(c.ConfigSHA256) {
		return fmt.Errorf("name a config_sha256 (%q) that is not 64 lower-case hex digits", c.ConfigSHA256)
	}
	entries := len(c.Steps)
	for _, st := range c.Steps {
		entries += len(st.Groups)
		for _, g := range st.Groups {
			entries += len(g.Choices)
		}
	}
	if entries > fomod.MaxChoices {
		return fmt.Errorf("hold %d entries, over the %d limit", entries, fomod.MaxChoices)
	}
	if len(c.FileStates) > fomod.MaxChoices {
		return fmt.Errorf("hold %d file states, over the %d limit", len(c.FileStates), fomod.MaxChoices)
	}
	name := func(what, s string, index *int) error {
		if hasUnsafeRunes(s) || len(s) > maxCollectionLine {
			return fmt.Errorf("name a %s (%q) that contains control or formatting characters, or is over the %d-byte limit", what, s, maxCollectionLine)
		}
		if index != nil && *index < 0 {
			return fmt.Errorf("give the %s %q a negative index", what, s)
		}
		return nil
	}
	for _, st := range c.Steps {
		if err := name("step", st.Name, st.Index); err != nil {
			return err
		}
		for _, g := range st.Groups {
			if err := name("group", g.Name, g.Index); err != nil {
				return err
			}
			for _, o := range g.Choices {
				if err := name("option", o.Name, o.Index); err != nil {
					return err
				}
			}
		}
	}
	for path, state := range c.FileStates {
		if path == "" || hasUnsafeRunes(path) || len(path) > maxCollectionLine {
			return fmt.Errorf("name a file state path (%q) that is empty, contains control or formatting characters, or is over the %d-byte limit", path, maxCollectionLine)
		}
		switch state {
		case fomod.FileMissing.String(), fomod.FileInactive.String(), fomod.FileActive.String():
		default:
			return fmt.Errorf("give %q the file state %q, which is not Missing, Inactive or Active", path, state)
		}
	}
	return nil
}

// isLowerHexSHA256 reports whether s is a sha256 as lmm records one: 64
// lower-case hex digits.
func isLowerHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// hasUnsafeRunes reports whether s holds invalid UTF-8, a control
// character (tabs and line breaks included), a format character (bidi
// marks and controls, zero-width characters) or a line/paragraph
// separator: anything that could make a string display as other than it
// is.
func hasUnsafeRunes(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}
	return false
}

// describeCollectionGame names a portable game identity for a message.
func describeCollectionGame(g domain.CollectionGame) string {
	var parts []string
	if g.SteamAppID != "" {
		parts = append(parts, "Steam app "+g.SteamAppID)
	}
	srcs := make([]string, 0, len(g.Sources))
	for src, id := range g.Sources {
		srcs = append(srcs, src+" "+id)
	}
	sort.Strings(srcs)
	parts = append(parts, srcs...)
	name := g.Name
	if name == "" {
		name = "a game"
	}
	if len(parts) == 0 {
		return name + " (the document gives no Steam app id or source ids)"
	}
	return name + " (" + strings.Join(parts, ", ") + ")"
}

// unionFeatures is declared and derived together, deduplicated, in name
// order.
func unionFeatures(declared, derived []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range append(append([]string(nil), declared...), derived...) {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func gameIDs(games []*domain.Game) []string {
	out := make([]string, len(games))
	for i, g := range games {
		out[i] = g.ID
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// installerRecordLines is installerChoiceLines for an item in either form:
// a mod recorded per file (#634) prefixes each line with the file it
// answers, as "file <id>", in the order the entry pins its files (any file
// it records but does not pin after them, by id).
func installerRecordLines(item domain.CollectionItem) []string {
	files := item.Ref.FomodFiles
	if files.IsZero() {
		return installerChoiceLines(item.Ref.Fomod)
	}
	ids := slices.Clone(item.Ref.FileIDs)
	for id := range files {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.SortStableFunc(ids[len(item.Ref.FileIDs):], strings.Compare)
	var out []string
	for _, id := range ids {
		c, ok := files[id]
		if !ok || c.IsZero() {
			continue
		}
		label := "file " + domain.SanitizeLine(id)
		for _, line := range installerChoiceLines(c) {
			out = append(out, label+": "+line)
		}
	}
	return out
}

// installerChoiceLines words recorded FOMOD choices for a plan (#615): one
// "Step > Group: A, B" line per answered group ("none" for a group answered
// with no pick), sanitised for display. Nil for no choices.
func installerChoiceLines(c domain.FomodChoices) []string {
	var out []string
	for _, step := range c.Steps {
		for _, group := range step.Groups {
			picks := make([]string, 0, len(group.Choices))
			for _, o := range group.Choices {
				picks = append(picks, domain.SanitizeLine(o.Name))
			}
			list := strings.Join(picks, ", ")
			if list == "" {
				list = "none"
			}
			out = append(out, domain.SanitizeLine(step.Name)+" > "+domain.SanitizeLine(group.Name)+": "+list)
		}
	}
	return out
}

// Caps on a stranger's file load orders (#620): how many orders, and how
// many files each. The largest surveyed order holds 275 archives.
const (
	maxCollectionLoadOrders     = 16
	maxCollectionLoadOrderFiles = 10000
	// maxLoadOrderName is a file name's limit: no filesystem lmm runs on
	// takes a longer one.
	maxLoadOrderName = 255
)

// checkCollectionLoadOrders refuses a stranger's file load orders that lmm
// would not keep as written (#620). An id is a plain token (lower-case
// letters, digits and .-_), as the game's adapter names its orders. Every
// name is a file's name in the order's folder, matched against the
// deployed files and never rewritten: one that is empty, starts or ends
// with whitespace (the game trims each line), is . or .., holds
// a path separator, a control, escape or bidi/format character, or runs
// over a file name's length refuses the document.
func checkCollectionLoadOrders(orders domain.FileLoadOrders) error {
	if len(orders) > maxCollectionLoadOrders {
		return fmt.Errorf("it has more than %d load orders", maxCollectionLoadOrders)
	}
	for id, names := range orders {
		if !validLoadOrderID(id) {
			return fmt.Errorf("a load order's id (%q) is not a plain lower-case token", id)
		}
		if len(names) > maxCollectionLoadOrderFiles {
			return fmt.Errorf("the %s load order lists more than %d files", id, maxCollectionLoadOrderFiles)
		}
		for _, n := range names {
			if strings.TrimSpace(n) == "" || n != strings.TrimSpace(n) || n == "." || n == ".." || strings.ContainsAny(n, `/\`) || hasUnsafeRunes(n) || len(n) > maxLoadOrderName {
				return fmt.Errorf("a file name in the %s load order (%q) is empty, starts or ends with a space, is . or .., contains a path separator or control or formatting characters, or is over the %d-byte limit", id, n, maxLoadOrderName)
			}
		}
	}
	return nil
}

// validLoadOrderID reports whether id is a load-order id's spelling: 1 to
// 64 lower-case ASCII letters, digits and .-_, starting with a letter or
// digit.
func validLoadOrderID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '.' || r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// maxCollectionPlugins bounds a stranger's plugin list: every count in it -
// order entries, rule groups, per-plugin rules, and each rule's names - is
// at most this. A game loads 4,351 plugins at most (255 full, 4,096
// light), so a real list is well inside it.
const maxCollectionPlugins = 10000

// checkCollectionPlugins refuses a stranger's plugin list that lmm would
// not keep as written (#616). Every name is a plugin's file name, matched
// against the deployed plugins and never rewritten: one that is empty,
// holds a control, escape or bidi/format character or a path separator, or
// runs over the line limit refuses the document, as a hook command does.
func checkCollectionPlugins(p domain.ProfilePlugins) error {
	if len(p.Order) > maxCollectionPlugins || len(p.Rules.Groups) > maxCollectionPlugins || len(p.Rules.Plugins) > maxCollectionPlugins {
		return fmt.Errorf("it lists more than %d plugins, groups or rules", maxCollectionPlugins)
	}
	name := func(what, s string) error {
		if strings.TrimSpace(s) == "" || hasUnsafeRunes(s) || strings.ContainsAny(s, `/\`) || len(s) > maxCollectionLine {
			return fmt.Errorf("a %s (%q) is empty, contains control or formatting characters or a path separator, or is over the %d-byte limit", what, s, maxCollectionLine)
		}
		return nil
	}
	names := func(what string, list []string) error {
		if len(list) > maxCollectionPlugins {
			return fmt.Errorf("a %s list holds more than %d names", what, maxCollectionPlugins)
		}
		for _, s := range list {
			if err := name(what, s); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range p.Order {
		if err := name("plugin name", e.Name); err != nil {
			return err
		}
	}
	for _, g := range p.Rules.Groups {
		if err := name("group name", g.Name); err != nil {
			return err
		}
		if err := names("group", g.After); err != nil {
			return err
		}
	}
	for _, r := range p.Rules.Plugins {
		if err := name("plugin name", r.Name); err != nil {
			return err
		}
		if r.Group != "" {
			if err := name("group name", r.Group); err != nil {
				return err
			}
		}
		if err := names("plugin", r.After); err != nil {
			return err
		}
		if err := names("plugin", r.Req); err != nil {
			return err
		}
	}
	return nil
}
