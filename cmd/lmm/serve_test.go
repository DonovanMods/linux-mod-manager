package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServeCmd_FlagDefaults pins the documented defaults
// (docs/plans/2026-08-30-serve-design.md §Architecture: "--addr (default
// 127.0.0.1:7420), --no-open").
func TestServeCmd_FlagDefaults(t *testing.T) {
	addr := serveCmd.Flags().Lookup("addr")
	require.NotNil(t, addr)
	assert.Equal(t, "127.0.0.1:7420", addr.DefValue)

	noOpen := serveCmd.Flags().Lookup("no-open")
	require.NotNil(t, noOpen)
	assert.Equal(t, "false", noOpen.DefValue)
}

// TestRunServe_NonLoopbackAddr_PrintsWarningAndDrainsOnCancel covers both
// of Task 3's serve-command RED assertions at once: binding a non-loopback
// address prints the loud warning, and cancelling the command's context
// makes it return cleanly (the server's graceful-shutdown path) rather than
// hanging or erroring.
func TestRunServe_NonLoopbackAddr_PrintsWarningAndDrainsOnCancel(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	prevAddr, prevNoOpen := serveAddr, serveNoOpen
	serveAddr = "0.0.0.0:0"
	serveNoOpen = true
	t.Cleanup(func() { serveAddr, serveNoOpen = prevAddr, prevNoOpen })

	ready := make(chan struct{})
	prevReady := serveReady
	serveReady = func(string) { close(ready) }
	t.Cleanup(func() { serveReady = prevReady })

	ctx, cancel := context.WithCancel(context.Background())
	cmd := &cobra.Command{Use: "test"}
	cmd.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	done := make(chan error, 1)
	go func() { done <- runServe(cmd, nil) }()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server never became ready")
	}
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not return after ctx cancellation")
	}

	assert.Contains(t, stderr.String(), "WARNING")
	assert.Contains(t, stdout.String(), "lmm serve listening on")
}

// TestRunServe_WildcardAddr_ServesRequests proves task-3 review Important
// 1's fix at the full command level: before the fix, allowedHost was
// pinned to the resolved wildcard address (e.g. "[::]:44957") - a Host no
// real client ever sends - so a wildcard `--addr` 403'd every request,
// including this one, despite the loud warning claiming it was reachable.
func TestRunServe_WildcardAddr_ServesRequests(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	prevAddr, prevNoOpen := serveAddr, serveNoOpen
	serveAddr = "0.0.0.0:0"
	serveNoOpen = true
	t.Cleanup(func() { serveAddr, serveNoOpen = prevAddr, prevNoOpen })

	ready := make(chan string, 1)
	prevReady := serveReady
	serveReady = func(addr string) { ready <- addr }
	t.Cleanup(func() { serveReady = prevReady })

	ctx, cancel := context.WithCancel(context.Background())
	cmd := &cobra.Command{Use: "test"}
	cmd.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	done := make(chan error, 1)
	go func() { done <- runServe(cmd, nil) }()

	var addr string
	select {
	case addr = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server never became ready")
	}

	_, port, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	// /api/v1/status rather than "/": this test's subject is the Host
	// allow-list on a wildcard bind, not which frontend the root route
	// serves, so it probes an endpoint whose existence does not depend on
	// that (docs/plans/2026-08-31-serve-spa-design.md replaced the
	// server-rendered page layer with an SPA shell).
	resp, err := http.Get("http://127.0.0.1:" + port + "/api/v1/status")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not return after ctx cancellation")
	}
}

// TestRunServe_LoopbackAddr_NoWarning is the non-loopback test's inverse:
// the default (loopback) address prints no warning.
func TestRunServe_LoopbackAddr_NoWarning(t *testing.T) {
	configDir = t.TempDir()
	dataDir = t.TempDir()
	prevAddr, prevNoOpen := serveAddr, serveNoOpen
	serveAddr = "127.0.0.1:0"
	serveNoOpen = true
	t.Cleanup(func() { serveAddr, serveNoOpen = prevAddr, prevNoOpen })

	ready := make(chan struct{})
	prevReady := serveReady
	serveReady = func(string) { close(ready) }
	t.Cleanup(func() { serveReady = prevReady })

	ctx, cancel := context.WithCancel(context.Background())
	cmd := &cobra.Command{Use: "test"}
	cmd.SetContext(ctx)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	done := make(chan error, 1)
	go func() { done <- runServe(cmd, nil) }()

	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("server never became ready")
	}
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("runServe did not return after ctx cancellation")
	}

	assert.Empty(t, stderr.String())
}
