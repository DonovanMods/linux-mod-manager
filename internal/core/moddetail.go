package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// ModDetail is everything both interfaces need to render a mod's full detail:
// the source-side metadata plus, when the mod is installed in the named
// profile, its local install state. Composed here rather than in each caller
// because the install state spans three stores - the DB row (version,
// policy), the profile YAML (lock), and the game config (pak conversion
// eligibility) - and frontends are thin adapters over core, so no frontend
// re-derives that join for itself (#86).
type ModDetail struct {
	Mod       *domain.Mod      `json:"mod,omitempty"`
	Installed *InstalledDetail `json:"installed,omitempty"`
	// Changelog is populated best-effort from the source's optional
	// source.ChangelogProvider capability (#87) - absent when the source
	// does not implement it, or when it has nothing to report. A provider
	// error never fails ModDetail; it lands in Notes instead.
	Changelog string `json:"changelog,omitzero"`
	// Notes carries non-fatal degradations (currently: a failed changelog
	// fetch) - populated only when something best-effort didn't work, never
	// present on a clean fetch.
	Notes []string `json:"notes,omitempty"`
}

// InstalledDetail is the local install state. Nil on ModDetail when the mod
// is not installed in the profile - an ordinary state, not an error.
type InstalledDetail struct {
	Version       string              `json:"version"`
	Profile       string              `json:"profile"`
	UpdatePolicy  domain.UpdatePolicy `json:"update_policy"`
	Locked        bool                `json:"locked"`
	LockedVersion string              `json:"locked_version,omitempty"`
	// ConvertPaks is nil when pak conversion does not apply to this mod at
	// all (not a merge-compile game, or no pak merge source) - distinct from
	// a non-nil pointer to false, which means "applies, and is off".
	ConvertPaks *bool `json:"convert_paks,omitempty"`
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
	detail.Changelog, detail.Notes = s.modChangelog(ctx, sourceID, game, modID, mod.Version)

	// Only a genuine "not installed" - the ordinary case for a mod browsed
	// from search - omits the Installed block. Any other failure is a real
	// DB error and must surface rather than masquerade as an uninstalled
	// mod, which the user cannot tell apart from an absence (#236).
	installed, err := s.GetInstalledMod(ctx, sourceID, modID, game.ID, profile)
	switch {
	case errors.Is(err, domain.ErrModNotFound):
		return detail, nil
	case err != nil:
		return nil, fmt.Errorf("loading installed mod: %w", err)
	}

	info := &InstalledDetail{
		Version:      installed.Version,
		Profile:      profile,
		UpdatePolicy: installed.UpdatePolicy,
	}
	if game.DeployMode == domain.DeployCompile && s.ModHasPakMergeSource(game, installed) {
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

// modChangelog best-effort fetches modID's changelog text from sourceID's
// optional source.ChangelogProvider capability (#87) - the same
// registry-lookup + type-assert pattern GetMod uses for the sourceID ->
// source-specific game ID mapping. A source that doesn't implement the
// capability, or a live call failure, both degrade to an empty changelog:
// the failure additionally appends a Note rather than propagating, since a
// changelog is decoration on ModDetail, not something callers should have
// to handle as an error.
func (s *Service) modChangelog(ctx context.Context, sourceID string, game *domain.Game, modID, version string) (changelog string, notes []string) {
	src, err := s.registry.Get(sourceID)
	if err != nil {
		return "", nil
	}
	provider, ok := src.(source.ChangelogProvider)
	if !ok {
		return "", nil
	}

	sourceGameID := game.ID
	if id, ok := game.SourceIDs[sourceID]; ok && id != "" {
		sourceGameID = id
	}

	changelog, err = provider.Changelog(ctx, sourceGameID, modID, version)
	if err != nil {
		return "", []string{fmt.Sprintf("changelog unavailable: %v", err)}
	}
	return changelog, nil
}
