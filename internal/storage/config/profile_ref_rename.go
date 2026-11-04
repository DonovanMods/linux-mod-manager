package config

import (
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// ErrModRefExists refuses a RenameProfileModRef whose new identity the
// profile already lists: renaming onto it would leave the document listing
// one mod twice, as two references that may disagree.
var ErrModRefExists = errors.New("mod already listed in profile")

// ModRefRename is one RenameProfileModRef: the reference to From* takes
// the identity To* and the installed-version record Version.
type ModRefRename struct {
	// FromSourceID and FromModID name the reference as the profile lists
	// it now.
	FromSourceID, FromModID string
	// ToSourceID and ToModID are the identity it is renamed to.
	ToSourceID, ToModID string
	// Version is the renamed reference's installed-version record.
	Version string
}

// RenameProfileModRef renames gameID/profileName's reference to
// r.FromSourceID/r.FromModID in place (#593, the profile half of a re-link):
// it keeps its index in the load order, takes r.To*'s identity and
// r.Version, and carries every other field it has - FileIDs, Locked,
// Disabled, Category and any a ModReference gains later - unchanged. Only
// the keys that change are edited in the file, so the reference keeps its
// comments and layout, as every in-place save does (SaveProfile). A mod
// listed more than once (a hand edit) has every copy renamed, so the copies
// never disagree about which mod they are.
//
// It does not consult Locked: whether a locked reference may be renamed is
// the caller's rule to enforce before calling.
//
// A profile that does not list the old identity returns
// domain.ErrModNotFound, and one that already lists the new identity
// ErrModRefExists; neither writes anything.
func RenameProfileModRef(configDir, gameID, profileName string, r ModRefRename) (SaveReport, error) {
	profile, err := LoadProfile(configDir, gameID, profileName)
	if err != nil {
		return SaveReport{}, err
	}
	from, to := domain.ModKey(r.FromSourceID, r.FromModID), domain.ModKey(r.ToSourceID, r.ToModID)
	found := false
	for _, ref := range profile.Mods {
		switch domain.ModKey(ref.SourceID, ref.ModID) {
		case from:
			found = true
		case to:
			return SaveReport{}, fmt.Errorf("renaming %s to %s in profile %q: %w", from, to, profileName, ErrModRefExists)
		}
	}
	if !found {
		return SaveReport{}, domain.ErrModNotFound
	}
	for i := range profile.Mods {
		ref := &profile.Mods[i]
		if domain.ModKey(ref.SourceID, ref.ModID) == from {
			ref.SourceID, ref.ModID, ref.Version = r.ToSourceID, r.ToModID, r.Version
		}
	}

	path, err := ProfilePath(configDir, profile.GameID, profile.Name)
	if err != nil {
		return SaveReport{}, err
	}
	save, err := planProfileSave(path, path, profile, map[string]string{from: to})
	if err != nil {
		return SaveReport{Path: path}, err
	}
	return save.write()
}
