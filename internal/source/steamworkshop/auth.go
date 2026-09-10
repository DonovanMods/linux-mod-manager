// Package steamworkshop: this file is Tier 2's credential half (#269 W2) -
// where the user's own Steam Web API key comes from, what it is called,
// and how lmm proves one works before storing it.
//
// The key is the USER'S, always. Valve's Web API Terms of Use make a key
// personal and confidential, so lmm embeds none, ships none, and shares
// none; every keyed call below carries a key the person running lmm
// obtained for themselves. That is the same bring-your-own-key posture the
// NexusMods and CurseForge sources already have, and the precedent every
// other Steam-adjacent tool follows.
package steamworkshop

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// envKey is the environment variable this source honours.
//
// It is Steam's conventional name rather than lmm's derived
// LMM_STEAMWORKSHOP_API_KEY, because a user who has a Steam Web API key at
// all almost certainly already exports it under this name for some other
// tool. app.ResolveAPIKey consults EnvKeyProvider before falling back to
// the derived convention, so naming it here is the whole change.
const envKey = "STEAM_WEB_API_KEY"

// queryFilesPath is IPublishedFileService's search endpoint - the one call
// in this package that REQUIRES a key, and the one lmm probes a candidate
// key against.
const queryFilesPath = "/IPublishedFileService/QueryFiles/v1/"

// validationAppID is Spacewar, Valve's public test app. Every account can
// query it, it always exists, and asking for one row of it is the cheapest
// truthful "does this key work?" question there is.
const validationAppID = "480"

var (
	_ source.EnvKeyProvider           = (*Source)(nil)
	_ source.AuthInstructionsProvider = (*Source)(nil)
	_ source.KeyValidator             = (*Source)(nil)
)

// EnvKey implements source.EnvKeyProvider.
func (s *Source) EnvKey() string { return envKey }

// AuthInstructions implements source.AuthInstructionsProvider: the setup
// steps `lmm auth login steamworkshop` and the web UI's Setup page print.
//
// The confidentiality sentence is not decoration. A Steam Web API key is
// tied to the person's own Steam account, and the failure mode lmm is
// warning against - pasting a key found in a forum post, or handing yours
// to someone else - is both a ToU violation and a real account risk.
func (s *Source) AuthInstructions() string {
	return "Get a free key at https://steamcommunity.com/dev/apikey. " +
		"The key is personal and confidential — never share it, and never paste someone else's."
}

// SetAPIKey stores the Steam Web API key used for the keyed Tier-2 calls.
// app.registerSource calls it once at startup with whatever ResolveAPIKey
// found; the keyless Tier-1 endpoints are unaffected either way.
//
// It also records the key's FINGERPRINT, which is all the search cache
// needs to keep one credential's answers from being served to another.
func (s *Source) SetAPIKey(key string) {
	s.client.http.SetAPIKey(key)
	s.client.keyID = keyFingerprint(key)
}

// IsAuthenticated reports whether a Steam Web API key is configured.
//
// `lmm source list`'s auth column reads it through app.authState, whose
// fallback for a source that does not implement it is "authenticated" -
// which for this source would be a flat lie in the overwhelmingly common
// case of a user who has never asked for a key.
func (s *Source) IsAuthenticated() bool { return s.client.http.IsAuthenticated() }

// keyFingerprint is the first 8 hex of a key's SHA-256, or "" for no key -
// the same shape app.AuthStatusReport uses to identify a stored credential
// it must never carry (#79).
func keyFingerprint(key string) string {
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])[:8]
}

// ValidateKey implements source.KeyValidator: the live check `lmm auth
// login steamworkshop` (and POST /api/v1/auth/steamworkshop) runs before
// storing a key.
//
// It is a KEYED call on purpose. Every other endpoint this package uses is
// keyless, so a keyless probe would return 200 for a key that is complete
// nonsense; QueryFiles is the endpoint that actually refuses one. A valid
// key answers 200 even when the query matches nothing at all, which is why
// the probe asks for a single row of Spacewar and ignores the result.
//
// The candidate key travels as a per-call parameter and is never written
// to the client, so validating a key the user then declines to store
// cannot leave the source authenticated with it.
func (s *Source) ValidateKey(ctx context.Context, key string) error {
	if key == "" {
		return errors.New("invalid Steam Web API key: the key is empty")
	}
	q := url.Values{}
	q.Set("query_type", "1")
	q.Set("numperpage", "1")
	q.Set("appid", validationAppID)

	var resp queryFilesResponse
	if err := s.client.keyed(key).DoJSON(ctx, "GET", queryFilesPath+"?"+q.Encode(), &resp); err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			// Valve's live-verified refusal is 403 "Please verify your key=
			// parameter"; the client maps it to ErrAuthRequired. The key is
			// deliberately absent from the message - a validator's error text
			// is passed straight through to the web UI (api_auth.go).
			return errors.New("invalid Steam Web API key")
		}
		return fmt.Errorf("validating Steam Web API key: %w", err)
	}
	return nil
}
