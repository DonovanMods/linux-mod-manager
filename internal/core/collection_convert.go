// collection_convert.go is the source-collection converter (#607 C2,
// #609 C4; design docs/plans/2026-11-10-collections-design.md §1, §3, §6,
// §12): a collection's list (source.Collection) and recipe
// (source.CollectionManifest) in, lmm's version-1 collection document out,
// with the gate's verdict beside it. The document is what L1's import
// pipeline (collection_import.go) already reads, so a converted collection
// imports as an ordinary lmm collection.
//
// Pure: no network, no disk, no Service. The gate of §3 is applied here: a
// feature of the source's format with no lmm representation yet is raised
// by name, and the document is never asked to carry it. What lmm CAN
// represent is baked into the document: pins and locks from the update
// policy, the install phases and the before/after rules as the profile's
// mod order, recorded FOMOD choices as each entry's fomod block, external
// items as the document's external list, optional items by opting in.
package core

import (
	"cmp"
	"container/heap"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// The features a converted collection can raise (design §3), beside the
// document's own (CollectionFeatureExternal, CollectionFeatureFomod).
const (
	// CollectionFeatureList is the list itself: always raised, always
	// supported.
	CollectionFeatureList = "list"
	// CollectionFeatureModRules is before/after rules among the items,
	// replayed as the profile's mod order.
	CollectionFeatureModRules = "mod_rules"
	// CollectionFeatureOptional is optional items, installed only when
	// opted in.
	CollectionFeatureOptional = "optional"
	// CollectionFeaturePhase is install phases, replayed as install order.
	CollectionFeaturePhase = "phase"
	// CollectionFeatureFomodUnknownInstaller is recorded choices for an
	// installer other than FOMOD.
	CollectionFeatureFomodUnknownInstaller = "fomod_unknown_installer"
	// CollectionFeatureFomodMultiFile is one mod whose files carry more
	// than one installer: its choices are recorded per file (an entry's
	// fomod_files, #634), which an lmm collection document requires by
	// this name so that an older lmm - which installs one installer per
	// mod - refuses it.
	CollectionFeatureFomodMultiFile = "fomod_multi_file"
	// CollectionFeatureModTypeMixed is one mod whose files state different
	// mod types: lmm routes a mod's files by one type.
	CollectionFeatureModTypeMixed = "mod_type_mixed"
	// CollectionFeatureFileList is an exact file list (Vortex's replicate
	// mode, C5).
	CollectionFeatureFileList = "file_list"
	// CollectionFeatureBundled is an item shipped inside the collection's
	// own archive (C5/L3).
	CollectionFeatureBundled = "bundled"
	// CollectionFeaturePatches is binary patches applied to an item (C5).
	CollectionFeaturePatches = "patches"
	// CollectionFeatureFileOverrides is paths an item must win.
	CollectionFeatureFileOverrides = "file_overrides"
	// CollectionFeaturePluginOrder is a plugin list or plugin rules: in an
	// lmm collection document, the profile's `plugins:` block (#616); in a
	// Nexus recipe, `plugins`/`pluginRules`, which a conversion maps onto
	// that block (#611) - for a game whose plugin list lmm manages, and
	// refused for any other, where the list would never be written.
	CollectionFeaturePluginOrder = "plugin_order"
	// CollectionFeaturePluginRulesIncompatible is a plugin rule saying two
	// plugins are incompatible (LOOT's `inc`, Vortex's `incompatible`): the
	// profile's plugin rules have no place for one, so it is refused rather
	// than dropped (#611).
	CollectionFeaturePluginRulesIncompatible = "plugin_rules_incompatible"
	// CollectionFeatureManifestUnavailable is a collection whose recipe
	// could not be read, so nothing about how to install it is known.
	CollectionFeatureManifestUnavailable = "manifest_unavailable"
	// CollectionFeatureModTypePrefix prefixes a stated mod type
	// ("mod_type:dinput"), supported for a game whose adapter routes it.
	CollectionFeatureModTypePrefix = "mod_type:"
	// CollectionFeatureFileLoadOrderPrefix prefixes a file-based load order
	// ("file_load_order:cyberpunk2077-archive", C9).
	CollectionFeatureFileLoadOrderPrefix = "file_load_order:"
	// CollectionFeatureManifestKeyPrefix prefixes a recipe key lmm does not
	// recognise ("manifest:mergedData").
	CollectionFeatureManifestKeyPrefix = "manifest:"
)

// convertSupported is what a conversion bakes into the document rather than
// asking it to carry: always supported, beside the document's own supported
// set and the mod types the game routes.
var convertSupported = map[string]bool{
	CollectionFeatureList:     true,
	CollectionFeatureModRules: true,
	CollectionFeatureOptional: true,
	CollectionFeaturePhase:    true,
}

// convertPending are the features an lmm collection document supports but
// a conversion cannot yet carry into one: the converter refuses them even
// though the document's supported set has them, so a recipe needing one is
// never converted with that part dropped. Empty since #611 mapped a
// recipe's plugin order (plugin_order is gated per game instead:
// convertInput.pluginOrder); kept for the next feature in that position.
var convertPending = map[string]bool{}

// CollectionItemState is what an import does with one converted item.
type CollectionItemState string

const (
	// CollectionItemWillInstall is an item lmm will download and install.
	CollectionItemWillInstall CollectionItemState = "will_install"
	// CollectionItemOptionalSkipped is an optional item not opted in.
	CollectionItemOptionalSkipped CollectionItemState = "optional_skipped"
	// CollectionItemExternal is an item lmm lists for the user to fetch.
	CollectionItemExternal CollectionItemState = "external"
	// CollectionItemUnsupported is an item needing a feature lmm lacks: a
	// required one blocks the import, an optional one is skipped.
	CollectionItemUnsupported CollectionItemState = "unsupported"
	// CollectionItemInstalled is an item the profile already has at the
	// pinned file: nothing to do.
	CollectionItemInstalled CollectionItemState = "installed"
	// CollectionItemManualDownload is an item the account cannot download
	// through the source's API (a free NexusMods account): the import
	// records it, and its page is linked for the user to fetch it.
	CollectionItemManualDownload CollectionItemState = "manual_download"
)

// ConvertedCollection is a source collection converted into an lmm
// collection document, with what the conversion found.
type ConvertedCollection struct {
	// SourceID is the source the collection lives on; Slug, Revision, Name,
	// URL and Author identify it there.
	SourceID string `json:"source_id"`
	Slug     string `json:"slug"`
	Revision int    `json:"revision"`
	Name     string `json:"name"`
	URL      string `json:"url,omitempty"`
	Author   string `json:"author,omitempty"`
	// Document is the version-1 collection document: what an import
	// installs. Not on the wire: the import plan it becomes is.
	Document *domain.CollectionDocument `json:"-"`
	// Items is every item of the collection, in the collection's own order,
	// with what the import does with it.
	Items []ConvertedCollectionItem `json:"items"`
	// Counts counts Items by state.
	Counts map[CollectionItemState]int `json:"counts"`
	// Features is every feature the collection raised, supported or not, in
	// name order; Unsupported the ones that block the import.
	Features    []string                       `json:"features"`
	Unsupported []UnsupportedCollectionFeature `json:"unsupported,omitempty"`
	// Tools are the curator's tool shortcuts: listed, never created or run.
	Tools []CollectionTool `json:"tools,omitempty"`
	// Notes are what the conversion did that a person should know.
	Notes []string `json:"notes,omitempty"`
	// WithOptional and Optional echo what was opted in - every optional
	// item, or those named by ID - so a frontend re-plans with the same
	// selection.
	WithOptional bool     `json:"with_optional,omitzero"`
	Optional     []string `json:"optional,omitempty"`
	// AccountTier is the source account's tier, when the source reports one
	// (source.AccountTierReporter); ManualDownload says the account is not
	// served downloads through the API, so every item to install is a
	// manual download and the import saves the profile with them pending.
	AccountTier    string `json:"account_tier,omitempty"`
	ManualDownload bool   `json:"manual_download,omitzero"`
	// AutoUpdate names the document's entries (by domain.ModKey) whose
	// installed mod the import sets to the auto update policy: a mod whose
	// every file the curator marked "latest" installs the tested file now
	// and follows the newest one from the next update check on (owner
	// ruling, 2026-11-13). The profile document has no update policy - it
	// is the installed row's - so the import applies it.
	AutoUpdate []string `json:"auto_update,omitempty"`
	// ManifestError says why the recipe could not be read; empty when it
	// was.
	ManifestError string `json:"manifest_error,omitempty"`
	// LoadOrders are the recipe's file load orders the import carries
	// (#620), with how many of each order's files the recipe ties to one of
	// the collection's mods.
	LoadOrders []ConvertedLoadOrder `json:"load_orders,omitempty"`

	// loadOrderMods maps, per load-order id, a file of the order to the
	// collection mod the recipe's hint ties it to, for the plan.
	loadOrderMods map[string]map[string]string

	// pendingCollection holds the collection-level features the recipe
	// raised while its items were read, gated when the document is built.
	pendingCollection []string
}

// ConvertedCollectionItem is one item of a converted collection.
type ConvertedCollectionItem struct {
	// ID identifies the item within the collection - "<mod id>:<file id>"
	// for a mod file, "item:<n>" otherwise - and is what opting an optional
	// item in names.
	ID string `json:"id"`
	// Name and Version are display facts; ModID and FileID the file on the
	// source.
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	ModID   string `json:"mod_id,omitempty"`
	FileID  string `json:"file_id,omitempty"`
	// FileCategory is the source's category for the file (MAIN, OPTIONAL,
	// OLD_VERSION, ARCHIVED, ...), and URL its page.
	FileCategory string `json:"file_category,omitempty"`
	URL          string `json:"url,omitempty"`
	// State is what the import does with the item.
	State CollectionItemState `json:"state"`
	// Optional, Phase and UpdatePolicy are the curator's.
	Optional     bool   `json:"optional,omitzero"`
	Phase        int    `json:"phase,omitzero"`
	UpdatePolicy string `json:"update_policy,omitempty"`
	// ModType is the item's stated mod type, "" for the game's default.
	ModType string `json:"mod_type,omitempty"`
	// Installer reports that the collection records installer choices for
	// the item.
	Installer bool `json:"installer,omitzero"`
	// Instructions are the curator's words for the item.
	Instructions string `json:"instructions,omitempty"`
	// AutoUpdate reports that the import sets the installed mod's update
	// policy to auto (ConvertedCollection.AutoUpdate), and Note is what
	// the conversion did with the item that a person should know.
	AutoUpdate bool   `json:"auto_update,omitzero"`
	Note       string `json:"note,omitempty"`
	// Features are the features the item raises that lmm cannot import.
	Features []string `json:"features,omitempty"`
	// MD5 and SizeBytes describe the curator's file.
	MD5       string `json:"md5,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitzero"`
}

// CollectionTool is a curator's tool shortcut, listed for information.
type CollectionTool struct {
	Name string `json:"name"`
	Exe  string `json:"exe,omitempty"`
}

// convertInput is everything one conversion reads.
type convertInput struct {
	sourceID string
	gameID   string
	game     domain.CollectionGame
	list     source.Collection
	// manifest is the recipe; nil when it could not be read, and
	// manifestErr then says why.
	manifest    *source.CollectionManifest
	manifestErr string
	// withOptional opts every optional item in; optional opts in the items
	// it names (ConvertedCollectionItem.ID).
	withOptional bool
	optional     map[string]bool
	// modTypeSupported reports whether the game routes a stated mod type;
	// nil supports none.
	modTypeSupported func(modType string) bool
	// pluginOrder reports that lmm manages the game's plugin list (its
	// adapter is a Tier 1 PluginOrderer), so a recipe's plugin order can
	// be replayed (#611).
	pluginOrder bool
	// loadOrders reports which of a recipe's file load orders the game's
	// adapter writes (#620); nil supports none.
	loadOrders func(id string) bool
}

// item is one collection entry during a conversion.
type convItem struct {
	ConvertedCollectionItem
	index    int
	kind     source.CollectionItemKind
	logical  string
	included bool
	choices  []domain.FomodStepChoice
	// unsupported records the item's unsupported features (also kept in
	// Features).
	unsupported bool
}

// inEntry reports whether it belongs in its mod's profile entry: it is
// installed, or it is a required item that blocks the import (whose entry
// the verdict names either way).
func (it *convItem) inEntry() bool { return it.included || (it.unsupported && !it.Optional) }

// convertCollection converts one collection (see the file comment).
//
// Everything the source and the curator wrote is untrusted (#607 review 5):
// the input is sanitised first (sanitizeConvertInput) - every display
// string cleaned and capped, every link a plain web address or dropped,
// every feature-name suffix a safe token or escaped - and the document is
// then held to L1's own rules (sanitizeCollection, FOMOD names included),
// so no caller can hand an unsanitised document to the import planner. A
// document L1 would refuse is an error (ErrInvalidCollection).
func convertCollection(raw convertInput) (*ConvertedCollection, error) {
	in, err := sanitizeConvertInput(raw)
	if err != nil {
		return nil, err
	}
	out := &ConvertedCollection{
		SourceID: in.sourceID,
		Slug:     in.list.ID,
		Revision: in.list.Revision,
		Name:     in.list.Name,
		URL:      in.list.URL,
		Author:   in.list.Author,
	}
	features := map[string][]string{CollectionFeatureList: nil}
	raise := func(name, item string) {
		items := features[name]
		if item != "" && !slices.Contains(items, item) {
			items = append(items, item)
		}
		features[name] = items
	}
	collectionLevel := map[string]bool{}
	raiseCollection := func(name string) {
		raise(name, "")
		collectionLevel[name] = true
	}

	var items []*convItem
	var plugins domain.ProfilePlugins
	if in.manifest != nil {
		items = convertManifestItems(in, out)
		var pluginFeatures []string
		plugins, pluginFeatures = convertPlugins(in.manifest, out)
		for _, f := range pluginFeatures {
			raiseCollection(f)
		}
		// #620: each file load order is gated by whether the game's
		// adapter writes it (supported, below).
		for _, lo := range in.manifest.FileLoadOrders {
			raiseCollection(CollectionFeatureFileLoadOrderPrefix + lo.ID)
		}
	} else {
		out.ManifestError = in.manifestErr
		raiseCollection(CollectionFeatureManifestUnavailable)
		items = convertListItems(in)
	}

	// Item-level features, and whether each item is included.
	for _, it := range items {
		if it.Optional {
			raise(CollectionFeatureOptional, "")
		}
		if it.Phase > 0 {
			raise(CollectionFeaturePhase, "")
		}
		if it.Installer {
			raise(CollectionFeatureFomod, "")
		}
		if it.ModType != "" {
			raise(CollectionFeatureModTypePrefix+it.ModType, "")
		}
		if it.kind == source.CollectionItemExternal {
			raise(CollectionFeatureExternal, "")
		}
		for _, f := range it.Features {
			raise(f, "")
		}
		it.unsupported = len(it.Features) > 0
		optedIn := !it.Optional || in.withOptional || in.optional[it.ID]
		switch {
		case it.unsupported:
			it.State = CollectionItemUnsupported
		case !optedIn:
			it.State = CollectionItemOptionalSkipped
		case it.kind == source.CollectionItemExternal:
			it.State = CollectionItemExternal
			it.included = true
		default:
			it.State = CollectionItemWillInstall
			it.included = true
		}
	}

	// A mod's files share one profile entry: what cannot share one is a
	// feature of every file of it.
	groups := groupByMod(items)
	for _, g := range groups {
		var installers int
		types := map[string]bool{}
		for _, it := range g {
			if !it.inEntry() {
				continue
			}
			if it.Installer {
				installers++
			}
			types[it.ModType] = true
		}
		if installers > 1 {
			// #634: replayed per file, so it no longer blocks; the feature
			// is named, as fomod is, for the document's requires.
			raise(CollectionFeatureFomodMultiFile, "")
		}
		var extra []string
		if len(types) > 1 {
			extra = append(extra, CollectionFeatureModTypeMixed)
		}
		if len(extra) == 0 {
			continue
		}
		for _, it := range g {
			if !it.inEntry() {
				continue
			}
			for _, f := range extra {
				it.Features = append(it.Features, f)
				raise(f, "")
			}
			it.unsupported, it.included, it.State = true, false, CollectionItemUnsupported
		}
	}

	// The verdict: a collection-level feature blocks; an item's blocks when
	// the item is required, and skips it when it is optional.
	supported := func(name string) bool {
		if convertPending[name] {
			return false
		}
		if name == CollectionFeaturePluginOrder {
			return in.pluginOrder
		}
		if convertSupported[name] || supportedCollectionFeatures[name] {
			return true
		}
		if t, ok := strings.CutPrefix(name, CollectionFeatureModTypePrefix); ok {
			return in.modTypeSupported != nil && in.modTypeSupported(t)
		}
		if id, ok := strings.CutPrefix(name, CollectionFeatureFileLoadOrderPrefix); ok {
			return in.loadOrders != nil && in.loadOrders(id)
		}
		return false
	}
	blocking := map[string][]string{}
	for name := range collectionLevel {
		if !supported(name) {
			blocking[name] = nil
		}
	}
	var skippedOptional []string
	for _, it := range items {
		if !it.unsupported {
			continue
		}
		if it.Optional {
			skippedOptional = append(skippedOptional, it.Name)
			continue
		}
		for _, f := range it.Features {
			blocking[f] = appendUnique(blocking[f], itemRef(in.sourceID, it))
		}
	}
	for name := range blocking {
		out.Unsupported = append(out.Unsupported, UnsupportedCollectionFeature{Feature: name, Items: blocking[name]})
	}
	sort.Slice(out.Unsupported, func(i, j int) bool { return out.Unsupported[i].Feature < out.Unsupported[j].Feature })
	for name := range features {
		out.Features = append(out.Features, name)
	}
	sort.Strings(out.Features)
	if len(skippedOptional) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d optional item(s) need something this lmm cannot install and are skipped: %s.",
			len(skippedOptional), strings.Join(skippedOptional, ", ")))
	}

	out.Document = buildCollectionDocument(in, out, items, plugins, convertLoadOrders(in, out, items))
	warnings, err := sanitizeCollection(out.Document)
	if err != nil {
		return nil, err
	}
	out.Notes = append(out.Notes, warnings...)
	for _, it := range items {
		out.Items = append(out.Items, it.ConvertedCollectionItem)
	}
	out.WithOptional = in.withOptional
	for id := range in.optional {
		out.Optional = append(out.Optional, id)
	}
	sort.Strings(out.Optional)
	out.recount()
	return out, nil
}

// appendUnique appends s to list unless it is there.
func appendUnique(list []string, s string) []string {
	if slices.Contains(list, s) {
		return list
	}
	return append(list, s)
}

// itemRef names an item in a gate verdict: "<source>:<mod id>" for a mod
// file, as L1's verdicts name items, else its name.
func itemRef(sourceID string, it *convItem) string {
	if it.ModID != "" {
		return domain.ModKey(sourceID, it.ModID)
	}
	return it.Name
}

// convertManifestItems turns the recipe's mods into conversion items, the
// list's facts (file category, page) joined on by file id.
func convertManifestItems(in convertInput, out *ConvertedCollection) []*convItem {
	m := in.manifest
	if m.Name != "" {
		out.Name = m.Name
	}
	if m.Author != "" {
		out.Author = m.Author
	}
	listed := map[string]source.CollectionItem{}
	for _, li := range in.list.Items {
		if li.Kind == source.CollectionItemModFile && li.FileID != "" {
			listed[li.FileID] = li
		}
	}
	gameSourceID := in.game.Sources[in.sourceID]
	// #607 review 6: a recipe for another game is refused, never looked up
	// in this one.
	if m.GameSourceID != "" && gameSourceID != "" && m.GameSourceID != gameSourceID {
		collectionFeature(out, CollectionFeatureManifestKeyPrefix+"info.domainName")
	}
	recipeFiles := map[string]bool{}
	var unlisted []string
	items := make([]*convItem, 0, len(m.Mods))
	for i, mod := range m.Mods {
		it := &convItem{index: i}
		it.Name = mod.Name
		it.Version = mod.Version
		it.Optional = mod.Optional
		it.Phase = mod.Phase
		it.UpdatePolicy = mod.UpdatePolicy
		it.ModType = mod.ModType
		it.Instructions = mod.Instructions
		it.MD5 = mod.MD5
		it.SizeBytes = mod.SizeBytes
		it.ID = fmt.Sprintf("item:%d", i)
		it.logical = mod.LogicalFilename
		switch mod.SourceType {
		case "nexus":
			it.kind = source.CollectionItemModFile
			it.ModID, it.FileID = mod.ModID, mod.FileID
			if it.ModID == "" || it.FileID == "" {
				it.Features = append(it.Features, CollectionFeatureManifestKeyPrefix+"mods.source.fileId")
			} else {
				it.ID = it.ModID + ":" + it.FileID
				recipeFiles[it.FileID] = true
			}
			if li, ok := listed[it.FileID]; ok {
				it.FileCategory, it.URL = li.FileCategory, li.URL
				if it.Name == "" {
					it.Name = li.Name
				}
			} else if len(listed) > 0 && it.FileID != "" {
				unlisted = append(unlisted, it.Name)
			}
			// An item of ANOTHER game (its own domainName) would be looked up
			// in this one, and install a different mod with the same id.
			if mod.DomainName != "" && mod.DomainName != gameSourceID && gameSourceID != "" {
				it.Features = append(it.Features, CollectionFeatureManifestKeyPrefix+"mods.domainName")
			}
			// A loader its own installer sets up is a step for the user, by
			// the type the source states for it on its game (#635).
			if p, ok := prerequisiteFor(in.sourceID, cmp.Or(mod.DomainName, m.GameSourceID, gameSourceID), mod.ModType); ok {
				it.asPrerequisite(p)
			}
		case "bundle":
			it.kind = source.CollectionItemBundled
			it.Features = append(it.Features, CollectionFeatureBundled)
		case "browse", "direct", "manual":
			it.kind = source.CollectionItemExternal
			it.URL = mod.URL
		default:
			it.Features = append(it.Features, CollectionFeatureManifestKeyPrefix+"mods.source.type")
		}
		switch mod.UpdatePolicy {
		case "", "exact", "prefer", "latest":
		default:
			it.Features = append(it.Features, CollectionFeatureManifestKeyPrefix+"mods.source.updatePolicy")
		}
		switch mod.InstallerType {
		case "":
		case "fomod":
			it.Installer = true
			it.choices = mod.Choices
		default:
			it.Features = append(it.Features, CollectionFeatureFomodUnknownInstaller)
		}
		if len(mod.FileList) > 0 {
			it.Features = append(it.Features, CollectionFeatureFileList)
		}
		if len(mod.Patches) > 0 {
			it.Features = append(it.Features, CollectionFeaturePatches)
		}
		if len(mod.FileOverrides) > 0 {
			it.Features = append(it.Features, CollectionFeatureFileOverrides)
		}
		if it.ModType != "" && (in.modTypeSupported == nil || !in.modTypeSupported(it.ModType)) {
			it.Features = append(it.Features, CollectionFeatureModTypePrefix+it.ModType)
		}
		items = append(items, it)
	}
	for _, u := range m.Unknown {
		name := CollectionFeatureManifestKeyPrefix + u.Key
		if u.ModIndex >= 0 && u.ModIndex < len(items) {
			it := items[u.ModIndex]
			it.Features = appendUnique(it.Features, name)
		} else {
			collectionFeature(out, name)
		}
	}
	for _, t := range m.Tools {
		out.Tools = append(out.Tools, CollectionTool(t))
	}
	if len(out.Tools) > 0 {
		names := make([]string, len(out.Tools))
		for i, t := range out.Tools {
			names[i] = t.Name
		}
		out.Notes = append(out.Notes, fmt.Sprintf("The collection lists %d tool shortcut(s) (%s); lmm neither creates nor runs a collection's tools.",
			len(names), strings.Join(names, ", ")))
	}
	var missing int
	for fid := range listed {
		if !recipeFiles[fid] {
			missing++
		}
	}
	if missing > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d file(s) of the collection's published list are not in its recipe; the recipe is followed.", missing))
	}
	if len(unlisted) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d file(s) of the recipe are not in the collection's published list (%s); the recipe is followed.",
			len(unlisted), strings.Join(unlisted, ", ")))
	}
	// #607 review 7: the published list's external resources are items too.
	// One the recipe already names (by its link) is the same item.
	recipeLinks := map[string]bool{}
	for _, it := range items {
		if it.kind == source.CollectionItemExternal && it.URL != "" {
			recipeLinks[it.URL] = true
		}
	}
	for i, li := range in.list.Items {
		if li.Kind != source.CollectionItemExternal || (li.URL != "" && recipeLinks[li.URL]) {
			continue
		}
		it := &convItem{index: len(m.Mods) + i, kind: source.CollectionItemExternal}
		it.ID = fmt.Sprintf("external:%d", i)
		it.Name, it.URL, it.Optional, it.Instructions = li.Name, li.URL, li.Optional, li.Instructions
		items = append(items, it)
	}
	return items
}

// collectionFeature records a collection-level feature found in the recipe,
// for buildCollectionDocument to gate (pendingCollection): none of them is
// in the supported set.
func collectionFeature(out *ConvertedCollection, name string) {
	out.pendingCollection = appendUnique(out.pendingCollection, name)
}

// convertListItems is the list alone, for a collection whose recipe could
// not be read: enough to show what it holds, never to import it.
func convertListItems(in convertInput) []*convItem {
	items := make([]*convItem, 0, len(in.list.Items))
	for i, li := range in.list.Items {
		it := &convItem{index: i, kind: li.Kind}
		it.ID = fmt.Sprintf("item:%d", i)
		it.Name = li.Name
		it.Version = li.Version
		it.ModID, it.FileID = li.ModID, li.FileID
		it.FileCategory = li.FileCategory
		it.URL = li.URL
		it.Optional = li.Optional
		it.UpdatePolicy = li.UpdatePolicy
		it.Instructions = li.Instructions
		it.SizeBytes = li.SizeBytes
		if li.Kind == source.CollectionItemModFile && li.ModID != "" && li.FileID != "" {
			it.ID = li.ModID + ":" + li.FileID
		}
		if li.Kind == source.CollectionItemBundled {
			it.Features = append(it.Features, CollectionFeatureBundled)
		}
		items = append(items, it)
	}
	return items
}

// groupByMod groups the mod-file items by mod id, in first-appearance
// order; every other item is a group of its own.
func groupByMod(items []*convItem) [][]*convItem {
	var groups [][]*convItem
	at := map[string]int{}
	for _, it := range items {
		if it.kind != source.CollectionItemModFile || it.ModID == "" {
			groups = append(groups, []*convItem{it})
			continue
		}
		if i, ok := at[it.ModID]; ok {
			groups[i] = append(groups[i], it)
			continue
		}
		at[it.ModID] = len(groups)
		groups = append(groups, []*convItem{it})
	}
	return groups
}

// policyRank orders update policies by strictness, for a mod whose files
// state different ones: the strictest wins.
func policyRank(p string) int {
	switch p {
	case "", "exact":
		return 2
	case "prefer":
		return 1
	}
	return 0
}

// buildCollectionDocument builds the document from the included items: one
// entry per mod, in install-phase order with the before/after rules
// applied, the external items, the plugin list (convertPlugins) and the
// file load orders (convertLoadOrders).
func buildCollectionDocument(in convertInput, out *ConvertedCollection, items []*convItem, plugins domain.ProfilePlugins, loadOrders domain.FileLoadOrders) *domain.CollectionDocument {
	doc := &domain.CollectionDocument{
		FormatVersion: domain.CollectionFormatVersion,
		Name:          out.Name,
		GameID:        in.gameID,
		Game:          in.game,
		Plugins:       plugins,
		LoadOrders:    loadOrders,
		Info: domain.CollectionInfo{
			ID:      in.sourceID + ":" + out.Slug,
			Version: out.Revision,
			Title:   out.Name,
			Author:  out.Author,
		},
	}
	if m := in.manifest; m != nil {
		doc.Info.Description = m.Description
		doc.Info.InstallInstructions = m.InstallInstructions
		if len(m.GameVersions) > 0 {
			doc.Game.Versions = slices.Clone(m.GameVersions)
		}
	}
	origin := domain.CollectionOrigin{SourceID: in.sourceID, Slug: out.Slug, Revision: out.Revision, URL: out.URL, Author: out.Author}
	if !origin.IsZero() {
		doc.Info.Origin = &origin
	}

	// One entry per mod, from its included files.
	type entry struct {
		item    domain.CollectionItem
		members []*convItem
		phase   int
		first   int
		// fileRules are before/after rules between two of the entry's own
		// files (#634), as edges between members: they order its
		// file_ids, which is the order its files' trees layer in.
		fileRules [][2]*convItem
	}
	var entries []*entry
	entryOf := map[*convItem]*entry{}
	for _, g := range groupByMod(items) {
		var e *entry
		rank := -1
		for _, it := range g {
			if !it.included {
				continue
			}
			if it.kind == source.CollectionItemExternal {
				doc.External = append(doc.External, domain.CollectionExternalItem{Name: it.Name, URL: it.URL, Instructions: it.Instructions})
				continue
			}
			if e == nil {
				e = &entry{phase: it.Phase, first: it.index}
				e.item.Name = it.Name
				// The stated type is the entry's (#607): one mod, one type -
				// files that disagree were refused as mod_type_mixed.
				e.item.Ref = domain.ModReference{SourceID: in.sourceID, ModID: it.ModID, Version: it.Version, ModType: it.ModType}
				entries = append(entries, e)
			}
			e.members = append(e.members, it)
			entryOf[it] = e
			e.phase = min(e.phase, it.Phase)
			// Every policy installs the curator's exact file: it is the one the
			// collection was tested with, and it costs no lookup (ruled
			// 2026-11-13). What the policy changes is what follows.
			e.item.Ref.FileIDs = append(e.item.Ref.FileIDs, it.FileID)
			e.item.Files = append(e.item.Files, domain.CollectionFileRef{ID: it.FileID, MD5: it.MD5, Size: it.SizeBytes})
			if r := policyRank(it.UpdatePolicy); r > rank {
				rank = r
			}
			if it.Installer && len(it.choices) == 0 {
				it.Note = "the collection records an installer for this item but no answers, so its installer will ask at install"
			}
		}
		if e == nil {
			continue
		}
		// The recorded answers: one installer's as the entry's fomod, as
		// before; several files' per file (#634), keyed by file id - every
		// installer file counted, so one without answers asks for its own
		// rather than replay another file's.
		var installers []*convItem
		for _, it := range e.members {
			if it.Installer {
				installers = append(installers, it)
			}
		}
		switch {
		case len(installers) == 1 && len(installers[0].choices) > 0:
			e.item.Ref.Fomod = domain.FomodChoices{Steps: slices.Clone(installers[0].choices)}
		case len(installers) > 1:
			for _, it := range installers {
				if len(it.choices) == 0 {
					continue
				}
				if e.item.Ref.FomodFiles == nil {
					e.item.Ref.FomodFiles = domain.FomodFiles{}
				}
				e.item.Ref.FomodFiles[it.FileID] = domain.FomodChoices{Steps: slices.Clone(it.choices)}
			}
		}
		// exact locks the entry; prefer leaves it unlocked; latest leaves
		// it unlocked AND auto-updating - but only when every file of the
		// mod is latest, since one exact file keeps the whole entry locked.
		switch rank {
		case 2:
			e.item.Ref.Locked = e.item.Ref.Version != ""
			if !e.item.Ref.Locked {
				out.Notes = append(out.Notes, fmt.Sprintf("%s is pinned to the curator's file but names no version, so it cannot be locked; an update may move it.", e.item.Name))
			}
		case 0:
			out.AutoUpdate = append(out.AutoUpdate, domain.ModKey(in.sourceID, e.item.Ref.ModID))
		}
		for _, it := range e.members {
			if it.UpdatePolicy != "latest" {
				continue
			}
			switch rank {
			case 0:
				it.AutoUpdate = true
				it.Note = "curator asks for the latest file; installs the tested file now, then auto-updates"
			case 1:
				it.Note = "curator asks for the latest file, but another file of this mod prefers its tested file, so the mod does not auto-update; an update may move it"
			default:
				it.Note = "curator asks for the latest file, but another file of this mod is pinned exactly, so it stays on the tested file"
			}
		}
	}

	// Install order: phase first, then the collection's own order, with the
	// before/after rules applied over it.
	slices.SortStableFunc(entries, func(a, b *entry) int {
		return cmp.Or(cmp.Compare(a.phase, b.phase), cmp.Compare(a.first, b.first))
	})
	pos := make(map[*entry]int, len(entries))
	for i, e := range entries {
		pos[e] = i
	}
	edges := make([][]int, len(entries))
	indegree := make([]int, len(entries))
	addEdge := func(from, to *entry) {
		a, b := pos[from], pos[to]
		if a == b || slices.Contains(edges[a], b) {
			return
		}
		edges[a] = append(edges[a], b)
		indegree[b]++
	}
	if m := in.manifest; m != nil {
		resolve := ruleResolver(items)
		var outside, unmatched, notInstalled, informational, ordering int
		informationalTypes := map[string]bool{}
		for _, r := range m.ModRules {
			switch r.Type {
			case "before", "after":
				ordering++
			case "requires", "recommends", "conflicts", "provides":
				informational++
				informationalTypes[r.Type] = true
				continue
			default:
				collectionFeature(out, CollectionFeatureManifestKeyPrefix+"modRules.type")
				continue
			}
			src, srcHow := resolve(r.Source)
			ref, refHow := resolve(r.Reference)
			if src == nil || ref == nil {
				if srcHow == ruleUnmatched || refHow == ruleUnmatched {
					unmatched++
				} else {
					outside++
				}
				continue
			}
			es, er := entryOf[src], entryOf[ref]
			if es == nil || er == nil {
				notInstalled++
				continue
			}
			// A key lmm does not know on a rule it applies is a feature
			// of the collection; on a rule it drops, it changes nothing.
			for _, k := range r.Unknown {
				collectionFeature(out, CollectionFeatureManifestKeyPrefix+"modRules."+safeFeatureSuffix(k))
			}
			if es == er {
				// #634: two files of one mod - Vortex installs each as its
				// own mod, ordered by this rule, so it orders which file's
				// tree wins a path both write.
				if src != ref {
					if r.Type == "before" {
						es.fileRules = append(es.fileRules, [2]*convItem{src, ref})
					} else {
						es.fileRules = append(es.fileRules, [2]*convItem{ref, src})
					}
				}
				continue
			}
			if r.Type == "before" {
				addEdge(es, er)
			} else {
				addEdge(er, es)
			}
		}
		if outside > 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("%d ordering rule(s) name a mod that is not in this collection, and were ignored: they order nothing among its members.", outside))
		}
		if unmatched > 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("%d ordering rule(s) name a mod lmm cannot match to one item (a version range, a version none of its files has, or several files of that name), and were ignored.", unmatched))
		}
		if notInstalled > 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("%d ordering rule(s) name an item that is not being installed, and were ignored.", notInstalled))
		}
		if informational > 0 {
			types := make([]string, 0, len(informationalTypes))
			for t := range informationalTypes {
				types = append(types, t)
			}
			sort.Strings(types)
			out.Notes = append(out.Notes, fmt.Sprintf("%d informational rule(s) (%s) are not applied: the collection's list already is what it requires.",
				informational, strings.Join(types, ", ")))
		}
		if ordering > 0 {
			out.Features = appendSorted(out.Features, CollectionFeatureModRules)
		}
	}
	for _, e := range entries {
		if len(e.fileRules) > 0 {
			orderEntryFiles(&e.item, e.members, e.fileRules, out)
		}
	}
	order, cycles := stableTopoSort(len(entries), edges, indegree)
	for _, cycle := range cycles {
		names := make([]string, len(cycle))
		for i, n := range cycle {
			names[i] = entries[n].item.Name
		}
		out.Notes = append(out.Notes, fmt.Sprintf("The collection's ordering rules form a cycle among %s; lmm breaks it at %s, and every other rule holds.",
			strings.Join(names, ", "), names[0]))
	}
	lowest := -1
	var early []string
	for k := len(order) - 1; k >= 0; k-- {
		e := entries[order[k]]
		if lowest >= 0 && e.phase > lowest {
			early = append(early, e.item.Name)
		}
		if lowest < 0 || e.phase < lowest {
			lowest = e.phase
		}
	}
	if len(early) > 0 {
		slices.Reverse(early)
		out.Notes = append(out.Notes, fmt.Sprintf("The collection's ordering rules put %s before a mod of an earlier install phase; the rules are followed.",
			strings.Join(early, ", ")))
	}
	for _, i := range order {
		doc.Items = append(doc.Items, entries[i].item)
	}

	// Collection-level features the recipe raised, gated here.
	for _, name := range out.pendingCollection {
		out.Features = appendSorted(out.Features, name)
		if !slices.ContainsFunc(out.Unsupported, func(f UnsupportedCollectionFeature) bool { return f.Feature == name }) {
			out.Unsupported = append(out.Unsupported, UnsupportedCollectionFeature{Feature: name})
		}
	}
	sort.Slice(out.Unsupported, func(i, j int) bool { return out.Unsupported[i].Feature < out.Unsupported[j].Feature })
	out.pendingCollection = nil

	// The document requires what its content needs, mod types included
	// (each entry's ModType), exactly as an export derives it.
	doc.Requires = deriveCollectionRequires(doc)
	return doc
}

// orderEntryFiles records the order one entry's files layer in, from the
// before/after rules between its own files (#634), stable on the
// collection's order: a mod whose files each carry an installer has them
// share one cache entry, and a later file's tree wins a path an earlier
// one also writes - as a later-installed mod would in Vortex, which
// installs each file as its own mod. It is the entry's fomod_order, the
// hint every flow layers by (canonicalLayerOrder); file_ids keep the
// collection's order. A cycle among them keeps the collection's order for
// the files in it, with a note. Only a per-file entry records it: it has
// no say over any other mod's install.
func orderEntryFiles(item *domain.CollectionItem, members []*convItem, rules [][2]*convItem, out *ConvertedCollection) {
	if item.Ref.FomodFiles.IsZero() {
		return
	}
	index := make(map[*convItem]int, len(members))
	for i, m := range members {
		index[m] = i
	}
	edges := make([][]int, len(members))
	indegree := make([]int, len(members))
	for _, r := range rules {
		a, okA := index[r[0]]
		b, okB := index[r[1]]
		if !okA || !okB || a == b || slices.Contains(edges[a], b) {
			continue
		}
		edges[a] = append(edges[a], b)
		indegree[b]++
	}
	order, cycles := stableTopoSort(len(members), edges, indegree)
	if len(cycles) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("The collection's ordering rules between %s's own files form a cycle; lmm breaks it, and every other rule holds.", item.Name))
	}
	item.Ref.FomodOrder = make([]string, 0, len(order))
	for _, i := range order {
		item.Ref.FomodOrder = append(item.Ref.FomodOrder, members[i].FileID)
	}
}

// appendSorted inserts s into the sorted list unless it is there.
func appendSorted(list []string, s string) []string {
	i, found := slices.BinarySearch(list, s)
	if found {
		return list
	}
	return slices.Insert(list, i, s)
}

// ruleMatch says how a rule reference resolved.
type ruleMatch int

const (
	// ruleMatched: one item.
	ruleMatched ruleMatch = iota
	// ruleOutside: no item has the file or the logical name - a mod outside
	// the collection.
	ruleOutside
	// ruleUnmatched: items have the logical name, but the reference cannot
	// be matched to one of them - a version range lmm does not evaluate, a
	// version none of them has, or several files of that name.
	ruleUnmatched
)

// ruleResolver resolves a rule's reference to the item it names: by the
// file's md5 first (every rule of the first sample resolved so), then by
// logical file name - the one item of that name when the rule names no
// version or "*", else the item whose version is exactly the one named. A
// reference naming another version, a range, or a name several items share
// is not matched (ruleUnmatched): guessing would order the wrong mod.
func ruleResolver(items []*convItem) func(source.ManifestModRef) (*convItem, ruleMatch) {
	byMD5 := map[string]*convItem{}
	byLogical := map[string][]*convItem{}
	for _, it := range items {
		if it.MD5 != "" {
			if _, ok := byMD5[it.MD5]; !ok {
				byMD5[it.MD5] = it
			}
		}
		if it.logical != "" {
			byLogical[it.logical] = append(byLogical[it.logical], it)
		}
	}
	return func(r source.ManifestModRef) (*convItem, ruleMatch) {
		if it, ok := byMD5[r.FileMD5]; ok && r.FileMD5 != "" {
			return it, ruleMatched
		}
		cands := byLogical[r.LogicalFileName]
		if r.LogicalFileName == "" || len(cands) == 0 {
			return nil, ruleOutside
		}
		v := strings.TrimSuffix(r.VersionMatch, "+prefer")
		if v == "" || v == "*" {
			if len(cands) == 1 {
				return cands[0], ruleMatched
			}
			return nil, ruleUnmatched
		}
		if strings.ContainsAny(v, "<>=~^|, ") {
			return nil, ruleUnmatched
		}
		for _, it := range cands {
			if it.Version == v {
				return it, ruleMatched
			}
		}
		return nil, ruleUnmatched
	}
}

// intHeap is a min-heap of entry positions.
type intHeap []int

func (h intHeap) Len() int           { return len(h) }
func (h intHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h intHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *intHeap) Push(x any)        { *h = append(*h, x.(int)) }
func (h *intHeap) Pop() any {
	old := *h
	n := old[len(old)-1]
	*h = old[:len(old)-1]
	return n
}

// stableTopoSort orders n nodes so every edge a->b puts a before b,
// otherwise keeping the nodes' own order (Kahn's algorithm, always taking
// the lowest-numbered ready node). When nothing is ready, every remaining
// node waits on another remaining one, so the remaining graph holds a cycle:
// its strongly connected components are found, one that no other remaining
// component points into is a cycle nothing else is holding up, and of those
// the one holding the lowest-numbered node is broken there - that node is
// released anyway. A node merely DOWNSTREAM of a cycle is never the one
// released, so its own rules still hold. cycles lists every break, as the
// cycle's remaining nodes in order: releasing one node of a cycle with a
// chord can leave a smaller cycle, which is broken (and listed) in turn.
func stableTopoSort(n int, edges [][]int, indegree []int) (order []int, cycles [][]int) {
	indegree = slices.Clone(indegree)
	done := make([]bool, n)
	h := &intHeap{}
	for i := 0; i < n; i++ {
		if indegree[i] == 0 {
			heap.Push(h, i)
		}
	}
	for len(order) < n {
		if h.Len() == 0 {
			comp := remainingComponents(n, edges, done)
			fed := map[int]bool{}
			for u := 0; u < n; u++ {
				if done[u] {
					continue
				}
				for _, v := range edges[u] {
					if !done[v] && comp[u] != comp[v] {
						fed[comp[v]] = true
					}
				}
			}
			release := -1
			for u := 0; u < n; u++ {
				if !done[u] && !fed[comp[u]] {
					release = u
					break
				}
			}
			// Every break is reported, a second one inside what is left of
			// the same cycle included: each is a rule lmm did not follow.
			var members []int
			for u := 0; u < n; u++ {
				if !done[u] && comp[u] == comp[release] {
					members = append(members, u)
				}
			}
			cycles = append(cycles, members)
			indegree[release] = 0
			heap.Push(h, release)
		}
		i := heap.Pop(h).(int)
		if done[i] {
			continue
		}
		done[i] = true
		order = append(order, i)
		for _, j := range edges[i] {
			indegree[j]--
			if indegree[j] == 0 && !done[j] {
				heap.Push(h, j)
			}
		}
	}
	return order, cycles
}

// remainingComponents labels each node not yet done with its strongly
// connected component among the nodes not yet done (Tarjan's algorithm);
// a done node is -1.
func remainingComponents(n int, edges [][]int, done []bool) []int {
	index := make([]int, n)
	low := make([]int, n)
	onStack := make([]bool, n)
	comp := make([]int, n)
	for i := range index {
		index[i], comp[i] = -1, -1
	}
	var stack []int
	next, label := 0, 0
	var visit func(u int)
	visit = func(u int) {
		index[u], low[u] = next, next
		next++
		stack = append(stack, u)
		onStack[u] = true
		for _, v := range edges[u] {
			if done[v] {
				continue
			}
			if index[v] < 0 {
				visit(v)
				low[u] = min(low[u], low[v])
			} else if onStack[v] {
				low[u] = min(low[u], index[v])
			}
		}
		if low[u] == index[u] {
			for {
				w := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				onStack[w] = false
				comp[w] = label
				if w == u {
					break
				}
			}
			label++
		}
	}
	for u := 0; u < n; u++ {
		if !done[u] && index[u] < 0 {
			visit(u)
		}
	}
	return comp
}

// recount sets Counts from Items' states.
func (c *ConvertedCollection) recount() {
	c.Counts = make(map[CollectionItemState]int)
	for _, it := range c.Items {
		c.Counts[it.State]++
	}
}
