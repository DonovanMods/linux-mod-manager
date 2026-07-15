package custom

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// defaultManifestRefresh is the remote-manifest cache TTL when the definition
// does not set manifest.refresh.
const defaultManifestRefresh = 15 * time.Minute

// maxManifestSize bounds how much of a remote manifest we read into memory.
// Real manifests are KBs; 10 MiB is generous and prevents a hostile or broken
// server from exhausting memory.
const maxManifestSize = 10 << 20 // 10 MiB

// Manifest is a ModSource backed by a published mod-list document (design §3).
// Remote manifests are fetched on demand and cached in memory for the
// configured TTL; local paths are read on every operation (cheap).
// Construction is pure: a valid definition always registers, and fetch/parse
// problems surface as operation errors naming the manifest URL.
type Manifest struct {
	id        string
	name      string
	url       string // https URL, or absolute local path (~ expanded)
	isRemote  bool
	refresh   time.Duration
	allowHTTP bool
	auth      *AuthConfig

	apiKey     string
	httpClient *http.Client
	now        func() time.Time // injectable for TTL tests

	mu        sync.Mutex
	cached    *manifestDoc
	fetchedAt time.Time
}

// NewManifest constructs a manifest source from a validated definition. It
// performs no I/O — the manifest is first fetched when an operation needs it.
func NewManifest(def SourceDefinition) (*Manifest, error) {
	cfg := def.Manifest

	refresh := defaultManifestRefresh
	if cfg.Refresh != "" {
		d, err := time.ParseDuration(cfg.Refresh)
		if err != nil {
			return nil, fmt.Errorf("manifest.refresh: %w", err) // unreachable after Validate, kept for safety
		}
		refresh = d
	}

	u := cfg.URL
	isRemote := strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")
	if !isRemote {
		if strings.HasPrefix(u, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("expanding %q: %w", u, err)
			}
			u = filepath.Join(home, u[2:])
		}
		abs, err := filepath.Abs(u)
		if err != nil {
			return nil, fmt.Errorf("resolving %q: %w", u, err)
		}
		u = abs
	}

	return &Manifest{
		id:         def.ID,
		name:       def.Name,
		url:        u,
		isRemote:   isRemote,
		refresh:    refresh,
		allowHTTP:  def.AllowHTTP,
		auth:       cfg.Auth,
		httpClient: http.DefaultClient,
		now:        time.Now,
	}, nil
}

// ID returns the source ID.
func (m *Manifest) ID() string { return m.id }

// Name returns the source name.
func (m *Manifest) Name() string { return m.name }

// SetAPIKey provides the API key resolved at startup (env var or token store).
func (m *Manifest) SetAPIKey(key string) { m.apiKey = key }

// IsAuthenticated reports whether an API key is configured. Only meaningful
// when the definition declares auth (Capabilities().Auth).
func (m *Manifest) IsAuthenticated() bool { return m.apiKey != "" }

// fetch returns the parsed manifest, honoring the TTL cache for remote URLs.
// Errors name the source and manifest URL so users can act on them.
func (m *Manifest) fetch(ctx context.Context) (*manifestDoc, error) {
	if !m.isRemote {
		data, err := os.ReadFile(m.url)
		if err != nil {
			return nil, fmt.Errorf("source %q: reading manifest %s: %w", m.id, m.url, err)
		}
		doc, err := parseManifest(data, m.allowHTTP)
		if err != nil {
			return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
		}
		return doc, nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cached != nil && m.now().Sub(m.fetchedAt) < m.refresh {
		return m.cached, nil
	}

	doc, err := m.fetchRemote(ctx)
	if err != nil {
		return nil, err
	}
	m.cached = doc
	m.fetchedAt = m.now()
	return doc, nil
}

// fetchRemote downloads and parses the manifest document. Callers hold m.mu.
func (m *Manifest) fetchRemote(ctx context.Context) (*manifestDoc, error) {
	reqURL := m.url
	if m.auth != nil && m.auth.APIKey.In == "query" && m.apiKey != "" {
		u, err := addQueryParam(reqURL, m.auth.APIKey.Name, m.apiKey)
		if err != nil {
			return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
		}
		reqURL = u
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
	}
	if m.auth != nil && m.auth.APIKey.In == "header" && m.apiKey != "" {
		req.Header.Set(m.auth.APIKey.Name, m.apiKey)
	}

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("source %q: fetching manifest %s: %w", m.id, m.url, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source %q: fetching manifest %s: HTTP %d", m.id, m.url, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize+1))
	if err != nil {
		return nil, fmt.Errorf("source %q: reading manifest %s: %w", m.id, m.url, err)
	}
	if len(data) > maxManifestSize {
		return nil, fmt.Errorf("source %q: manifest %s exceeds %d bytes", m.id, m.url, maxManifestSize)
	}

	doc, err := parseManifest(data, m.allowHTTP)
	if err != nil {
		return nil, fmt.Errorf("source %q: manifest %s: %w", m.id, m.url, err)
	}
	return doc, nil
}

// addQueryParam returns rawURL with name=value appended to its query string,
// preserving existing parameters. Values are URL-escaped.
func addQueryParam(rawURL, name, value string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}
	q := u.Query()
	q.Set(name, value)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
