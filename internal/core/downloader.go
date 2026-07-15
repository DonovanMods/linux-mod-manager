package core

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultMaxAttempts       = 3
	defaultInitialBackoff    = time.Second
	defaultBackoffMultiplier = 2
)

// DownloadProgress represents the current state of a download
type DownloadProgress struct {
	TotalBytes int64   // Total size in bytes (0 if unknown)
	Downloaded int64   // Bytes downloaded so far
	Percentage float64 // Completion percentage (0-100)
}

// ProgressFunc is called periodically during download with progress updates
type ProgressFunc func(DownloadProgress)

// DownloadResult contains the outcome of a download
type DownloadResult struct {
	Path     string // Final file path
	Size     int64  // Bytes downloaded
	Checksum string // MD5 hash of downloaded file (recorded in the DB)
	SHA256   string // SHA-256 of downloaded file (compared against source-declared checksums)
}

// Downloader handles HTTP file downloads with progress tracking
type Downloader struct {
	httpClient *http.Client
}

// NewDownloader creates a new Downloader with the given HTTP client
// If httpClient is nil, http.DefaultClient is used
func NewDownloader(httpClient *http.Client) *Downloader {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Downloader{
		httpClient: httpClient,
	}
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
// on transient failures (exponential backoff). Progress updates are sent to
// the optional progressFn callback.
func (d *Downloader) Download(ctx context.Context, url, destPath string, progressFn ProgressFunc) (*DownloadResult, error) {
	return d.DownloadWithHeaders(ctx, url, destPath, nil, progressFn)
}

// DownloadWithHeaders is Download with extra request headers applied to every
// attempt — used for authenticated file downloads from custom sources.
func (d *Downloader) DownloadWithHeaders(ctx context.Context, url, destPath string, headers map[string]string, progressFn ProgressFunc) (*DownloadResult, error) {
	var lastErr error
	backoff := defaultInitialBackoff

	for attempt := 1; attempt <= defaultMaxAttempts; attempt++ {
		result, err := d.downloadOnce(ctx, url, destPath, headers, progressFn)
		if err == nil {
			return result, nil
		}
		lastErr = err

		if attempt == defaultMaxAttempts {
			break
		}

		// Check if error is retryable (including HTTP status from our wrapped error)
		var httpErr *httpStatusError
		if errors.As(err, &httpErr) {
			if !isRetryableHTTP(httpErr.code) {
				return nil, err
			}
		} else if ctx.Err() != nil || !isRetryableNet(err) {
			return nil, err
		}

		// Sleep with backoff; respect context
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("download: %w", ctx.Err())
		case <-timer.C:
		}
		backoff *= defaultBackoffMultiplier
	}

	return nil, lastErr
}

// httpStatusError carries an HTTP status code for retry decisions.
type httpStatusError struct {
	code int
	msg  string
}

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
// this — is sufficient) and otherwise preserves the default 10-redirect
// cap. Calls with no headers (plain Download) use the base client
// untouched.
func (d *Downloader) redirectSafeClient(headers map[string]string) *http.Client {
	if len(headers) == 0 {
		return d.httpClient
	}
	client := *d.httpClient // shallow copy: shares Transport/Timeout/Jar
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host {
			for name := range headers {
				req.Header.Del(name)
			}
		}
		return nil
	}
	return &client
}

// downloadOnce performs a single download attempt (no retries).
func (d *Downloader) downloadOnce(ctx context.Context, url, destPath string, headers map[string]string, progressFn ProgressFunc) (result *DownloadResult, err error) {
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
		httpErr := &httpStatusError{code: resp.StatusCode, msg: fmt.Sprintf("HTTP error: %d %s", resp.StatusCode, resp.Status)}
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
	md5Hasher := md5.New()
	shaHasher := sha256.New()
	reader := &progressReader{
		reader:     resp.Body,
		totalBytes: totalBytes,
		progressFn: progressFn,
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
	progressFn ProgressFunc
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.downloaded += int64(n)
		if r.progressFn != nil {
			progress := DownloadProgress{
				TotalBytes: r.totalBytes,
				Downloaded: r.downloaded,
			}
			if r.totalBytes > 0 {
				progress.Percentage = float64(r.downloaded) / float64(r.totalBytes) * 100
			}
			r.progressFn(progress)
		}
	}
	return n, err
}
