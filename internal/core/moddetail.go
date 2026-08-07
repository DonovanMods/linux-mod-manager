package core

import (
	"context"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/internal/storage/config"
)

// ModDetail is everything both interfaces need to render a mod's full detail:
// the source-side metadata plus, when the mod is installed in the named
// profile, its local install state. Composed here rather than in each caller
// because the install state spans three stores - the DB row (version,
// policy), the profile YAML (lock), and the game config (pak conversion
// eligibility) - and the CLI/TUI parity directive forbids each surface
// re-deriving that join for itself (#86).
type ModDetail struct {
	Mod       *domain.Mod
	Installed *InstalledDetail
}

// InstalledDetail is the local install state. Nil on ModDetail when the mod
// is not installed in the profile - an ordinary state, not an error.
type InstalledDetail struct {
	Version       string
	Profile       string
	UpdatePolicy  domain.UpdatePolicy
	Locked        bool
	LockedVersion string
	// ConvertPaks is nil when pak conversion does not apply to this mod at
	// all (not a merge-compile game, or no pak merge source) - distinct from
	// a non-nil pointer to false, which means "applies, and is off".
	ConvertPaks *bool
}

// ModDetail fetches modID from sourceID and joins whatever local install
// state exists for it in profile. The source fetch is a live network call for
// remote sources (Service.GetMod does not cache), so callers on a UI thread
// must run this off the render path.
func (s *Service) ModDetail(ctx context.Context, game *domain.Game, profile, sourceID, modID string) (*ModDetail, error) {
	mod, err := s.GetMod(ctx, sourceID, game.ID, modID)
	if err != nil {
		return nil, fmt.Errorf("mod not found: %w", err)
	}
	detail := &ModDetail{Mod: mod}

	// A GetInstalledMod failure means "not installed" - the ordinary case for
	// a mod browsed from search - so it omits the block rather than failing
	// the whole call (verbatim from doModShow's own convention).
	installed, err := s.GetInstalledMod(sourceID, modID, game.ID, profile)
	if err != nil {
		return detail, nil
	}

	info := &InstalledDetail{
		Version:      installed.Version,
		Profile:      profile,
		UpdatePolicy: installed.UpdatePolicy,
	}
	if game.DeployMode == domain.DeployCompile && s.ModHasPakMergeSource(installed) {
		v := installed.ConvertPaks
		info.ConvertPaks = &v
	}
	// Lock lives in the profile YAML, not the DB. A load failure degrades to
	// "unlocked" rather than failing the call - same as doModShow, which
	// ignores this error.
	if prof, perr := config.LoadProfile(s.ConfigDir(), game.ID, profile); perr == nil {
		if ref := prof.FindRef(sourceID, modID); ref != nil && ref.Locked {
			info.Locked = true
			info.LockedVersion = ref.Version
		}
	}
	detail.Installed = info
	return detail, nil
}
