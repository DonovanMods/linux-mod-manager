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
	// than one set of recorded installer choices: lmm installs one
	// installer per mod.
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
	// CollectionFeaturePluginOrder is a plugin list or plugin rules (C6).
	CollectionFeaturePluginOrder = "plugin_order"
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
)

// ConvertedCollection is a source collection converted into an lmm
// collection document, with what the conversion found.
type ConvertedCollection struct {
	// SourceID is the source the collection lives on; Slug, Revision, Name,
	// URL and Author identify it there.
	SourceID string
	Slug     string
	Revision int
	Name     string
	URL      string
	Author   string
	// Document is the version-1 collection document: what an import
	// installs.
	Document *domain.CollectionDocument
	// Items is every item of the collection, in the collection's own order,
	// with what the import does with it.
	Items []ConvertedCollectionItem
	// Features is every feature the collection raised, supported or not, in
	// name order; Unsupported the ones that block the import.
	Features    []string
	Unsupported []UnsupportedCollectionFeature
	// Tools are the curator's tool shortcuts: listed, never created or run.
	Tools []CollectionTool
	// Notes are what the conversion did that a person should know.
	Notes []string
	// ManifestError says why the recipe could not be read; empty when it
	// was.
	ManifestError string

	// pendingCollection holds the collection-level features the recipe
	// raised while its items were read, gated when the document is built.
	pendingCollection []string
}

// ConvertedCollectionItem is one item of a converted collection.
type ConvertedCollectionItem struct {
	// ID identifies the item within the collection - "<mod id>:<file id>"
	// for a mod file, "item:<n>" otherwise - and is what opting an optional
	// item in names.
	ID string
	// Name and Version are display facts; ModID and FileID the file on the
	// source.
	Name    string
	Version string
	ModID   string
	FileID  string
	// FileCategory is the source's category for the file (MAIN, OPTIONAL,
	// OLD_VERSION, ARCHIVED, ...), and URL its page.
	FileCategory string
	URL          string
	// State is what the import does with the item.
	State CollectionItemState
	// Optional, Phase and UpdatePolicy are the curator's.
	Optional     bool
	Phase        int
	UpdatePolicy string
	// ModType is the item's stated mod type, "" for the game's default.
	ModType string
	// Installer reports that the collection records installer choices for
	// the item.
	Installer bool
	// Instructions are the curator's words for the item.
	Instructions string
	// Features are the features the item raises that lmm cannot import.
	Features []string
	// MD5 and SizeBytes describe the curator's file.
	MD5       string
	SizeBytes int64
}

// CollectionTool is a curator's tool shortcut, listed for information.
type CollectionTool struct {
	Name string
	Exe  string
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
	// latest resolves an item whose update policy is "latest" to the file
	// to install instead of the curator's. nil, or ok false, pins the
	// curator's file, unlocked (the default until the owner rules on #607's
	// Q3).
	latest func(m source.ManifestMod) (fileID, version string, ok bool)
	// modTypeSupported reports whether the game routes a stated mod type;
	// nil supports none.
	modTypeSupported func(modType string) bool
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
func convertCollection(in convertInput) *ConvertedCollection {
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
	if in.manifest != nil {
		items = convertManifestItems(in, out)
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
		var extra []string
		if installers > 1 {
			extra = append(extra, CollectionFeatureFomodMultiFile)
		}
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
		if convertSupported[name] || supportedCollectionFeatures[name] {
			return true
		}
		if t, ok := strings.CutPrefix(name, CollectionFeatureModTypePrefix); ok {
			return in.modTypeSupported != nil && in.modTypeSupported(t)
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

	out.Document = buildCollectionDocument(in, out, items)
	for _, it := range items {
		out.Items = append(out.Items, it.ConvertedCollectionItem)
	}
	return out
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
	recipeFiles := map[string]bool{}
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
		if mod.ModType != "" && (in.modTypeSupported == nil || !in.modTypeSupported(mod.ModType)) {
			it.Features = append(it.Features, CollectionFeatureModTypePrefix+mod.ModType)
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
	if len(m.Plugins) > 0 || !m.PluginRules.IsZero() {
		collectionFeature(out, CollectionFeaturePluginOrder)
	}
	for _, lo := range m.FileLoadOrders {
		collectionFeature(out, CollectionFeatureFileLoadOrderPrefix+lo.ID)
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
		out.Notes = append(out.Notes, fmt.Sprintf("The collection's list and its recipe disagree on %d file(s); the recipe is followed.", missing))
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
// applied, and the external items.
func buildCollectionDocument(in convertInput, out *ConvertedCollection, items []*convItem) *domain.CollectionDocument {
	doc := &domain.CollectionDocument{
		FormatVersion: domain.CollectionFormatVersion,
		Name:          out.Name,
		GameID:        in.gameID,
		Game:          in.game,
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
	}
	var entries []*entry
	entryOf := map[*convItem]*entry{}
	var latestPinned, latestFallback []string
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
				e.item.Ref = domain.ModReference{SourceID: in.sourceID, ModID: it.ModID, Version: it.Version}
				entries = append(entries, e)
			}
			e.members = append(e.members, it)
			entryOf[it] = e
			e.phase = min(e.phase, it.Phase)
			fileID, file := it.FileID, domain.CollectionFileRef{ID: it.FileID, MD5: it.MD5, Size: it.SizeBytes}
			if it.UpdatePolicy == "latest" {
				resolved := false
				if in.latest != nil && in.manifest != nil {
					if fid, version, ok := in.latest(in.manifest.Mods[it.index]); ok && fid != "" {
						fileID, file, resolved = fid, domain.CollectionFileRef{ID: fid}, true
						if len(e.members) == 1 {
							e.item.Ref.Version = version
						}
					} else {
						latestFallback = append(latestFallback, it.Name)
					}
				}
				if !resolved {
					latestPinned = append(latestPinned, it.Name)
				}
			}
			e.item.Ref.FileIDs = append(e.item.Ref.FileIDs, fileID)
			e.item.Files = append(e.item.Files, file)
			if r := policyRank(it.UpdatePolicy); r > rank {
				rank = r
			}
			if it.Installer && len(it.choices) > 0 {
				e.item.Ref.Fomod = domain.FomodChoices{Steps: slices.Clone(it.choices)}
			}
		}
		if e != nil && rank == 2 && e.item.Ref.Version != "" {
			e.item.Ref.Locked = true
		}
	}
	if len(latestFallback) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("No newer file of the same logical file was found for %s, so the curator's file is installed.",
			strings.Join(latestFallback, ", ")))
	}
	if n := len(latestPinned) - len(latestFallback); n > 0 && in.latest == nil {
		out.Notes = append(out.Notes, fmt.Sprintf("%d item(s) follow their newest file upstream (update policy \"latest\"): lmm installs the file the curator pinned and leaves it unlocked, so an update can move it.", n))
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
		var outside, notInstalled, informational int
		informationalTypes := map[string]bool{}
		for _, r := range m.ModRules {
			switch r.Type {
			case "before", "after":
			case "requires", "recommends", "conflicts", "provides":
				informational++
				informationalTypes[r.Type] = true
				continue
			default:
				collectionFeature(out, CollectionFeatureManifestKeyPrefix+"modRules.type")
				continue
			}
			src, ref := resolve(r.Source), resolve(r.Reference)
			if src == nil || ref == nil {
				outside++
				continue
			}
			es, er := entryOf[src], entryOf[ref]
			if es == nil || er == nil {
				notInstalled++
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
		if len(m.ModRules) > 0 {
			out.Features = appendSorted(out.Features, CollectionFeatureModRules)
		}
	}
	order, cycle := stableTopoSort(len(entries), edges, indegree)
	if len(cycle) > 0 {
		names := make([]string, len(cycle))
		for i, n := range cycle {
			names[i] = entries[n].item.Name
		}
		out.Notes = append(out.Notes, fmt.Sprintf("The collection's ordering rules form a cycle among %s; those mods keep the collection's own order.",
			strings.Join(names, ", ")))
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

	requires := deriveCollectionRequires(doc)
	for _, e := range entries {
		for _, it := range e.members {
			if it.ModType != "" {
				requires = appendUnique(requires, CollectionFeatureModTypePrefix+it.ModType)
			}
		}
	}
	sort.Strings(requires)
	doc.Requires = requires
	return doc
}

// appendSorted inserts s into the sorted list unless it is there.
func appendSorted(list []string, s string) []string {
	i, found := slices.BinarySearch(list, s)
	if found {
		return list
	}
	return slices.Insert(list, i, s)
}

// ruleResolver resolves a rule's reference to the item it names: by the
// file's md5 first (every rule of the first sample resolved so), then by
// logical file name with a matching version (any version when the rule
// says "*" or names none, or when exactly one item has that logical name).
// A reference matching nothing is a mod outside the collection.
func ruleResolver(items []*convItem) func(source.ManifestModRef) *convItem {
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
	return func(r source.ManifestModRef) *convItem {
		if it, ok := byMD5[r.FileMD5]; ok && r.FileMD5 != "" {
			return it
		}
		if r.LogicalFileName == "" {
			return nil
		}
		cands := byLogical[r.LogicalFileName]
		v := strings.TrimSuffix(r.VersionMatch, "+prefer")
		if v != "" && v != "*" {
			for _, it := range cands {
				if it.Version == v {
					return it
				}
			}
		}
		if len(cands) == 1 {
			return cands[0]
		}
		return nil
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
// the lowest-numbered ready node). A cycle is broken by taking its
// lowest-numbered node anyway; the nodes taken so are returned as cycle.
func stableTopoSort(n int, edges [][]int, indegree []int) (order, cycle []int) {
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
			for i := 0; i < n; i++ {
				if !done[i] {
					cycle = append(cycle, i)
					indegree[i] = 0
					heap.Push(h, i)
					break
				}
			}
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
	return order, cycle
}
