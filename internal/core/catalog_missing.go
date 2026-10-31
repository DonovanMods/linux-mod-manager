package core

import (
	"cmp"
	"context"
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
	// Successors are the mods in the same source that look like this one
	// republished under a new ID (#590): see CatalogSuccessor. One is a
	// suggestion; several are ambiguous, listed and none picked. Never acted
	// on by core. omitempty.
	Successors []CatalogSuccessor `json:"successors,omitempty"`
}

// CatalogSuccessor is one likely new identity of a mod its source's catalog
// no longer has (#590): a mod in the same source with the same name and the
// same author, both compared folded (case, spacing and punctuation aside).
// A frontend offers it as the target of a re-link (`lmm mod edit
// --to-source-id`, the web UI's mod_relink job); nothing re-links on its
// own, because a name and an author are evidence, not proof.
type CatalogSuccessor struct {
	ModID   string `json:"mod_id"`
	Name    string `json:"name,omitempty"`
	Author  string `json:"author,omitempty"`
	Version string `json:"version,omitempty"`
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

// catalogSuccessorSearchSize is how many hits the one successor search per
// gone mod asks for: a name search ranks an exact name near the top, and a
// candidate past this many same-named hits is not one to suggest anyway.
const catalogSuccessorSearchSize = 50

// catalogSuccessors looks in gone's own source for the mods that look like
// gone republished under a new ID (#590): one name search (the source's own,
// or its local index, #360/#410 - whatever SearchMods answers from), kept to
// the hits with the same folded name and the same folded author. A gone mod
// with no recorded name or author has no successor: "same name and author"
// is the claim every frontend makes of one, and it must be true. When some
// candidates carry the installed version or a newer one, an older copy is
// dropped; otherwise every candidate stays. Best-effort: a failed search
// suggests nothing, and the caller's report stands as it was.
func (s *Service) catalogSuccessors(ctx context.Context, gameID string, gone domain.Mod) []CatalogSuccessor {
	nameKey, authorKey := searchNameKey(gone.Name), searchNameKey(gone.Author)
	if nameKey == "" || authorKey == "" {
		return nil
	}
	result, err := s.SearchMods(ctx, gone.SourceID, gameID, gone.Name, "", nil, 0, catalogSuccessorSearchSize)
	if err != nil {
		return nil
	}
	var all, current []CatalogSuccessor
	seen := make(map[string]bool)
	for _, m := range result.Mods {
		if m.ID == gone.ID || seen[m.ID] || searchNameKey(m.Name) != nameKey || searchNameKey(m.Author) != authorKey {
			continue
		}
		seen[m.ID] = true
		c := CatalogSuccessor{ModID: m.ID, Name: m.Name, Author: m.Author, Version: m.Version}
		all = append(all, c)
		if gone.Version == "" || m.Version == "" || domain.CompareVersions(m.Version, gone.Version) >= 0 {
			current = append(current, c)
		}
	}
	if len(current) > 0 {
		return current
	}
	return all
}

// fillCatalogSuccessors stamps each of refs with its successors (#590),
// reading the gone mod's name, author and version from installed.
func (s *Service) fillCatalogSuccessors(ctx context.Context, gameID string, installed []domain.InstalledMod, refs []CatalogModRef) {
	for i := range refs {
		for j := range installed {
			if installed[j].SourceID == refs[i].SourceID && installed[j].ID == refs[i].ModID {
				refs[i].Successors = s.catalogSuccessors(ctx, gameID, installed[j].Mod)
				break
			}
		}
	}
}
