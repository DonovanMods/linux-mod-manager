package core

import "errors"

// ItemFailure is one per-item failure inside a batch flow's result
// document: which mod failed, and why (#308).
//
// Counters alone ("failed: 3") are enough for a summary line and useless
// for anything else. The plain renderers print the detail live, from the
// event stream, at the point of occurrence - but `--json` suppresses events
// by design, so the detail simply did not exist on the wire. Every entry
// here is appended at exactly the point its flow bumps its Failed counter,
// carrying the same reason the event's Detail carries, so the document and
// the stream can never disagree about why something failed.
//
// A live-printing frontend must NOT print these as well, for the same
// reason it must not batch-print Warnings: every failure would appear
// twice. They are for callers that take the result and never watched the
// stream.
type ItemFailure struct {
	// SourceID/ModID identify the mod, when the flow knows them. An adopt
	// entry that matched no catalogue mod carries neither: there is no mod
	// to name, only the archive on disk.
	SourceID string `json:"source_id,omitempty"`
	ModID    string `json:"mod_id,omitempty"`
	// Name is what the flow's own plain line calls the item - the mod's
	// name where there is one, otherwise the scanned file name.
	Name string `json:"name,omitempty"`
	// Reason is the failure text the event that reported it carries.
	// Profile import's Detail IS this string; adopt's Detail is
	// "\u2717 <name>: <reason>", i.e. this string with the prefix that
	// Name now carries separately - the same two facts, split rather
	// than reworded.
	Reason string `json:"reason"`
	// Refusal is set when the failure is part of an install-shaped Apply
	// that refused before writing anything (#602): RefusalLoaderRequired
	// (or another refusal code) for an item that refused, RefusalHeldBack
	// for one that was fine but was not written because another item
	// refused. Empty for an ordinary per-item failure.
	Refusal string `json:"refusal,omitempty"`
}

// The refusal codes ItemFailure.Refusal and InstalledRef.Refusal carry
// (#602). A script tells "this mod needs something" from "this mod was only
// held back" by the code, never by the sentence.
const (
	// RefusalLoaderRequired: the item needs a mod loader the game does not
	// declare (*LoaderRequiredError).
	RefusalLoaderRequired = "loader_required"
	// RefusalHeldBack: the item acquired cleanly, but nothing was written
	// because another item refused.
	RefusalHeldBack = "held_back"
	// RefusalOther: the item refused for a reason with no code of its own.
	RefusalOther = "refused"
)

// refusalCode is the code an acquire refusal is reported under.
func refusalCode(err error) string {
	var loaderErr *LoaderRequiredError
	if errors.As(err, &loaderErr) {
		return RefusalLoaderRequired
	}
	return RefusalOther
}

// heldBackReason is the sentence a held-back item's failure carries.
const heldBackReason = "held back: another mod refused, so nothing was written"
