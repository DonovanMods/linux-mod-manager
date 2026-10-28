package app

import (
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
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
	return finishSourceValidation(report, def, err)
}

// ValidateSourceContent is ValidateSourceFile for a definition that has no
// file yet: the web UI's source editor validates a draft the user is still
// typing, and writing it to a temp file first would be both pointless and a
// lie (the report's Path would name a file the user never created). It
// parses and validates the same bytes LoadSourceDefinitionFile would have
// read, through the same config.ParseSourceDefinition, so the two paths
// cannot drift on what "valid" means or on how a failure is worded.
//
// The returned report's Path is empty - the honest answer for content with
// no file - which is the only difference from ValidateSourceFile's document.
func ValidateSourceContent(data []byte) (*SourceValidationReport, source.SourceDefinition, error) {
	report := &SourceValidationReport{}
	def, err := config.ParseSourceDefinition(data)
	return finishSourceValidation(report, def, err)
}

// finishSourceValidation fills report in from one parse attempt's outcome -
// the half ValidateSourceFile and ValidateSourceContent share.
func finishSourceValidation(report *SourceValidationReport, def source.SourceDefinition, err error) (*SourceValidationReport, source.SourceDefinition, error) {
	if err != nil {
		report.Errors = []string{err.Error()}
		return report, source.SourceDefinition{}, err
	}
	report.ID = def.ID
	report.Type = def.Type
	report.Valid = true
	return report, def, nil
}
