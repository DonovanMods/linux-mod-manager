// manual_only_cleared.go is #627's serve half: a manual-only mod (#543)
// whose source says it serves the mod through its API again is cleared by
// core's update check, and that one check reports it
// (UpdateCheckReport.ManualOnlyCleared). The SPA re-reads the updates after
// every job, and an automatic update of the very mod may follow at once, so
// a notice carried by one report alone would flash and vanish. The server
// therefore remembers each one per game and profile, as it remembers #590's
// re-links (auto_relink.go), and puts it on every report until the user's
// explicit "check for updates" (?refresh=1) - or until the mod is recorded
// manual-only again, or is no longer installed, when the notice would be
// stale. Process-lifetime: a restart forgets them, and the database holds
// the truth (the record is gone).
package serve

import (
	"slices"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// manualOnlyNotices is the server's memory of the manual-only mods its
// update checks cleared. Its zero value is ready to use.
type manualOnlyNotices struct {
	mu sync.Mutex
	// byKey is, per autoUpdateKey, the mods cleared since the last explicit
	// check, in the order they were cleared.
	byKey map[string][]core.ManualOnlyCleared
}

// remember adds fresh - what this check cleared - to key's notices and
// returns them all. refresh, the user's explicit check, forgets the earlier
// ones first. An earlier notice whose mod installed (the rows the check ran
// against, read before it) shows as manual-only again, or no longer lists,
// is dropped; fresh ones are kept, though their rows still carry the mark
// the check has just cleared.
func (n *manualOnlyNotices) remember(key string, fresh []core.ManualOnlyCleared, installed []domain.InstalledMod, refresh bool) []core.ManualOnlyCleared {
	current := make(map[string]bool, len(installed))
	for _, m := range installed {
		k := domain.ModKey(m.SourceID, m.ID)
		current[k] = current[k] || !m.ManualOnly
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	var kept []core.ManualOnlyCleared
	if !refresh {
		kept = slices.DeleteFunc(slices.Clone(n.byKey[key]), func(c core.ManualOnlyCleared) bool {
			k := domain.ModKey(c.SourceID, c.ModID)
			return !current[k] || slices.ContainsFunc(fresh, func(f core.ManualOnlyCleared) bool {
				return domain.ModKey(f.SourceID, f.ModID) == k
			})
		})
	}
	kept = append(kept, fresh...)
	if len(kept) == 0 {
		delete(n.byKey, key)
		return nil
	}
	if n.byKey == nil {
		n.byKey = map[string][]core.ManualOnlyCleared{}
	}
	n.byKey[key] = kept
	return slices.Clone(kept)
}
