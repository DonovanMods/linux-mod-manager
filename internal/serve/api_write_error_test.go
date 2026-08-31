package serve_test

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
)

// writeFailRecorder is an httptest.ResponseRecorder whose Write always
// fails - simulating a client that vanishes mid-response (the one failure
// core.EncodeJSON can actually report back to writeJSON, since headers are
// already sent by the time it runs).
type writeFailRecorder struct {
	*httptest.ResponseRecorder
}

func (writeFailRecorder) Write([]byte) (int, error) {
	return 0, errors.New("simulated write failure")
}

// TestServer_APIWriteJSON_LogsEncodeFailure is the task-5 gate review's
// Minor 5 fix: writeJSON discarded core.EncodeJSON's error
// (`_ = core.EncodeJSON(w, v)`), so a mid-stream write failure left a
// truncated 200 body with nothing logged. Headers are already sent by
// then, so logging is all that's available (cmd/lmm's emitJSON, by
// contrast, can still return the error to its caller). Asserts the
// failure reaches the server's logger.
func TestServer_APIWriteJSON_LogsEncodeFailure(t *testing.T) {
	src := newFakeSource("fake")
	svc, _ := newFixtureServiceWithSource(t, src)

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	srv := serve.New(svc, logger, serve.Options{Addr: testAddr})

	req := httptest.NewRequest(http.MethodGet, "http://"+testAddr+"/api/v1/status", nil)
	rec := writeFailRecorder{httptest.NewRecorder()}
	srv.Handler().ServeHTTP(rec, req)

	assert.Contains(t, logBuf.String(), "simulated write failure", "a mid-stream encode/write failure must be logged, not silently discarded")
}
