package app

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
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
// ("stored", from `lmm auth login`) or the environment variable ("env").
// Authenticated is false and Via is empty for a source with no usable key
// from either place.
//
// WHAT THIS SAYS ABOUT THE KEY ITSELF (#79). A STORED credential is
// encrypted at rest and never appears in this report: the row carries
// KeyFingerprint - the first 8 hex of the key's SHA-256, enough to answer
// "is this still the one I pasted?" - and no KeyMasked at all. A key from
// the ENVIRONMENT is one lmm already holds in the clear (it is in the
// process environment either way), so that row keeps the masked form, which
// a user can recognise at a glance, AND gains the fingerprint, so the two
// kinds of row can be compared.
//
// Precisely, since it is a security claim (review 2): db.ListTokens DOES
// decrypt each row, because the fingerprint is over the KEY - it has to be,
// or a stored row and an environment row could not be compared, which is
// the whole point of carrying it on both. What the plaintext never does is
// escape the process: db.TokenInfo has no field to return it in, so a leak
// into this report could not compile.
//
// CreatedAt/UpdatedAt are the stored credential's timestamps - when it was
// first stored and when it was last replaced - and are zero for an
// environment key, which has no such history.
//
// Unreadable marks a stored row that exists but did not decrypt (the key
// file was replaced, or the row is damaged). Such a row is NOT
// authenticated - nothing can be sent with it - and the environment
// variable, if set, takes over as it would for a source with no row at all;
// the flag stays set either way, because "there is a dead credential here,
// log in again" is the actionable part.
type AuthSourceStatus struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Authenticated  bool      `json:"authenticated"`
	Via            string    `json:"via,omitempty"`
	EnvVar         string    `json:"env_var,omitempty"`
	KeyMasked      string    `json:"key_masked,omitempty"`
	KeyFingerprint string    `json:"key_fingerprint,omitempty"`
	Unreadable     bool      `json:"unreadable,omitzero"`
	CreatedAt      time.Time `json:"created_at,omitzero"`
	UpdatedAt      time.Time `json:"updated_at,omitzero"`
}

// OrphanedToken is a stored API key AuthStatus found with no auth-capable
// source it belongs to. Reason distinguishes a source that is still
// registered but no longer declares auth ("auth_not_declared" - e.g. a
// custom source's manifest dropped its `auth:` block) from one that isn't
// registered at all ("not_registered" - e.g. its definition file was
// deleted after `lmm auth login`); the two have different remedies.
//
// KeyFingerprint identifies the key so the plain renderer can reproduce its
// "(key ...)" text from this report alone. It replaced the pre-#79 masked
// key, which could only be produced by decrypting a credential nothing was
// going to use - the whole point of encrypting them at rest. Unreadable
// marks an orphan that did not decrypt at all; its fingerprint is empty.
type OrphanedToken struct {
	ID             string `json:"id"`
	Reason         string `json:"reason"`
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	Unreadable     bool   `json:"unreadable,omitzero"`
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

	// ONE read of the credential table for the whole report, and it is the
	// one that cannot hand back a key (#79): every stored row this report
	// mentions - a registered source's and an orphan's alike - is described
	// from this listing, which returns db.TokenInfo and has no field a
	// credential could travel in.
	//
	// A key-file-level failure (missing, wrong mode, malformed) comes back
	// as the error and takes the whole report with it, deliberately: the
	// answer is then about the installation rather than any one source, and
	// there is one remedy to print. Only a single row that will not decrypt
	// degrades to Readable:false - see db.ListTokens.
	tokens, err := svc.ListSourceTokens(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing stored tokens: %w", err)
	}
	stored := make(map[string]db.TokenInfo, len(tokens))
	for _, tok := range tokens {
		stored[tok.SourceID] = tok
	}

	report := &AuthStatusReport{}
	for _, src := range sources {
		id := src.ID()
		registered[id] = true

		envKey := EnvKeyFor(src)
		row := AuthSourceStatus{ID: id, Name: src.Name(), EnvVar: envKey}
		tok, hasRow := stored[id]
		row.Unreadable = hasRow && !tok.Readable

		switch apiKey := os.Getenv(envKey); {
		case hasRow && tok.Readable:
			row.Authenticated = true
			row.Via = "stored"
			row.KeyFingerprint = tok.Fingerprint
			row.CreatedAt, row.UpdatedAt = tok.CreatedAt, tok.UpdatedAt
		case apiKey != "":
			// Also the fallback for a row that will not decrypt: that
			// credential cannot authenticate anything, so it must not
			// shadow one that can.
			row.Authenticated = true
			row.Via = "env"
			row.KeyMasked = MaskAPIKey(apiKey)
			row.KeyFingerprint = db.TokenFingerprint(apiKey)
		}
		report.Sources = append(report.Sources, row)
	}

	// Two distinct causes land here: the source is still registered but its
	// declaration dropped auth (svc.GetSource succeeds), or nothing
	// registered matches the ID at all (GetSource fails).
	for _, tok := range tokens {
		if registered[tok.SourceID] {
			continue
		}
		reason := "not_registered"
		if _, err := svc.GetSource(tok.SourceID); err == nil {
			reason = "auth_not_declared"
		}
		report.Orphaned = append(report.Orphaned, OrphanedToken{
			ID:             tok.SourceID,
			Reason:         reason,
			KeyFingerprint: tok.Fingerprint,
			Unreadable:     !tok.Readable,
		})
	}

	return report, nil
}

// MaskAPIKey returns a masked version of an API key (shows first 3 and last
// 3 chars). Keys of 8 characters or fewer are fully masked instead: showing
// 6 of 7-8 characters exposes most of the key, defeating the point of
// masking.
//
// Since #79 this is only applied to a key lmm holds in the clear anyway -
// one read from the environment. A STORED credential is identified by
// db.TokenFingerprint instead, which needs no decryption at all.
func MaskAPIKey(key string) string {
	if len(key) <= 8 {
		return "***"
	}
	return key[:3] + "..." + key[len(key)-3:]
}
