package core

import (
	"cmp"
	"errors"
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
)

// CatalogModRef names one installed mod in a report: its identity and the
// display name a frontend prints. UpdateCheckReport.CatalogMissing lists
// these for the mods their source's catalog no longer has (#539).
type CatalogModRef struct {
	SourceID string `json:"source_id"`
	ModID    string `json:"mod_id"`
	Name     string `json:"name,omitempty"`
}

// ModNotInCatalogError reports that a source's catalog has no mod by ModID
// (#539) - removed, delisted, or republished under a new ID. It is what
// Service.GetMod returns for a source's domain.ErrModNotFound, and what
// PlanUpdate returns for an installed mod its source's update check
// reported gone, so every surface says the same readable thing instead of
// the source's raw transport error. errors.Is(err, domain.ErrModNotFound)
// holds, so a caller that only branches on "not found" (serve's 404) needs
// nothing new.
type ModNotInCatalogError struct {
	SourceID string
	// SourceName is the source's display name, for the message; empty
	// falls back to SourceID.
	SourceName string
	ModID      string
	// Err is the source's own error, kept for errors.Is/As; may be nil.
	Err error
}

// Error names the mod and the catalog, and the two usual reasons.
func (e *ModNotInCatalogError) Error() string {
	return fmt.Sprintf("mod %s is not in %s's catalog (removed, or republished under a new ID)", e.ModID, cmp.Or(e.SourceName, e.SourceID))
}

// Unwrap exposes the source's own error.
func (e *ModNotInCatalogError) Unwrap() error { return e.Err }

// Is reports domain.ErrModNotFound whether or not Err is set.
func (e *ModNotInCatalogError) Is(target error) bool { return target == domain.ErrModNotFound }

// notInCatalog builds the ModNotInCatalogError for sourceID's modID, naming
// the source by its display name when the registry still has it.
func (s *Service) notInCatalog(sourceID, modID string, err error) *ModNotInCatalogError {
	e := &ModNotInCatalogError{SourceID: sourceID, ModID: modID, Err: err}
	if src, gerr := s.registry.Get(sourceID); gerr == nil {
		e.SourceName = src.Name()
	}
	return e
}

// classifyNotInCatalog turns a source's domain.ErrModNotFound for modID into
// a ModNotInCatalogError; anything else is returned unchanged.
func (s *Service) classifyNotInCatalog(sourceID, modID string, err error) error {
	if err == nil || !errors.Is(err, domain.ErrModNotFound) {
		return err
	}
	var typed *ModNotInCatalogError
	if errors.As(err, &typed) {
		return err
	}
	return s.notInCatalog(sourceID, modID, err)
}

// splitCatalogMissing takes the per-mod source.ModNotFoundError a source's
// update check returned for mods out of err (#539), naming each against the
// batch it was handed. It returns the ids found missing, and the remainder of
// err - nil when the missing mods were all that went wrong. A not-found for
// an id the batch never held names nothing installed, so it stays an error.
func splitCatalogMissing(batch []domain.InstalledMod, err error) (map[string]bool, error) {
	notFound, rest := source.SplitModNotFound(err)
	if len(notFound) == 0 {
		return nil, rest
	}
	inBatch := make(map[string]bool, len(batch))
	for _, m := range batch {
		inBatch[m.ID] = true
	}
	missing := make(map[string]bool, len(notFound))
	var stray []error
	for _, nf := range notFound {
		if inBatch[nf.ModID] {
			missing[nf.ModID] = true
		} else {
			stray = append(stray, nf)
		}
	}
	if len(stray) > 0 {
		rest = errors.Join(append([]error{rest}, stray...)...)
	}
	return missing, rest
}
