package serve

// Internal (package serve) tests for GET /api/v1/events - the ONE
// multiplexed job-lifecycle stream the activity tray follows for the whole
// session (docs/plans/2026-08-31-serve-spa-design.md §Jobs).
//
// The per-job stream (sse_events_internal_test.go) already covers framing,
// replay and flushing. What is new here, and what nothing else can catch:
// a late subscriber must be given the CURRENT set of jobs before it is
// given live ones (or the tray opens empty until something happens), a job
// must appear exactly once across that seam, and a download's per-read tick
// storm must not be forwarded verbatim to a stream that is open all session.

import (
	"bufio"
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nextFrame reads one complete SSE frame off a live stream and parses it,
// so a test can assert frame-by-frame in order rather than on a body that
// never ends (this stream has no terminal frame - it is the session's).
func nextFrame(t *testing.T, reader *bufio.Reader) sseFrame {
	t.Helper()
	var frame sseFrame
	for _, line := range readFrame(t, reader) {
		switch {
		case strings.HasPrefix(line, "event: "):
			frame.Event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			frame.Data = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, ": "):
			frame.Comment = strings.TrimPrefix(line, ": ")
		default:
			t.Fatalf("unparsable SSE line %q", line)
		}
	}
	return frame
}

// decodeFrame decodes a frame's payload strictly into v.
func decodeFrame(t *testing.T, frame sseFrame, v any) {
	t.Helper()
	require.NoError(t, json.Unmarshal([]byte(frame.Data), v, json.RejectUnknownMembers(true)),
		"frame %q payload must decode into its declared document", frame.Event)
}

// openActivityStream starts a real listening server for s and opens the
// multiplexed stream against it, returning the reader positioned at the
// first frame.
func openActivityStream(t *testing.T, s *Server) *bufio.Reader {
	t.Helper()
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	resp := getStream(t, srv, "/api/v1/events")
	t.Cleanup(func() { _ = resp.Body.Close() })
	require.Equal(t, sseContentType, resp.Header.Get("Content-Type"))
	return bufio.NewReader(resp.Body)
}

// TestAPIEvents_SnapshotFirstThenLiveLifecycle is the headline: the stream
// opens with the tray's current contents and then narrates every job from
// job_started through job_progress to job_done.
//
// It also stands in for the flush probe the per-job stream has: every frame
// below is read off a real socket, through securityHeaders -> hostCheck ->
// requestLogging, WHILE the job is still running - which cannot happen
// unless the flush reaches the wire through the whole chain.
func TestAPIEvents_SnapshotFirstThenLiveLifecycle(t *testing.T) {
	s, _ := newLiveFixtureServer(t)
	finished := runFinishedJob(t, s, "deploy", &core.DeployResult{Deployed: 1}, nil)

	reader := openActivityStream(t, s)

	snapshot := nextFrame(t, reader)
	require.Equal(t, activitySnapshotEvent, snapshot.Event, "a late subscriber is caught up before it is told anything new")
	var index jobsIndex
	decodeFrame(t, snapshot, &index)
	require.Len(t, index.Jobs, 1)
	assert.Equal(t, finished, index.Jobs[0].ID)

	proceed := make(chan struct{})
	id, err := s.jobs.Start("install", func(_ context.Context, sink core.EventSink) (any, error) {
		<-proceed
		sink(core.StepEvent{
			Scope:  core.Scope{Op: core.OpInstall, ModName: "Mod One", Index: 1, Total: 2},
			Phase:  core.InstallDeploying,
			Detail: "linking files",
		})
		return nil, nil
	})
	require.NoError(t, err)

	started := nextFrame(t, reader)
	require.Equal(t, activityStartedEvent, started.Event)
	var startedRow jobSummary
	decodeFrame(t, started, &startedRow)
	assert.Equal(t, id, startedRow.ID)
	assert.Equal(t, "install", startedRow.Kind)
	assert.Equal(t, jobRunning, startedRow.State)

	close(proceed)

	progress := nextFrame(t, reader)
	require.Equal(t, activityProgressEvent, progress.Event)
	var frame jobProgressFrame
	decodeFrame(t, progress, &frame)
	assert.Equal(t, id, frame.JobID, "every frame names the job it belongs to")
	assert.Equal(t, "install", frame.Kind)
	assert.Equal(t, "step", frame.Type)
	assert.Equal(t, "install", frame.Op)
	assert.Equal(t, core.InstallDeploying.String(), frame.Phase)
	assert.Equal(t, "linking files", frame.Detail)
	assert.Equal(t, "Mod One", frame.ModName)
	assert.Equal(t, 1, frame.Index)
	assert.Equal(t, 2, frame.Total)

	done := nextFrame(t, reader)
	require.Equal(t, activityDoneEvent, done.Event)
	var doneRow jobSummary
	decodeFrame(t, done, &doneRow)
	assert.Equal(t, id, doneRow.ID)
	assert.Equal(t, jobSucceeded, doneRow.State)
	assert.False(t, doneRow.EndedAt.IsZero())
}

// TestAPIEvents_FailedJobDoneFrameCarriesTheEnvelope: the tray offers a
// failure's next step in place, so the terminal frame has to carry the
// envelope rather than merely say "failed".
func TestAPIEvents_FailedJobDoneFrameCarriesTheEnvelope(t *testing.T) {
	s, _ := newLiveFixtureServer(t)
	reader := openActivityStream(t, s)
	require.Equal(t, activitySnapshotEvent, nextFrame(t, reader).Event)

	_, err := s.jobs.Start("install", func(context.Context, core.EventSink) (any, error) {
		return nil, &core.ConflictError{Conflicts: []core.Conflict{{
			RelativePath:    "Mods/a.pak",
			CurrentSourceID: "fake",
			CurrentModID:    "m9",
		}}}
	})
	require.NoError(t, err)

	require.Equal(t, activityStartedEvent, nextFrame(t, reader).Event)
	done := nextFrame(t, reader)
	require.Equal(t, activityDoneEvent, done.Event)
	var row jobSummary
	decodeFrame(t, done, &row)
	assert.Equal(t, jobFailed, row.State)
	require.NotNil(t, row.Error)
	assert.Contains(t, row.Error.Error, "file conflict detected")
	assert.NotNil(t, row.Error.Details)
}

// TestAPIEvents_DownloadTicksAreCoalescedToWholePercent is the one that
// stops this endpoint being a firehose. internal/core's progressReader
// emits a DownloadEvent on EVERY non-empty read (downloader.go), which is
// thousands of events for one large mod. The per-job stream carries them
// all - that is what a viewer watching a download wants - but this stream
// is open for the whole session, so it forwards a download tick only when
// its WHOLE percent changes. TotalBytes is set (a known Content-Length) so
// the gate takes this path rather than its byte-delta fallback
// (TestAPIEvents_ChunkedDownloadsCoalesceByByteDelta, below).
func TestAPIEvents_DownloadTicksAreCoalescedToWholePercent(t *testing.T) {
	s, _ := newLiveFixtureServer(t)
	reader := openActivityStream(t, s)
	require.Equal(t, activitySnapshotEvent, nextFrame(t, reader).Event)

	percents := []float64{10, 10.4, 10.9, 11.2, 11.8, 12}
	_, err := s.jobs.Start("install", func(_ context.Context, sink core.EventSink) (any, error) {
		for _, pct := range percents {
			sink(core.DownloadEvent{
				Scope:      core.Scope{Op: core.OpDownload},
				Phase:      core.InstallDownloading,
				Percent:    pct,
				Downloaded: int64(pct * 1000),
				TotalBytes: 10000,
			})
		}
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, activityStartedEvent, nextFrame(t, reader).Event)

	var got []int
	for {
		frame := nextFrame(t, reader)
		if frame.Event == activityDoneEvent {
			break
		}
		require.Equal(t, activityProgressEvent, frame.Event)
		var payload jobProgressFrame
		decodeFrame(t, frame, &payload)
		assert.EqualValues(t, 10000, payload.TotalBytes)
		got = append(got, int(payload.Percent))
	}
	assert.Equal(t, []int{10, 11, 12}, got, "six ticks spanning three whole percents are three frames")
}

// TestAPIEvents_ChunkedDownloadsCoalesceByByteDelta is
// TestAPIEvents_DownloadTicksAreCoalescedToWholePercent's twin for the case
// the percent gate cannot see at all: a download with no Content-Length
// (chunked transfer-encoding) reports TotalBytes 0 for every tick
// (downloader.go's contract), so int(Percent) is always 0 and the percent
// gate would forward exactly one frame for the whole download
// (task-2-review.md Important 1). The byte-delta fallback forwards a tick
// once Downloaded has grown by activityByteDeltaThreshold since the last
// forwarded one, so jobProgressFrame's Downloaded field still moves. It
// also covers the gate's per-file reset: a second file's first tick is
// forwarded even though its own Downloaded count is far smaller than the
// first file's last forwarded one.
func TestAPIEvents_ChunkedDownloadsCoalesceByByteDelta(t *testing.T) {
	s, _ := newLiveFixtureServer(t)
	reader := openActivityStream(t, s)
	require.Equal(t, activitySnapshotEvent, nextFrame(t, reader).Event)

	const threshold = activityByteDeltaThreshold
	file1 := &domain.DownloadableFile{Name: "one.zip"}
	file2 := &domain.DownloadableFile{Name: "two.zip"}
	ticks := []struct {
		file       *domain.DownloadableFile
		modName    string
		downloaded int64
	}{
		{file1, "Mod One", 500_000},                       // first tick of the job: always forwarded
		{file1, "Mod One", 900_000},                       // +400,000: below the threshold, coalesced
		{file1, "Mod One", 500_000 + threshold + 100_000}, // crosses the threshold: forwarded
		{file1, "Mod One", 500_000 + threshold + 200_000}, // +100,000: coalesced
		{file2, "Mod Two", 50_000},                        // a second file: forwarded despite the small absolute count
	}
	_, err := s.jobs.Start("install", func(_ context.Context, sink core.EventSink) (any, error) {
		for _, tk := range ticks {
			sink(core.DownloadEvent{
				Scope:      core.Scope{Op: core.OpDownload, ModName: tk.modName},
				Phase:      core.InstallDownloading,
				File:       tk.file,
				Downloaded: tk.downloaded,
				TotalBytes: 0,
			})
		}
		return nil, nil
	})
	require.NoError(t, err)
	require.Equal(t, activityStartedEvent, nextFrame(t, reader).Event)

	var got []int64
	for {
		frame := nextFrame(t, reader)
		if frame.Event == activityDoneEvent {
			break
		}
		require.Equal(t, activityProgressEvent, frame.Event)
		var payload jobProgressFrame
		decodeFrame(t, frame, &payload)
		assert.Zero(t, payload.TotalBytes, "total size stays unknown for the whole download")
		assert.Zero(t, payload.Percent, "percent is meaningless when the total is unknown")
		got = append(got, payload.Downloaded)
	}
	assert.Equal(t, []int64{500_000, 500_000 + threshold + 100_000, 50_000}, got,
		"the first tick, the tick that crosses the byte-delta threshold, and the next file's first tick are forwarded")
}

// TestAPIEvents_HeartbeatOnTheInjectedClock: an idle session stream still
// has to look alive, and it runs on the same clock seam the per-job streams
// use so no assertion waits a real 15 seconds.
func TestAPIEvents_HeartbeatOnTheInjectedClock(t *testing.T) {
	s, _ := newLiveFixtureServer(t)
	ticks := make(chan time.Time)
	s.heartbeat = func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} }

	reader := openActivityStream(t, s)
	require.Equal(t, activitySnapshotEvent, nextFrame(t, reader).Event)

	ticks <- time.Now()
	assert.Equal(t, "heartbeat", nextFrame(t, reader).Comment)
}

// TestAPIEvents_ClientDropReleasesTheWatcher is the leak test: a closed tab
// must not leave a watcher attached to the registry for every future job to
// be written into.
func TestAPIEvents_ClientDropReleasesTheWatcher(t *testing.T) {
	s, _ := newLiveFixtureServer(t)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	require.NoError(t, err)
	resp, err := srv.Client().Do(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	nextFrame(t, bufio.NewReader(resp.Body))
	require.Equal(t, 1, s.jobs.watcherCount(), "the open stream is the registry's one watcher")

	cancel()
	_ = resp.Body.Close()

	requireEventually(t, func() bool { return s.jobs.watcherCount() == 0 },
		"the dropped client's watcher to be released")
}

// TestAPIEvents_DrainingServerClosesTheStream: this stream never ends on
// its own, so without watching the draining signal it would hold the entire
// shutdown grace open - the same hazard the per-job streams answer.
func TestAPIEvents_DrainingServerClosesTheStream(t *testing.T) {
	svc, _ := newDeployFixtureService(t)
	serveCtx, cancelServe := context.WithCancel(t.Context())
	s := New(t.Context(), svc, slog.New(slog.DiscardHandler),
		Options{Addr: "127.0.0.1:0", ShutdownGrace: 5 * time.Second})

	addr, err := s.Listen()
	require.NoError(t, err)
	served := make(chan error, 1)
	go func() { served <- s.Serve(serveCtx) }()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
		fmt.Sprintf("http://%s/api/v1/events", addr.String()), nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	reader := bufio.NewReader(resp.Body)
	require.Equal(t, activitySnapshotEvent, nextFrame(t, reader).Event)

	cancelServe()

	closed := make(chan error, 1)
	go func() {
		_, readErr := io.ReadAll(reader)
		closed <- readErr
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("the activity stream did not close when the server began draining")
	}

	select {
	case err := <-served:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
}

// TestActivityFrameNames_ArePinned: every test in this file refers to the
// four frame-name constants, never their literal strings, so renaming one
// is invisible to the whole suite - while the README, CLAUDE.md and Unit 3
// all depend on the literals themselves (task-2-review.md Minor 1). Unit 3
// consumes these names verbatim; changing any value here is a wire break.
func TestActivityFrameNames_ArePinned(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"snapshot", activitySnapshotEvent, "snapshot"},
		{"job_started", activityStartedEvent, "job_started"},
		{"job_progress", activityProgressEvent, "job_progress"},
		{"job_done", activityDoneEvent, "job_done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.value)
		})
	}
}

// TestActivityBus_LaggingWatcherIsDropped: publishing must never block a
// running Apply, so a watcher that cannot keep up is disconnected exactly
// the way a lagging per-job subscriber is - and the browser's EventSource
// reconnects into a fresh snapshot, which is why losing the backlog costs
// nothing here.
func TestActivityBus_LaggingWatcherIsDropped(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 50)

	_, live, cancel := r.watch(1)
	defer cancel()

	for i := range 4 {
		r.publish(activityEvent{Name: activityProgressEvent, Payload: jobProgressFrame{Index: i}})
	}

	// The first send fits the buffer; the next overflows it and closes the
	// channel, so draining reaches a closed channel rather than blocking.
	drained := 0
	for range live {
		drained++
	}
	assert.Equal(t, 1, drained, "one buffered frame, then the drop")
	assert.Equal(t, 0, r.watcherCount(), "a dropped watcher is unregistered, not merely closed")
}

// TestActivityBus_EveryJobAppearsExactlyOnceAcrossTheSeam is the reason
// watch takes its snapshot and registers its channel in ONE critical
// section. With twenty jobs starting while a watcher registers, every job
// must be either in the snapshot or in a job_started frame - never both,
// and never neither. Run under -race, with -count to shake the interleaving.
func TestActivityBus_EveryJobAppearsExactlyOnceAcrossTheSeam(t *testing.T) {
	r := newTestRegistry(t, t.Context(), 8, 100)

	const jobs = 20
	start := make(chan struct{})
	ids := make(chan jobID, jobs)
	var wg sync.WaitGroup
	for range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			id, err := r.Start("deploy", func(context.Context, core.EventSink) (any, error) {
				return nil, nil
			})
			if err == nil {
				ids <- id
			}
		}()
	}

	close(start)
	snapshot, live, cancel := r.watch(256)
	defer cancel()
	wg.Wait()
	close(ids)

	seen := map[jobID]int{}
	for _, row := range snapshot {
		seen[row.ID]++
	}
	deadline := time.After(10 * time.Second)
	for len(seen) < jobs {
		select {
		case ev, ok := <-live:
			require.True(t, ok, "the watcher was dropped before every job was seen")
			if ev.Name != activityStartedEvent {
				continue
			}
			seen[ev.Payload.(jobSummary).ID]++
		case <-deadline:
			t.Fatalf("saw only %d of %d jobs", len(seen), jobs)
		}
	}

	// The loop above stops the instant every job HAS an entry, not once
	// every queued frame has been read - a duplicate job_started for an
	// already-seen job that is still queued behind the loop's last NEW one
	// would otherwise never be read, and seen[id] == 2 would never be
	// observed (task-2-review.md Minor 3). Drain whatever is left,
	// non-blockingly, before the per-id assertions below.
drain:
	for {
		select {
		case ev, ok := <-live:
			if !ok {
				break drain
			}
			if ev.Name == activityStartedEvent {
				seen[ev.Payload.(jobSummary).ID]++
			}
		default:
			break drain
		}
	}

	for id := range ids {
		assert.Equal(t, 1, seen[id], "job %s must appear exactly once across the snapshot/live seam", id)
	}
}
