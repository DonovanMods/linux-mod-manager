package domain

import (
	"errors"
	"fmt"
	"slices"
)

// GameCurseForge is a game's CurseForge-specific settings (#544) - the
// games.yaml `curseforge:` block.
//
// CurseForge serves several games under one game id: World of Warcraft is
// game 1 for Retail, Classic Era, Cataclysm Classic and the rest, and what
// tells their files apart is each file's own classification - the
// gameVersionTypeId on its sortableGameVersions. GameVersionTypes names the
// ones THIS game accepts, so a Classic install is never offered, installed
// or updated to a Retail file. It is the source's own vocabulary, not a
// list of WoW flavors lmm knows about: any CurseForge game whose files vary
// by version type can use it.
type GameCurseForge struct {
	// GameVersionTypes are the CurseForge gameVersionTypeIds the game
	// accepts. Every id is positive. Empty is never stored: a game with no
	// ids has no block at all (nil), which is "every file", today's
	// behaviour.
	GameVersionTypes []int `json:"game_version_types"`
}

// ErrInvalidCurseForgeVersionType reports a CurseForge game version type id
// that is not a positive integer.
var ErrInvalidCurseForgeVersionType = errors.New("invalid CurseForge game version type")

// NewGameCurseForge validates ids and returns the block a game carries for
// them: nil when there are none (no setting), otherwise the ids with
// duplicates dropped, in the order given. A non-positive id wraps
// ErrInvalidCurseForgeVersionType.
func NewGameCurseForge(ids []int) (*GameCurseForge, error) {
	var out []int
	for _, id := range ids {
		if id <= 0 {
			return nil, fmt.Errorf("%w: %d (a gameVersionTypeId is a positive integer)", ErrInvalidCurseForgeVersionType, id)
		}
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return &GameCurseForge{GameVersionTypes: out}, nil
}

// CurseForgeVersionTypes is the game's accepted CurseForge game version
// type ids; nil when it accepts every file. Safe on a nil game.
func (g *Game) CurseForgeVersionTypes() []int {
	if g == nil || g.CurseForge == nil {
		return nil
	}
	return g.CurseForge.GameVersionTypes
}
