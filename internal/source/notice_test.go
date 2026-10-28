package source_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNotify_ReachesTheObserverOnTheContext pins the seam #436 needs: a
// source deep inside a call reports a wait to whoever set up the context,
// without every signature between them growing a callback.
func TestNotify_ReachesTheObserverOnTheContext(t *testing.T) {
	var got []source.Notice
	ctx := source.WithNotices(t.Context(), func(n source.Notice) { got = append(got, n) })

	source.Notify(ctx, source.Notice{Kind: source.NoticeRetry, Source: "Thunderstore", Attempt: 2, MaxAttempts: 3, Wait: 12 * time.Second})

	require.Len(t, got, 1)
	assert.Equal(t, source.NoticeRetry, got[0].Kind)
	assert.Equal(t, 12*time.Second, got[0].Wait)
}

// TestNotify_WithoutAnObserverIsANoOp: most calls have nobody listening, and
// a source must be able to report unconditionally.
func TestNotify_WithoutAnObserverIsANoOp(t *testing.T) {
	assert.NotPanics(t, func() {
		source.Notify(t.Context(), source.Notice{Kind: source.NoticeRetry})
		source.Notify(context.Background(), source.Notice{Kind: source.NoticeSuspended})
	})
}

// TestWithNotices_TheInnermostObserverWins: a job's own observer replaces
// the process-wide one for everything under it, so a notice is reported
// exactly once, to the most specific listener.
func TestWithNotices_TheInnermostObserverWins(t *testing.T) {
	var outer, inner int
	ctx := source.WithNotices(t.Context(), func(source.Notice) { outer++ })
	ctx = source.WithNotices(ctx, func(source.Notice) { inner++ })

	source.Notify(ctx, source.Notice{Kind: source.NoticeRetry})

	assert.Equal(t, 0, outer)
	assert.Equal(t, 1, inner)
}

// TestWithNotices_NilObserverSilencesTheOuterOne: nil is an explicit "nobody
// here", which is how a caller that reports progress its own way keeps an
// enclosing observer from printing the same thing twice.
func TestWithNotices_NilObserverSilencesTheOuterOne(t *testing.T) {
	var outer int
	ctx := source.WithNotices(t.Context(), func(source.Notice) { outer++ })
	ctx = source.WithNotices(ctx, nil)

	source.Notify(ctx, source.Notice{Kind: source.NoticeRetry})

	assert.Equal(t, 0, outer)
}

// TestRetryLaterError_CarriesWhenAndMatchesTheIndexSentinel: a refusal to
// even try names the moment it lifts, as data, and still classifies as the
// index being unavailable.
func TestRetryLaterError_CarriesWhenAndMatchesTheIndexSentinel(t *testing.T) {
	until := time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC) // far from any "now" a test runs at
	err := fmt.Errorf("fetching: %w", &source.RetryLaterError{
		Source: "Thunderstore", Until: until, Reason: "suspended after repeated failures",
	})

	var later *source.RetryLaterError
	require.True(t, errors.As(err, &later))
	assert.Equal(t, until, later.Until)
	assert.ErrorIs(t, err, source.ErrIndexUnavailable)
	assert.Contains(t, err.Error(), "not asking Thunderstore again until")
	assert.Contains(t, err.Error(), "suspended after repeated failures")
	assert.Contains(t, err.Error(), until.Local().Format("2006-01-02 15:04:05"), "a moment far from now carries its date, in local time")

	soon := time.Now().Add(5 * time.Minute)
	near := &source.RetryLaterError{Source: "Thunderstore", Until: soon, Reason: "x"}
	assert.Contains(t, near.Error(), "until "+soon.Local().Format("15:04:05")+": x", "a moment today is a clock time")
}
