package app

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// SourceProbeResult is `lmm source validate --probe`'s live smoke-test
// outcome: OK plus Summary is app.ProbeSource's human-readable success
// text; OK false plus Error is the live operation's own failure message -
// distinct from Valid=false on the enclosing SourceValidationReport, which
// means the definition ITSELF is broken (a probe never runs against one).
type SourceProbeResult struct {
	OK      bool   `json:"ok"`
	Summary string `json:"summary,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SourceValidationReport is `lmm source validate <file> --json`'s document
// (#309): ID and Type are empty for a file that failed to parse (there is
// no definition to read them from); Errors holds the single load/validate
// failure message LoadSourceDefinitionFile returns (never more than one
// today - it is fail-fast, not a collector); Warnings exists for parity
// with other validation-style reports but nothing currently populates it.
// Probe is set only when --probe ran.
type SourceValidationReport struct {
	Path     string             `json:"path"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Valid    bool               `json:"valid"`
	Errors   []string           `json:"errors"`
	Warnings []string           `json:"warnings"`
	Probe    *SourceProbeResult `json:"probe,omitzero"`
}

// ValidateSourceFile parses and validates path's source definition
// (LoadSourceDefinitionFile), returning the resulting SourceValidationReport
// (#309), the parsed definition (the zero value when invalid, since there
// is nothing to return), and the raw load/validate error (nil when valid) -
// a caller that wraps it for the --json error envelope keeps errors.Is/As
// working through Unwrap.
func ValidateSourceFile(path string) (*SourceValidationReport, source.SourceDefinition, error) {
	report := &SourceValidationReport{Path: path}
	def, err := LoadSourceDefinitionFile(path)
	if err != nil {
		report.Errors = []string{err.Error()}
		return report, source.SourceDefinition{}, err
	}
	report.ID = def.ID
	report.Type = def.Type
	report.Valid = true
	return report, def, nil
}
