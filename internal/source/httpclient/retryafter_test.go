package httpclient_test

import (
	"testing"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
	"github.com/stretchr/testify/assert"
)

// TestRetryAfter_ReadsBothFormsWithinTheCeiling pins every edge the T3
// review's F8 found, each to its exact value: an overflowing delay-seconds
// was "no hint" (retried after 333 ms), a merely absurd one wrapped, and an
// HTTP-date was never capped at all.
func TestRetryAfter_ReadsBothFormsWithinTheCeiling(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	const ceiling = 24 * time.Hour
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"", 0},
		{"   ", 0},
		{"5", 5 * time.Second},
		{" 5 ", 5 * time.Second},
		{"0", 0},
		{"-1", 0},
		{"+5", 0},
		{"1.5", 0},
		{"soon", 0},
		{"86400", ceiling},
		{"86401", ceiling},
		{"99999999999999", ceiling},       // wraps a time.Duration if multiplied
		{"99999999999999999999", ceiling}, // overflows int64: past any ceiling, not "no hint"
		{"9223372036854775807", ceiling},  // math.MaxInt64
		{"Wed, 10 Sep 2026 12:00:30 GMT", 30 * time.Second},
		{"Wed, 10 Sep 2026 11:59:00 GMT", 0},       // already past
		{"Fri, 11 Sep 2026 12:00:01 GMT", ceiling}, // a second past the ceiling
		{"Fri, 01 Jan 2300 00:00:00 GMT", ceiling}, // saturates Sub
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, httpclient.RetryAfter(tt.header, now, ceiling), "Retry-After: %q", tt.header)
	}
}
