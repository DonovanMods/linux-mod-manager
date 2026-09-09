package core

// ItemFailure is one per-item failure inside a batch flow's result
// document: which mod failed, and why (#308).
//
// Counters alone ("failed: 3") are enough for a summary line and useless
// for anything else. The plain renderers print the detail live, from the
// event stream, at the point of occurrence - but `--json` suppresses events
// by design, so the detail simply did not exist on the wire. Every entry
// here is appended at exactly the point its flow bumps its Failed counter,
// and its Reason is the event Detail verbatim, so the document and the
// stream can never disagree about why something failed.
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
	// Reason is the failure text, verbatim from the event that reported it.
	Reason string `json:"reason"`
}
