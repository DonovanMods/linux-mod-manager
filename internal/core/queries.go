package core

// queries.go holds core's read-only query types: the documents a frontend
// renders for `lmm list`, `lmm status`, `lmm search`, `lmm game list` and
// `lmm verify` (v2 Phase 3 Task 3, #301). Each one exists because the join
// behind it spans more than one store - the DB row, the profile YAML, the
// game config, the source registry - and frontends are thin adapters over
// core, so no frontend re-derives that join for itself (the same rule
// ModDetail was built under, #86). Every type carries snake_case json tags
// and a golden in testdata/json: these ARE the wire contract the CLI's
// --json switches to in Unit O, and `lmm serve` renders after that.
//
// Nothing here writes to stdout/stderr or reads stdin.

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/internal/domain"
)

// ModListing is one row of a ModList: the installed mod itself plus the
// state that lives outside its DB row.
//
//   - Locked/LockedVersion come from the profile YAML ref (#97), not the DB.
//     LockedVersion is the ref's own version - which may differ from the
//     embedded InstalledMod.Version - and is only ever set when Locked is
//     true.
//   - ConvertPaks is nil when pak conversion does not apply to this mod at
//     all (not a merge-compile game, or no pak merge source), distinct from
//     a non-nil pointer to false meaning "applies, and is off" - the same
//     tri-state InstalledDetail.ConvertPaks uses. It deliberately shadows
//     the embedded InstalledMod.ConvertPaks bool: the pointer is the
//     answer a listing needs (it can say "not applicable"), and being the
//     shallower field it also wins the `convert_paks` JSON key, so the
//     wire shape carries the tri-state rather than the raw flag.
type ModListing struct {
	domain.InstalledMod
	Locked        bool   `json:"locked"`
	LockedVersion string `json:"locked_version,omitempty"`
	ConvertPaks   *bool  `json:"convert_paks,omitempty"`
}

// ModList is everything `lmm list` renders: the mods installed in one
// profile, in the profile's load order.
type ModList struct {
	GameID  string       `json:"game_id"`
	Profile string       `json:"profile"`
	Mods    []ModListing `json:"mods"`
}

// ListMods returns the mods installed in profileName, in the profile's load
// order - the order that decides merge precedence (later = merged later =
// wins), not the DB's installed_at order (#201). A mod installed but absent
// from the load order is still listed (never silently dropped), placed
// first: OrderByProfile, not the deploy-only
// GetInstalledModsInProfileOrder, which deliberately omits it.
//
// A genuinely missing profile.yaml (domain.ErrProfileNotFound) is tolerated:
// a fresh profile with no YAML on disk yet simply has nothing locked and no
// load order. Any OTHER profile-load failure - including #172's fail-loud
// link_method validation - surfaces instead of silently degrading into a
// listing that shows no locks and treats every mod as absent from the load
// order (#203 release review).
func (s *Service) ListMods(ctx context.Context, game *domain.Game, profileName string) (*ModList, error) {
	mods, err := s.GetInstalledMods(ctx, game.ID, profileName)
	if err != nil {
		return nil, fmt.Errorf("getting installed mods: %w", err)
	}

	profile, err := s.NewProfileManager().Get(game.ID, profileName)
	if err != nil && !errors.Is(err, domain.ErrProfileNotFound) {
		return nil, fmt.Errorf("loading profile: %w", err)
	}

	// One precomputed map rather than a FindRef scan per mod: this loops
	// over every installed mod.
	lockedByKey := map[string]domain.ModReference{}
	if profile != nil {
		for _, ref := range profile.Mods {
			if ref.Locked {
				lockedByKey[domain.ModKey(ref.SourceID, ref.ModID)] = ref
			}
		}
	}

	ordered := OrderByProfile(profile, mods)
	list := &ModList{GameID: game.ID, Profile: profileName, Mods: make([]ModListing, len(ordered))}
	for i := range ordered {
		mod := ordered[i]
		row := ModListing{InstalledMod: mod}
		if ref, ok := lockedByKey[domain.ModKey(mod.SourceID, mod.ID)]; ok {
			row.Locked = true
			row.LockedVersion = ref.Version
		}
		if game.DeployMode == domain.DeployCompile && s.ModHasPakMergeSource(game, &mod) {
			v := mod.ConvertPaks
			row.ConvertPaks = &v
		}
		list.Mods[i] = row
	}
	return list, nil
}
