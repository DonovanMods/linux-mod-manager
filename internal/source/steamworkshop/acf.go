// Package steamworkshop is the built-in Steam Workshop mod source (#269).
//
// Tier 1 - what this package delivers today - is "adopt and track what
// Steam already downloaded": it reads the Steam client's own on-disk
// bookkeeping (steamapps/workshop/appworkshop_<appid>.acf, the same VDF
// dialect internal/source/steam already parses), reports each subscribed
// item as a mod lmm TRACKS but never deploys, and checks those items for
// updates through Valve's keyless GetPublishedFileDetails endpoint. lmm
// never links, copies, downloads, moves or removes a Workshop item's
// content; the Steam client owns those files where they sit and the game
// loads them from there.
//
// This file holds the ACF half: parsing an appworkshop manifest and
// scanning a set of Steam libraries for the items it declares. Nothing
// here touches the network.
package steamworkshop

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/steam"
)

// AppWorkshopItem is one entry of an appworkshop ACF's
// WorkshopItemsInstalled block: the Steam client's record of an item it has
// downloaded for this app. Manifest is the content id - the only stable,
// comparable version identity a Workshop item has - and may be absent on an
// older record, which is why TimeUpdated is kept as the secondary signal.
type AppWorkshopItem struct {
	FileID      string
	SizeOnDisk  int64
	TimeUpdated int64
	Manifest    string
}

// AppWorkshop is a parsed steamapps/workshop/appworkshop_<appid>.acf.
// Items is sorted by file id so a scan of the same library always reports
// the same order (VDF map iteration is not ordered).
type AppWorkshop struct {
	AppID      string
	SizeOnDisk int64
	Items      []AppWorkshopItem
}

// ParseAppWorkshop parses appworkshop_<appid>.acf content.
//
// An EMPTY-STUB manifest - WorkshopItemsInstalled {} , the shape Steam
// leaves behind for a workshop-capable app with nothing subscribed - parses
// cleanly to zero items; that is a normal state, not an error. A truncated
// or otherwise unreadable manifest IS an error: a silent empty scan there
// would read to the user as "you have nothing subscribed", which is a lie
// lmm must never tell about someone's library.
func ParseAppWorkshop(data string) (AppWorkshop, error) {
	if err := checkBalanced(data); err != nil {
		return AppWorkshop{}, fmt.Errorf("parsing appworkshop manifest: %w", err)
	}
	root, err := steam.ParseVDF(strings.NewReader(data))
	if err != nil {
		return AppWorkshop{}, fmt.Errorf("parsing appworkshop manifest: %w", err)
	}
	block, ok := root["AppWorkshop"].(steam.VDFMap)
	if !ok {
		return AppWorkshop{}, fmt.Errorf("appworkshop manifest: missing AppWorkshop block")
	}

	var aw AppWorkshop
	if v, ok := block["appid"].(string); ok {
		aw.AppID = v
	}
	aw.SizeOnDisk = atoi64(block["SizeOnDisk"])

	installed, ok := block["WorkshopItemsInstalled"].(steam.VDFMap)
	if !ok {
		// No block at all is the same fact as an empty one: nothing
		// subscribed. Steam writes the empty block, but an older or
		// hand-edited file need not.
		return aw, nil
	}
	for fileID, raw := range installed {
		entry, ok := raw.(steam.VDFMap)
		if !ok {
			continue
		}
		item := AppWorkshopItem{
			FileID:      fileID,
			SizeOnDisk:  atoi64(entry["size"]),
			TimeUpdated: atoi64(entry["timeupdated"]),
		}
		if v, ok := entry["manifest"].(string); ok {
			item.Manifest = v
		}
		aw.Items = append(aw.Items, item)
	}
	sort.Slice(aw.Items, func(i, j int) bool { return aw.Items[i].FileID < aw.Items[j].FileID })
	return aw, nil
}

// checkBalanced rejects a manifest whose braces do not close.
//
// steam.ParseVDF is deliberately lenient - it stops at end of input and
// returns whatever it managed to read - which is right for an appmanifest
// (where a missing tail costs one optional field) and wrong here: a
// half-written appworkshop file would parse into a SHORT item list, and a
// short list is indistinguishable from "you unsubscribed from the rest".
// Balancing the braces first is the cheapest way to tell truncation from a
// genuinely empty library, and it is checked here rather than inside
// ParseVDF so no existing caller's behaviour changes.
func checkBalanced(data string) error {
	depth := 0
	inQuote := false
	escaped := false
	for _, r := range data {
		switch {
		case escaped:
			escaped = false
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case inQuote:
			// Braces inside a quoted value are data, not structure.
		case r == '{':
			depth++
		case r == '}':
			depth--
			if depth < 0 {
				return fmt.Errorf("unbalanced braces: unexpected \"}\"")
			}
		}
	}
	if inQuote {
		return fmt.Errorf("unterminated quoted string")
	}
	if depth != 0 {
		return fmt.Errorf("unbalanced braces: %d block(s) left open", depth)
	}
	return nil
}

// atoi64 reads a VDF value as an int64, yielding 0 for anything that is not
// a decimal string. Every numeric field in an ACF is quoted, and a value
// lmm cannot read is a missing fact, never a parse failure.
func atoi64(v any) int64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// Scan is one ScanLibraries answer: the libraries that actually hold this
// app's workshop manifest, every item those manifests declare, and the
// non-fatal diagnostics collected on the way.
type Scan struct {
	Libraries []string
	Items     []domain.WorkshopItem
	Warnings  []string
}

// ManifestPath returns the appworkshop manifest path for appID inside a
// Steam library root.
func ManifestPath(library, appID string) string {
	return filepath.Join(library, "steamapps", "workshop", "appworkshop_"+appID+".acf")
}

// ContentPath returns the directory the Steam client installs one Workshop
// item into inside a Steam library root. This is the path lmm records as
// domain.InstalledMod.ExternalPath and never writes to.
func ContentPath(library, appID, fileID string) string {
	return filepath.Join(library, "steamapps", "workshop", "content", appID, fileID)
}

// ScanLibraries reads appID's workshop manifest from each library in turn
// and returns every item they declare, with each item's content directory
// resolved against the library that declared it.
//
// An item the manifest claims but disk no longer has is still reported:
// deciding what that means is `lmm verify`'s job (the design's presence
// tier), and omitting it here would make an unsubscribed-but-still-tracked
// item invisible instead of actionable. A manifest that will not parse is a
// warning against that one file, not a failed scan - one damaged ACF must
// not hide every other library's items.
func ScanLibraries(libraries []string, appID string) (Scan, error) {
	if err := validateAppID(appID); err != nil {
		return Scan{}, err
	}
	var scan Scan
	seen := make(map[string]bool)
	for _, lib := range libraries {
		path := ManifestPath(lib, appID)
		data, err := os.ReadFile(path)
		if err != nil {
			if !os.IsNotExist(err) {
				scan.Warnings = append(scan.Warnings, fmt.Sprintf("%s: %v", path, err))
			}
			continue
		}
		aw, err := ParseAppWorkshop(string(data))
		if err != nil {
			scan.Warnings = append(scan.Warnings, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		scan.Libraries = append(scan.Libraries, lib)
		for _, it := range aw.Items {
			// A file id comes from the ACF's own map keys and is joined into
			// ContentPath (which becomes ExternalPath) and used as the DB mod
			// id, so it gets the same decimal check the app id gets. Only
			// reads ever happen against that path, but the asymmetry is not
			// worth keeping: skip the item and say so.
			if !isDecimal(it.FileID) {
				scan.Warnings = append(scan.Warnings,
					fmt.Sprintf("%s: skipping item %q: file id must be decimal digits", path, it.FileID))
				continue
			}
			if seen[it.FileID] {
				// The same item claimed by two libraries: keep the first,
				// which is the first library in the caller's search order.
				continue
			}
			seen[it.FileID] = true
			scan.Items = append(scan.Items, domain.WorkshopItem{
				FileID:      it.FileID,
				Path:        ContentPath(lib, appID, it.FileID),
				SizeOnDisk:  it.SizeOnDisk,
				Manifest:    it.Manifest,
				TimeUpdated: it.TimeUpdated,
			})
		}
	}
	sort.Slice(scan.Items, func(i, j int) bool { return scan.Items[i].FileID < scan.Items[j].FileID })
	return scan, nil
}

// validateAppID rejects anything that is not a plain decimal Steam app id.
// The value comes from games.yaml's sources map - user-editable config - and
// is joined into filesystem paths, so it gets the same single-path-segment
// treatment domain.ErrInvalidGameID gives a game id.
func validateAppID(appID string) error {
	if appID == "" {
		return fmt.Errorf("steam app id is required")
	}
	if !isDecimal(appID) {
		return fmt.Errorf("invalid steam app id %q: must be decimal digits", appID)
	}
	return nil
}

// isDecimal reports whether s is a non-empty run of decimal digits - the
// shape both a Steam app id and a published-file id always have, and the
// only shape either is allowed to have before it is joined into a path.
func isDecimal(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
