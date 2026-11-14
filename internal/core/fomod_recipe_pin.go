// Package core: this file is the FOMOD md5 pin for a profile's own flows
// (#609). An import pins a collection's recorded choices to the md5s its
// document records (ImportPlan.archiveMD5); every other flow that replays
// them - a profile apply or sync after a --no-install import, an install, a
// deploy's self-heal - finds the md5 in the recipe of the collection the
// profile came from (its collection origin), through the same cached
// recipe `lmm profile fill` reads. Nothing is persisted on the profile.
//
// The pin holds only while the recording is a curator's that lmm has never
// installed: choices with no config_sha256. Once installed, lmm records the
// installer's own hash, which is the lasting pin, and a user who answered a
// mismatch is never asked again.
//
// Known limit: an lmm collection document that is not a source's (a
// hand-written or third-party .lmmc carrying files[].md5) has no recipe to
// read back; imported with --no-install, its md5s are checked by no later
// flow.
package core

import (
	"context"
	"crypto/md5"
	"fmt"
	"strconv"
	"sync"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// recipePin is one collection revision's recipe facts, read once.
type recipePin struct {
	once  sync.Once
	files map[string]recipeFile
	err   error
}

// forgetRecipePins drops the running flow's recipes (beginOp's release).
func (s *Service) forgetRecipePins() {
	if s == nil {
		return
	}
	s.recipePinsMu.Lock()
	s.recipePins = nil
	s.recipePinsMu.Unlock()
}

// pinnedRecipeFacts is the recipe facts of origin's collection for game, read at
// most once per flow (recipePins).
func (s *Service) pinnedRecipeFacts(ctx context.Context, game *domain.Game, origin domain.CollectionOrigin) (map[string]recipeFile, error) {
	key := game.ID + "\x00" + origin.SourceID + "\x00" + origin.Slug + "\x00" + strconv.Itoa(origin.Revision)
	s.recipePinsMu.Lock()
	if s.recipePins == nil {
		s.recipePins = map[string]*recipePin{}
	}
	pin, ok := s.recipePins[key]
	if !ok {
		pin = &recipePin{}
		s.recipePins[key] = pin
	}
	s.recipePinsMu.Unlock()
	pin.once.Do(func() {
		m, err := s.collectionRecipeFor(ctx, game, origin)
		if err != nil {
			pin.err = err
			return
		}
		pin.files, _ = recipeFacts(m, origin.SourceID)
	})
	return pin.files, pin.err
}

// recipeMD5For is fomodInput.recipeMD5 for sourceID/modID in profileName
// of game: nil when the profile did not come from a collection on that
// source. The profile is read only when the pin is first asked for.
func (s *Service) recipeMD5For(ctx context.Context, game *domain.Game, profileName, sourceID, modID string) func(fileID string) (string, error) {
	if game == nil || profileName == "" {
		return nil
	}
	origin := sync.OnceValue(func() *domain.CollectionOrigin {
		p, err := s.NewProfileManager().Get(ctx, game.ID, profileName)
		if err != nil || p.Collection == nil || p.Collection.Origin == nil {
			return nil
		}
		o := *p.Collection.Origin
		if o.SourceID != sourceID || o.Slug == "" || o.Revision <= 0 {
			return nil
		}
		return &o
	})
	return func(fileID string) (string, error) {
		o := origin()
		if o == nil {
			return "", nil
		}
		files, err := s.pinnedRecipeFacts(ctx, game, *o)
		if err != nil {
			return "", fmt.Errorf("reading collection %s revision %d's recipe: %w", o.Slug, o.Revision, err)
		}
		sum := files[recipeKey(modID, fileID)].md5
		if !isHexDigest(sum, md5.Size) {
			return "", nil
		}
		return sum, nil
	}
}
