// Package core: this file holds #592's mod categories - the effective
// category a mod list groups by, the one grouping rule both frontends share,
// the user-category mutation, and the update check's backfill of the
// source's category name.
package core

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// CategorySourceUser and CategorySourceSource are the values of a listing's
// CategorySource (#592): where its EffectiveCategory came from - the user's
// category on the profile ref, or the source's category name on the
// installed row. A mod with neither has an empty CategorySource.
const (
	CategorySourceUser   = "user"
	CategorySourceSource = "source"
)

// MaxCategoryLength is the longest user category SetModCategory accepts, in
// runes.
const MaxCategoryLength = 64

// ErrInvalidCategory is what an *InvalidCategoryError matches with
// errors.Is.
var ErrInvalidCategory = errors.New("invalid category")

// InvalidCategoryError is SetModCategory's refusal of a category name: one
// longer than MaxCategoryLength runes, or one with a control character
// (a newline, a tab) in it. Category is the name as given, after trimming.
type InvalidCategoryError struct {
	Category string
	Reason   string
}

// Error names the category and why it was refused.
func (e *InvalidCategoryError) Error() string {
	return fmt.Sprintf("invalid category %q: %s", e.Category, e.Reason)
}

// Is makes errors.Is(err, ErrInvalidCategory) true for an
// *InvalidCategoryError.
func (e *InvalidCategoryError) Is(target error) bool { return target == ErrInvalidCategory }

// NormalizeCategory is category as SetModCategory records it: surrounding
// whitespace trimmed, and refused with an *InvalidCategoryError when it is
// longer than MaxCategoryLength runes or carries a control character. An
// empty result (an empty or all-space name) means "clear". Exported so a
// frontend can validate input before it submits it, by the same rule.
func NormalizeCategory(category string) (string, error) {
	category = strings.TrimSpace(category)
	if !utf8.ValidString(category) {
		return "", &InvalidCategoryError{Category: category, Reason: "not valid UTF-8"}
	}
	if n := utf8.RuneCountInString(category); n > MaxCategoryLength {
		return "", &InvalidCategoryError{Category: category, Reason: fmt.Sprintf("%d characters, the most is %d", n, MaxCategoryLength)}
	}
	if strings.IndexFunc(category, unicode.IsControl) >= 0 {
		return "", &InvalidCategoryError{Category: category, Reason: "control characters are not allowed"}
	}
	return category, nil
}

// effectiveCategory is a mod's group membership: the user's category when
// set, else the source's category name, else none.
func effectiveCategory(userCategory, sourceName string) (name, from string) {
	switch {
	case userCategory != "":
		return userCategory, CategorySourceUser
	case sourceName != "":
		return sourceName, CategorySourceSource
	}
	return "", ""
}

// userCategories is each of profile's mods' user category, keyed by
// domain.ModKey and decided by its first reference (firstRefs). A nil
// profile has none.
func userCategories(profile *domain.Profile) map[string]string {
	out := map[string]string{}
	if profile == nil {
		return out
	}
	for key, ref := range firstRefs(profile.Mods) {
		if ref.Category != "" {
			out[key] = ref.Category
		}
	}
	return out
}

// ModCategoryGroup is one group of GroupModListByCategory: the mods whose
// EffectiveCategory is Name, in the order they were given. Name is empty for
// the mods with no category, which a frontend labels "Uncategorized".
type ModCategoryGroup struct {
	Name string       `json:"name"`
	Mods []ModListing `json:"mods"`
}

// GroupModListByCategory groups mods by their EffectiveCategory (#592) - the
// one grouping rule the CLI's `--group-by category` and the web library
// share. Within a group, mods keep the order given (a ModList's load order).
// Groups are sorted by name case-insensitively, ties broken by the exact
// name, and the group of mods with no category comes last. Names are
// compared exactly: "UI" and "ui" are two groups, and the user category is
// how a user merges them. ModList itself is never grouped: a group's order
// is not the deploy order.
func GroupModListByCategory(mods []ModListing) []ModCategoryGroup {
	index := map[string]int{}
	var groups []ModCategoryGroup
	for _, m := range mods {
		i, ok := index[m.EffectiveCategory]
		if !ok {
			i = len(groups)
			index[m.EffectiveCategory] = i
			groups = append(groups, ModCategoryGroup{Name: m.EffectiveCategory})
		}
		groups[i].Mods = append(groups[i].Mods, m)
	}
	slices.SortStableFunc(groups, func(a, b ModCategoryGroup) int {
		switch {
		case a.Name == "" || b.Name == "":
			// The uncategorised group sorts after every named one.
			return cmp.Compare(boolRank(a.Name == ""), boolRank(b.Name == ""))
		}
		return cmp.Or(cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.Name, b.Name))
	})
	return groups
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ListCategories is the distinct effective categories of the mods installed
// in game/profileName (#592), sorted the way GroupModListByCategory sorts
// its groups, with no empty name - the suggestions a category picker or a
// shell completion offers.
func (s *Service) ListCategories(ctx context.Context, game *domain.Game, profileName string) ([]string, error) {
	list, err := s.ListMods(ctx, game, profileName)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, g := range GroupModListByCategory(list.Mods) {
		if g.Name != "" {
			names = append(names, g.Name)
		}
	}
	return names, nil
}

// SetModCategory sets sourceID/modID's user category in gameID/profileName
// (#592), or clears it when category is empty (or only whitespace). The
// name is NormalizeCategory'd first, so a refusal is an
// *InvalidCategoryError (errors.Is ErrInvalidCategory) and writes nothing.
// The category is recorded on the profile ref - user intent, carried by
// export/import/duplicate/copy like the disabled marker - and overrides the
// source's category name wherever the mod list is grouped. A single
// beginOp-gated step with nothing to preview, like SetModLock. A mod that is
// not installed in the profile, or that the profile does not list, is
// domain.ErrModNotFound.
func (s *Service) SetModCategory(ctx context.Context, sourceID, modID, gameID, profileName, category string) (*ModSettingResult, error) {
	category, err := NormalizeCategory(category)
	if err != nil {
		return nil, err
	}
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err := s.GetInstalledMod(ctx, sourceID, modID, gameID, profileName); err != nil {
		return nil, err
	}
	if err := s.NewProfileManager().SetModCategory(ctx, gameID, profileName, sourceID, modID, category); err != nil {
		return nil, err
	}
	return s.modSettingResult(ctx, sourceID, modID, gameID, profileName)
}

// SetModCategory records category as sourceID/modID's user category on its
// profile ref (#592); "" clears it, and with the key's `omitempty` removes
// it, so a profile whose categories are all cleared is byte-identical to
// one that never had any. SetModDisabled's shape exactly: nothing else on
// the ref moves, every copy of a mod listed twice is written, an unchanged
// document is not rewritten, and a mod the profile does not list is
// domain.ErrModNotFound. category is recorded as given - Service.
// SetModCategory validates it.
func (pm *ProfileManager) SetModCategory(ctx context.Context, gameID, profileName, sourceID, modID, category string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	profile, err := config.LoadProfile(pm.configDir, gameID, profileName)
	if err != nil {
		return err
	}
	found, changed := false, false
	for i := range profile.Mods {
		if profile.Mods[i].SourceID == sourceID && profile.Mods[i].ModID == modID {
			found = true
			changed = changed || profile.Mods[i].Category != category
			profile.Mods[i].Category = category
		}
	}
	switch {
	case !found:
		return fmt.Errorf("mod %s:%s not in profile %q: %w", sourceID, modID, profileName, domain.ErrModNotFound)
	case !changed:
		return nil
	}
	return pm.save(profile)
}

// fetchedCategories collects the category names an update check's sources
// reported for the mods they fetched (source.ReportFetchedMod), keyed by
// domain.ModKey. Sources may report from several goroutines.
type fetchedCategories struct {
	mu    sync.Mutex
	names map[string]string
}

func (f *fetchedCategories) observe(mod domain.Mod) {
	if mod.CategoryName == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.names == nil {
		f.names = map[string]string{}
	}
	f.names[domain.ModKey(mod.SourceID, mod.ID)] = mod.CategoryName
}

// backfillCategoryNames records on each of installed's rows the category
// name its source reported in this update check when it is non-empty and
// differs from the stored one (#592) - a single-column write that touches no
// other column and never the profile. It is best-effort, so a failed write
// is logged, never returned: the name is display metadata, and the check it
// rides on must not fail over it. It costs no request - the names are what
// the check fetched anyway.
func (s *Service) backfillCategoryNames(ctx context.Context, gameID string, installed []domain.InstalledMod, fetched *fetchedCategories) {
	fetched.mu.Lock()
	names := fetched.names
	fetched.mu.Unlock()
	if len(names) == 0 || ctx.Err() != nil {
		return
	}
	for _, mod := range installed {
		name := names[domain.ModKey(mod.SourceID, mod.ID)]
		if name == "" || name == mod.CategoryName {
			continue
		}
		if err := s.db.SetModCategoryName(ctx, mod.SourceID, mod.ID, gameID, mod.ProfileName, name); err != nil && !errors.Is(err, domain.ErrModNotFound) {
			s.logger().Debug("recording category name", "mod", domain.ModKey(mod.SourceID, mod.ID), "err", err)
		}
	}
}

// withFetchedCategories returns ctx carrying an observer that collects the
// category names a source reports into the returned fetchedCategories.
func withFetchedCategories(ctx context.Context) (context.Context, *fetchedCategories) {
	fetched := &fetchedCategories{}
	return source.WithFetchedModObserver(ctx, fetched.observe), fetched
}
