package main

import (
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// version_display.go - the one place this CLI turns a mod's version-bearing
// fields into text a human reads. It is the twin of the SPA's
// internal/serve/spa/app/version.js, and exists for the same reason.
//
// Issue 269's approval note (docs/plans/2026-09-09-steam-workshop-design.md,
// "Approved with notes"): an EXTERNAL mod's domain.Mod.Version holds Steam's
// 19-digit content id - the item's version IDENTITY, and not a version
// anybody can read - so "no human-facing surface prints a 19-digit number as
// a version". The surface shows the item's revision date instead, and only
// `lmm mod show` and the SPA's mod page carry the manifest at all, labelled
// as such underneath.
//
// The rule was first written inline at the two surfaces that happened to be
// in scope (`lmm list`'s VERSION column, `lmm update`'s table) and five
// sibling surfaces went on printing the raw field: `mod show`'s header and
// its Lock line, `list -v`'s LOCKED column, `mod lock`'s two lines and
// `mod set-update --pin`'s. Each had a perfectly good reason not to share the
// first two's code - a different document, a lock target rather than an
// installed version - which is exactly why the rule needs functions every
// surface calls rather than a branch two of them happen to carry.

// workshopRevisionDate formats a Workshop item's revision timestamp as a
// plain date. Empty for a record that carries none.
//
// UTC, not local: `lmm mod show` renders the same instant off a .UTC() time
// and the SPA renders it through toISOString, so a local-time date here
// would show one item as two different dates on two surfaces for any user
// east of about UTC+11:30 (review Minor 7 - which is also why the test
// asserting "2025-12-03" only passed in the reviewer's timezone).
func workshopRevisionDate(unix int64) string {
	if unix <= 0 {
		return ""
	}
	return time.Unix(unix, 0).UTC().Format("2006-01-02")
}

// displayRevision words an external item's revision the way `lmm mod show`'s
// two version-bearing lines do: "revision of 2025-12-03", or "revision of
// unknown" for a row installed before lmm recorded the date. Both lines read
// one rule so the header and the Installed: line can never disagree.
func displayRevision(updatedAt time.Time) string {
	if d := workshopRevisionDate(updatedAt.Unix()); d != "" {
		return "revision of " + d
	}
	return "revision of unknown"
}

// sourceIsWorkshop reports whether sourceID's registered source is the
// workshop-capable one, by the same capability test core itself uses
// (Service.workshopSourceFor asks for source.WorkshopScanner rather than
// naming "steamworkshop", so core never imports the concrete package). The
// CLI needs it for one case core's own External flag cannot cover: a catalog
// document for an item the user has NOT adopted carries the content id in
// Version and has no installed row to carry External.
func sourceIsWorkshop(svc *core.Service, sourceID string) bool {
	src, err := svc.GetSource(sourceID)
	if err != nil {
		return false
	}
	_, ok := src.(source.WorkshopScanner)
	return ok
}
