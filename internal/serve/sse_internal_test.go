package serve

// Internal (package serve) tests for the SSE framing layer
// (docs/plans/2026-08-30-serve-impl.md Task 7, design §"Jobs and SSE":
// "one JSON object per typed core event, event: set to the event type
// name; comment heartbeats every ~15s"). The framing is asserted on the
// raw bytes, because the bytes ARE the contract a browser's EventSource
// parses - a frame that is merely "valid JSON somewhere in the body" is
// not a frame.

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sampleStepEvent is the typed core event every framing test writes - a
// fully-populated StepEvent, so the frame pins a realistic payload rather
// than an all-omitempty skeleton.
func sampleStepEvent() core.StepEvent {
	return core.StepEvent{
		Scope: core.Scope{
			Op:      core.OpDeploy,
			Mod:     &domain.ModReference{SourceID: "fake", ModID: "m1"},
			ModName: "Mod One",
			Index:   1,
			Total:   2,
		},
		Phase:  core.DeployDeployed,
		Detail: "deploying",
	}
}

// TestSSEStream_SetsStreamingHeaders pins the response headers a stream
// must carry: the SSE media type, no caching anywhere on the path, and the
// explicit proxy-buffering opt-out (task-7 ruling: "Content-Type
// text/event-stream + X-Accel-Buffering: no").
func TestSSEStream_SetsStreamingHeaders(t *testing.T) {
	rec := httptest.NewRecorder()

	newSSEStream(rec)

	assert.Equal(t, sseContentType, rec.Header().Get("Content-Type"))
	assert.Equal(t, "no", rec.Header().Get("X-Accel-Buffering"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestSSEStream_EventFrameIsNamePlusOneLineJSON is the framing headline:
// `event: <core event type name>` then exactly one `data:` line holding
// core.MarshalEvent's frozen {"type","data"} envelope, then the blank line
// that ends the frame.
func TestSSEStream_EventFrameIsNamePlusOneLineJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	stream := newSSEStream(rec)

	require.NoError(t, stream.sendEvent(sampleStepEvent()))

	frame := rec.Body.String()
	lines := strings.Split(strings.TrimSuffix(frame, "\n\n"), "\n")
	require.Len(t, lines, 2, "a frame is exactly one event: line and one data: line, then a blank line")
	assert.Equal(t, "event: step", lines[0])
	require.True(t, strings.HasSuffix(frame, "\n\n"), "every frame ends with a blank line")

	payload := strings.TrimPrefix(lines[1], "data: ")
	require.NotEqual(t, lines[1], payload, "the payload line must be prefixed with `data: `")

	var envelope struct {
		Type string `json:"type"`
		Data struct {
			Op      string `json:"op"`
			ModName string `json:"mod_name"`
			Phase   string `json:"phase"`
			Detail  string `json:"detail"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal([]byte(payload), &envelope))
	assert.Equal(t, "step", envelope.Type)
	assert.Equal(t, "deploy", envelope.Data.Op)
	assert.Equal(t, "Mod One", envelope.Data.ModName)
	assert.Equal(t, core.DeployDeployed.String(), envelope.Data.Phase)
	assert.Equal(t, "deploying", envelope.Data.Detail)
}

// TestSSEStream_EventFramePayloadMatchesTheEventGolden proves the SSE
// payload is not a serve-local re-encoding: it is byte-identical to
// core.MarshalEvent, the envelope internal/core/testdata/events/*.golden
// already freezes.
func TestSSEStream_EventFramePayloadMatchesTheEventGolden(t *testing.T) {
	rec := httptest.NewRecorder()
	stream := newSSEStream(rec)

	ev := sampleStepEvent()
	require.NoError(t, stream.sendEvent(ev))

	want, err := core.MarshalEvent(ev)
	require.NoError(t, err)
	assert.Equal(t, "event: step\ndata: "+string(want)+"\n\n", rec.Body.String())
}

// TestSSEStream_CommentFrame pins the heartbeat's shape: an SSE comment
// (a line starting with ":"), which every EventSource silently discards
// while it keeps the connection - and any intermediary - awake.
func TestSSEStream_CommentFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	stream := newSSEStream(rec)

	require.NoError(t, stream.comment("heartbeat"))

	assert.Equal(t, ": heartbeat\n\n", rec.Body.String())
}

// TestSSEStream_DocumentFrame covers the one non-event frame the stream
// emits - the terminal "done" frame carrying the job status document - and
// pins that its payload stays on ONE line, so the frame parses the same way
// an event frame does.
func TestSSEStream_DocumentFrame(t *testing.T) {
	rec := httptest.NewRecorder()
	stream := newSSEStream(rec)

	require.NoError(t, stream.sendDocument(sseDoneEvent, jobStatus{ID: "abc", Kind: "deploy", State: jobSucceeded}))

	frame := rec.Body.String()
	assert.True(t, strings.HasPrefix(frame, "event: done\ndata: {"), "got %q", frame)
	assert.Equal(t, 1, strings.Count(strings.TrimSuffix(frame, "\n\n"), "\n"), "the document payload must be one line")
	assert.Contains(t, frame, `"state":"succeeded"`)
}
