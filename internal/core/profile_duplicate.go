package core

import (
	"context"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// DuplicateProfile copies gameID's profile source to a new profile newName
// (#438) - the clone `lmm profile export` + `import` used to be the only way
// to make. It returns the ProfileResult document `lmm profile duplicate
// --json` emits: the new profile as written.
//
// The copy is faithful. The document keeps every mod ref in load order with
// its version, file IDs, lock and disabled markers (#97, #431), the link
// method and its explicit flag, the hooks and their explicit flags, and the
// config overrides - everything but is_default: a duplicate is never the
// active profile by the act of being made. Every installed row of source
// gets a twin under newName (copyRowToProfile, keeping its settings and
// its enabled flag), so the duplicate lists as installed from the moment
// it exists; none is deployed, because the files in the game directory are
// the active profile's - the duplicate's own deploy or switch is what puts
// them there. An external (Steam Workshop) row stays deployed: Steam has
// the item in place whatever profile is active.
//
// It is one of the sanctioned single-step mutations rather than a
// Plan/Apply pair, like CreateProfile and RenameProfile: there is nothing
// to preview beyond the two names the caller typed, and nothing in the game
// directory changes. Refusals happen before anything is written and are
// rename's own: an unknown source is domain.ErrProfileNotFound, a newName
// taken on disk or by orphaned rows is ErrProfileExists
// (refuseOccupiedName), an unusable newName fails config's path validation.
//
// Once the first write lands, cancellation no longer stops the chain
// (completeProfileWrite, Ruling 16): the document and its rows are one
// state, so a duplicate never ends with a profile file whose mods have no
// rows. A row that fails to save removes what the duplicate already wrote,
// file included, so a failed duplicate leaves no half of one behind.
func (s *Service) DuplicateProfile(ctx context.Context, gameID, source, newName string) (*ProfileResult, error) {
	release, err := s.beginOp(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	pm := s.NewProfileManager()
	profile, err := config.LoadProfile(pm.configDir, gameID, source)
	if err != nil {
		return nil, err
	}
	// An unusable name is refused as itself, as Create refuses it, not as
	// a save failure after the chain has started.
	if _, err := config.LoadProfile(pm.configDir, gameID, newName); errors.Is(err, domain.ErrInvalidProfileName) {
		return nil, err
	}
	if err := pm.refuseOccupiedName(ctx, gameID, newName); err != nil {
		return nil, err
	}
	game, err := s.GetGame(gameID)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.GetInstalledMods(ctx, gameID, source)
	if err != nil {
		return nil, fmt.Errorf("reading %s's installed mods: %w", source, err)
	}

	dup := *profile
	dup.Name = newName
	dup.IsDefault = false
	dup.Mods = append([]domain.ModReference(nil), profile.Mods...)

	err = completeProfileWrite(ctx, func(ctx context.Context) error {
		if err := pm.save(&dup); err != nil {
			return fmt.Errorf("saving profile: %w", err)
		}
		for i, row := range rows {
			msgs, err := s.copyRowToProfile(ctx, game, row, newName, rowCopy{
				enabled: row.Enabled, deployed: row.External, carrySettings: true,
			})
			if err != nil {
				return s.undoDuplicate(ctx, gameID, newName, rows[:i],
					fmt.Errorf("recording %s under %s: %w", row.Name, newName, err))
			}
			// A checksum the twin could not carry is never fatal (the
			// bytes are the same either way); it goes where a save's own
			// notices go, the Service's always-on warning channel.
			if pm.warn != nil {
				for _, msg := range msgs {
					_, _ = fmt.Fprintf(pm.warn, "warning: %s\n", msg)
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &ProfileResult{Profile: dup}, nil
}

// undoDuplicate is DuplicateProfile's compensation for a row that would not
// save: it removes the rows already copied under newName and the profile
// file, and returns cause annotated with what it could not undo.
func (s *Service) undoDuplicate(ctx context.Context, gameID, newName string, copied []domain.InstalledMod, cause error) error {
	for _, row := range copied {
		if err := s.db.DeleteInstalledMod(ctx, row.SourceID, row.ID, gameID, newName); err != nil {
			return fmt.Errorf("duplicating profile: %w (compensation also failed: could not remove %s's row: %v)", cause, row.Name, err)
		}
	}
	if err := config.DeleteProfile(s.configDir, gameID, newName); err != nil {
		return fmt.Errorf("duplicating profile: %w (compensation also failed: could not remove %s: %v)", cause, newName, err)
	}
	return fmt.Errorf("duplicating profile: %w (compensated: removed the partial %s)", cause, newName)
}
