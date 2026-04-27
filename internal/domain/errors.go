package domain

import (
	"errors"
	"strings"
)

var (
	ErrModNotFound     = errors.New("mod not found")
	ErrGameNotFound    = errors.New("game not found")
	ErrProfileNotFound = errors.New("profile not found")
	ErrDependencyLoop  = errors.New("circular dependency detected")
	ErrAuthRequired    = errors.New("authentication required")
	ErrInvalidConfig   = errors.New("invalid configuration")
	ErrFileConflict    = errors.New("file conflict detected")
	ErrDownloadFailed  = errors.New("download failed")
	ErrLinkFailed      = errors.New("link operation failed")
)

// DeployError aggregates a primary failure with optional rollback / cleanup
// failures discovered while reacting to it. Each cause stays independently
// inspectable via errors.Is and errors.As — callers can branch on the original
// error even when a rollback or cleanup also failed.
type DeployError struct {
	// Op is a humanised "operation: subject" prefix shown before the primary
	// error message (e.g. "deploying foo.esp"). Empty Op suppresses the prefix.
	Op string
	// Primary is the root cause and must be non-nil.
	Primary error
	// Cleanup is set when an attempt to undo the failed operation itself failed.
	Cleanup error
	// Rollback is set when restoring previously-good state failed in addition
	// to the primary error.
	Rollback error
}

func (e *DeployError) Error() string {
	var b strings.Builder
	if e.Op != "" {
		b.WriteString(e.Op)
		b.WriteString(": ")
	}
	// Primary is documented as required, but Error() is on the formatting
	// path (logs, %v, fmt.Errorf chains) — so guard rather than panic if a
	// future caller forgets to set it.
	if e.Primary != nil {
		b.WriteString(e.Primary.Error())
	} else {
		b.WriteString("<nil primary>")
	}
	if e.Cleanup != nil {
		b.WriteString("; cleanup failed: ")
		b.WriteString(e.Cleanup.Error())
	}
	if e.Rollback != nil {
		b.WriteString("; rollback failed: ")
		b.WriteString(e.Rollback.Error())
	}
	return b.String()
}

// Unwrap returns every non-nil cause so errors.Is and errors.As can walk the
// full chain — primary first, then cleanup, then rollback. Nil entries are
// skipped so the slice never contains a dangling nil.
func (e *DeployError) Unwrap() []error {
	out := make([]error, 0, 3)
	if e.Primary != nil {
		out = append(out, e.Primary)
	}
	if e.Cleanup != nil {
		out = append(out, e.Cleanup)
	}
	if e.Rollback != nil {
		out = append(out, e.Rollback)
	}
	return out
}
