package custom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// A custom api source's API key used to be stored unvalidated - "validated
// on first use" - because a YAML definition had no way to say what a valid
// key looks like (source-registry design §9). AuthValidateConfig is that
// way (#121): a declarative probe (method, path, expected status, an
// optional JSON field that must be present) which the source runs live at
// `lmm auth login`, exactly as NexusMods and CurseForge do.
//
// The probe is exposed as source.KeyValidator only for a definition that
// DECLARES one, because app.HasKeyValidator asks the type system whether a
// live check will happen - and it asks BEFORE there is a key to check, to
// decide whether to print "Validating..." or "stored, validated on first
// use". A source that always implemented the interface and then refused
// would make that question unanswerable, so the wiring is a wrapper type
// rather than a method that sometimes says no.

// apiKeyValidator is *API plus source.KeyValidator, the shape New returns
// for an api definition carrying an auth.validate probe. Embedding forwards
// every other method, including the duck-typed SetAPIKey/IsAuthenticated
// the app layer looks for.
type apiKeyValidator struct{ *API }

var _ source.KeyValidator = apiKeyValidator{}

// ValidateKey implements source.KeyValidator by running the definition's
// declared probe with key.
func (a apiKeyValidator) ValidateKey(ctx context.Context, key string) error {
	return a.validateKey(ctx, key)
}

// validateKey performs one probe request with the candidate key. It never
// touches a.apiKey: the key being checked may be a replacement for the one
// this source is already running with, and a failed check must leave the
// live source exactly as it was (CurseForge's ValidateKey makes the same
// promise with a call-scoped client).
//
// A rejected key is reported as domain.ErrAuthRequired so a caller can tell
// "the API said no" from "the API could not be reached"; the response body
// is deliberately NOT quoted into the message, since the one thing a 401
// body might echo is the key itself.
func (a *API) validateKey(ctx context.Context, key string) error {
	probe := a.validateProbe()
	if probe == nil {
		return fmt.Errorf("source %q: key validation: %w", a.id, source.ErrNotSupported)
	}

	rawURL := a.baseURL + probe.Path
	reqURL := rawURL
	if a.auth.APIKey.In == "query" && key != "" {
		withKey, err := addQueryParam(reqURL, a.auth.APIKey.Name, key)
		if err != nil {
			return fmt.Errorf("source %q: %w", a.id, err)
		}
		reqURL = withKey
	}

	method := strings.ToUpper(probe.Method)
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return fmt.Errorf("source %q: building key-validation request: %w", a.id, err)
	}
	if a.auth.APIKey.In == "header" && key != "" {
		req.Header.Set(a.auth.APIKey.Name, key)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // strip the URL (and any query-mode key) from the message
		}
		return fmt.Errorf("source %q: could not reach %s to validate the API key: %w",
			a.id, redactedURL(rawURL), err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if err := probeStatusVerdict(a.id, probe, resp.StatusCode, rawURL); err != nil {
		return err
	}
	if probe.Field == "" {
		return nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseSize+1))
	if err != nil {
		return fmt.Errorf("source %q: reading key-validation response: %w", a.id, err)
	}
	if len(data) > maxAPIResponseSize {
		return fmt.Errorf("source %q: key-validation response from %s exceeds %d bytes",
			a.id, redactedURL(rawURL), maxAPIResponseSize)
	}
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("source %q: parsing key-validation response from %s: %w",
			a.id, redactedURL(rawURL), err)
	}
	if _, ok := lookupPath(doc, probe.Field); !ok {
		return fmt.Errorf("source %q: %w: the API answered without %q, so this key is not recognised",
			a.id, domain.ErrAuthRequired, probe.Field)
	}
	return nil
}

// validateProbe returns the definition's declared probe, or nil.
func (a *API) validateProbe() *AuthValidateConfig {
	if a.auth == nil || a.auth.APIKey == nil {
		return nil
	}
	return a.auth.Validate
}

// probeStatusVerdict turns a probe response's status into a verdict. An
// explicit expected status is exact; without one, any 2xx passes. 401 and
// 403 are the API refusing the key and map to domain.ErrAuthRequired;
// anything else is reported as what it is - a service that could not answer
// the question, which is not the same as a bad key.
func probeStatusVerdict(sourceID string, probe *AuthValidateConfig, status int, rawURL string) error {
	switch {
	case probe.Status != 0 && status == probe.Status:
		return nil
	case probe.Status == 0 && status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return fmt.Errorf("source %q: %w: the API rejected this key (HTTP %d)",
			sourceID, domain.ErrAuthRequired, status)
	case probe.Status != 0:
		return fmt.Errorf("source %q: validating the API key against %s: HTTP %d, expected %d",
			sourceID, redactedURL(rawURL), status, probe.Status)
	default:
		return fmt.Errorf("source %q: validating the API key against %s: HTTP %d",
			sourceID, redactedURL(rawURL), status)
	}
}

// NewAPISource constructs an api source and returns it as a
// source.ModSource, wrapped so it implements source.KeyValidator exactly
// when the definition declares an auth.validate probe (#121). New's api
// case goes through this; NewAPI stays the concrete constructor for callers
// that want *API itself.
func NewAPISource(def SourceDefinition) (source.ModSource, error) {
	api, err := NewAPI(def)
	if err != nil {
		return nil, err
	}
	if api.validateProbe() == nil {
		return api, nil
	}
	return apiKeyValidator{api}, nil
}
