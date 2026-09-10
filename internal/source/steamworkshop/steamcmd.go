// Package steamworkshop: this file is Tier 3's Path B - an anonymous
// steamcmd shell-out for a Workshop item Valve serves no direct URL for.
//
// Every rule here comes from the #268 spike, and each one is a live trap it
// walked into:
//
//   - steamcmd is NEVER vendored and never auto-installed. It is probed at
//     call time with exec.LookPath, exactly as the 7z/rar extraction path
//     does (internal/core/extractor.go), and "not installed" is a
//     first-class answer with an install hint rather than a crash.
//   - +force_install_dir is ALWAYS pinned, and always BEFORE +login.
//     Without it, steamcmd finds the user's real Steam library and writes
//     into it.
//   - HOME (and the XDG variables under it) is isolated to lmm's own
//     directory, so the tool cannot discover the real install by any other
//     route. That home is PERSISTENT: a per-run one re-pays steamcmd's
//     ~200 MB self-bootstrap on every single download.
//   - Anonymous only. No password, no cached account session, no
//     credential of any kind ever reaches this code (the 2026-09-09
//     ruling).
package steamworkshop

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

const (
	// steamcmdTool is the binary lmm probes for and shells out to.
	steamcmdTool = "steamcmd"
	// outputTailLimit caps how much of a failed tool's combined output is
	// carried into the error, matching httpclient's errorBodyLimit
	// convention for an untrusted body.
	outputTailLimit = 4 * 1024
)

// steamcmdHeartbeat is how often lmm reports progress of its own.
// steamcmd's output is not a dependable progress stream, and a silent
// multi-gigabyte download is the worst possible readout, so bytes on disk
// are ticked out on a timer whether the tool says anything or not. It is a
// var, not a const, only so a test can shorten it (export_test.go).
var steamcmdHeartbeat = 15 * time.Second

// steamcmdTimeout bounds one download. Workshop items reach several
// gigabytes, so this is generous by design; it exists to stop a hung tool
// holding a mutation slot forever, not to police slow networks. A var, not
// a const, only so a test can shorten it (export_test.go).
var steamcmdTimeout = 30 * time.Minute

// steamcmdWaitDelay bounds cmd.Wait's I/O drain.
//
// cmd.Stdout is an *io.PipeWriter rather than an *os.File, so os/exec
// creates an OS pipe and a copying goroutine, and cmd.Wait does not return
// until that goroutine sees EOF; CommandContext's cancel function kills
// only the DIRECT child. steamcmd is a self-bootstrapping launcher that
// re-execs and spawns helpers, so a grandchild inheriting the write end
// would keep cmd.Run() blocked forever - past steamcmdTimeout, past a
// Ctrl-C, past `lmm serve`'s shutdown grace - while holding core's single
// mutation slot. WaitDelay is what stops that. A var only so a test can
// shorten it (export_test.go).
var steamcmdWaitDelay = 30 * time.Second

// The two results steamcmd names in its own download-item error line, and
// the only two failures lmm claims to understand.
//
// They are read off the OUTPUT rather than the exit code because steamcmd's
// exit status is not a reliable signal of either - it has been observed
// exiting 0 on a refused download. They are also read ONLY from the item's
// own line (see refusalLine): "(Failure)" is steamcmd's rendering of any
// generic k_EResultFail and appears on unrelated steps - a failed
// redistributable, a rejected depot manifest - where it means nothing about
// this download.
const (
	markerAnonymousRefused = "Failure"
	markerAccessDenied     = "Access Denied"
)

// refusalLine is the spike's exact observed line, e.g.
// `ERROR! Download item 3000000002 failed (Failure).` - anchored to the
// item's own download so a stray parenthetical elsewhere in a multi-megabyte
// log cannot be mistaken for a verdict on it.
var refusalLine = regexp.MustCompile(`ERROR!\s+Download item\s+\S+\s+failed\s+\(` +
	`(` + regexp.QuoteMeta(markerAnonymousRefused) + `|` + regexp.QuoteMeta(markerAccessDenied) + `)\)`)

// ReasonAnonymousRefused is what lmm tells a user whose download the
// publisher does not allow anonymously. It is the Tier-1 fallback the
// 2026-09-09 ruling requires: there is a way to get this item, and it runs
// through the Steam client, not through lmm.
const ReasonAnonymousRefused = "This game's publisher does not allow anonymous Workshop downloads. " +
	"Subscribe to the item in the Steam client, then run `lmm import --workshop` - lmm will track it in place."

// ReasonItemUnavailable explains steamcmd's other named refusal.
const ReasonItemUnavailable = "Steam will not serve this item: it is invalid, delisted, or not visible to you."

// ReasonSteamcmdMissing names the tool and where to get it. lmm does not
// install it: the self-bootstrap is ~200 MB, and a tool lmm installed is a
// tool the user cannot straightforwardly remove.
const ReasonSteamcmdMissing = "steamcmd is not installed. Install it from your distribution's packages or " +
	"https://developer.valvesoftware.com/wiki/SteamCMD, then retry."

// progressLine matches the only progress steamcmd reliably prints, e.g.
// ` Update state (0x61) downloading, progress: 78.90 (7890 / 10000)`.
var progressLine = regexp.MustCompile(`progress:\s*([0-9]+(?:\.[0-9]+)?)(?:\s*\((\d+)\s*/\s*(\d+)\))?`)

// Fetch implements source.Fetcher: it downloads one Workshop item into
// destDir with an anonymous steamcmd, and returns the directory the item
// landed in.
//
// mod.GameID is the Steam app id, per the Fetcher contract (core
// translates it). destDir is core's own staging directory, which is also
// what +force_install_dir is pinned to, so steamcmd writes there and
// nowhere else; the item lands at
// <destDir>/steamapps/workshop/content/<appid>/<fileid>/.
func (s *Source) Fetch(ctx context.Context, mod *domain.Mod, fileID, destDir string, progress source.FetchProgressFunc) (string, error) {
	if mod == nil {
		return "", fmt.Errorf("source %q: fetching: no mod given", sourceID)
	}
	if fileID == "" {
		fileID = mod.ID
	}
	appID := mod.GameID
	if appID == "" {
		return "", fmt.Errorf("source %q: fetching item %s: no Steam app id for this game - map the game to a numeric app id with `lmm game edit --source steamworkshop=<appid>`", sourceID, fileID)
	}

	if _, err := exec.LookPath(steamcmdTool); err != nil {
		return "", &domain.WorkshopFetchFailure{
			AppID: appID, PublishedFileID: fileID, Tool: steamcmdTool,
			Reason: ReasonSteamcmdMissing, Err: domain.ErrExternalToolMissing,
		}
	}

	home, err := s.steamcmdHome(destDir)
	if err != nil {
		return "", fmt.Errorf("source %q: preparing steamcmd home: %w", sourceID, err)
	}

	if progress == nil {
		progress = func(string, string, int64) {}
	}
	progress(source.FetchPhaseStarted, fmt.Sprintf("fetching Workshop item %s with steamcmd", fileID), 0)

	output, runErr := s.runSteamcmd(ctx, home, destDir, appID, fileID, progress)

	// What actually happened is decided by the exit status and the item on
	// disk, in that order - never by what the log happens to contain. See
	// classifySteamcmd.
	content := filepath.Join(destDir, "steamapps", "workshop", "content", appID, fileID)
	_, contentErr := os.Stat(content)
	if failure := classifySteamcmd(appID, fileID, content, output, runErr, contentErr); failure != nil {
		return "", failure
	}

	progress(source.FetchPhaseDone, fmt.Sprintf("Workshop item %s downloaded", fileID), dirSize(content))
	return content, nil
}

// runSteamcmd executes one download, streaming the tool's combined output
// through the progress parser while capping what it retains for an error
// message, and ticking a heartbeat so a silent download still reports.
func (s *Source) runSteamcmd(ctx context.Context, home, destDir, appID, fileID string, progress source.FetchProgressFunc) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, steamcmdTimeout)
	defer cancel()

	// +force_install_dir BEFORE +login: the spike's most dangerous trap is
	// steamcmd resolving the user's real library because the install dir
	// was not pinned yet when it logged in.
	cmd := exec.CommandContext(ctx, steamcmdTool,
		"+force_install_dir", destDir,
		"+login", "anonymous",
		"+workshop_download_item", appID, fileID,
		"+quit",
	)
	cmd.Env = steamcmdEnv(home)
	cmd.Dir = home
	cmd.WaitDelay = steamcmdWaitDelay

	// Two goroutines below report progress - the output scanner and the
	// heartbeat - and source.FetchProgressFunc promises the caller they
	// never arrive at once: core forwards each tick straight into an
	// EventSink, which is documented as being called synchronously on the
	// operation's goroutine. One mutex here is what makes that promise
	// true, so a sink may keep plain state (a spinner frame, a
	// \r-overwrite length) without a lock of its own.
	var reportMu sync.Mutex
	report := func(phase, detail string, bytes int64) {
		reportMu.Lock()
		defer reportMu.Unlock()
		progress(phase, detail, bytes)
	}

	collector := &outputCollector{}
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		scanner := bufio.NewScanner(reader)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			collector.add(line)
			if m := progressLine.FindStringSubmatch(line); m != nil {
				var done int64
				if len(m) > 2 && m[2] != "" {
					done, _ = strconv.ParseInt(m[2], 10, 64)
				}
				report(source.FetchPhaseProgress, strings.TrimSpace(line), done)
			}
		}
		_, _ = io.Copy(io.Discard, reader)
	}()

	// The heartbeat stops on exactly two events, both of which are certain
	// to happen: stopHeartbeat is closed by a defer that runs on EVERY
	// return path, and ctx is done on a timeout or a cancel. Neither
	// depends on cmd.Run() returning promptly.
	started := time.Now()
	stopHeartbeat := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopHeartbeat) }) }
	defer stop()

	var beat sync.WaitGroup
	beat.Add(1)
	go func() {
		defer beat.Done()
		ticker := time.NewTicker(steamcmdHeartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				bytes := dirSize(contentRoot(destDir))
				report(source.FetchPhaseProgress,
					fmt.Sprintf("still downloading item %s - %s on disk after %s",
						fileID, humanBytes(bytes), time.Since(started).Round(time.Second)),
					bytes)
			}
		}
	}()

	runErr := cmd.Run()
	stop()
	beat.Wait()
	_ = writer.Close()
	wg.Wait()
	_ = reader.Close()

	if ctx.Err() != nil {
		return collector.String(), fmt.Errorf("steamcmd: %w", ctx.Err())
	}
	// os/exec returns ErrWaitDelay only when the process itself exited
	// SUCCESSFULLY and no cancel occurred - a nonzero exit comes back as
	// its own *ExitError instead. So this means "the download finished; we
	// simply stopped waiting for a leftover grandchild to release the
	// pipe", which is not a failure of the download. The verdict is left
	// to the exit status and the content on disk, per classifySteamcmd.
	if errors.Is(runErr, exec.ErrWaitDelay) {
		runErr = nil
	}
	return collector.String(), runErr
}

// steamcmdEnv is the ONLY environment steamcmd is given: an isolated HOME
// with the XDG variables pointed inside it, plus the bare minimum needed
// to run at all. Building it from scratch rather than filtering os.Environ
// is what makes the isolation a property of the code rather than of a
// blocklist someone has to keep up to date - nothing about the user's real
// Steam install (STEAM_ROOT, STEAM_COMPAT_*, an inherited HOME) can leak
// in by being forgotten.
func steamcmdEnv(home string) []string {
	env := []string{
		"HOME=" + home,
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"PATH=" + os.Getenv("PATH"),
	}
	// A terminal type and a locale are pass-through conveniences: steamcmd
	// draws a progress display with them, and neither can name a Steam
	// installation.
	for _, name := range []string{"TERM", "LANG"} {
		if v := os.Getenv(name); v != "" {
			env = append(env, name+"="+v)
		}
	}
	return env
}

// steamcmdHome resolves - and creates - the isolated home steamcmd runs
// under: <CacheDir>/_steamworkshop/steamcmd-home, persistent so the tool's
// ~200 MB self-bootstrap is paid once rather than per download, and safe
// for the user to delete at any time.
//
// A source constructed with no cache dir (only test doubles, in practice)
// falls back to the same leaf inside the staging area core already owns:
// correct, just slower, and never a write into the user's real home. That
// fallback lands INSIDE destDir, which is why the heartbeat measures
// contentRoot rather than destDir - otherwise it would report the tool's
// own bootstrap as downloaded bytes.
func (s *Source) steamcmdHome(destDir string) (string, error) {
	root := s.cacheDir
	if root == "" {
		root = destDir
	}
	home := filepath.Join(root, "_steamworkshop", "steamcmd-home")
	if err := os.MkdirAll(home, 0700); err != nil {
		return "", err
	}
	return home, nil
}

// contentRoot is the only subtree a Workshop download puts content in:
// +force_install_dir pins steamcmd to destDir, and the item lands at
// <destDir>/steamapps/workshop/content/<appid>/<fileid>/. The heartbeat
// measures this rather than destDir so nothing else the tool leaves in the
// staging directory - its own isolated home, on the no-cache-dir fallback -
// is counted as bytes downloaded.
func contentRoot(destDir string) string {
	return filepath.Join(destDir, "steamapps")
}

// classifySteamcmd turns one run into the typed failure a frontend
// explains, or nil when it succeeded. runErr is what cmd.Run returned;
// contentErr is the os.Stat of the directory the item should have landed
// in.
//
// The order matters, and it is: exit status and content on disk FIRST, the
// log's markers only on the failure branch.
//
//   - A run that exited cleanly and left the item on disk succeeded. No
//     amount of "(Failure)" elsewhere in the log can overturn two facts
//     lmm can check directly, and steamcmd prints that parenthetical for
//     any generic k_EResultFail on steps unrelated to this download.
//   - Once the run HAS failed, the item's own error line is the authority,
//     whatever the exit status - steamcmd has been observed exiting 0 on a
//     refused download, which is the only reason the exit code alone is
//     not enough.
//   - Anything else is reported honestly as unclassified, carrying the
//     output tail rather than a guessed diagnosis.
func classifySteamcmd(appID, fileID, content, output string, runErr, contentErr error) error {
	if runErr == nil && contentErr == nil {
		return nil
	}

	if m := refusalLine.FindStringSubmatch(output); m != nil {
		switch m[1] {
		case markerAnonymousRefused:
			return &domain.WorkshopFetchFailure{
				AppID: appID, PublishedFileID: fileID, Tool: steamcmdTool,
				Reason: ReasonAnonymousRefused, Err: domain.ErrWorkshopAnonymousRefused,
			}
		case markerAccessDenied:
			return &domain.WorkshopFetchFailure{
				AppID: appID, PublishedFileID: fileID, Tool: steamcmdTool,
				Reason: ReasonItemUnavailable, Err: domain.ErrWorkshopItemUnavailable,
			}
		}
	}

	if runErr != nil {
		return &domain.WorkshopFetchFailure{
			AppID: appID, PublishedFileID: fileID, Tool: steamcmdTool,
			Reason:     fmt.Sprintf("steamcmd failed to download item %s.", fileID),
			OutputTail: tail(output),
			Err:        runErr,
		}
	}

	return &domain.WorkshopFetchFailure{
		AppID: appID, PublishedFileID: fileID, Tool: steamcmdTool,
		Reason:     "steamcmd reported no error but downloaded nothing.",
		OutputTail: tail(output),
		Err:        fmt.Errorf("no content at %s: %w", content, contentErr),
	}
}

// outputCollector keeps the tail of a tool's output without holding all of
// it: a steamcmd run can print megabytes, and only the end of it ever
// reaches an error message.
type outputCollector struct {
	mu    sync.Mutex
	lines []string
	size  int
}

func (c *outputCollector) add(line string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, line)
	c.size += len(line) + 1
	for c.size > outputTailLimit*2 && len(c.lines) > 1 {
		c.size -= len(c.lines[0]) + 1
		c.lines = c.lines[1:]
	}
}

func (c *outputCollector) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.lines, "\n")
}

// tail returns the last outputTailLimit bytes of s, on a rune boundary.
func tail(s string) string {
	if len(s) <= outputTailLimit {
		return s
	}
	cut := s[len(s)-outputTailLimit:]
	if i := strings.IndexByte(cut, '\n'); i >= 0 && i+1 < len(cut) {
		cut = cut[i+1:]
	}
	return cut
}

// dirSize sums the regular files under root - the bytes-on-disk signal the
// heartbeat reports. It needs no output parsing and works even when
// steamcmd says nothing at all; an unreadable entry is skipped rather than
// failing a progress tick.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // a progress reading must never fail the download
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// humanBytes renders a byte count the way a progress line should read.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGT"[exp])
}
