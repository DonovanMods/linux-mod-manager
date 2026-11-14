// collection_update_merge.go is a collection update's three-way merge of
// the profile's plugin list and overrides (#613; owner ruling 2026-11-14):
// the profile's own (what it has now), the baseline (what the collection
// last applied, domain.Profile.CollectionBaseline) and the new version.
//
// The rule is the mod entries' rule. What the user did not change follows
// the collection; what the collection did not change stays the user's;
// where both changed it, it is a conflict the user's version wins unless the
// caller overrides it. The baseline is what tells the two apart: a plugin
// whose state matches the baseline is the collection's, one whose state
// differs is the user's edit. A profile with no baseline (imported before
// there was one, restored from a snapshot, or rewritten by an older lmm)
// merges against an empty one: nothing in it is the collection's to remove,
// so the user's own plugins and overrides stay, and where the profile and
// the new version differ on one they both have, that is a conflict the
// user wins by default.
package core

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"slices"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// Conflict keys a CollectionUpdateOptions.Override names, besides an
// entry's domain.ModKey.
const (
	// collectionPluginOrderKey resolves the plugin order's conflict.
	collectionPluginOrderKey = "plugins:order"
	// collectionPluginRulesKey resolves the plugin rules' conflict.
	collectionPluginRulesKey = "plugins:rules"
)

// collectionPluginKey is the conflict key of one plugin's enabled state.
func collectionPluginKey(name string) string { return "plugin:" + name }

// collectionOverrideKey is the conflict key of one override path.
func collectionOverrideKey(path string) string { return "override:" + path }

// overrideHashes is each override's content sha256, by path; nil for none.
func overrideHashes(overrides map[string][]byte) map[string]string {
	if len(overrides) == 0 {
		return nil
	}
	out := make(map[string]string, len(overrides))
	for path, content := range overrides {
		out[path] = contentHash(content)
	}
	return out
}

func contentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// pluginMerge is the plugin list an update leads to, and its conflicts.
type pluginMerge struct {
	result domain.ProfilePlugins
	// enabled names the plugins whose on/off state both sides changed;
	// moved the plugins both reordered (empty when the order does not
	// conflict); rules whether both changed the rules.
	enabled []string
	moved   []string
	rules   bool
	// added and dropped name the plugins the result gains and loses.
	added, dropped []string
}

// mergePlugins merges p (the profile's), b (the baseline; hasBase false
// merges against an empty one, every shared difference a conflict) and n
// (the new version's). override says which
// conflicts take the collection's side. n zero leaves p as it is.
func mergePlugins(p, b, n domain.ProfilePlugins, hasBase bool, override func(key string) bool) pluginMerge {
	if n.IsZero() {
		return pluginMerge{result: p.Clone()}
	}
	if !hasBase {
		b = domain.ProfilePlugins{}
	}
	type state struct {
		enabled bool
		ok      bool
	}
	index := func(list []domain.PluginEntry) map[string]state {
		out := make(map[string]state, len(list))
		for _, e := range list {
			if _, dup := out[pluginKey(e.Name)]; !dup {
				out[pluginKey(e.Name)] = state{e.Enabled, true}
			}
		}
		return out
	}
	pIdx, bIdx, nIdx := index(p.Order), index(b.Order), index(n.Order)
	var m pluginMerge

	// On/off, plugin by plugin.
	enabled := map[string]bool{}
	for _, e := range n.Order {
		k := pluginKey(e.Name)
		ps, inP := pIdx[k]
		if !inP {
			continue
		}
		ns, bs := nIdx[k], bIdx[k]
		switch {
		case ps.enabled == ns.enabled:
			enabled[k] = ps.enabled
		case bs.ok && ps.enabled == bs.enabled:
			enabled[k] = ns.enabled // only the collection changed it
		case bs.ok && ns.enabled == bs.enabled:
			enabled[k] = ps.enabled // only the user did
		default:
			if !slices.Contains(m.enabled, e.Name) {
				m.enabled = append(m.enabled, e.Name)
			}
			enabled[k] = ps.enabled
			if override(collectionPluginKey(e.Name)) {
				enabled[k] = ns.enabled
			}
		}
	}

	// The order of the plugins both lists have.
	common := func(list []domain.PluginEntry, keep func(string) bool) []string {
		var out []string
		seen := map[string]bool{}
		for _, e := range list {
			k := pluginKey(e.Name)
			if keep(k) && !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
		return out
	}
	inBoth := func(k string) bool { return pIdx[k].ok && nIdx[k].ok }
	pOrd, nOrd := common(p.Order, inBoth), common(n.Order, inBoth)
	fromN := true
	if !slices.Equal(pOrd, nOrd) {
		inAll := func(k string) bool { return inBoth(k) && bIdx[k].ok }
		bOrd := common(b.Order, inAll)
		// With no baseline there is no telling who moved what: both did.
		userMoved := !hasBase || !slices.Equal(common(p.Order, inAll), bOrd)
		collMoved := !hasBase || !slices.Equal(common(n.Order, inAll), bOrd)
		switch {
		case !userMoved:
		case !collMoved:
			fromN = false
		default:
			m.moved = movedPlugins(pOrd, nOrd, p.Order)
			fromN = override(collectionPluginOrderKey)
		}
	}

	// A plugin one list has and the other does not: the collection dropped
	// one the user left alone (gone), the user dropped one the collection
	// left alone (stays gone), or it is new on either side (kept).
	keepPOnly := func(e domain.PluginEntry) bool {
		bs := bIdx[pluginKey(e.Name)]
		return !bs.ok || bs.enabled != e.Enabled
	}
	keepNOnly := func(e domain.PluginEntry) bool {
		bs := bIdx[pluginKey(e.Name)]
		return !bs.ok || bs.enabled != e.Enabled
	}
	var order []domain.PluginEntry
	if fromN {
		order = weavePlugins(n.Order, p.Order, nIdx, keepNOnly, keepPOnly)
	} else {
		order = weavePlugins(p.Order, n.Order, pIdx, keepPOnly, keepNOnly)
	}
	for i := range order {
		if en, ok := enabled[pluginKey(order[i].Name)]; ok {
			order[i].Enabled = en
		}
	}
	m.result.Order = order

	// The rules, as one.
	switch {
	case reflect.DeepEqual(p.Rules, n.Rules), reflect.DeepEqual(p.Rules, b.Rules):
		m.result.Rules = n.Rules
	case reflect.DeepEqual(n.Rules, b.Rules):
		m.result.Rules = p.Rules
	default:
		m.rules = true
		m.result.Rules = p.Rules
		if override(collectionPluginRulesKey) {
			m.result.Rules = n.Rules
		}
	}

	rIdx := index(order)
	for _, e := range order {
		if !pIdx[pluginKey(e.Name)].ok {
			m.added = append(m.added, e.Name)
		}
	}
	for _, e := range p.Order {
		if !rIdx[pluginKey(e.Name)].ok && !slices.Contains(m.dropped, e.Name) {
			m.dropped = append(m.dropped, e.Name)
		}
	}
	return m
}

// weavePlugins is primary's plugins in primary's order, each kept when
// it is in both lists or keepPrimaryOnly says so, with the plugins only
// secondary has - when keepSecondaryOnly says so - each placed just after
// the plugin before it in secondary's order that the result has (first, when
// none is).
func weavePlugins[S any](primary, secondary []domain.PluginEntry, primaryIdx map[string]S, keepPrimaryOnly, keepSecondaryOnly func(domain.PluginEntry) bool) []domain.PluginEntry {
	secondaryIdx := map[string]bool{}
	for _, e := range secondary {
		secondaryIdx[pluginKey(e.Name)] = true
	}
	var base []domain.PluginEntry
	in := map[string]bool{}
	for _, e := range primary {
		k := pluginKey(e.Name)
		if in[k] || (!secondaryIdx[k] && !keepPrimaryOnly(e)) {
			continue
		}
		in[k] = true
		base = append(base, e)
	}
	after := map[string][]domain.PluginEntry{}
	anchor := ""
	for _, e := range secondary {
		k := pluginKey(e.Name)
		if in[k] {
			anchor = k
			continue
		}
		if _, inPrimary := primaryIdx[k]; inPrimary || !keepSecondaryOnly(e) {
			continue
		}
		in[k] = true
		after[anchor] = append(after[anchor], e)
	}
	out := slices.Clone(after[""])
	for _, e := range base {
		out = append(out, e)
		out = append(out, after[pluginKey(e.Name)]...)
	}
	return out
}

// movedPlugins names the plugins outside the longest run both orders keep
// in the same sequence: the ones either side moved. names gives each key
// its display name.
func movedPlugins(a, b []string, names []domain.PluginEntry) []string {
	// Longest common subsequence, O(len(a)*len(b)); a plugin list is at
	// most a few thousand names.
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else {
				dp[i][j] = max(dp[i+1][j], dp[i][j+1])
			}
		}
	}
	keep := map[string]bool{}
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			keep[a[i]] = true
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			i++
		default:
			j++
		}
	}
	display := map[string]string{}
	for _, e := range names {
		if _, ok := display[pluginKey(e.Name)]; !ok {
			display[pluginKey(e.Name)] = e.Name
		}
	}
	var out []string
	for _, k := range a {
		if !keep[k] {
			out = append(out, display[k])
		}
	}
	return out
}

// overrideMerge is the overrides an update leads to, its new baseline
// hashes, and what it did.
type overrideMerge struct {
	result   map[string][]byte
	baseline map[string]string
	// added and changed take the new version's content; removed are the
	// collection's own the new version drops; conflicts the paths both
	// sides changed; heldBack the restricted ones not applied.
	added, changed, removed, conflicts []string
	heldBack                           []RestrictedOverride
}

// mergeOverrides merges the profile's overrides p with the new version's
// n against the baseline hashes b (hasBase false merges against an empty
// one: the profile's overrides are all kept, and one the new version
// rewrites is a conflict).
// override says which conflicts take the collection's side; allowAny lets
// restricted overrides through.
func mergeOverrides(p map[string][]byte, b map[string]string, hasBase bool, n map[string]string, override func(string) bool, allowAny bool) overrideMerge {
	if !hasBase {
		b = nil
	}
	m := overrideMerge{result: map[string][]byte{}, baseline: map[string]string{}}
	for path, content := range p {
		m.result[path] = content
	}
	paths := make([]string, 0, len(n))
	for path := range n {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		want := []byte(n[path])
		wantHash := contentHash(want)
		have, inP := p[path]
		baseHash, inB := b[path]
		take := false
		switch {
		case inP && contentHash(have) == wantHash:
			m.baseline[path] = wantHash
			continue
		case (inB && inP && contentHash(have) == baseHash) || (!inB && !inP):
			take = true // only the collection changed it, or it is new
		case inB && wantHash == baseHash:
			// Only the user changed it (or removed it): theirs.
			m.baseline[path] = baseHash
			continue
		default:
			m.conflicts = append(m.conflicts, path)
			take = override(collectionOverrideKey(path))
		}
		if !take {
			m.baseline[path] = wantHash
			continue
		}
		if !allowAny {
			if reason := restrictedOverrideReason(path); reason != "" {
				m.heldBack = append(m.heldBack, RestrictedOverride{Path: path, Reason: reason})
				if inB {
					m.baseline[path] = baseHash
				}
				continue
			}
		}
		if inP {
			m.changed = append(m.changed, path)
		} else {
			m.added = append(m.added, path)
		}
		m.result[path] = want
		m.baseline[path] = wantHash
	}
	// The collection's own the new version no longer carries go, unless the
	// user changed them.
	for path, baseHash := range b {
		if _, inN := n[path]; inN {
			continue
		}
		if have, ok := p[path]; ok && contentHash(have) == baseHash {
			delete(m.result, path)
			m.removed = append(m.removed, path)
		}
	}
	sort.Strings(m.removed)
	if len(m.result) == 0 {
		m.result = nil
	}
	if len(m.baseline) == 0 {
		m.baseline = nil
	}
	return m
}

// mergeCollectionPlugins is updateTarget's plugin list and the baseline it
// records (its overrides filled by mergeCollectionOverrides).
func mergeCollectionPlugins(plan *CollectionUpdatePlan, base *domain.Profile, c updateChoices) (domain.ProfilePlugins, *domain.CollectionBaseline) {
	b, hasBase := baselineOf(base)
	m := mergePlugins(base.Plugins, b.Plugins, plan.doc.Plugins, hasBase, c.overrides)
	return m.result, &domain.CollectionBaseline{Plugins: plan.doc.Plugins.Clone(), LoadOrders: plan.doc.LoadOrders.Clone()}
}

// mergeCollectionOverrides is updateTarget's overrides and the restricted
// ones it held back; it records their baseline hashes in baseline.
func mergeCollectionOverrides(plan *CollectionUpdatePlan, base *domain.Profile, c updateChoices, baseline *domain.CollectionBaseline) (map[string][]byte, []RestrictedOverride) {
	b, hasBase := baselineOf(base)
	m := mergeOverrides(base.Overrides, b.Overrides, hasBase, plan.doc.Overrides, c.overrides, c.allowAny)
	baseline.Overrides = m.baseline
	return m.result, m.heldBack
}

// baselineOf is profile's baseline, and whether it has one.
func baselineOf(profile *domain.Profile) (domain.CollectionBaseline, bool) {
	if profile.CollectionBaseline == nil {
		return domain.CollectionBaseline{}, false
	}
	return *profile.CollectionBaseline, true
}

// collectionLoadOrderKey is the conflict key of one file load order's
// order (#620).
func collectionLoadOrderKey(id string) string { return "load_order:" + id }

// CollectionLoadOrderChange is what an update does to one of the profile's
// file load orders (#620): the files it adds and drops, whether it reorders
// the files both have, and - when the user and the collection both
// reordered them since the collection last applied it - the files moved,
// a conflict the user's order wins unless the apply overrides it
// ("load_order:<id>").
type CollectionLoadOrderChange struct {
	ID        string   `json:"id"`
	Added     []string `json:"added,omitempty"`
	Removed   []string `json:"removed,omitempty"`
	Reordered bool     `json:"reordered,omitzero"`
	Moved     []string `json:"moved,omitempty"`
}

// loadOrderEntries is names as plugin entries, so a load order merges the
// way a plugin list's order does (it has no on/off state).
func loadOrderEntries(names []string) domain.ProfilePlugins {
	out := domain.ProfilePlugins{}
	for _, name := range names {
		out.Order = append(out.Order, domain.PluginEntry{Name: name, Enabled: true})
	}
	return out
}

// mergeLoadOrders merges the profile's file load orders p with the new
// version's n against the baseline b, id by id, with the plugin list's
// order rule (mergePlugins); hasBase false merges against an empty base.
// An id the new version does not carry stays as the profile has it.
func mergeLoadOrders(p, b, n domain.FileLoadOrders, hasBase bool, override func(string) bool) (domain.FileLoadOrders, []CollectionLoadOrderChange) {
	result := p.Clone()
	var changes []CollectionLoadOrderChange
	for _, id := range n.IDs() {
		ov := func(k string) bool { return k == collectionPluginOrderKey && override(collectionLoadOrderKey(id)) }
		m := mergePlugins(loadOrderEntries(p[id]), loadOrderEntries(b[id]), loadOrderEntries(n[id]), hasBase, ov)
		names := make([]string, 0, len(m.result.Order))
		for _, e := range m.result.Order {
			names = append(names, e.Name)
		}
		if result == nil {
			result = domain.FileLoadOrders{}
		}
		result[id] = names
		if slices.Equal(names, p[id]) && len(m.moved) == 0 {
			continue
		}
		kept := map[string]bool{}
		for _, name := range names {
			kept[pluginKey(name)] = true
		}
		had := map[string]bool{}
		var before, after []string
		for _, name := range p[id] {
			had[pluginKey(name)] = true
			if kept[pluginKey(name)] {
				before = append(before, pluginKey(name))
			}
		}
		for _, name := range names {
			if had[pluginKey(name)] {
				after = append(after, pluginKey(name))
			}
		}
		changes = append(changes, CollectionLoadOrderChange{ID: id, Added: m.added, Removed: m.dropped,
			Reordered: !slices.Equal(before, after), Moved: m.moved})
	}
	if result.IsZero() {
		result = nil
	}
	return result, changes
}
