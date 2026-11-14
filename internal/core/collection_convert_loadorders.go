// collection_convert_loadorders.go maps a source recipe's file-based load
// orders (a Nexus recipe's fbLoadOrders) onto the profile's load_orders:
// list (#620; design docs/plans/2026-11-10-collections-design.md §3).
//
// Each order raises file_load_order:<id>, supported for a game whose
// adapter writes that order (adapter.FileLoadOrderer: Cyberpunk 2077's
// cyberpunk2077-archive). Only a supported order is carried into the
// document; any other blocks the import, as before. The names are the
// curator's, kept exactly as written - they are matched against the files
// deployed - and L1's checks (checkCollectionLoadOrders) refuse one that
// could not be.
package core

import (
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// ConvertedLoadOrder is what a conversion did with one of a recipe's file
// load orders: how many files the profile's list holds, and how many of
// them the recipe's own hint ties to one of the collection's mods.
type ConvertedLoadOrder struct {
	ID      string `json:"id"`
	Files   int    `json:"files"`
	Matched int    `json:"matched"`
}

// convertLoadOrders is m's file load orders the game supports, as the
// profile's load_orders: list, recording on out what each holds and which
// collection mod each entry belongs to (out.loadOrderMods, for the plan).
// Entries are compared case-insensitively, as the game compares them: a
// name listed twice keeps its first place. A blank name names no file and
// is skipped. A disabled entry is listed in place: the game's list cannot
// switch a file off, and Vortex's extension writes it too.
func convertLoadOrders(in convertInput, out *ConvertedCollection, items []*convItem) domain.FileLoadOrders {
	m := in.manifest
	if m == nil || in.loadOrders == nil {
		return nil
	}
	modNames := map[string]string{}
	for _, it := range items {
		if it.ModID != "" && it.kind == source.CollectionItemModFile {
			if _, seen := modNames[it.ModID]; !seen {
				modNames[it.ModID] = it.Name
			}
		}
	}
	var orders domain.FileLoadOrders
	for _, lo := range m.FileLoadOrders {
		if !in.loadOrders(lo.ID) {
			continue
		}
		seen := map[string]bool{}
		var names []string
		repeated, disabled := 0, 0
		mods := map[string]string{}
		for _, e := range lo.Files {
			if strings.TrimSpace(e.Name) == "" {
				continue
			}
			key := loadOrderKey(e.Name)
			if seen[key] {
				repeated++
				continue
			}
			seen[key] = true
			names = append(names, e.Name)
			if !e.Enabled {
				disabled++
			}
			if name, ok := hintedMod(e.ModHint, modNames); ok {
				mods[e.Name] = name
			}
		}
		if len(names) == 0 {
			continue
		}
		if orders == nil {
			orders = domain.FileLoadOrders{}
		}
		orders[lo.ID] = names
		out.LoadOrders = append(out.LoadOrders, ConvertedLoadOrder{ID: lo.ID, Files: len(names), Matched: len(mods)})
		if out.loadOrderMods == nil {
			out.loadOrderMods = map[string]map[string]string{}
		}
		out.loadOrderMods[lo.ID] = mods
		if repeated > 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("%d file(s) are listed more than once in the collection's %s load order (file names ignore case); each keeps its first place.", repeated, lo.ID))
		}
		if disabled > 0 {
			out.Notes = append(out.Notes, fmt.Sprintf("%d file(s) of the collection's %s load order are marked disabled; the game's list cannot switch a file off, so they are listed in place, as Vortex lists them.", disabled, lo.ID))
		}
	}
	return orders
}

// hintedMod is the name of the one collection mod whose id appears in hint
// as a number of its own (Vortex's "<name> <mod id> <version> ..." or
// "<version>-<mod id>-..."), and whether exactly one does. It is display
// data: a guess the plan shows, never a match anything depends on.
func hintedMod(hint string, modNames map[string]string) (string, bool) {
	var found string
	for _, tok := range strings.FieldsFunc(hint, func(r rune) bool { return r < '0' || r > '9' }) {
		name, ok := modNames[tok]
		if !ok {
			continue
		}
		if found != "" && found != name {
			return "", false
		}
		found = name
	}
	return found, found != ""
}
