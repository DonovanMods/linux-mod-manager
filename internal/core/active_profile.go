package core

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// profileFlags is what a game's profile files say about which of them is
// active: every profile, by FILE name (the name every row and every load
// uses), the ones whose file says `is_default: true`, and why each file that
// could not be read at all could not be. Every list is sorted.
type profileFlags struct {
	gameID     string
	configDir  string
	names      []string
	flagged    []string
	unreadable map[string]error
}

// readProfileFlags reads every profile file of gameID. Only the profiles
// directory itself failing to list is an error: a file that cannot be read
// is recorded, for the caller to decide about.
func readProfileFlags(configDir, gameID string) (profileFlags, error) {
	names, err := config.ListProfiles(configDir, gameID)
	if err != nil {
		return profileFlags{}, err
	}
	slices.Sort(names)
	flags := profileFlags{gameID: gameID, configDir: configDir, names: names, unreadable: make(map[string]error)}
	for _, name := range names {
		profile, err := config.LoadProfile(configDir, gameID, name)
		if err != nil {
			flags.unreadable[name] = err
			continue
		}
		if profile.IsDefault {
			flags.flagged = append(flags.flagged, name)
		}
	}
	return flags, nil
}

// ambiguous reports whether the files name no single active profile: some
// cannot be read, or several are marked, or none is while several exist.
// A game with no profile file, or with one, readable and unmarked, is not
// ambiguous (coordinator ruling A on #445 F2): there is one answer.
func (f profileFlags) ambiguous() bool {
	if len(f.unreadable) > 0 {
		return true
	}
	switch len(f.flagged) {
	case 1:
		return false
	case 0:
		return len(f.names) > 1
	default:
		return true
	}
}

// active returns the game's active profile, or ErrActiveProfileUnknown -
// naming the cause and `lmm profile list` - when the files do not say which
// it is. A game with no profile file at all is "default", the profile every
// frontend resolves for it.
func (f profileFlags) active() (string, error) {
	if err := f.unknown(); err != nil {
		return "", err
	}
	switch {
	case len(f.flagged) == 1:
		return f.flagged[0], nil
	case len(f.names) == 1:
		return f.names[0], nil
	default:
		return "default", nil
	}
}

// unknown is active's refusal, or nil when the files name one profile.
func (f profileFlags) unknown() error {
	if !f.ambiguous() {
		return nil
	}
	list := fmt.Sprintf("`lmm profile list --game %s`", f.gameID)
	if len(f.unreadable) > 0 {
		var files []string
		var causes []error
		for _, name := range slices.Sorted(maps.Keys(f.unreadable)) {
			path, err := config.ProfilePath(f.configDir, f.gameID, name)
			if err != nil {
				path = name
			}
			files = append(files, path)
			causes = append(causes, f.unreadable[name])
		}
		// The causes stay in the chain: a caller that branches on why a
		// profile cannot be read (domain.ErrInvalidLinkMethod) still can.
		return fmt.Errorf("%w for %s: its profile file %s cannot be read, and it could be the active one - fix or remove it, then check with %s: %w",
			ErrActiveProfileUnknown, f.gameID, strings.Join(files, ", "), list, errors.Join(causes...))
	}
	switch len(f.flagged) {
	case 0:
		return fmt.Errorf("%w for %s: none of its profiles (%s) is marked `is_default: true` - %s shows them, and `lmm profile switch <name> --game %s` marks the one whose mods the game directory holds",
			ErrActiveProfileUnknown, f.gameID, strings.Join(f.names, ", "), list, f.gameID)
	default:
		return fmt.Errorf("%w for %s: its profiles %s are all marked `is_default: true` - %s shows them, and `lmm profile switch <name> --game %s` leaves just one marked",
			ErrActiveProfileUnknown, f.gameID, strings.Join(f.flagged, ", "), list, f.gameID)
	}
}
