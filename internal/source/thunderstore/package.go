// Package thunderstore: this file is the PACKAGE model - the four ModSource
// reads that answer "what is this mod, what versions does it have, and
// where do I get one" (#360 §3.2, §3.3, §3.4).
//
// Every one of them is answered from the local index with NO request of any
// kind: index.json holds a searchable row per package and the byte range of
// its full record in packages.jsonl, so a metadata read is a map lookup and
// a version list is one positioned read of a file on disk. That is the
// whole reason the index carries a detail store rather than only a search
// projection.
//
// The shape that makes the rest of lmm work here unchanged: a Thunderstore
// package version IS one file. There is no file list, no primary-file
// choice and no optional-versus-main, so GetModFiles returns one
// domain.DownloadableFile per VERSION - which is exactly what
// core.ResolveVersionFiles (#96), `lmm mod files`, the rollback flow and
// the SPA's versions table already assume.
package thunderstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// GetMod returns one package's metadata. community is the Thunderstore
// community slug core has already translated the game into; modID is the
// package's full_name.
//
// It is answered from the index ROW, not from the detail record, and
// deliberately so: the row is what a search result was built from, so a
// hit the user clicked and the mod `lmm install` then resolves are
// guaranteed to be the same document rather than two mappings that can
// drift.
func (s *Source) GetMod(ctx context.Context, community, modID string) (*domain.Mod, error) {
	row, _, err := s.rowFor(ctx, community, modID)
	if err != nil {
		return nil, err
	}
	mod := modFromRow(community, row)
	return &mod, nil
}

// GetModFiles returns one downloadable file per version, newest first -
// Thunderstore's own order, which everything downstream relies on.
//
// Size is EXACT (file_size, to the byte), which is why this source
// implements ExactFileSizer; SHA256 is empty because Thunderstore publishes
// no checksum, and an exact size is what core verifies a completed download
// against instead.
func (s *Source) GetModFiles(ctx context.Context, mod *domain.Mod) ([]domain.DownloadableFile, error) {
	rec, err := s.recordFor(ctx, mod.GameID, mod.ID)
	if err != nil {
		return nil, err
	}
	ns, name, ok := SplitPackage(rec.FullName)
	if !ok {
		return nil, fmt.Errorf("source %q: %q is not a Thunderstore package name: %w",
			sourceID, rec.FullName, domain.ErrModNotFound)
	}
	display := displayName(name)

	files := make([]domain.DownloadableFile, 0, len(rec.Versions))
	for i, v := range rec.Versions {
		files = append(files, domain.DownloadableFile{
			ID:        v.Version,
			Name:      display + " " + v.Version,
			FileName:  fmt.Sprintf("%s-%s-%s.zip", ns, name, v.Version),
			Version:   v.Version,
			Size:      v.FileSize,
			IsPrimary: i == 0,
			Category:  "MAIN",
		})
	}
	return files, nil
}

// GetDownloadURL builds the download URL for one version. It is pure string
// construction - no token, no expiry, no second round trip - which is
// exactly why fileID is VALIDATED against the package's own version list
// first: without that, a caller could synthesise a plausible URL for a
// version that was never published, and the only thing that would notice is
// a 404 halfway through an install.
//
// The URL is built against the source's configured base rather than the
// production constant, because unlike SourceURL and PictureURL - which are
// links for a HUMAN to follow and always name the real site - this one is a
// request lmm makes, and a test must be able to serve it.
func (s *Source) GetDownloadURL(ctx context.Context, mod *domain.Mod, fileID string) (string, error) {
	rec, err := s.recordFor(ctx, mod.GameID, mod.ID)
	if err != nil {
		return "", err
	}
	if !recordHasVersion(rec, fileID) {
		return "", fmt.Errorf("source %q: package %q has no version %q: %w",
			sourceID, mod.ID, fileID, domain.ErrModNotFound)
	}
	ns, name, ok := SplitPackage(rec.FullName)
	if !ok {
		return "", fmt.Errorf("source %q: %q is not a Thunderstore package name: %w",
			sourceID, rec.FullName, domain.ErrModNotFound)
	}
	return fmt.Sprintf("%s/package/download/%s/%s/%s/", s.client.baseURL, ns, name, fileID), nil
}

// ExactFileSizes implements source.ExactFileSizer: Thunderstore reports
// file_size to the byte, and publishes no checksum at all - so the size is
// the only thing a completed download can be verified against, and core is
// entitled to fail a short body loudly rather than install a truncated zip.
func (s *Source) ExactFileSizes() bool { return true }

// recordHasVersion reports whether the package actually published fileID.
func recordHasVersion(rec packageRecord, fileID string) bool {
	for _, v := range rec.Versions {
		if v.Version == fileID {
			return true
		}
	}
	return false
}

// displayName is a package name as a human reads it. Thunderstore names
// cannot contain a space, so authors use underscores; every other surface
// in lmm shows a name, not an identifier.
func displayName(name string) string { return strings.ReplaceAll(name, "_", " ") }

// rowFor resolves one package's index row, building or refreshing the index
// first exactly as Search does - correctness must not depend on something
// else having primed it.
//
// A package the index does not hold is domain.ErrModNotFound, never a
// source failure: "this community does not publish that package" and "lmm
// could not ask" are different facts, and a caller branches on them
// differently.
func (s *Source) rowFor(ctx context.Context, community, modID string) (indexRow, *residentIndex, error) {
	if err := validateCommunity(community); err != nil {
		return indexRow{}, nil, err
	}
	wm, rows, present, err := s.ensureIndex(ctx, community, false, nil)
	if !present {
		return indexRow{}, nil, err
	}
	// err here is a refresh that failed over an index still worth reading;
	// the stale copy answers, exactly as it does for a search.
	idx, err := s.residentFor(community, wm, rows)
	if err != nil {
		return indexRow{}, nil, indexUnavailable(community, err)
	}
	row, ok := idx.row(modID)
	if !ok {
		return indexRow{}, nil, fmt.Errorf("source %q: community %q publishes no package %q: %w",
			sourceID, community, modID, domain.ErrModNotFound)
	}
	return row, idx, nil
}

// recordFor resolves one package's FULL record - every version, with its
// size, its publication date and its dependencies - out of packages.jsonl,
// addressed by the byte range its index row carries.
func (s *Source) recordFor(ctx context.Context, community, modID string) (packageRecord, error) {
	row, _, err := s.rowFor(ctx, community, modID)
	if err != nil {
		return packageRecord{}, err
	}
	rec, err := s.store.readRecord(community, row)
	if err != nil {
		return packageRecord{}, indexUnavailable(community, err)
	}
	return rec, nil
}
