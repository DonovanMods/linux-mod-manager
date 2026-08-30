package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitService_RegistersSources(t *testing.T) {
	// Use temp directories to avoid polluting real config
	configDir = t.TempDir()
	dataDir = t.TempDir()

	svc, err := initService(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, svc.Close())
	})

	// NexusMods should be registered by default
	src, err := svc.GetSource("nexusmods")
	require.NoError(t, err, "nexusmods source should be registered by default")
	assert.Equal(t, "nexusmods", src.ID())
	assert.Equal(t, "Nexus Mods", src.Name())

	// CurseForge should be registered by default
	src, err = svc.GetSource("curseforge")
	require.NoError(t, err, "curseforge source should be registered by default")
	assert.Equal(t, "curseforge", src.ID())
	assert.Equal(t, "CurseForge", src.Name())
}

// TestInitService_UsesFlagsNotEnvironment pins the CLI→app seam: the global
// --config/--data flag values are what app.Open receives, so XDG variables in
// the environment must not redirect a run that set the flags.
func TestInitService_UsesFlagsNotEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "should-not-be-used"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "should-not-be-used"))
	configDir = t.TempDir()
	dataDir = filepath.Join(t.TempDir(), "lmm")

	svc, err := initService(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })

	assert.Equal(t, configDir, svc.ConfigDir())
	_, err = os.Stat(filepath.Join(dataDir, "lmm.db"))
	assert.NoError(t, err, "the database must live under the --data directory")
}

// TestRunRoot_PropagatesContextCancellation pins the contract that the root command
// runs under the caller's context, so SIGINT and explicit cancellation reach RunE
// handlers via cmd.Context(). Regression guard against reverting to rootCmd.Execute().
func TestRunRoot_PropagatesContextCancellation(t *testing.T) {
	waitCmd := &cobra.Command{
		Use:    "internal-test-wait",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			<-cmd.Context().Done()
			return cmd.Context().Err()
		},
	}
	rootCmd.AddCommand(waitCmd)
	t.Cleanup(func() {
		rootCmd.RemoveCommand(waitCmd)
		rootCmd.SetArgs(nil)
		// ExecuteContext caches its ctx on the singleton (cobra only
		// defaults ctx when nil), so without this reset the cancelled
		// context above poisons every later bare Execute() call in the
		// test binary - surfaced as shuffle-order failures in tests
		// that drive context-sensitive paths.
		rootCmd.SetContext(context.Background())
	})
	rootCmd.SetArgs([]string{"internal-test-wait"})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := runRoot(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

// TestPrintCancelledNotice_PlainVsJSON pins Minor 3 of the Unit R final
// review (Ruling 16 addendum): Execute's context.Canceled/ErrCancelled exit
// path used to print nothing at all - exit code 2 with no line on stdout or
// stderr naming the cancellation, even though `lmm --help` documents 2 as
// "cancelled by the user". Plain mode now prints "Cancelled." to stderr;
// --json stays silent here, matching Ruling 15's "no extra text alongside a
// JSON contract" convention (the JSON contract itself carries no envelope
// for a cancellation exit, so --json emits nothing at all).
func TestPrintCancelledNotice_PlainVsJSON(t *testing.T) {
	t.Run("plain", func(t *testing.T) {
		var buf bytes.Buffer
		printCancelledNotice(&buf, false)
		assert.Equal(t, "Cancelled.\n", buf.String())
	})

	t.Run("json", func(t *testing.T) {
		var buf bytes.Buffer
		printCancelledNotice(&buf, true)
		assert.Empty(t, buf.String(), "--json must emit nothing extra on a cancellation exit")
	})
}

// TestRoot_LogLevel_InvalidErrorTextIsExactEverywhere pins Important #1 of
// the Task 1 review: an invalid --log-level must produce exactly
// newCLILogger's error text - no pflag *InvalidValueError wrapper prefix
// (`invalid argument %q for %q flag: `) and no `initializing service: `
// prefix - on every path that can reach flag parsing, whether or not that
// path would otherwise short-circuit past PersistentPreRunE (--help,
// --version, completion) or open a Service (game list). Also pins that the
// failure maps to Execute's exit-1 branch (not the exit-2 cancellation
// branch), that the printed text is byte-identical in both plain (stderr)
// and --json (stdout) mode, and - per the whole-branch review's Important #1
// - that the --json envelope is chosen regardless of whether --json appears
// before or after the bad --log-level on the command line: pflag's
// FlagSet.Parse stops at the first Set error, so a --json placed after the
// bad --log-level is never reached by ParseFlags itself, and
// logLevelFlagErrorFunc must fall back to scanning rawArgs to find it.
func TestRoot_LogLevel_InvalidErrorTextIsExactEverywhere(t *testing.T) {
	const wantErrText = `invalid --log-level "loud": expected off, error, warn, info, or debug`
	wantPlain := "Error: " + wantErrText + "\n"

	// wantJSON is reportError's actual --json rendering of wantErrText,
	// captured via the real code path: this test's concern is whether the
	// same error text reaches --json output regardless of flag order, not
	// emitJSON's byte-for-byte envelope framing (pinned separately in
	// jsonout_test.go).
	oldJSONForCapture := jsonOutput
	jsonOutput = true
	wantJSON := captureStdout(t, func() error { reportError(errors.New(wantErrText)); return nil })
	jsonOutput = oldJSONForCapture

	cases := []struct {
		name string
		args []string
		json bool
	}{
		{"game list plain", []string{"--log-level", "loud", "game", "list"}, false},
		{"game list --json", []string{"--json", "--log-level", "loud", "game", "list"}, true},
		// --log-level before --json: ParseFlags aborts on --log-level's Set
		// error before ever reaching --json, so jsonOutput must come from
		// logLevelFlagErrorFunc's rawArgs scan, not from --json's own Set.
		{"game list --log-level then --json", []string{"--log-level", "loud", "--json", "game", "list"}, true},
		// Flags after the subcommand: --json is parsed (and Set) before
		// --log-level errors, so this order already worked before the fix -
		// kept here as a regression guard against the fix narrowing scope.
		{"list --json --log-level (flags after subcommand)", []string{"list", "--json", "--log-level", "loud"}, true},
		{"--help", []string{"--log-level", "loud", "--help"}, false},
		{"--version", []string{"--log-level", "loud", "--version"}, false},
		{"completion bash", []string{"--log-level", "loud", "completion", "bash"}, false},
		{"game list --help", []string{"--log-level", "loud", "game", "list", "--help"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errb bytes.Buffer
			rootCmd.SetOut(&out)
			rootCmd.SetErr(&errb)
			oldJSON := jsonOutput
			oldRawArgs := rawArgs
			jsonOutput = false
			rawArgs = tc.args
			t.Cleanup(func() {
				rootCmd.SetOut(nil)
				rootCmd.SetErr(nil)
				logLevel = "off"
				jsonOutput = oldJSON
				rawArgs = oldRawArgs
			})
			rootCmd.SetArgs(tc.args)

			err := runRoot(context.Background())
			require.Error(t, err)
			assert.False(t, errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled),
				"a flag-parse failure must map to Execute's exit-1 branch, not the exit-2 cancellation branch")
			assert.EqualError(t, err, wantErrText)
			assert.Equal(t, tc.json, jsonOutput, "jsonOutput must reflect whether --json was parsed before the flag error")
			assert.NotContains(t, out.String(), "Usage:", "a flag-parse failure must not print cobra's usage/help text")

			if tc.json {
				printed := captureStdout(t, func() error { reportError(err); return nil })
				assert.Equal(t, wantJSON, printed)
			} else {
				printed, _ := captureStderrErr(t, func() error { reportError(err); return nil })
				assert.Equal(t, wantPlain, printed)
			}
		})
	}
}

// TestRoot_FlagErrorFunc_OnlyUnwrapsLogLevel pins that the pflag
// InvalidValueError unwrap is scoped to --log-level only: an invalid value
// for a different flag keeps cobra's default wrapped
// `invalid argument %q for %q flag: ...` text untouched.
func TestRoot_FlagErrorFunc_OnlyUnwrapsLogLevel(t *testing.T) {
	rootCmd.SetOut(io.Discard)
	rootCmd.SetErr(io.Discard)
	t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil) })
	rootCmd.SetArgs([]string{"--json=notabool", "game", "list"})

	err := runRoot(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid argument "notabool" for "--json" flag:`)
}
