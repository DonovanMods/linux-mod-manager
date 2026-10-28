package source

import (
	"errors"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ModNotFoundError is how an update check reports ONE installed mod its
// source's catalog no longer has (#539) - deleted, delisted, or republished
// under a new ID. It is domain.ErrModNotFound to errors.Is, and names the
// mod to errors.As, so core can report the mod as gone (and keep checking
// the rest) without knowing anything about the source.
//
// A source returns one per vanished mod, errors.Join-ed with whatever else
// went wrong; wrapping the join in its own context is fine. core's update
// check takes these out of the error and reports them, so a check that
// failed only because mods were gone did not fail.
type ModNotFoundError struct {
	// ModID is the installed mod's id, exactly as the source was handed it.
	ModID string
	// Err is the source's own description of the miss; nil reads as a
	// generic "mod <id>: mod not found".
	Err error
}

// Error is the source's own message, or a generic one naming the mod.
func (e *ModNotFoundError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "mod " + e.ModID + ": " + domain.ErrModNotFound.Error()
}

// Unwrap exposes the source's own error.
func (e *ModNotFoundError) Unwrap() error { return e.Err }

// Is reports domain.ErrModNotFound whether or not the source's own error
// wraps it.
func (e *ModNotFoundError) Is(target error) bool { return target == domain.ErrModNotFound }

// SplitModNotFound takes every *ModNotFoundError out of err's tree - through
// errors.Join and %w wraps alike - and returns them in order, with what is
// left: nil when the not-found mods were all that went wrong, err itself
// when there were none.
//
// A wrap whose subtree held both kinds is replaced by what remains of its
// subtree: its own context described the mixed set (a "3 check(s) failed"
// count, say) and would misdescribe the remainder. A bare
// domain.ErrModNotFound that names no mod stays in the remainder - there is
// nothing to report it against.
func SplitModNotFound(err error) (missing []*ModNotFoundError, rest error) {
	if err == nil {
		return nil, nil
	}
	if nf, ok := err.(*ModNotFoundError); ok {
		return []*ModNotFoundError{nf}, nil
	}
	switch node := err.(type) {
	case interface{ Unwrap() []error }:
		var rests []error
		for _, child := range node.Unwrap() {
			m, r := SplitModNotFound(child)
			missing = append(missing, m...)
			if r != nil {
				rests = append(rests, r)
			}
		}
		if len(missing) == 0 {
			return nil, err
		}
		return missing, errors.Join(rests...)
	case interface{ Unwrap() error }:
		m, r := SplitModNotFound(node.Unwrap())
		if len(m) == 0 {
			return nil, err
		}
		return m, r
	}
	return nil, err
}
