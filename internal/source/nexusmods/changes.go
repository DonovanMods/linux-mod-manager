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
// lmm differs in one place: a mod whose last check is older than any
// listing reaches is not checked in full but asked about alone - its own
// updated_time, one request that still saves the file list (#672's first
// ask) - and checked in full only if that moved.
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
	// days, inside the month the longest period covers). An older check
	// is answered by the mod's own updated time instead.
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
// listing for every mod checked within listingReach, and the mod's own
// updated time for each one checked before that. A request that fails
// answers nothing for the mods it was for - each is then checked in full,
// which reports the failure - so the only error is the caller giving up.
func (n *NexusMods) UnchangedSince(ctx context.Context, gameID string, since map[string]time.Time) (map[string]time.Time, error) {
	now := n.client.clock()
	out := make(map[string]time.Time)
	var oldest time.Time
	var listed, alone []string
	for id, at := range since {
		if now.Sub(at)+changeSkew >= listingReach {
			alone = append(alone, id)
			continue
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

	for _, id := range alone {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		modID, err := strconv.Atoi(id)
		if err != nil {
			continue
		}
		data, err := n.client.GetMod(ctx, gameID, modID)
		if err != nil || data.UpdatedTime.IsZero() || goneStatuses[data.Status] ||
			!data.UpdatedTime.Before(since[id].Add(-changeSkew)) {
			continue
		}
		out[id] = now
	}
	return out, ctx.Err()
}
