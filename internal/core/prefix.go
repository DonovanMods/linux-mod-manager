package core

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/adapter"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/wineprefix"
)

// PrefixOriginExplicit and PrefixOriginSteam are PrefixStatus.Origin's
// values (#605): the prefix games.yaml's `prefix:` names, or the one derived
// from Steam's compatdata for the game's app id.
const (
	PrefixOriginExplicit = "explicit"
	PrefixOriginSteam    = "steam_compatdata"
)

// PrefixUserExplicit, PrefixUserProton and PrefixUserOnly are
// PrefixStatus.UserOrigin's values (#605): games.yaml's `prefix_user:`,
// Proton's and umu's fixed "steamuser", or the one user directory drive_c/
// users holds besides Public.
const (
	PrefixUserExplicit = "explicit"
	PrefixUserProton   = wineprefix.UserRuleProton
	PrefixUserOnly     = wineprefix.UserRuleOnly
)

// FindingPrefixUnresolved is the finding a game whose adapter needs its
// prefix (adapter.PrefixConsumer) carries when the prefix, or the user
// directory inside it, cannot be resolved (#605). A finding, never an
// error: whatever needs the prefix is skipped and says so, and everything
// else proceeds.
const FindingPrefixUnresolved = "prefix_unresolved"

// VerifyStatusPrefixUnresolved is the status of the verify row that
// carries FindingPrefixUnresolved: a warning, which does not fail the run,
// whose note says what is missing and whose FixableReason says what to
// set. It is never fixable - which prefix a game runs in is the user's to
// say.
const VerifyStatusPrefixUnresolved = FindingPrefixUnresolved

// PrefixStatus is where a game's Wine/Proton prefix is, how lmm knows, and
// which Windows user directory inside it is the game's (#605) - `lmm game
// show`'s "prefix_status", and the input every later writer into the prefix
// (#355's Plugins.txt) resolves through.
//
// The prefix is resolved in order: an explicit `prefix:`, which wins even
// when it names nothing usable (a typo is reported, never silently replaced
// by a different directory); else Steam's compatdata for the game's app id,
// <library>/steamapps/compatdata/<appid>/pfx, in EVERY Steam library, the
// most recently used one when several exist; else nothing. The user is
// `prefix_user:` when set; else "steamuser" in a Proton or umu prefix; else
// the one directory under drive_c/users besides Public.
type PrefixStatus struct {
	// Path is the prefix - the directory holding drive_c. Set whenever a
	// candidate was found or named, even one that turned out unusable
	// (Problem says why); "" when there is none at all.
	Path string `json:"path,omitempty"`
	// Origin is how Path was found: PrefixOriginExplicit or
	// PrefixOriginSteam.
	Origin string `json:"origin,omitempty"`
	// SteamAppID is the app id the compatdata probe looked for, when lmm
	// knows one and games.yaml names no prefix.
	SteamAppID string `json:"steam_app_id,omitempty"`
	// Library is the Steam library Path is in, for a PrefixOriginSteam
	// prefix.
	Library string `json:"library,omitempty"`
	// Candidates lists every compatdata prefix found for the app when there
	// were several, the chosen one first; Note says why it won. Absent when
	// there was one or none.
	Candidates []PrefixCandidate `json:"candidates,omitempty"`
	// Note explains a choice among Candidates, in words.
	Note string `json:"note,omitempty"`
	// User is the Windows user directory name under drive_c/users - set
	// when one was picked or named, even a named one that is not there.
	User string `json:"user,omitempty"`
	// UserOrigin is how User was picked: PrefixUserExplicit,
	// PrefixUserProton or PrefixUserOnly.
	UserOrigin string `json:"user_origin,omitempty"`
	// UserPath is <Path>/drive_c/users/<User>, when Resolved. It is CHECKED
	// (wineprefix.CheckUserDir): a directory that, symlinks resolved, is
	// still inside the prefix's drive_c/users, so a writer into it cannot
	// be sent out of the prefix by a user directory linked to the host's
	// home. One that leaves is unresolved, and Problem says so.
	UserPath string `json:"user_path,omitempty"`
	// Resolved reports that both the prefix and the user directory are
	// usable: what a writer into the prefix requires.
	Resolved bool `json:"resolved"`
	// Problem says, in words, why the prefix is not Resolved, and what to
	// set; "" when it is.
	Problem string `json:"problem,omitempty"`
	// Needed reports that the game's adapter writes into the prefix
	// (adapter.PrefixConsumer). False for every game whose adapter does
	// not, for which an unresolved prefix is nothing lmm needs.
	Needed bool `json:"needed,omitzero"`
	// Finding is FindingPrefixUnresolved when the prefix is Needed and not
	// Resolved, "" otherwise.
	Finding string `json:"finding,omitempty"`
}

// PrefixCandidate is one Steam library's compatdata prefix for a game, and
// when it was last used.
type PrefixCandidate struct {
	Library    string    `json:"library"`
	Path       string    `json:"path"`
	ModifiedAt time.Time `json:"modified_at"`
}

// SetSteamLibraries registers how this Service lists the Steam libraries on
// the machine, which a game's prefix is derived from (#605). The
// composition root calls it (app.RegisterLaunchers); until it does, only an
// explicit `prefix:` resolves.
func (s *Service) SetSteamLibraries(fn func() []string) {
	s.steamLibrariesPtr.Store(&fn)
}

// steamLibraries is every Steam library SetSteamLibraries' function lists,
// or none.
func (s *Service) steamLibraries() []string {
	fn := s.steamLibrariesPtr.Load()
	if fn == nil || *fn == nil {
		return nil
	}
	return (*fn)()
}

// steamAppID is the Steam app id lmm knows for game - launch.steam_app_id,
// the steamworkshop mapping, then the library scan - the same answer a
// launch acts on. Without a Launcher there is no library scan.
func (s *Service) steamAppID(game *domain.Game) string {
	l := s.launcher()
	if l == nil {
		l = &Launcher{}
	}
	return l.steamApp(game).AppID
}

// ResolvePrefix resolves game's prefix and the user directory inside it
// (PrefixStatus). Read-only, and never an error: a prefix lmm cannot
// resolve is a Problem in the answer, and a FindingPrefixUnresolved when
// the game's adapter needs one.
func (s *Service) ResolvePrefix(game *domain.Game) *PrefixStatus {
	st := s.locatePrefix(game)
	if st.Problem == "" {
		s.resolvePrefixUser(game, st)
	}
	st.Resolved = st.Problem == ""
	if st.Resolved {
		st.UserPath = filepath.Join(st.Path, "drive_c", "users", st.User)
	}
	if a, err := s.AdapterFor(game); err == nil && adapter.UsesPrefix(a, game) {
		st.Needed = true
		if !st.Resolved {
			st.Finding = FindingPrefixUnresolved
		}
	}
	return st
}

// locatePrefix is ResolvePrefix's first half: the prefix itself, or the
// Problem that leaves it unresolved.
func (s *Service) locatePrefix(game *domain.Game) *PrefixStatus {
	if game.Prefix != "" {
		st := &PrefixStatus{Path: game.Prefix, Origin: PrefixOriginExplicit}
		st.Problem = explicitPrefixProblem(game.Prefix)
		return st
	}
	st := &PrefixStatus{}
	appID := s.steamAppID(game)
	if appID == "" {
		st.Problem = "lmm knows no Steam app id for this game, so it cannot find the game's prefix: set prefix: to the Wine/Proton prefix the game runs in (`lmm game edit " + game.ID + " --prefix <dir>`)"
		return st
	}
	st.SteamAppID = appID
	found := wineprefix.SteamCompatdata(s.steamLibraries(), appID)
	if len(found) == 0 {
		st.Problem = fmt.Sprintf("no Steam compatdata prefix for app %s in any Steam library: run the game once through Steam (with Proton), or set prefix: (`lmm game edit %s --prefix <dir>`)", appID, game.ID)
		return st
	}
	st.Path, st.Origin, st.Library = found[0].Path, PrefixOriginSteam, found[0].Library
	if len(found) > 1 {
		for _, c := range found {
			st.Candidates = append(st.Candidates, PrefixCandidate{Library: c.Library, Path: c.Path, ModifiedAt: c.ModTime.UTC()})
		}
		st.Note = fmt.Sprintf("app %s has a compatdata prefix in %d Steam libraries; using the most recently used one, in %s (last used %s) - set prefix: to choose another",
			appID, len(found), found[0].Library, found[0].ModTime.UTC().Format(time.RFC3339))
	}
	return st
}

// explicitPrefixProblem is why an explicit `prefix:` is unusable, or "".
func explicitPrefixProblem(prefix string) string {
	if strings.TrimSpace(prefix) != prefix {
		return fmt.Sprintf("prefix: %q has leading or trailing whitespace", prefix)
	}
	if !filepath.IsAbs(prefix) {
		return fmt.Sprintf("prefix: %q is not an absolute path", prefix)
	}
	if _, err := os.Stat(prefix); errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("prefix: %s does not exist - run the game once, or correct prefix:", prefix)
	}
	if !wineprefix.IsPrefix(prefix) {
		// Steam's compatdata/<appid> folder holds the prefix in pfx/, and is
		// the folder a file manager shows - an easy one to name instead.
		if pfx := filepath.Join(prefix, "pfx"); wineprefix.IsPrefix(pfx) {
			return fmt.Sprintf("prefix: %s is not a Wine prefix, but %s is - append /pfx (Steam keeps the prefix in its compatdata folder's pfx)", prefix, pfx)
		}
		return fmt.Sprintf("prefix: %s is not a Wine prefix (a prefix holds drive_c and system.reg)", prefix)
	}
	return ""
}

// resolvePrefixUser is ResolvePrefix's second half, for a prefix that
// resolved: the user directory, or the Problem that leaves it unresolved.
func (s *Service) resolvePrefixUser(game *domain.Game, st *PrefixStatus) {
	users := filepath.Join(st.Path, "drive_c", "users")
	if game.PrefixUser != "" {
		st.User, st.UserOrigin = game.PrefixUser, PrefixUserExplicit
		if err := wineprefix.CheckUserName(game.PrefixUser); err != nil {
			st.Problem = "prefix_user: " + err.Error()
			return
		}
		if err := wineprefix.CheckUserDir(st.Path, game.PrefixUser); err != nil {
			st.Problem = "prefix_user: " + err.Error() + " - correct prefix_user:"
		}
		return
	}
	found := wineprefix.FindUser(st.Path)
	if found.Name != "" {
		st.User, st.UserOrigin = found.Name, found.Rule
		return
	}
	if found.Outside != "" {
		st.Problem = fmt.Sprintf("%s: lmm will not write outside the prefix - make it a real directory, or set prefix_user: to another",
			wineprefix.CheckUserDir(st.Path, found.Outside))
		return
	}
	if len(found.Candidates) == 0 {
		st.Problem = fmt.Sprintf("%s has no user directory besides Public: run the game once in the prefix, or set prefix_user:", users)
		return
	}
	st.Problem = fmt.Sprintf("%s has several user directories (%s): set prefix_user: to the one the game uses (`lmm game edit %s --prefix-user <name>`)",
		users, strings.Join(found.Candidates, ", "), game.ID)
}

// gamePrefixStatus is the PrefixStatus a game document carries: nil for a
// game with nothing to show - no prefix set or found, and none needed - so
// every such game's document is the shape it was before #605.
func (s *Service) gamePrefixStatus(game *domain.Game) *PrefixStatus {
	st := s.ResolvePrefix(game)
	if st.Path == "" && !st.Needed && game.PrefixUser == "" {
		return nil
	}
	return st
}
