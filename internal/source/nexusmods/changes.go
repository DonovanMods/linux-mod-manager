// Package nexusmods: this file is the source's change signal (#672) - how
// NexusMods says which installed mods changed since lmm last checked them,
// so an update check or a verify fetches only those mods' file lists.
//
// # Where the numbers come from
//
// The v1 API lists every mod of a game that changed within a period,
// GET /v1/games/{game}/mods/updated.json?period=1d|1w|1m, each with its
// latest_file_update and latest_mod_activity (UNIX seconds). Vortex
// (Nexus-Mods/Vortex, src/renderer/src/extensions/nexus_integration/util.ts
// filterByUpdateList, and util/checkModsVersion.ts fetchRecentUpdates; read
// for facts only) does exactly this: one listing per game, the period the
// smallest of 1d/1w/1m that reaches back to the least recently checked mod,
// a mod's change time the larger of the two fields, a mod checked again
// when that time is after its last check, and every mod checked again
// regardless once its last check is 28 days old. It records the time of a
// check as five minutes before it was made, "since the list is cached and
// might be that outdated" - listingStaleness below.
//
// A mod whose last check is older than any listing reaches is checked in
// full, as Vortex does. Answering it from the mod's own updated_time
// instead (one request that would still save the file list) was tried and
// measured against the live API on 2026-11-22 (#672 review): across seven
// recently changed mods in two games updated_timestamp matched the newest
// file's upload every time, but did not follow the mod's later activity -
// one mod's listing showed activity 33 seconds after its upload that
// updated_timestamp never reflected - so it cannot vouch for a change that
// is not a file upload, such as the mod's version edited on its page.
package nexusmods

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	// listingStaleness is how far behind the moment it was asked
	// NexusMods' updated-mods listing may be (Vortex's five minutes): a
	// mod is vouched for only up to then.
	listingStaleness = 5 * time.Minute
	// changeSkew widens "changed after lmm's check" by the difference
	// between lmm's clock and NexusMods', in the safe direction: a change
	// this close before the check is still treated as after it.
	changeSkew = 10 * time.Minute
	// listingReach is the oldest check a listing answers for (Vortex's 28
	// days, inside the month the longest period covers). A mod checked
	// before that is checked in full.
	listingReach = 28 * 24 * time.Hour
)

// listingPeriods are the periods updated.json offers, shortest first, and
// how far back each reaches.
var listingPeriods = []struct {
	name  string
	reach time.Duration
}{
	{"1d", 24 * time.Hour},
	{"1w", 7 * 24 * time.Hour},
	{"1m", 30 * 24 * time.Hour},
}

// UpdatedMod is one row of the updated-mods listing.
type UpdatedMod struct {
	ModID             int   `json:"mod_id"`
	LatestFileUpdate  int64 `json:"latest_file_update"`
	LatestModActivity int64 `json:"latest_mod_activity"`
}

// changedAt is the later of the row's two times (Vortex's rule).
func (u UpdatedMod) changedAt() time.Time {
	return time.Unix(max(u.LatestFileUpdate, u.LatestModActivity), 0)
}

// GetUpdatedMods fetches the mods of gameDomain that changed within period
// ("1d", "1w" or "1m").
func (c *Client) GetUpdatedMods(ctx context.Context, gameDomain, period string) ([]UpdatedMod, error) {
	path := fmt.Sprintf("/v1/games/%s/mods/updated.json?period=%s", gameDomain, period)
	var rows []UpdatedMod
	if err := c.doRequest(ctx, http.MethodGet, path, &rows); err != nil {
		return nil, fmt.Errorf("listing updated mods: %w", err)
	}
	return rows, nil
}

// UnchangedSince implements source.ChangeTracker: the mods of gameID that
// have not changed since lmm last checked them, from one updated-mods
// listing for every mod checked within listingReach. A mod checked before
// that is not answered, and so is checked in full. A listing that fails
// answers nothing - each mod is then checked in full, which reports the
// failure - so the only error is the caller giving up.
func (n *NexusMods) UnchangedSince(ctx context.Context, gameID string, since map[string]time.Time) (map[string]time.Time, error) {
	now := n.client.clock()
	out := make(map[string]time.Time)
	var oldest time.Time
	var listed []string
	for id, at := range since {
		if now.Sub(at)+changeSkew >= listingReach {
			continue // too old for any listing: checked in full
		}
		listed = append(listed, id)
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}

	if len(listed) > 0 {
		age := now.Sub(oldest) + changeSkew
		period := listingPeriods[len(listingPeriods)-1].name
		for _, p := range listingPeriods {
			if age < p.reach {
				period = p.name
				break
			}
		}
		rows, err := n.client.GetUpdatedMods(ctx, gameID, period)
		if err == nil {
			changed := make(map[string]time.Time, len(rows))
			for _, row := range rows {
				changed[strconv.Itoa(row.ModID)] = row.changedAt()
			}
			vouched := now.Add(-listingStaleness)
			for _, id := range listed {
				if at, in := changed[id]; in && !at.Before(since[id].Add(-changeSkew)) {
					continue
				}
				out[id] = vouched
			}
		}
	}

	return out, ctx.Err()
}
