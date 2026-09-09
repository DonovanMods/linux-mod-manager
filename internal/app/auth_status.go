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
// auth-capable source and how (if at all) it is authenticated. EnvVar
// names the environment variable this source honours - filled for every
// row, whether or not it is the ACTIVE credential (#333 Minor #5: the web
// UI's Auth section needs it to show the "or set <ENV_VAR>" hint on a row
// that has never been authenticated, not only one that already is via
// env). Via distinguishes which credential is ACTIVE: a stored token
// ("stored", from `lmm auth login`) or the environment variable
// ("env") - KeyMasked is populated alongside Via. Authenticated is false
// and Via/KeyMasked are empty for a source with no key from either place.
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

	// RestartRequired reports that the credential write this report answers
	// DID happen, but the running process is still using the old one
	// (#334, carried from the Unit 7 review). It is never set by AuthStatus
	// itself - a plain status read has no such event to report - only by a
	// caller that attempted a live re-key and could not complete it:
	// today `lmm serve`'s login/logout routes, whose RekeySource swap can
	// fail outright or fail to get the mutation gate within its grace.
	//
	// Before this field, that case answered "authenticated" and said
	// nothing: the response was true about the stored key and silent about
	// the fact that nothing would use it until a restart, with only a
	// server-side WARN to show for it. A frontend renders this as exactly
	// that - the key is saved, restart to pick it up - rather than the
	// wire simply carrying no such signal.
	//
	// omitzero: a report from a surface that never attempts a live swap
	// (`lmm auth status --json`, `lmm auth login --json`) carries no key at
	// all rather than a bare false.
	RestartRequired bool `json:"restart_required,omitzero"`
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

		envKey := EnvKeyFor(src)
		row := AuthSourceStatus{ID: id, Name: src.Name(), EnvVar: envKey}
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
			if apiKey := os.Getenv(envKey); apiKey != "" {
				row.Authenticated = true
				row.Via = "env"
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
