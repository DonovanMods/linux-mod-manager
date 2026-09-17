package main

// T3 review F2: `lmm import`'s scan mode and `lmm verify --fix` ran on
// cmd.Context() rather than the context withServiceOpts hands a command -
// the one that prints what a source is waiting on - so a throttled,
// suspended or cold index lookup during either was silent. These tests hand
// each command a context that prints notices and a cobra command whose own
// context does not, which is exactly the difference a regression would
// fall into.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// throttleNotice is the notice both fakes raise.
var throttleNotice = source.Notice{
	Kind: source.NoticeRetry, Source: "Thunderstore", Reason: source.RetryRateLimited,
	Attempt: 2, MaxAttempts: 3, Wait: 12 * time.Second,
}

const throttleLine = "Rate limited by Thunderstore; retrying in 12s (attempt 2 of 3)."

// bareCommand is a cobra command whose own context prints nothing.
func bareCommand() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	return cmd
}

func TestImportScan_SaysWhatTheLookupIsWaitingOn(t *testing.T) {
	svc, game := setupDoImportTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	game.DeployMode = domain.DeployCopy
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "AcmeMod-1.0.zip"), []byte("payload"), 0o644))

	src := newFakeMatchSource("acme-source")
	src.notice = &throttleNotice
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	importSkipMatch = false
	importDryRun = true

	var err error
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() error {
			err = doImport(withSourceNotices(context.Background()), bareCommand(), svc, game, nil)
			return nil
		})
	})
	require.NoError(t, err)
	assert.Contains(t, stderr, throttleLine, "the scan's lookup runs on the command's context")
}

// TestImportScan_ALookupThatFailedSaysSo: every source failing to answer
// used to be a --verbose line, so the mods just read "local" with nothing
// saying lmm never got to ask (review F2's second half).
func TestImportScan_ALookupThatFailedSaysSo(t *testing.T) {
	svc, game := setupDoImportTest(t)
	require.NoError(t, svc.SaveGame(context.Background(), game))
	game.DeployMode = domain.DeployCopy
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "AcmeMod-1.0.zip"), []byte("payload"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(game.ModPath, "OtherMod-2.0.zip"), []byte("payload"), 0o644))

	src := newFakeMatchSource("acme-source")
	src.searchErr = &source.RetryLaterError{Source: "Thunderstore", Until: time.Now().Add(5 * time.Minute), Reason: "suspended after repeated failures"}
	svc.RegisterSource(src)
	game.SourceIDs = map[string]string{"acme-source": "g1"}
	importSkipMatch = false
	importDryRun = true
	verbose = false

	out, err := captureStdoutErr(t, func() error {
		return doImport(withSourceNotices(context.Background()), bareCommand(), svc, game, nil)
	})
	require.NoError(t, err)
	assert.Contains(t, out, "AcmeMod-1.0.zip -> local (lookup failed)")
	assert.Contains(t, out, "OtherMod-2.0.zip -> local (lookup failed)")
	assert.Equal(t, 1, strings.Count(out, "not asking Thunderstore again until"),
		"the reason is said once, not once per mod: %q", out)
}

func TestVerifyFix_SaysWhatTheRedownloadIsWaitingOn(t *testing.T) {
	_, svc, game, src := setupDoVerifyRedownloadTest(t)
	src.notice = &throttleNotice

	var err error
	stderr := captureStderr(t, func() {
		_ = captureStdout(t, func() error {
			err = doVerify(withSourceNotices(context.Background()), svc, game, nil)
			return nil
		})
	})
	_ = err // the redownload itself fails (the fixture serves no file); the notice is the point
	assert.Contains(t, stderr, throttleLine, "verify --fix repairs on the command's context")
}

// TestSourceNotice_EndsAnOpenProgressLine is T3 review F12: a notice
// printed while a download bar was open landed on the end of the bar's
// line.
func TestSourceNotice_EndsAnOpenProgressLine(t *testing.T) {
	t.Cleanup(func() { progressLineOpen.Store(false); resetSourceNoticeState() })
	stdout, stderr, _ := captureStdoutAndStderr(t, func() error {
		printProgressLine("\r  [%s] %.1f%%", "=====     ", 50.0)
		printSourceNotice(core.WarningEvent{Message: throttleLine})
		printSourceNotice(core.WarningEvent{Message: throttleLine})
		return nil
	})
	assert.Equal(t, "\r  [=====     ] 50.0%\n", stdout, "the bar's line is ended once, before the first notice")
	assert.Equal(t, throttleLine+"\n"+throttleLine+"\n", stderr)
}

// TestSourceNotice_ARepeatedHoldIsSaidOnce: an import scan whose lookups a
// held host refuses one by one printed the same "Not asking ... until"
// line for each (the fix round's real-binary run). The same hold, said
// again with nothing in between, is not news.
func TestSourceNotice_ARepeatedHoldIsSaidOnce(t *testing.T) {
	t.Cleanup(resetSourceNoticeState)
	held := core.WarningEvent{Phase: core.SourceSuspended, Message: "Not asking Thunderstore again until 19:14:39 (in 5m0s)."}
	retry := core.WarningEvent{Phase: core.SourceRetrying, Message: throttleLine}
	_, stderr, _ := captureStdoutAndStderr(t, func() error {
		printSourceNotice(held)
		printSourceNotice(held)
		printSourceNotice(held)
		printSourceNotice(retry)
		printSourceNotice(retry)
		printSourceNotice(held)
		return nil
	})
	assert.Equal(t, held.Message+"\n"+throttleLine+"\n"+throttleLine+"\n"+held.Message+"\n", stderr,
		"a hold repeated back to back is said once; retries, each a wait of its own, are all said")
}
