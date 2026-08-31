package serve_test

import (
	"testing"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/serve"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIsLoopbackAddr covers the non-loopback-warning decision
// (docs/plans/2026-08-30-serve-design.md §Security: "a non-loopback bind
// prints a loud warning").
func TestIsLoopbackAddr(t *testing.T) {
	tests := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:7420", true},
		{"localhost:7420", true},
		{"[::1]:7420", true},
		{"0.0.0.0:7420", false},
		{":7420", false}, // wildcard host - binds every interface
		{"192.168.1.5:7420", false},
		{"example.com:7420", false},
	}
	for _, tt := range tests {
		t.Run(tt.addr, func(t *testing.T) {
			got, err := serve.IsLoopbackAddr(tt.addr)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestIsLoopbackAddr_MalformedAddr covers the error path: a Host:port that
// fails to parse is reported, not silently treated as either answer.
func TestIsLoopbackAddr_MalformedAddr(t *testing.T) {
	_, err := serve.IsLoopbackAddr("not-a-valid-addr")
	assert.Error(t, err)
}
