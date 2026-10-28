package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// ErrSourceNotAuthCapable is returned by ValidateSourceKey for an id that
// is not a registered auth-capable source: either nothing registered under
// it, or a source whose definition declares no auth. Typed because both
// frontends branch on it - the CLI into its "unsupported source (auth-capable
// sources: ...)" hint, `lmm serve` into a 404.
var ErrSourceNotAuthCapable = errors.New("not a registered auth-capable source")

// IsAuthCapableSource reports whether sourceID names a registered source
// whose definition declares auth. It answers from AuthCapableSources - the
// same query AuthStatus and the CLI's picker use - so no caller can
// disagree with the others about which sources can hold a key.
//
// It exists as a plain bool because `lmm serve` may not import
// internal/source (its own boundary ratchet), so it cannot ask
// source.CapabilitiesOf itself.
func IsAuthCapableSource(svc *core.Service, sourceID string) bool {
	_, ok := authCapableSource(svc, sourceID)
	return ok
}

// authCapableSource returns the registered auth-capable source with this
// id. Unexported: the *source.ModSource is what internal/app callers need
// and what internal/serve must never name.
func authCapableSource(svc *core.Service, sourceID string) (source.ModSource, bool) {
	for _, src := range AuthCapableSources(svc) {
		if src.ID() == sourceID {
			return src, true
		}
	}
	return nil, false
}

// HasKeyValidator reports whether sourceID's registered source performs a
// LIVE key check (source.KeyValidator). A frontend needs the answer BEFORE
// it has a key to check - the CLI prints "Validating... " only for a source
// that will actually validate, and reports "stored, validated on first use"
// rather than "Successfully authenticated" for one that will not. Same
// question ValidateSourceKey answers afterwards, asked of the same
// interface, so the two can never disagree.
func HasKeyValidator(svc *core.Service, sourceID string) bool {
	src, ok := authCapableSource(svc, sourceID)
	if !ok {
		return false
	}
	_, has := src.(source.KeyValidator)
	return has
}

// ValidateSourceKey performs sourceID's LIVE API-key check where the source
// implements one (source.KeyValidator: NexusMods and CurseForge today), and
// reports which of the two things happened:
//
//   - validated=true, err=nil  - a real call was made and the key passed.
//   - validated=false, err=nil - the source has no validator; a key for it
//     is stored unvalidated and exercised on first use, which is what a
//     custom source can honestly promise.
//   - err != nil               - the source declares no auth at all
//     (ErrSourceNotAuthCapable), or its validator refused/could not reach
//     its service. The error is the validator's own, unwrapped, so a caller
//     can classify it (a domain.ErrAuthRequired verdict vs. a transport
//     failure) and render its message.
//
// The distinction is not cosmetic: the CLI prints "Successfully
// authenticated" only for the first case and an honest "stored, validated
// on first use" for the second, and `lmm serve` refuses to STORE a key
// whenever err is non-nil. This lives in app, not core, because deciding it
// means naming source.KeyValidator, which core must not do for a concrete
// capability check either - and `lmm serve` cannot name it at all.
//
// The key is used and discarded: it is never logged, stored, or included in
// any error this function returns.
func ValidateSourceKey(ctx context.Context, svc *core.Service, sourceID, apiKey string) (validated bool, err error) {
	src, ok := authCapableSource(svc, sourceID)
	if !ok {
		return false, fmt.Errorf("%q is %w", sourceID, ErrSourceNotAuthCapable)
	}
	validator, has := src.(source.KeyValidator)
	if !has {
		return false, nil
	}
	if err := validator.ValidateKey(ctx, apiKey); err != nil {
		return false, err
	}
	return true, nil
}
