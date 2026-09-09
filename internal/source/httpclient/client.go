// Package httpclient is a thin JSON HTTP client used by mod-source SDKs
// (NexusMods, CurseForge, ...). It centralises auth-header injection,
// status-code mapping (401 -> domain.ErrAuthRequired), JSON decode, and
// limited body reads on errors. Source-specific behaviour (extra status
// codes, body parsing) plugs in via the optional ErrorMapper.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// errorBodyLimit caps how much of an error response we read into memory before
// surfacing it. Sources can return verbose HTML on outages; 10 KiB is plenty
// for an actionable error message and bounds memory use.
const errorBodyLimit = 10 * 1024

// Options configures a Client. BaseURL, AuthHeader, and AuthLabel are
// required and validated by New (which panics on omission); the rest have
// sensible zero-value defaults.
type Options struct {
	HTTPClient *http.Client
	BaseURL    string
	APIKey     string
	// AuthHeader is the request header used to forward APIKey, e.g. "apikey"
	// (NexusMods) or "x-api-key" (CurseForge).
	AuthHeader string
	// AuthLabel is the human-readable source name interpolated into the
	// "<label> API key required" error returned on 401.
	AuthLabel string
	// ErrorMapper, when set, is consulted before the default non-2xx mapping.
	// Return nil to defer to the default; return a non-nil error to short-
	// circuit (e.g. translate 404 to a domain error).
	ErrorMapper func(status int, body []byte, requestPath string) error
	// MaxResponseBytes caps how much of a SUCCESSFUL response body is read
	// before decoding; a body over the cap is an error rather than an
	// unbounded allocation. Zero (the default) reads without a cap, which is
	// every existing caller's behaviour. Error bodies are separately capped
	// at errorBodyLimit regardless.
	MaxResponseBytes int64
}

// Client is a small JSON HTTP client wrapping net/http for use by mod-source
// SDKs. Construct via New; configure via Options.
type Client struct {
	httpClient       *http.Client
	baseURL          string
	apiKey           string
	authHeader       string
	authLabel        string
	errorMapper      func(int, []byte, string) error
	maxResponseBytes int64
}

// New returns a Client configured with opts. Panics when a required field
// (BaseURL, AuthHeader, AuthLabel) is empty — the package is internal and
// only ever constructed at startup, so a missing required field is a
// programming error worth catching loudly.
func New(opts Options) *Client {
	if opts.BaseURL == "" {
		panic("httpclient.New: BaseURL is required")
	}
	if opts.AuthHeader == "" {
		panic("httpclient.New: AuthHeader is required")
	}
	if opts.AuthLabel == "" {
		panic("httpclient.New: AuthLabel is required")
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{
		httpClient:       httpClient,
		baseURL:          opts.BaseURL,
		apiKey:           opts.APIKey,
		authHeader:       opts.AuthHeader,
		authLabel:        opts.AuthLabel,
		errorMapper:      opts.ErrorMapper,
		maxResponseBytes: opts.MaxResponseBytes,
	}
}

// SetAPIKey updates the API key used for subsequent requests.
func (c *Client) SetAPIKey(key string) { c.apiKey = key }

// SetBaseURL replaces the configured base URL. Used by tests that wire a
// httptest server in front of the real client.
func (c *Client) SetBaseURL(u string) { c.baseURL = u }

// IsAuthenticated reports whether the client has a non-empty API key.
func (c *Client) IsAuthenticated() bool { return c.apiKey != "" }

// BaseURL returns the configured base URL (used by callers that need to
// build URLs outside of DoJSON, e.g. download endpoints).
func (c *Client) BaseURL() string { return c.baseURL }

// HTTPClient returns the underlying *http.Client (used by callers that need
// to issue raw downloads or non-JSON requests with the same transport).
func (c *Client) HTTPClient() *http.Client { return c.httpClient }

// DoJSON performs an HTTP request against baseURL+path and JSON-decodes the
// response body into result. Auth header is set when an APIKey is configured.
// Non-2xx responses are first offered to ErrorMapper; if ErrorMapper returns
// nil (or is unset), 401 is mapped to domain.ErrAuthRequired and other
// statuses are surfaced as "API error (status N): <body>".
//
// It is DoJSONBody with no request body; the two share every rule.
func (c *Client) DoJSON(ctx context.Context, method, path string, result interface{}) error {
	return c.DoJSONBody(ctx, method, path, nil, result)
}

// DoJSONBody is DoJSON with a JSON request body: body is marshalled and sent
// with a Content-Type of application/json, and the response is decoded into
// result exactly as DoJSON decodes it - same auth header, same ErrorMapper,
// same 401/status mapping, same capped error-body read. A nil body sends no
// body and no Content-Type at all, which is what DoJSON delegates.
//
// Added for CurseForge's batch POST /v1/mods (#28), which needs a method and
// a body DoJSON's signature cannot express; DoJSON's own signature is
// unchanged so no existing call site moves.
func (c *Client) DoJSONBody(ctx context.Context, method, path string, body, result interface{}) (err error) {
	var reader io.Reader
	if body != nil {
		encoded, merr := json.Marshal(body)
		if merr != nil {
			return fmt.Errorf("encoding request body: %w", merr)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set(c.authHeader, c.apiKey)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("executing request: %w", err)
	}
	defer func() {
		if cerr := resp.Body.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("closing response body: %w", cerr)
		}
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// errBody, not body: the REQUEST body is this function's own
		// parameter, and shadowing it here read as if it were being
		// reassigned (Track C review, finding 10).
		errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit))
		if readErr != nil {
			return fmt.Errorf("API error (status %d); reading body: %w", resp.StatusCode, readErr)
		}
		if c.errorMapper != nil {
			if mapped := c.errorMapper(resp.StatusCode, errBody, path); mapped != nil {
				return mapped
			}
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%w: %s API key required", domain.ErrAuthRequired, c.authLabel)
		}
		return fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(errBody))
	}

	// 204 No Content has no body to decode; treat as success.
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	if c.maxResponseBytes > 0 {
		// Read +1 byte past the cap so "exactly at the cap" still decodes
		// and only one byte over it is refused, before anything is parsed.
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, c.maxResponseBytes+1))
		if readErr != nil {
			return fmt.Errorf("reading response: %w", readErr)
		}
		if int64(len(payload)) > c.maxResponseBytes {
			return fmt.Errorf("response exceeds %d bytes", c.maxResponseBytes)
		}
		// Decoded through the SAME json.Decoder the uncapped path below
		// uses, not json.Unmarshal: the two disagree on trailing data
		// (Decode reads one document and ignores the rest, Unmarshal
		// errors) and on the message an empty body produces, and only
		// CurseForge sets a cap - a per-caller split in a shared client
		// (Track C review, finding 9).
		if err := json.NewDecoder(bytes.NewReader(payload)).Decode(result); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
		return nil
	}

	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
