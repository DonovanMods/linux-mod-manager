package core

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source/httpclient"
)

const (
	defaultMaxAttempts       = 3
	defaultInitialBackoff    = time.Second
	defaultBackoffMultiplier = 2

	// downloadMaxRetryAfter is the longest server-named wait a download
	// sits through (#436). A throttle that asks for longer fails the
	// download NOW, naming the wait, rather than holding a job silent for
	// however long a CDN chose; the thunderstore source's maxRetryAfter,
	// and its reasoning, is the same number.
	downloadMaxRetryAfter = time.Minute
	// downloadStallTimeout is how long a download may go without receiving
	// a byte before its attempt fails as stalled (#436). The default
	// client had NO timeout at all, so a body that stopped arriving held a
	// job forever. A minute, not the index's thirty seconds: a mod archive
	// comes from whichever CDN its source uses, some of which pause while
	// they fetch the object from their origin.
	downloadStallTimeout = time.Minute
)

// DownloadResult contains the outcome of a download
type DownloadResult struct {
	Path     string `json:"path"`     // Final file path
	Size     int64  `json:"size"`     // Bytes downloaded
	Checksum string `json:"checksum"` // MD5 hash of downloaded file (recorded in the DB)
	SHA256   string `json:"sha256"`   // SHA-256 of downloaded file (compared against source-declared checksums)
}

// Downloader handles HTTP file downloads with progress tracking
type Downloader struct {
	httpClient *http.Client
	log        *slog.Logger
	// sleep waits out one backoff, or returns ctx's error the moment the
	// caller gives up. A field so a test asserts the policy without
	// spending it.
	sleep func(context.Context, time.Duration) error
	now   func() time.Time
}

// NewDownloader creates a new Downloader with the given HTTP client. If
// httpClient is nil, the downloader uses a client whose transport fails a
// transfer that stops delivering bytes for downloadStallTimeout (#436),
// over http.DefaultTransport.
func NewDownloader(httpClient *http.Client) *Downloader {
	if httpClient == nil {
		httpClient = &http.Client{Transport: &httpclient.IdleTimeout{
			Base: http.DefaultTransport, Timeout: downloadStallTimeout,
		}}
	}
	return &Downloader{
		httpClient: httpClient,
		log:        slog.New(slog.DiscardHandler),
		sleep:      sleepOrDone,
		now:        time.Now,
	}
}

// sleepOrDone waits out d, or returns ctx's error as soon as it is done.
func sleepOrDone(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// SetLogger sets the logger used for diagnostics (retry attempts). nil
// resets to discard, which is also the default.
func (d *Downloader) SetLogger(l *slog.Logger) {
	if l == nil {
		l = slog.New(slog.DiscardHandler)
	}
	d.log = l
}

// isRetryableHTTP returns true for status codes that warrant a retry (transient/server overload).
func isRetryableHTTP(statusCode int) bool {
	return statusCode == http.StatusRequestTimeout || statusCode == http.StatusTooManyRequests ||
		(statusCode >= 500 && statusCode < 600)
}

// isRetryableNet returns true only for network errors that are typically transient
// (timeouts, temporary failures). Non-transient errors (e.g. permission denied,
// connection refused, DNS failure) return false so the caller fails fast instead of retrying.
func isRetryableNet(err error) bool {
	if err == nil {
		return false
	}
	// A stalled transfer is transient, and is checked FIRST: the stall
	// guard cancels the request to unblock its read, so the error also
	// carries a context cancellation that is not the caller's.
	if errors.Is(err, httpclient.ErrStalled) {
		return true
	}
	// Do not retry on context cancellation or deadline
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if ok := errors.As(err, &netErr); ok && netErr.Timeout() {
		return true
	}
	// Only retry on known-transient net errors; unknown errors (incl. permission, IO) fail fast
	return false
}

// Download fetches a file from the URL and saves it to destPath, with retries
// on transient failures (exponential backoff). Progress ticks are sent to
// the optional sink as DownloadEvent.
func (d *Downloader) Download(ctx context.Context, url, destPath string, sink EventSink) (*DownloadResult, error) {
	return d.DownloadWithHeaders(ctx, url, destPath, nil, sink)
}

// DownloadWithHeaders is Download with extra request headers applied to every
// attempt — used for authenticated file downloads from custom sources.
func (d *Downloader) DownloadWithHeaders(ctx context.Context, url, destPath string, headers map[string]string, sink EventSink) (*DownloadResult, error) {
	var lastErr error
	backoff := defaultInitialBackoff

	for attempt := 1; attempt <= defaultMaxAttempts; attempt++ {
		result, err := d.downloadOnce(ctx, url, destPath, headers, sink)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if attempt == defaultMaxAttempts {
			break
		}

		// Check if error is retryable (including HTTP status from our wrapped error)
		retry := source.Notice{
			Kind: source.NoticeRetry, Source: hostOf(url), Download: true,
			Attempt: attempt + 1, MaxAttempts: defaultMaxAttempts, Wait: backoff, Err: err,
		}
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) {
			if !isRetryableHTTP(httpErr.code) {
				return nil, err
			}
			retry.Status = httpErr.code
			retry.Reason = source.RetryServerError
			if httpErr.code == http.StatusTooManyRequests {
				retry.Reason = source.RetryRateLimited
			}
			// A server-named wait is a FLOOR: coming back sooner is how a
			// throttled client is throttled again.
			if httpErr.retryAfter > downloadMaxRetryAfter {
				return nil, fmt.Errorf("%w: the server asked lmm to wait %s before trying again", err, httpErr.retryAfter)
			}
			retry.Wait = max(retry.Wait, httpErr.retryAfter)
		} else if ctx.Err() != nil || !isRetryableNet(err) {
			return nil, err
		} else {
			retry.Reason = source.RetryNetworkError
		}

		d.log.Debug("download attempt failed; retrying", "attempt", attempt, "backoff", retry.Wait, "err", err)
		source.Notify(ctx, retry)

		// Sleep with backoff; respect context
		if err := d.sleep(ctx, retry.Wait); err != nil {
			return nil, fmt.Errorf("download: %w", err)
		}
		backoff *= defaultBackoffMultiplier
	}

	return nil, lastErr
}

// httpStatusError carries an HTTP status code for retry decisions, and the
// wait the response asked for, if any.
type httpStatusError struct {
	code       int
	msg        string
	retryAfter time.Duration
}

// Error implements the error interface, returning the message the HTTP
// response produced (the status code itself is read via a type assertion,
// not parsed back out of this string).
func (e *httpStatusError) Error() string {
	return e.msg
}

// redirectSafeClient returns the HTTP client to use for one download
// attempt. Go's http.Client automatically strips only the Authorization and
// Cookie headers on a cross-host redirect; any other header we set —
// notably an API-key header for authenticated custom-source downloads — is
// otherwise forwarded verbatim to whatever host the redirect points at. When
// headers are supplied, this returns a shallow copy of the base client with
// a CheckRedirect that deletes those header names once the redirect leaves
// the original request's scheme+host (Go re-applies the original request's
// headers to each redirect, so deleting them here — the documented hook for
// this — is sufficient) and enforces the default 10-redirect cap. If the
// base client already had its own CheckRedirect, it is chained: our
// header-stripping and redirect-cap logic runs first, then the base policy
// runs and its error (if any) is returned — a base client's own redirect
// policy must never be silently discarded. Calls with no headers (plain
// Download) use the base client untouched.
func (d *Downloader) redirectSafeClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return d.httpClient
	}
	client := *d.httpClient // shallow copy: shares Transport/Timeout/Jar
	baseCheckRedirect := d.httpClient.CheckRedirect
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
			for name := range headers {
				req.Header.Del(name)
			}
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if baseCheckRedirect != nil {
			return baseCheckRedirect(req, via)
		}
		return nil
	}
	return &client
}

// downloadOnce performs a single download attempt (no retries).
func (d *Downloader) downloadOnce(ctx context.Context, url, destPath string, headers map[string]string, sink EventSink) (result *DownloadResult, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	resp, err := d.redirectSafeClient(headers).Do(req)
	if err != nil {
		// *url.Error's Error() embeds the request URL verbatim, which for
		// query-mode auth (custom sources' GetDownloadURL) contains the API
		// key. Unwrap to the transport error and report a query-stripped URL
		// instead — %w still wraps the inner net error so isRetryableNet's
		// errors.As(err, &netErr) keeps working for retry classification.
		var uerr *neturl.Error
		if errors.As(err, &uerr) {
			reportURL := uerr.URL
			if parsed, perr := neturl.Parse(uerr.URL); perr == nil {
				parsed.RawQuery = ""
				reportURL = parsed.String()
			}
			return nil, fmt.Errorf("executing request to %s: %w", reportURL, uerr.Err)
		}
		return nil, fmt.Errorf("executing request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing response body: %w", cerr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		httpErr := &httpStatusError{
			code: resp.StatusCode, msg: fmt.Sprintf("HTTP error: %d %s", resp.StatusCode, resp.Status),
			retryAfter: retryAfterOf(resp.Header.Get("Retry-After"), d.now()),
		}
		return nil, httpErr
	}

	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating directory: %w", err)
	}

	tempPath := destPath + ".tmp"
	file, err := os.Create(tempPath)
	if err != nil {
		return nil, fmt.Errorf("creating file: %w", err)
	}
	removeTemp := true
	defer func() {
		_ = file.Close()
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	totalBytes := resp.ContentLength
	if totalBytes < 0 {
		// http.Response.ContentLength is -1 when unknown (e.g. chunked
		// transfer-encoding); DownloadEvent's contract is 0 for unknown.
		totalBytes = 0
	}
	md5Hasher := md5.New()
	shaHasher := sha256.New()
	reader := &progressReader{
		reader:     resp.Body,
		totalBytes: totalBytes,
		sink:       sink,
	}
	teeReader := io.TeeReader(reader, io.MultiWriter(md5Hasher, shaHasher))

	written, err := io.Copy(file, teeReader)
	if err != nil {
		return nil, fmt.Errorf("downloading file: %w", err)
	}

	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("closing file: %w", err)
	}

	if err := os.Rename(tempPath, destPath); err != nil {
		return nil, fmt.Errorf("renaming file: %w", err)
	}
	removeTemp = false

	return &DownloadResult{
		Path:     destPath,
		Size:     written,
		Checksum: hex.EncodeToString(md5Hasher.Sum(nil)),
		SHA256:   hex.EncodeToString(shaHasher.Sum(nil)),
	}, nil
}

// progressReader wraps an io.Reader to track download progress
type progressReader struct {
	reader     io.Reader
	totalBytes int64
	downloaded int64
	sink       EventSink
}

// Read implements io.Reader, forwarding to the wrapped reader and emitting
// a DownloadEvent tick on every non-empty read.
func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.downloaded += int64(n)
		if r.sink != nil {
			var pct float64
			if r.totalBytes > 0 {
				pct = float64(r.downloaded) / float64(r.totalBytes) * 100
			}
			r.sink(DownloadEvent{
				Scope:      Scope{Op: OpDownload},
				Downloaded: r.downloaded,
				TotalBytes: r.totalBytes,
				Percent:    pct,
			})
		}
	}
	return n, err
}

// retryAfterOf reads a Retry-After value in either RFC 9110 form -
// delay-seconds, or an HTTP-date judged against now. Anything unparseable,
// negative or past is 0.
func retryAfterOf(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		// Clamped before the multiplication, which would otherwise wrap for
		// an absurd value; a year is far past anything lmm waits.
		return time.Duration(min(max(secs, 0), 365*24*60*60)) * time.Second
	}
	at, err := http.ParseTime(v)
	if err != nil {
		return 0
	}
	return max(at.Sub(now), 0)
}

// hostOf names the server a download is waiting on - the host, never the
// full URL, which for an authenticated custom source carries a key.
func hostOf(raw string) string {
	u, err := neturl.Parse(raw)
	if err != nil || u.Host == "" {
		return "the download server"
	}
	return u.Host
}
