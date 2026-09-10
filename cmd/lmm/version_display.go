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

// displayModVersion is the version text for one MOD - a core.ModListing row,
// a domain.InstalledMod, or anything else carrying the same three facts.
// (Not to be confused with computeDisplayVersion in root.go, which renders
// lmm's OWN build version for `lmm --version`.)
//
// For an EXTERNAL mod it is the item's revision date (domain.Mod.UpdatedAt,
// which the Workshop source stamps from Steam's own time_updated), falling
// back to the tables' own "-" when the row carries none: a blank cell under a
// heading reads as a rendering bug, while "-" says the document has no date,
// which is a real state. For every other mod it is the version verbatim,
// which is what every surface did before this rule existed.
func displayModVersion(external bool, version string, updatedAt time.Time) string {
	if !external {
		return version
	}
	if d := workshopRevisionDate(updatedAt.Unix()); d != "" {
		return d
	}
	return "-"
}

// displayUpdateTarget is displayModVersion's other half: the text for the version
// an update would move the mod TO.
//
// A Workshop update's target is another 19-digit content id, and lmm has no
// date for a revision it has not seen, so "newer" is the whole of what it can
// truthfully say about it - which is also all a user can act on, since Steam
// applies the update itself either way.
func displayUpdateTarget(external bool, newVersion string) string {
	if external {
		return "newer"
	}
	return newVersion
}

// displayLockTarget renders a lock or pin TARGET as a human reads it: "v1.2.3"
// for an ordinary mod, and the EMPTY STRING for an external one.
//
// Unlike displayModVersion there is no date to substitute here. A lock names one
// specific revision, and for a Workshop item that revision's only name is the
// content id - so the honest rendering is to say the mod is locked and stop,
// which is exactly what the SPA's modrows.js#lockedNote settled on. A caller
// wording a line words it without the target; a caller filling a table column
// says "yes".
func displayLockTarget(external bool, version string) string {
	if external {
		return ""
	}
	return "v" + version
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
