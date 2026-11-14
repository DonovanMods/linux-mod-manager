// Package core: deploy_prefix.go is #636's third deploy root - mod content
// a game loads from inside its Wine/Proton prefix rather than from its
// install directory (Baldur's Gate 3's .pak mods, which it reads only from
// AppData/Local/Larian Studios/Baldur's Gate 3/Mods under the prefix's
// Windows user directory). The game's adapter routes a file there
// (adapter.RootPrefix) and says where under the user directory it goes
// (adapter.PrefixPlacer); the deployed path is a domain.PrefixFile relative
// to that directory, so a record means the same thing whichever adapter
// later reads it.
//
// The prefix is resolved by core (ResolvePrefix: games.yaml's prefix: and
// prefix_user:, else Steam's compatdata) onto the copy of the game a
// frontend gets (domain.Game.PrefixRoot), for a game whose adapter places
// files there or that has such files recorded. A prefix that does not
// resolve is never replaced by another directory: a deploy into it, and
// any flow over files recorded there, is refused with PrefixUnresolvedError,
// whose message says what to set.
//
// The user directory is the user's own tree, so every write and removal
// there is held to #616's rules for the prefix: the user directory is
// re-checked (wineprefix.CheckUserDir) immediately before, the directories
// beneath it are walked one at a time with no link followed, and they are
// matched case-insensitively, as Wine matches them (prefixPath). A file
// lmm's replaces - a user's own .pak of the same name - is captured into
// the originals store first, under OriginalRootPrefix, and put back when
// lmm's file goes. lmm never removes a directory there: the folders under
// the user directory are the game's.
package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/db"
)

// PrefixUnresolvedError refuses a flow that has to touch files in a game's
// Wine/Proton prefix (#636) - a .pak mod Baldur's Gate 3 loads from the
// prefix, or one deployed there before - when lmm cannot resolve the
// prefix or the user directory inside it. Problem is ResolvePrefix's
// answer, which names what to set. lmm never deploys such a file anywhere
// else instead.
type PrefixUnresolvedError struct {
	GameID  string
	Problem string
}

// Error says what is refused, why, and what to set - the Problem's own
// remedy when it names one, else the edit that sets the prefix.
func (e *PrefixUnresolvedError) Error() string {
	problem := e.Problem
	if problem == "" {
		problem = "no prefix is set or found"
	}
	msg := fmt.Sprintf("game %s deploys mod files into its Wine/Proton prefix, which lmm cannot resolve: %s", e.GameID, problem)
	if strings.Contains(problem, "lmm game edit") {
		return msg
	}
	return msg + fmt.Sprintf(" (set it with `lmm game edit %s --prefix <dir>`, and --prefix-user <name> when the prefix has several users)", e.GameID)
}

// withPrefixRoot sets g.PrefixRoot - g a copy the caller owns - for a game
// whose adapter places files in the prefix or that has files recorded
// there, when the prefix resolves; it is nil otherwise.
func (s *Service) withPrefixRoot(g *domain.Game) *domain.Game {
	if g == nil {
		return g
	}
	g.PrefixRoot = nil
	if !s.usesPrefixRoot(g) {
		return g
	}
	if st := s.ResolvePrefix(g); st.Resolved {
		g.PrefixRoot = &domain.ResolvedPrefix{Path: st.Path, User: st.User}
	}
	return g
}

// usesPrefixRoot reports whether g's files may live under the prefix: its
// adapter places files there, or a deploy recorded some (a game switched
// away from such an adapter still has them to remove).
func (s *Service) usesPrefixRoot(g *domain.Game) bool {
	if a, err := s.AdapterFor(g); err == nil && adapter.PlacesInPrefix(a) {
		return true
	}
	if s.db == nil {
		return false
	}
	has, err := s.db.HasDeployedFilesUnder(context.Background(), g.ID, domain.RootPrefix)
	return err == nil && has
}

// prefixUnresolved is the refusal for a flow over game's prefix files when
// game has no resolved prefix, nil when it has one.
func (s *Service) prefixUnresolved(game *domain.Game) error {
	if game.PrefixRoot != nil {
		return nil
	}
	return &PrefixUnresolvedError{GameID: game.ID, Problem: s.ResolvePrefix(game).Problem}
}

// refusePrefixRecords refuses a flow over game while it has files recorded
// under its prefix and the prefix does not resolve: lmm cannot tell what is
// there, so it neither removes those files nor forgets them. nil for a game
// with a resolved prefix, or none of its files there.
func refusePrefixRecords(ctx context.Context, d *db.DB, game *domain.Game) error {
	if game == nil || game.PrefixRoot != nil || d == nil {
		return nil
	}
	has, err := d.HasDeployedFilesUnder(ctx, game.ID, domain.RootPrefix)
	if err != nil {
		return err
	}
	if has {
		return &PrefixUnresolvedError{GameID: game.ID, Problem: "the files lmm deployed there cannot be found until it is"}
	}
	return nil
}

// refusePrefixRecords is the package function for a flow that holds the
// Service: the same refusal, with ResolvePrefix's own Problem in it.
func (s *Service) refusePrefixRecords(ctx context.Context, game *domain.Game) error {
	if err := refusePrefixRecords(ctx, s.db, game); err != nil {
		var refused *PrefixUnresolvedError
		if errors.As(err, &refused) {
			return s.prefixUnresolved(game)
		}
		return err
	}
	return nil
}

// refuseModPrefixRecords is Installer.refusePrefixRecorded for a Service
// flow over one mod, with ResolvePrefix's own Problem in the refusal.
func (s *Service) refuseModPrefixRecords(ctx context.Context, game *domain.Game, mod *domain.Mod, profileName string) error {
	if err := s.getInstaller(game).refusePrefixRecorded(ctx, game, mod, profileName); err != nil {
		var refused *PrefixUnresolvedError
		if errors.As(err, &refused) {
			return s.prefixUnresolved(game)
		}
		return err
	}
	return nil
}

// refusePrefixItems refuses a deploy of items into game's prefix when it
// does not resolve.
func refusePrefixItems(game *domain.Game, items []deployItem) error {
	if game == nil || game.PrefixRoot != nil {
		return nil
	}
	for _, it := range items {
		if it.path.IsPrefix() {
			return &PrefixUnresolvedError{GameID: game.ID, Problem: "the prefix (or the user directory inside it) is not set and could not be found"}
		}
	}
	return nil
}

// prefixItem is the deployed path of member, which a's RootRouter sends to
// the prefix: where its PrefixPlacer places it, under the user directory.
// An adapter that cannot say (adapter.PrefixPath) is refused: the file is
// never deployed anywhere else.
func prefixItem(a adapter.GameAdapter, game *domain.Game, member string) (domain.DeployPath, error) {
	rel, ok := adapter.PrefixPath(a, game, filepath.ToSlash(member))
	if !ok {
		id := ""
		if a != nil {
			id = a.ID()
		}
		return domain.DeployPath{}, fmt.Errorf("the game's adapter (%s) routes %s into the prefix without a usable place for it", id, member)
	}
	return domain.PrefixFile(filepath.FromSlash(rel)), nil
}

// prefixTarget is where rel, a path relative to the prefix user directory
// dir, is on disk: each directory on the way spelled as it already exists
// there, matched case-insensitively as Wine matches it (an exact match
// first, else the first in byte order that folds to it) - so the path every
// stat and hash reads is the one prefixPath writes through. A directory not
// there yet keeps rel's spelling, as does the file's own name.
func prefixTarget(dir, rel string) string {
	comps := strings.Split(filepath.ToSlash(rel), "/")
	at := dir
	for i, comp := range comps {
		if i == len(comps)-1 {
			at = filepath.Join(at, comp)
			break
		}
		if _, err := os.Lstat(filepath.Join(at, comp)); err == nil {
			at = filepath.Join(at, comp)
			continue
		}
		real := ""
		if entries, err := os.ReadDir(at); err == nil {
			for _, e := range entries { // ReadDir sorts by name
				if strings.EqualFold(e.Name(), comp) {
					real = e.Name()
					break
				}
			}
		}
		if real == "" {
			return filepath.Join(append([]string{at}, comps[i:]...)...)
		}
		at = filepath.Join(at, real)
	}
	return at
}

// openInPrefix is rootLinker.open for a linker held to a prefix user
// directory: the user directory re-checked, then dst's directory opened
// beneath it with no link followed and each directory matched
// case-insensitively (prefixPath.open). create makes missing directories
// with their canonical spelling.
func (r rootLinker) openInPrefix(dst string, create bool) (*os.File, string, error) {
	userDir := r.prefix.UserDir()
	if userDir == "" {
		return nil, "", fmt.Errorf("%s: the game's prefix is not resolved", dst)
	}
	rel, ok := relUnder(userDir, dst)
	if !ok {
		return nil, "", fmt.Errorf("refusing %s: it is not beneath the prefix's user directory %s", dst, userDir)
	}
	var comps []string
	if d := filepath.Dir(filepath.FromSlash(rel)); d != "." {
		comps = strings.Split(filepath.ToSlash(d), "/")
	}
	dir, err := prefixPath{prefix: r.prefix.Path, user: r.prefix.User, comps: comps}.open(create)
	if err != nil {
		return nil, "", err
	}
	return dir, filepath.Base(filepath.FromSlash(rel)), nil
}

// prefixOfUserDir is the prefix a user directory, <prefix>/drive_c/users/
// <user>, belongs to - the inverse of ResolvedPrefix.UserDir, for a restore
// that knows only the path it writes to.
func prefixOfUserDir(userDir string) (*domain.ResolvedPrefix, bool) {
	clean := filepath.Clean(userDir)
	users := filepath.Dir(clean)
	driveC := filepath.Dir(users)
	if filepath.Base(users) != "users" || filepath.Base(driveC) != "drive_c" {
		return nil, false
	}
	return &domain.ResolvedPrefix{Path: filepath.Dir(driveC), User: filepath.Base(clean)}, true
}
