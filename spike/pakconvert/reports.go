package pakconvert

// GroundTruthReport is one dual-form mod's conversion-vs-author comparison,
// written to <corpus>/reports/<id>-groundtruth.json.
type GroundTruthReport struct {
	ModID         string
	ModName       string
	Verdict       string // "PASS" | "EXPLAINED" | "DIVERGED"
	Residuals     []Residual
	EmbeddedMatch string // "" (no embedded .EXMOD) | "match" | "mismatch: <detail>"
	StaleRows     int
	Findings      []Finding
}

// Residual is one row where our conversion and the author's exmodz produce
// different final table states, with its classification.
type Residual struct {
	Table  string
	Row    string
	Class  string // "stale-pak-change" | "exmodz-newer-than-pak" | "diverged"
	Detail string
}
