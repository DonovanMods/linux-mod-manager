package core

import (
	"context"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// rowCopy says what copyRowToProfile writes: the copy's two flags, its link
// method, and whether it keeps the source row's own per-row settings.
type rowCopy struct {
	enabled, deployed bool
	// method is a fresh copy's link method; its zero value is what a
	// profile import has always recorded. carrySettings ignores it and keeps
	// the source row's own.
	method domain.LinkMethod
	// carrySettings keeps everything the source row says - update policy,
	// update history, convert-paks, skipped version, link method - and only
	// re-homes it.
	// A switch's mint (#60) and a profile duplicate (#438) want that: the
	// copy IS the same profile's intent under a new name. Unset builds the
	// row fresh, as importCachedMod builds one (#445 final gate F-D): what
	// a row says about ITS profile is not another profile's.
	carrySettings bool
}

// copyRowToProfile saves row - an installed row of some other profile - as
// profile's own: the one per-profile row mint every flow that copies a mod
// between profiles shares (a switch's ErrModNotFound fallback, a profile
// apply's cachedRowElsewhere enable, a profile import's AlreadyCached
// bucket, a profile duplicate and a mod copy, #437/#438).
//
// The copy describes the same cached bytes, so it carries row's file IDs
// and checksums (#372's rule, #514): the checksums are read BEFORE the
// save, because saveInstalledMod rewrites installed_mod_files and
// recordFileChecksums has to run after it. A checksum that cannot be read
// or written is a message without a "Warning: " prefix - never fatal, the
// files are correct either way - and the save's own failure is the error.
// AdoptedInPlace, External and ExternalPath are facts about the mod, so a
// fresh row keeps them too.
func (s *Service) copyRowToProfile(ctx context.Context, game *domain.Game, row domain.InstalledMod, profile string, c rowCopy) ([]string, error) {
	checksums, read := s.rowChecksums(ctx, game, row)
	msgs := make([]string, 0, len(read))
	for _, msg := range read {
		msgs = append(msgs, strings.TrimPrefix(msg, "Warning: "))
	}

	mod := row.Mod
	// Normalize GameID to the lmm game: a row read back carries it already,
	// but one built from a source's answer may name the source's own id.
	mod.GameID = game.ID
	var copied domain.InstalledMod
	if c.carrySettings {
		copied = row
		copied.Mod = mod
	} else {
		copied = domain.InstalledMod{
			Mod:            mod,
			UpdatePolicy:   domain.UpdateNotify,
			LinkMethod:     c.method,
			FileIDs:        row.FileIDs,
			AdoptedInPlace: row.AdoptedInPlace,
			External:       row.External,
			ExternalPath:   row.ExternalPath,
		}
	}
	copied.ProfileName = profile
	copied.Enabled = c.enabled
	copied.Deployed = c.deployed

	if err := s.saveInstalledMod(ctx, &copied); err != nil {
		return msgs, err
	}
	return append(msgs, s.recordFileChecksums(ctx, row.SourceID, row.ID, game.ID, profile, checksums)...), nil
}
