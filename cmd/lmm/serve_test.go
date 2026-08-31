package main

import (
	"bytes"
	"context"
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
