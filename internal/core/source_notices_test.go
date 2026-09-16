package core_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoticeText is the sentence table (#436): written once, here, and
// printed verbatim by the CLI and shown on a web UI job.
func TestNoticeText(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.Local)
	tests := []struct {
		name   string
		notice source.Notice
		want   string
	}{
		{
			"rate limited",
			source.Notice{Kind: source.NoticeRetry, Source: "Thunderstore", Reason: source.RetryRateLimited, Status: 429, Attempt: 2, MaxAttempts: 3, Wait: 12*time.Second + 40*time.Millisecond},
			"Rate limited by Thunderstore; retrying in 12s (attempt 2 of 3).",
		},
		{
			"server error",
			source.Notice{Kind: source.NoticeRetry, Source: "Thunderstore", Reason: source.RetryServerError, Status: 503, Attempt: 3, MaxAttempts: 3, Wait: 1500 * time.Millisecond},
			"Thunderstore answered HTTP 503; retrying in 1.5s (attempt 3 of 3).",
		},
		{
			"network error",
			source.Notice{Kind: source.NoticeRetry, Source: "gcdn.thunderstore.io", Reason: source.RetryNetworkError, Attempt: 2, MaxAttempts: 3, Wait: 300 * time.Millisecond, Err: errors.New("connection reset")},
			"Could not reach gcdn.thunderstore.io; retrying in 300ms (attempt 2 of 3).",
		},
		{
			// T3 review F1: a stalled transfer REACHED the host, so it is
			// not "could not reach" it.
			"stalled",
			source.Notice{Kind: source.NoticeRetry, Source: "Thunderstore", Reason: source.RetryStalled, Attempt: 2, MaxAttempts: 3, Wait: 700 * time.Millisecond},
			"The transfer from Thunderstore stalled; retrying in 700ms (attempt 2 of 3).",
		},
		{
			"suspended",
			source.Notice{Kind: source.NoticeSuspended, Source: "Thunderstore", Until: now.Add(4*time.Minute + 30*time.Second)},
			"Not asking Thunderstore again until 12:04:30 (in 4m30s).",
		},
		{
			// T3 review F9: a hold on one community names it.
			"one community suspended",
			source.Notice{Kind: source.NoticeSuspended, Source: "Thunderstore", GameID: "broken-community", Until: now.Add(5 * time.Minute)},
			"Not asking Thunderstore about broken-community again until 12:05:00 (in 5m0s).",
		},
		{
			"suspended for a day",
			source.Notice{Kind: source.NoticeSuspended, Source: "Thunderstore", Until: now.Add(26 * time.Hour)},
			"Not asking Thunderstore again until " + now.Add(26*time.Hour).Format("2006-01-02 15:04:05") + " (in 26h0m0s).",
		},
		{
			"index building",
			source.Notice{Kind: source.NoticeIndexBuilding, Source: "Thunderstore", GameID: "lethal-company"},
			"Building the Thunderstore index for lethal-company (one-time)...",
		},
		{
			// T3 review F12: a build too quick to time said "in 0s".
			"index built at once",
			source.Notice{Kind: source.NoticeIndexBuilt, Source: "Thunderstore", GameID: "tiny", Packages: 13},
			"Indexed 13 packages.",
		},
		{
			"index built in milliseconds",
			source.Notice{Kind: source.NoticeIndexBuilt, Source: "Thunderstore", GameID: "tiny", Packages: 13, Elapsed: 42 * time.Millisecond},
			"Indexed 13 packages in 42ms.",
		},
		{
			"index built",
			source.Notice{Kind: source.NoticeIndexBuilt, Source: "Thunderstore", GameID: "lethal-company", Packages: 50707, Elapsed: 4123 * time.Millisecond},
			"Indexed 50707 packages in 4.1s.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, core.NoticeTextAtForTest(tt.notice, now))
		})
	}
}

// TestWithSourceNotices_TurnsNoticesIntoEvents: a retry and a suspension
// are warnings (the user must see why nothing is moving), an index build is
// a step, and each carries the sentence and the phase a frontend keys on.
func TestWithSourceNotices_TurnsNoticesIntoEvents(t *testing.T) {
	sink, got := core.RecordEvents()
	ctx := core.WithSourceNotices(t.Context(), sink)

	source.Notify(ctx, source.Notice{Kind: source.NoticeIndexBuilding, Source: "Thunderstore", GameID: "repo"})
	source.Notify(ctx, source.Notice{Kind: source.NoticeRetry, Source: "Thunderstore", Reason: source.RetryRateLimited, Attempt: 2, MaxAttempts: 3, Wait: 12 * time.Second})
	source.Notify(ctx, source.Notice{Kind: source.NoticeSuspended, Source: "Thunderstore", Until: time.Now().Add(time.Minute)})
	source.Notify(ctx, source.Notice{Kind: source.NoticeRetry, Source: "cdn.example", Download: true, Reason: source.RetryServerError, Status: 502, Attempt: 2, MaxAttempts: 3, Wait: time.Second})
	source.Notify(ctx, source.Notice{Kind: source.NoticeIndexBuilt, Source: "Thunderstore", GameID: "repo", Packages: 3})

	require.Len(t, *got, 5)

	building, ok := (*got)[0].(core.StepEvent)
	require.True(t, ok, "an index build is a step: %T", (*got)[0])
	assert.Equal(t, core.IndexRefreshStarted, building.Phase)
	assert.Equal(t, core.OpSourceIndex, building.Op)
	assert.Equal(t, "Building the Thunderstore index for repo (one-time)...", building.Detail)

	retry, ok := (*got)[1].(core.WarningEvent)
	require.True(t, ok, "a retry is a warning: %T", (*got)[1])
	assert.Equal(t, core.SourceRetrying, retry.Phase)
	assert.Equal(t, core.OpSourceIndex, retry.Op)
	assert.Equal(t, "Rate limited by Thunderstore; retrying in 12s (attempt 2 of 3).", retry.Message)

	suspended, ok := (*got)[2].(core.WarningEvent)
	require.True(t, ok)
	assert.Equal(t, core.SourceSuspended, suspended.Phase)
	assert.Contains(t, suspended.Message, "Not asking Thunderstore again until")

	download, ok := (*got)[3].(core.WarningEvent)
	require.True(t, ok)
	assert.Equal(t, core.OpDownload, download.Op, "a download's wait is the download's")

	built, ok := (*got)[4].(core.StepEvent)
	require.True(t, ok)
	assert.Equal(t, core.IndexRefreshDone, built.Phase)
}

// TestWithSourceNotices_SerialisesConcurrentNotices: an aggregate search
// fans out across sources, but an EventSink is only ever called from one
// goroutine at a time - the contract every sink is written against.
func TestWithSourceNotices_SerialisesConcurrentNotices(t *testing.T) {
	var inside, overlaps int
	var mu sync.Mutex
	sink := func(core.Event) {
		mu.Lock()
		inside++
		if inside > 1 {
			overlaps++
		}
		mu.Unlock()
		time.Sleep(time.Microsecond)
		mu.Lock()
		inside--
		mu.Unlock()
	}
	ctx := core.WithSourceNotices(t.Context(), sink)

	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 20 {
				source.Notify(ctx, source.Notice{Kind: source.NoticeRetry, Source: "x", Attempt: 2, MaxAttempts: 3})
			}
		})
	}
	wg.Wait()
	assert.Zero(t, overlaps)
}

// TestWithSourceNotices_NilSinkLeavesTheContextAlone: a flow run with no
// sink must not silence an observer set further out.
func TestWithSourceNotices_NilSinkLeavesTheContextAlone(t *testing.T) {
	var outer int
	ctx := source.WithNotices(context.Background(), func(source.Notice) { outer++ })
	ctx = core.WithSourceNotices(ctx, nil)
	source.Notify(ctx, source.Notice{Kind: source.NoticeRetry})
	assert.Equal(t, 1, outer)
}
