package main

import (
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/stretchr/testify/assert"
)

// TestReportError_JSON_OperationInProgressError pins the --json envelope
// for #317's cross-process refusal: the holder is DATA, so a script does
// not have to parse it back out of the sentence.
func TestReportError_JSON_OperationInProgressError(t *testing.T) {
	withJSONOutput(t)

	started := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	err := &core.OperationInProgressError{PID: 4242, StartedAt: started}
	out := captureStdout(t, func() error { reportError(err); return nil })

	assert.Equal(t, "{\n"+
		"  \"error\": \"another lmm operation is in progress (pid 4242, since 2026-09-09T12:00:00Z)\",\n"+
		"  \"details\": {\n"+
		"    \"pid\": 4242,\n"+
		"    \"started_at\": \"2026-09-09T12:00:00Z\"\n"+
		"  }\n"+
		"}\n", out)
}

// TestReportError_Plain_OperationInProgressError is the same refusal in
// the terminal: a sentence that names what to wait for.
func TestReportError_Plain_OperationInProgressError(t *testing.T) {
	started := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	err := &core.OperationInProgressError{PID: 4242, StartedAt: started}
	assert.Equal(t, "another lmm operation is in progress (pid 4242, since 2026-09-09T12:00:00Z)", err.Error())

	// An unreadable lock file leaves no holder to name, and the message
	// says only what it knows rather than claiming process 0.
	assert.Equal(t, "another lmm operation is in progress", (&core.OperationInProgressError{}).Error())
}
