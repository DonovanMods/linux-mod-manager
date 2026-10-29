package source

import (
	"context"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// gameContextKey is the context key WithGame stores the game under.
type gameContextKey struct{}

// WithGame returns ctx carrying the lmm game a source call is made for
// (#544).
//
// A source call names the game only by the SOURCE's own identifier
// (domain.Game.SourceIDs), and two lmm games can share one: CurseForge
// serves every World of Warcraft flavor as game 1, so "wow-retail" and
// "wow-classic" both map it to "1". A source whose answer depends on the
// rest of the game's configuration - which of its files a game accepts -
// reads the game back with GameFromContext. It is a context value rather
// than a parameter so the ModSource interface stays the same for every
// source that has no such setting, which is all the others.
//
// Core sets it on every call it makes for a configured game. A source must
// treat its absence (a probe, a test, a call with no game) as "no per-game
// settings": today's behaviour. A nil game stores nothing.
func WithGame(ctx context.Context, game *domain.Game) context.Context {
	if game == nil {
		return ctx
	}
	return context.WithValue(ctx, gameContextKey{}, game)
}

// GameFromContext is the game WithGame stored in ctx, or nil. The game is
// shared, so a source must not modify it.
func GameFromContext(ctx context.Context) *domain.Game {
	game, _ := ctx.Value(gameContextKey{}).(*domain.Game)
	return game
}
