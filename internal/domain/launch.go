package domain

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// GameLaunch is how a game is started (#501) - games.yaml's `launch:`
// block. Every member is optional: a game added from Steam launches with
// no block at all, because core finds its app id.
//
// It carries no json tags, and domain.Game's member is `json:"-"`: the
// argv comes only from games.yaml, so no JSON document - a game listing,
// or a request body - can carry it in either direction. The wire view of
// a game's launch is core.LaunchInfo.
type GameLaunch struct {
	// Via names the launch method to use ("steam", "command", or one a
	// later build registers). Empty tries every method in core's order.
	Via string
	// SteamAppID overrides the app id lmm would otherwise find. Digits
	// only.
	SteamAppID string
	// Command is the argv to run - never a shell string. Its arguments
	// may use LaunchPlaceholders; command[0] may start with ~/.
	Command []string
	// BattleNet is the `battlenet:` block (#545): which Wine prefix's
	// Battle.net client starts the game. Nil for every other game.
	BattleNet *BattleNetLaunch
}

// LaunchViaBattleNet is the method name `via: battlenet` selects: ask the
// Battle.net client in launch.battlenet.prefix to start the game.
const LaunchViaBattleNet = "battlenet"

// BattleNetRunnerUmu and BattleNetRunnerWine are launch.battlenet.runner's
// two values: umu-run (a Proton prefix, as umu-launcher makes) and a plain
// wine (any other prefix).
const (
	BattleNetRunnerUmu  = "umu"
	BattleNetRunnerWine = "wine"
)

// BattleNetLaunch is games.yaml's `launch.battlenet:` block (#545).
type BattleNetLaunch struct {
	// Prefix is the Wine prefix Battle.net is installed in - the directory
	// holding drive_c. Absolute, or starting with ~/. Required.
	Prefix string
	// Product is the code Battle.net's --exec="launch <code>" takes, e.g.
	// "WoW" for World of Warcraft (retail). Empty opens the Battle.net
	// client and leaves the user to press Play: the launch codes are not
	// the product codes detection reads, and lmm only fills in one it
	// knows.
	Product string
	// Runner is BattleNetRunnerUmu or BattleNetRunnerWine. Empty picks
	// umu for a Proton prefix and wine for any other.
	Runner string
	// Proton is umu-run's PROTONPATH - a Proton name umu knows
	// ("GE-Proton") or a path. Empty is umu's own default. umu only.
	Proton string
}

// battleNetLaunchCodePattern is a Battle.net launch code: letters, digits,
// underscores and hyphens - it lands inside one argv element, but a code
// with a space or a quote in it is a typo, not a code.
var battleNetLaunchCodePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ErrInvalidLaunch reports a games.yaml `launch:` block lmm refuses to
// load: an unknown placeholder, an empty program, `via: command` with no
// command, a non-numeric app id, or a malformed method name.
var ErrInvalidLaunch = errors.New("invalid launch configuration")

// LaunchPlaceholders are the `{name}` tokens launch.command may use, in
// the order error messages list them.
var LaunchPlaceholders = []string{"game_id", "install_path", "mod_path", "profile", "steam_app_id"}

// LaunchViaCommand is the method name `via: command` selects: run
// launch.command.
const LaunchViaCommand = "command"

// placeholderPattern matches a `{name}` token. Only a lower-case
// identifier is a placeholder; any other brace - a JSON argument, "{}" -
// is the command's own text.
var placeholderPattern = regexp.MustCompile(`\{([a-z_]+)\}`)

// launchViaPattern is a method name: lower-case letters, digits and single
// interior hyphens, like an adapter name.
var launchViaPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// NewGameLaunch validates a `launch:` block and returns what a game
// carries for it: nil when nothing is set. Every refusal wraps
// ErrInvalidLaunch and names the key at fault.
//
// Whether Via names a method that exists is not this layer's question -
// it is core's, asked when it resolves the launch - so a games.yaml
// naming a method a later build ships still loads.
func NewGameLaunch(via, steamAppID string, command []string) (*GameLaunch, error) {
	return NewGameLaunchWithBattleNet(via, steamAppID, command, nil)
}

// NewGameLaunchWithBattleNet is NewGameLaunch with a `battlenet:` block
// (#545), validated the same way: `via: battlenet` needs the block, the
// block needs a prefix, and every refusal names its key.
func NewGameLaunchWithBattleNet(via, steamAppID string, command []string, bn *BattleNetLaunch) (*GameLaunch, error) {
	via = strings.TrimSpace(via)
	steamAppID = strings.TrimSpace(steamAppID)
	if via == "" && steamAppID == "" && len(command) == 0 && bn == nil {
		return nil, nil
	}
	if via == LaunchViaBattleNet && bn == nil {
		return nil, fmt.Errorf("%w: launch.battlenet.prefix is required with via: battlenet", ErrInvalidLaunch)
	}
	bn, err := validBattleNetLaunch(bn)
	if err != nil {
		return nil, err
	}
	if via != "" && !launchViaPattern.MatchString(via) {
		return nil, fmt.Errorf("%w: launch.via %q is not a launch method name (lower-case letters, digits and single interior hyphens)", ErrInvalidLaunch, via)
	}
	if via == LaunchViaCommand && len(command) == 0 {
		return nil, fmt.Errorf("%w: launch.command is required with via: command", ErrInvalidLaunch)
	}
	if steamAppID != "" && strings.Trim(steamAppID, "0123456789") != "" {
		return nil, fmt.Errorf("%w: launch.steam_app_id %q must be digits only", ErrInvalidLaunch, steamAppID)
	}
	if len(command) > 0 && strings.TrimSpace(command[0]) == "" {
		return nil, fmt.Errorf("%w: launch.command[0] is empty: it is the program to run", ErrInvalidLaunch)
	}
	if len(command) > 0 && isRelativeProgramPath(command[0]) {
		return nil, fmt.Errorf("%w: launch.command[0] %q is a relative path: give an absolute one, one starting with ~/ or {install_path}, or a bare program name to look up on PATH", ErrInvalidLaunch, command[0])
	}
	for i, arg := range command {
		for _, m := range placeholderPattern.FindAllStringSubmatch(arg, -1) {
			if !isLaunchPlaceholder(m[1]) {
				return nil, fmt.Errorf("%w: launch.command[%d] uses unknown placeholder %s (valid: {%s})",
					ErrInvalidLaunch, i, m[0], strings.Join(LaunchPlaceholders, "}, {"))
			}
		}
	}
	return &GameLaunch{Via: via, SteamAppID: steamAppID, Command: append([]string(nil), command...), BattleNet: bn}, nil
}

// validBattleNetLaunch trims and checks a `battlenet:` block, returning a
// copy the game owns; nil stays nil.
func validBattleNetLaunch(bn *BattleNetLaunch) (*BattleNetLaunch, error) {
	if bn == nil {
		return nil, nil
	}
	out := BattleNetLaunch{
		Prefix:  strings.TrimSpace(bn.Prefix),
		Product: strings.TrimSpace(bn.Product),
		Runner:  strings.ToLower(strings.TrimSpace(bn.Runner)),
		Proton:  strings.TrimSpace(bn.Proton),
	}
	switch {
	case out.Prefix == "":
		return nil, fmt.Errorf("%w: launch.battlenet.prefix is required: the Wine prefix Battle.net is installed in", ErrInvalidLaunch)
	case !strings.HasPrefix(out.Prefix, "/") && !strings.HasPrefix(out.Prefix, "~/"):
		return nil, fmt.Errorf("%w: launch.battlenet.prefix %q is a relative path: give an absolute one, or one starting with ~/", ErrInvalidLaunch, out.Prefix)
	case out.Runner != "" && out.Runner != BattleNetRunnerUmu && out.Runner != BattleNetRunnerWine:
		return nil, fmt.Errorf("%w: launch.battlenet.runner %q is not %s or %s", ErrInvalidLaunch, out.Runner, BattleNetRunnerUmu, BattleNetRunnerWine)
	case out.Proton != "" && out.Runner == BattleNetRunnerWine:
		return nil, fmt.Errorf("%w: launch.battlenet.proton is umu-run's PROTONPATH; it does nothing with runner: wine", ErrInvalidLaunch)
	case out.Product != "" && !battleNetLaunchCodePattern.MatchString(out.Product):
		return nil, fmt.Errorf("%w: launch.battlenet.product %q is not a Battle.net launch code (letters, digits, _ and -, e.g. WoW)", ErrInvalidLaunch, out.Product)
	}
	return &out, nil
}

// isRelativeProgramPath reports whether prog is a path relative to
// whichever directory lmm runs in: it names a directory, but starts with
// none of /, ~/, {install_path} or {mod_path}. A bare name is not a path;
// it is looked up on PATH.
func isRelativeProgramPath(prog string) bool {
	if !strings.Contains(prog, "/") {
		return false
	}
	for _, root := range []string{"/", "~/", "{install_path}", "{mod_path}"} {
		if strings.HasPrefix(prog, root) {
			return false
		}
	}
	return true
}

func isLaunchPlaceholder(name string) bool {
	return slices.Contains(LaunchPlaceholders, name)
}

// ExpandLaunchPlaceholders replaces every `{name}` in s that values has a
// value for. A token it has none for is left as written.
func ExpandLaunchPlaceholders(s string, values map[string]string) string {
	return placeholderPattern.ReplaceAllStringFunc(s, func(tok string) string {
		if v, ok := values[tok[1:len(tok)-1]]; ok {
			return v
		}
		return tok
	})
}

// LaunchPlaceholdersUsed returns each placeholder name argv uses, once, in
// the order it first appears.
func LaunchPlaceholdersUsed(argv []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, arg := range argv {
		for _, m := range placeholderPattern.FindAllStringSubmatch(arg, -1) {
			if isLaunchPlaceholder(m[1]) && !seen[m[1]] {
				seen[m[1]] = true
				out = append(out, m[1])
			}
		}
	}
	return out
}
