package app

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// AuthCapableSources returns every source registered on svc whose
// CapabilitiesOf(src).Auth is true, sorted by ID - the built-ins
// (unconditionally registered) alongside any auth-capable custom source.
// Shared by `lmm auth login`/`logout`'s interactive picker and error hint
// (cmd/lmm calls this directly) and by AuthStatus below, so the two can
// never disagree about which sources are auth-capable.
func AuthCapableSources(svc *core.Service) []source.ModSource {
	all := svc.ListSources()
	capable := make([]source.ModSource, 0, len(all))
	for _, src := range all {
		if source.CapabilitiesOf(src).Auth {
			capable = append(capable, src)
		}
	}
	sort.Slice(capable, func(i, j int) bool { return capable[i].ID() < capable[j].ID() })
	return capable
}

// AuthSourceStatus is one row of an AuthStatusReport: a registered
// auth-capable source and how (if at all) it is authenticated. Via
// distinguishes a stored token ("stored", from `lmm auth login`) from an
// environment variable ("env") - EnvVar and KeyMasked are populated
// alongside it. Authenticated is false and the other three fields are
// empty for a source with no key from either place.
type AuthSourceStatus struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Authenticated bool   `json:"authenticated"`
	Via           string `json:"via,omitempty"`
	EnvVar        string `json:"env_var,omitempty"`
	KeyMasked     string `json:"key_masked,omitempty"`
}

// OrphanedToken is a stored API key AuthStatus found with no auth-capable
// source it belongs to. Reason distinguishes a source that is still
// registered but no longer declares auth ("auth_not_declared" - e.g. a
// custom source's manifest dropped its `auth:` block) from one that isn't
// registered at all ("not_registered" - e.g. its definition file was
// deleted after `lmm auth login`); the two have different remedies.
// KeyMasked carries the masked key so the plain renderer can reproduce its
// "(key: ...)" text from this report alone.
type OrphanedToken struct {
	ID        string `json:"id"`
	Reason    string `json:"reason"`
	KeyMasked string `json:"key_masked"`
}

// AuthStatusReport is `lmm auth status --json`'s document (#309): every
// registered auth-capable source's authentication state
// (AuthCapableSources order, i.e. sorted by ID), then every stored token
// that belongs to none of them. The plain text `lmm auth status` prints is
// rebuilt from this report byte-identically.
type AuthStatusReport struct {
	Sources  []AuthSourceStatus `json:"sources"`
	Orphaned []OrphanedToken    `json:"orphaned"`
}

// AuthStatus assembles the `lmm auth status` report: one row per registered
// auth-capable source (built-in and custom, uniformly - sorted by ID via
// AuthCapableSources), then a pass surfacing stored tokens that don't
// belong to any auth-capable source (#309).
func AuthStatus(ctx context.Context, svc *core.Service) (*AuthStatusReport, error) {
	sources := AuthCapableSources(svc)
	registered := make(map[string]bool, len(sources))

	report := &AuthStatusReport{}
	for _, src := range sources {
		id := src.ID()
		registered[id] = true

		row := AuthSourceStatus{ID: id, Name: src.Name()}
		token, err := svc.GetSourceToken(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("checking %s: %w", id, err)
		}
		switch {
		case token != nil:
			row.Authenticated = true
			row.Via = "stored"
			row.KeyMasked = MaskAPIKey(token.APIKey)
		default:
			envKey := EnvKeyFor(src)
			if apiKey := os.Getenv(envKey); apiKey != "" {
				row.Authenticated = true
				row.Via = "env"
				row.EnvVar = envKey
				row.KeyMasked = MaskAPIKey(apiKey)
			}
		}
		report.Sources = append(report.Sources, row)
	}

	// Two distinct causes land here: the source is still registered but its
	// declaration dropped auth (svc.GetSource succeeds), or nothing
	// registered matches the ID at all (GetSource fails).
	tokens, err := svc.ListSourceTokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing stored tokens: %w", err)
	}
	for _, tok := range tokens {
		if registered[tok.SourceID] {
			continue
		}
		reason := "not_registered"
		if _, err := svc.GetSource(tok.SourceID); err == nil {
			reason = "auth_not_declared"
		}
		report.Orphaned = append(report.Orphaned, OrphanedToken{ID: tok.SourceID, Reason: reason, KeyMasked: MaskAPIKey(tok.APIKey)})
	}

	return report, nil
}

// MaskAPIKey returns a masked version of an API key (shows first 3 and last
// 3 chars). Keys of 8 characters or fewer are fully masked instead: showing
// 6 of 7-8 characters exposes most of the key, defeating the point of
// masking.
func MaskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:3] + "..." + key[len(key)-3:]
}
