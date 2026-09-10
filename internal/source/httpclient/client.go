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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// errorBodyLimit caps how much of an error response we read into memory before
// surfacing it. Sources can return verbose HTML on outages; 10 KiB is plenty
// for an actionable error message and bounds memory use.
const errorBodyLimit = 10 * 1024

// redactedKey stands in for the configured API key wherever a message would
// otherwise carry it.
const redactedKey = "[redacted]"

// Options configures a Client. BaseURL, AuthLabel, and ONE of AuthHeader /
// AuthQueryParam are required and validated by New (which panics on
// omission); the rest have sensible zero-value defaults.
type Options struct {
	HTTPClient *http.Client
	BaseURL    string
	APIKey     string
	// AuthHeader is the request header used to forward APIKey, e.g. "apikey"
	// (NexusMods) or "x-api-key" (CurseForge).
	AuthHeader string
	// AuthQueryParam is the alternative to AuthHeader for an API that takes
	// its key as a QUERY PARAMETER rather than a header - Valve's Steam Web
	// API takes "key=" (#269). Exactly one of the two is used: when this is
	// set the key is appended to the request URL and no auth header is
	// sent, and when it is empty the AuthHeader path is unchanged. Set
	// neither and New panics; set both and AuthHeader wins, since that is
	// what every existing source uses.
	AuthQueryParam string
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
	authQueryParam   string
	authLabel        string
	errorMapper      func(int, []byte, string) error
	maxResponseBytes int64
}

// New returns a Client configured with opts. Panics when a required field
// (BaseURL, AuthLabel, and one of AuthHeader / AuthQueryParam) is empty —
// the package is internal and only ever constructed at startup, so a
// missing required field is a programming error worth catching loudly.
func New(opts Options) *Client {
	if opts.BaseURL == "" {
		panic("httpclient.New: BaseURL is required")
	}
	if opts.AuthHeader == "" && opts.AuthQueryParam == "" {
		panic("httpclient.New: AuthHeader or AuthQueryParam is required")
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
		authQueryParam:   opts.AuthQueryParam,
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
// result exactly as DoJSON decodes it - same auth injection, same ErrorMapper,
// same 401/status mapping, same capped error-body read. A nil body sends no
// body and no Content-Type at all, which is what DoJSON delegates.
//
// Added for CurseForge's batch POST /v1/mods (#28), which needs a method and
// a body DoJSON's signature cannot express; DoJSON's own signature is
// unchanged so no existing call site moves.
func (c *Client) DoJSONBody(ctx context.Context, method, path string, body, result interface{}) error {
	var reader io.Reader
	if body != nil {
		encoded, merr := json.Marshal(body)
		if merr != nil {
			return fmt.Errorf("encoding request body: %w", merr)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.authURL(path), reader)
	if err != nil {
		return c.requestError("creating request", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.applyAuthHeader(req)
	req.Header.Set("Accept", "application/json")
	return c.do(req, path, result)
}

// DoForm performs a form POST against baseURL+path and JSON-decodes the
// response body into result: Content-Type application/x-www-form-urlencoded
// with form as the body, and otherwise identical to DoJSON — the same auth
// injection, the same ErrorMapper hook, the same 401 -> domain.ErrAuthRequired
// mapping, the same errorBodyLimit and the same decode.
//
// It exists for Valve's Steam Web API (#269), which takes its arguments as
// a POST form (an item batch is itemcount + publishedfileids[i]) rather
// than as a JSON body or a query string.
func (c *Client) DoForm(ctx context.Context, path string, form url.Values, result any) error {
	body := form.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authURL(path), strings.NewReader(body))
	if err != nil {
		return c.requestError("creating request", path, err)
	}
	c.applyAuthHeader(req)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	return c.do(req, path, result)
}

// authURL returns the absolute request URL for path, appending the API key
// as a query parameter when the client is configured for query-parameter
// auth (and a key is set). A path that already carries its own query string
// keeps it. Header-auth clients — every source that predates #269 — get
// baseURL+path back unchanged.
func (c *Client) authURL(path string) string {
	full := c.baseURL + path
	if c.authHeader != "" || c.authQueryParam == "" || c.apiKey == "" {
		return full
	}
	sep := "?"
	if strings.Contains(full, "?") {
		sep = "&"
	}
	return full + sep + url.QueryEscape(c.authQueryParam) + "=" + url.QueryEscape(c.apiKey)
}

// requestError reports a failure that never produced a response, naming the
// caller-supplied PATH rather than the resolved URL.
//
// net/http reports both an unparseable URL and a transport failure as a
// *url.Error whose Error() embeds the RESOLVED url — which, for a
// query-parameter auth client (Steam's Web API, #269), carries the user's
// API key. That message is printed to the terminal, written into
// `lmm serve`'s error envelope and pasted into bug reports, so the URL is
// dropped here rather than at each of the dozens of call sites downstream
// (W2 review, Critical 1). Unwrapping to the inner error keeps
// errors.Is/errors.As working for every caller that classifies on it.
func (c *Client) requestError(op, requestPath string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("%s to %s: %w", op, requestPath, c.redactError(err))
}

// redact replaces the configured API key wherever it appears in s, in every
// form that can reach a message - see keyForms. It is the belt to
// requestError's braces: an upstream error page is free to echo the request
// back, and nothing this client returns may carry the credential.
func (c *Client) redact(s string) string {
	if c.apiKey == "" {
		return s
	}
	for _, form := range c.keyForms() {
		s = strings.ReplaceAll(s, form, redactedKey)
	}
	return s
}

// keyForms are the encodings of the configured key that can appear in a
// message: the raw value, and the url.QueryEscape form authURL actually
// puts on the wire, when the two differ (W2 re-review, N1).
//
// The escaped form is the one a body-echoing upstream quotes back, so
// matching only the raw value would walk straight past a key carrying a
// space, "/", "+" or "=". A well-formed Steam Web API key is 32 hex
// characters, for which QueryEscape is the identity; what this covers is
// the malformed CANDIDATE a user pastes at `lmm auth login`, which is
// validated live - against an upstream free to quote it - before it is
// ever stored. QueryEscape is the only encoding to check because authURL
// is the only place this client puts a key in a URL, and it puts it in the
// QUERY; nothing here builds a path from the key.
func (c *Client) keyForms() []string {
	forms := []string{c.apiKey}
	if esc := url.QueryEscape(c.apiKey); esc != c.apiKey {
		forms = append(forms, esc)
	}
	return forms
}

// carriesKey reports whether s contains the key in any of keyForms.
func (c *Client) carriesKey(s string) bool {
	for _, form := range c.keyForms() {
		if strings.Contains(s, form) {
			return true
		}
	}
	return false
}

// redactError is redact for an error, preserving the chain when there is
// nothing to redact (the overwhelmingly common case) and flattening it to a
// scrubbed message when there is — a wrapped error's text cannot be
// rewritten any other way, and a leaked key outranks a preserved Unwrap.
func (c *Client) redactError(err error) error {
	if c.apiKey == "" || !c.carriesKey(err.Error()) {
		return err
	}
	return errors.New(c.redact(err.Error()))
}

// applyAuthHeader sets the auth header when the client is configured for
// header auth and a key is set. A query-parameter client never sends one.
func (c *Client) applyAuthHeader(req *http.Request) {
	if c.authHeader != "" && c.apiKey != "" {
		req.Header.Set(c.authHeader, c.apiKey)
	}
}

// do executes req and applies the shared response contract DoJSON and
// DoForm both promise. requestPath is the caller-supplied path (not the
// resolved URL) so an ErrorMapper never sees a key that auth injection
// appended.
func (c *Client) do(req *http.Request, requestPath string, result any) (err error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return c.requestError("executing request", requestPath, err)
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
		// Redacted before the mapper, not after: a mapper is free to
		// interpolate the body into its own message (CurseForge's 403 does),
		// and an upstream error page that echoes the request back is exactly
		// how a key reaches a terminal.
		errBody = []byte(c.redact(string(errBody)))
		if c.errorMapper != nil {
			if mapped := c.errorMapper(resp.StatusCode, errBody, requestPath); mapped != nil {
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
