package serve

// Issue 583: the two slow Mission Control reads - the update check and the
// verify behind the Health card - stay plain GETs, and a caller that names
// a ?progress= token is told how far along each one is on the session
// activity stream, as read_progress frames carrying that token.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CheckUpdatesWithProgress makes the updates fixture a per-mod reporter, the
// shape a real one-request-per-mod source has (source.UpdateProgressReporter).
func (s *updatesSource) CheckUpdatesWithProgress(ctx context.Context, installed []domain.InstalledMod, report source.UpdateProgressFunc) ([]domain.Update, error) {
	for i, mod := range installed {
		report(i+1, len(installed), mod.Name)
		s.raiseNotice(ctx)
	}
	return s.CheckUpdates(ctx, installed)
}

// readProgressFrames drains every read_progress frame already published to
// live - the GET has returned by the time this runs, and publishing happens
// on the request's own goroutine, so nothing is still on its way.
func readProgressFrames(live <-chan activityEvent) []readProgressFrame {
	var frames []readProgressFrame
	for {
		select {
		case ev, ok := <-live:
			if !ok {
				return frames
			}
			if ev.Name == activityReadProgressEvent {
				frames = append(frames, ev.Payload.(readProgressFrame))
			}
		default:
			return frames
		}
	}
}

func TestAPIUpdates_ProgressTokenPublishesReadProgress(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)
	_, live, cancel := s.jobs.watch(64)
	defer cancel()

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/updates", game)+"&progress=tok-1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	frames := readProgressFrames(live)
	require.Len(t, frames, len(updatableModIDs), "one frame per mod checked")
	for i, f := range frames {
		assert.Equal(t, "tok-1", f.Token)
		assert.Equal(t, readUpdates, f.Read)
		assert.Equal(t, game.ID, f.GameID)
		assert.Equal(t, "default", f.Profile)
		assert.Equal(t, i+1, f.Index)
		assert.Equal(t, len(updatableModIDs), f.Total)
		assert.NotEmpty(t, f.ModName)
	}
}

func TestAPIUpdates_NoProgressTokenPublishesNothing(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)
	_, live, cancel := s.jobs.watch(64)
	defer cancel()

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/updates", game), "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, readProgressFrames(live), "progress is opt-in: a plain GET publishes nothing")
}

func TestAPIReads_BadProgressTokenIsRefused(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)
	for _, path := range []string{"/api/v1/updates", "/api/v1/health"} {
		for _, token := range []string{"has space", "semi;colon", string(make([]byte, 65))} {
			rec := doAPI(s, http.MethodGet, scoped(path, game)+"&progress="+url.QueryEscape(token), "")
			assert.Equal(t, http.StatusBadRequest, rec.Code, "%s progress=%q", path, token)
		}
	}
}

func TestAPIHealth_ProgressTokenPublishesReadProgressAndKeepsTheMemo(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)
	_, live, cancel := s.jobs.watch(64)
	defer cancel()

	// Re-verify: forced, so the tier really runs and reports as it goes.
	rec := doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&force=1&progress=h1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	frames := readProgressFrames(live)
	require.Len(t, frames, len(updatableModIDs), "one frame per mod verified")
	for i, f := range frames {
		assert.Equal(t, "h1", f.Token)
		assert.Equal(t, readHealth, f.Read)
		assert.Equal(t, i+1, f.Index)
		assert.Equal(t, len(updatableModIDs), f.Total)
	}

	// The ordinary hydrate, now asking to watch: the installation has not
	// changed, so the memo still answers it - asking for progress must not
	// cost the walk #336's memo exists to save.
	rec = doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&progress=h2", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var report core.VerifyReport
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
	assert.True(t, report.Result.Cached, "the memo answered")
	assert.Empty(t, readProgressFrames(live), "a memo hit has no run to report")
}

func TestReadProgressGate_CapsFramesPerRead(t *testing.T) {
	var g readProgressGate
	sent := 0
	for i := 1; i <= 1000; i++ {
		if g.admit(i, 1000) {
			sent++
		}
	}
	assert.LessOrEqual(t, sent, 101, "at most one frame per whole percent")
	assert.True(t, (&readProgressGate{}).admit(1, 1000), "the first tick always goes")

	var last readProgressGate
	for i := 1; i < 1000; i++ {
		last.admit(i, 1000)
	}
	assert.True(t, last.admit(1000, 1000), "the last tick always goes")
}

// throttleNotice is what a source's transport says while it waits out a
// rate limit (#671).
var throttleNotice = source.Notice{
	Kind: source.NoticeRetry, Source: "NexusMods", Reason: source.RetryRateLimited,
	Status: http.StatusTooManyRequests, Attempt: 2, MaxAttempts: 3, Wait: 30 * time.Second,
}

// TestAPIReads_AThrottleWaitIsSaidOnTheReadsProgress (#671): the Health
// card's verify and the update check are plain GETs, not jobs, so a source
// waiting out a rate limit under one went unsaid in the web UI. A read
// that names a progress token now carries the wait, in core's sentence, on
// a read_progress frame at the position the read has reached - and the
// next tick goes out whatever the gate says, so the line stops saying it
// once the read moves on.
func TestAPIReads_AThrottleWaitIsSaidOnTheReadsProgress(t *testing.T) {
	const said = "Rate limited by NexusMods; retrying in 30s (attempt 2 of 3)."
	for _, tc := range []struct {
		path, read, query string
	}{
		{"/api/v1/updates", readUpdates, "&progress=u1"},
		{"/api/v1/health", readHealth, "&force=1&progress=h1"},
	} {
		t.Run(tc.read, func(t *testing.T) {
			src := newUpdatesSource(t)
			notice := throttleNotice
			src.notice = &notice
			s, _, game := newUpdatesFixtureServerWith(t, src)
			_, live, cancel := s.jobs.watch(64)
			defer cancel()

			rec := doAPI(s, http.MethodGet, scoped(tc.path, game)+tc.query, "")
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			frames := readProgressFrames(live)
			var noticed []readProgressFrame
			at := -1
			for i, f := range frames {
				assert.Equal(t, tc.read, f.Read)
				if f.Notice != "" {
					noticed = append(noticed, f)
					at = i
				}
			}
			require.Len(t, noticed, 1, "the wait is said once")
			assert.Equal(t, said, noticed[0].Notice)
			assert.Equal(t, len(updatableModIDs), noticed[0].Total, "at the read's own position")
			assert.Positive(t, noticed[0].Index)
			require.Greater(t, len(frames), at+1, "a later tick follows it")
			assert.Empty(t, frames[at+1].Notice, "and clears it")
		})
	}
}

func TestAPIReads_NoProgressTokenNoNoticeFrames(t *testing.T) {
	src := newUpdatesSource(t)
	notice := throttleNotice
	src.notice = &notice
	s, _, game := newUpdatesFixtureServerWith(t, src)
	_, live, cancel := s.jobs.watch(64)
	defer cancel()

	rec := doAPI(s, http.MethodGet, scoped("/api/v1/health", game)+"&force=1", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Empty(t, readProgressFrames(live))
}

// TestReadReporter_ARepeatedNoticeIsNotSentAgain (#671 review): notice
// frames pass the gate, and a long hold refuses every remaining mod with
// the same sentence - about 700 frames on a 353-mod verify, each notice
// forcing the tick after it through too. A notice saying what the last
// one said is dropped, and forces nothing.
func TestReadReporter_ARepeatedNoticeIsNotSentAgain(t *testing.T) {
	s, _, game := newUpdatesFixtureServer(t)
	_, live, cancel := s.jobs.watch(2048)
	defer cancel()
	r := s.newReadReporter("tok", readHealth, selection{Game: game, Profile: "default"})
	sink := r.sink()
	ctx := r.watch(t.Context())

	// What the transport raises for each mod a held source refuses.
	until := time.Now().Add(4 * time.Minute)
	for i := 1; i <= 353; i++ {
		sink(core.VerifyEvent{Kind: core.VerifyEvProgress, Scope: core.Scope{Index: i, Total: 353, ModName: "m"}})
		source.Notify(ctx, source.Notice{Kind: source.NoticeSuspended, Source: "NexusMods", Until: until})
	}

	frames := readProgressFrames(live)
	notices := 0
	for _, f := range frames {
		if f.Notice != "" {
			notices++
		}
	}
	assert.LessOrEqual(t, notices, 3, "the same sentence is said once, not per mod")
	assert.LessOrEqual(t, len(frames), 110, "and the ticks keep to the gate's hundred or so")
}
