package source_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// #539: a per-mod "not in the catalog" is domain.ErrModNotFound to any
// caller that only asks errors.Is, and names its mod to one that asks
// errors.As - whatever the source's own wording was.
func TestModNotFoundError(t *testing.T) {
	cause := errors.New("fetching mods/x: HTTP 404")
	err := fmt.Errorf("wrapped: %w", &source.ModNotFoundError{ModID: "x", Err: cause})

	if !errors.Is(err, domain.ErrModNotFound) {
		t.Errorf("errors.Is(%v, ErrModNotFound) = false", err)
	}
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(%v, cause) = false; the source's own error must stay reachable", err)
	}
	var nf *source.ModNotFoundError
	if !errors.As(err, &nf) || nf.ModID != "x" {
		t.Fatalf("errors.As = %v (%+v), want ModID x", errors.As(err, &nf), nf)
	}
	if got := nf.Error(); got != cause.Error() {
		t.Errorf("Error() = %q, want the source's own message %q", got, cause.Error())
	}
	if got := (&source.ModNotFoundError{ModID: "y"}).Error(); got != "mod y: mod not found" {
		t.Errorf("Error() with no cause = %q", got)
	}
}

// SplitModNotFound takes every per-mod not-found out of an error tree,
// through joins and wraps, and returns what is left - nil when nothing else
// went wrong.
func TestSplitModNotFound(t *testing.T) {
	a := &source.ModNotFoundError{ModID: "a"}
	b := &source.ModNotFoundError{ModID: "b"}
	other := errors.New("HTTP 500")

	t.Run("nil", func(t *testing.T) {
		missing, rest := source.SplitModNotFound(nil)
		if missing != nil || rest != nil {
			t.Errorf("SplitModNotFound(nil) = %v, %v", missing, rest)
		}
	})

	t.Run("only missing mods leaves no error", func(t *testing.T) {
		err := fmt.Errorf("source x: 2 failed: %w", errors.Join(a, b))
		missing, rest := source.SplitModNotFound(err)
		if len(missing) != 2 || missing[0].ModID != "a" || missing[1].ModID != "b" {
			t.Errorf("missing = %v, want a, b", missing)
		}
		if rest != nil {
			t.Errorf("rest = %v, want nil", rest)
		}
	})

	t.Run("mixed keeps the rest without the missing", func(t *testing.T) {
		err := fmt.Errorf("source x: 2 failed: %w", errors.Join(a, other))
		missing, rest := source.SplitModNotFound(err)
		if len(missing) != 1 || missing[0].ModID != "a" {
			t.Errorf("missing = %v, want a", missing)
		}
		if !errors.Is(rest, other) || errors.Is(rest, domain.ErrModNotFound) {
			t.Errorf("rest = %v, want only the other error", rest)
		}
	})

	t.Run("an unrelated error is returned as is", func(t *testing.T) {
		err := fmt.Errorf("ctx: %w", other)
		missing, rest := source.SplitModNotFound(err)
		if missing != nil || rest != err {
			t.Errorf("SplitModNotFound = %v, %v; want nil, the error itself", missing, rest)
		}
	})

	t.Run("a bare ErrModNotFound names no mod and stays an error", func(t *testing.T) {
		err := fmt.Errorf("lookup: %w", domain.ErrModNotFound)
		missing, rest := source.SplitModNotFound(err)
		if missing != nil || rest != err {
			t.Errorf("SplitModNotFound = %v, %v; want nil, the error itself", missing, rest)
		}
	})
}
