package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/source"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/storage/config"
)

// Launching a game (#501). lmm resolves HOW from games.yaml - the game's
// launch.command, else Steam with an app id it can find - and starts it as
// a transient systemd user service, so the game belongs to the user
// manager rather than to lmm: a restart of lmm-serve.service (a
// self-update) does not take the game down with it, and the service's
// NoNewPrivileges and friends do not carry over to it. The design is
// docs/plans/2026-10-30-launch-game-design.md.
//
// The argv comes only from games.yaml. A frontend passes a game, a profile
// and Force; nothing it sends becomes part of the command line.

// ErrLaunchUnavailable is Launch's answer on a Service with no Launcher
// registered (app.RegisterLaunchers is what registers one).
var ErrLaunchUnavailable = errors.New("launching games is not available")

// LaunchOptions are a launch's choices.
type LaunchOptions struct {
	// Profile is the profile to launch; empty is the game's active one.
	// Any other profile is refused (ErrProfileNotActive): the game
	// directory holds the active profile's mods.
	Profile string
	// Force launches although the profile has undeployed changes, which is
	// otherwise a *LaunchDriftError. The result then carries a warning.
	Force bool
	// DryRun resolves everything and checks everything, and starts
	// nothing.
	DryRun bool
	// AllowDetached lets a host with no systemd user manager start the
	// game as a detached child instead - the CLI's fallback. lmm serve
	// leaves it false: its child would sit in lmm-serve.service's cgroup
	// and die with it, so there the launch is refused with
	// *SystemdUnavailableError.
	AllowDetached bool
}

// LaunchResult is what a launch did, or under DryRun would do - `lmm
// launch --json`'s document.
type LaunchResult struct {
	// Game and Profile are what was launched.
	Game    string `json:"game"`
	Profile string `json:"profile"`
	// Method is the launch method that applied: "command" or "steam".
	Method string `json:"method"`
	// Argv is the game's own command line.
	Argv []string `json:"argv"`
	// Command is the whole command line lmm runs: systemd-run with its
	// options and then Argv, or Argv alone when started detached.
	Command []string `json:"command"`
	// Unit is the transient systemd user service the game runs in; empty
	// when it was started detached.
	Unit string `json:"unit,omitempty"`
	// Requested is true when Argv only asks another program to start the
	// game - Steam - so success means the request was made, not that the
	// game is running.
	Requested bool `json:"requested,omitzero"`
	// Undeployed is how many mods' deployed state disagreed with the
	// profile when a Force launch went ahead anyway.
	Undeployed int `json:"undeployed,omitzero"`
	// Warnings are things the user should know about a launch that went
	// ahead: today, undeployed changes.
	Warnings []string `json:"warnings,omitempty"`
	// DryRun is true when nothing was started.
	DryRun bool `json:"dry_run,omitzero"`
}

// LaunchInfo is how a game would launch, or why it cannot: GameStatus's
// `launch` member, which the web UI's Play button reads. Method is empty
// exactly when Reason is set.
type LaunchInfo struct {
	Method string `json:"method,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// LaunchUnconfiguredError is a game lmm cannot launch: no launch method
// applies, or the one games.yaml picks cannot run (its program is not
// installed, a placeholder has no value). Reason says which, and the fix.
type LaunchUnconfiguredError struct {
	GameID string
	Reason string
}

// Error names the game and the reason.
func (e *LaunchUnconfiguredError) Error() string {
	return fmt.Sprintf("cannot launch %s: %s", e.GameID, e.Reason)
}

// LaunchDriftError refuses to launch a profile whose deployed files do not
// match it (#501): Undeployed mods are enabled but not deployed, or
// deployed but disabled (StillDeployed of them), or the game's mod_path
// needs attention (ModPathError). A frontend offers to deploy first when
// DeployClears, or to launch anyway with LaunchOptions.Force.
type LaunchDriftError struct {
	GameID  string `json:"game_id"`
	Profile string `json:"profile"`
	// Undeployed is every mod out of step, StillDeployed included.
	Undeployed int `json:"undeployed"`
	// StillDeployed is the mods disabled but still deployed, which a
	// deploy leaves where they are (it is not a converge run); `lmm
	// profile apply` takes them down.
	StillDeployed int `json:"still_deployed,omitzero"`
	// ModPathError is ModPathProblem's sentence, when there is one; its
	// fix is the user's, not a deploy's.
	ModPathError string `json:"mod_path_error,omitempty"`
	// DeployClears is true when a deploy alone puts the profile in step:
	// only enabled mods are undeployed.
	DeployClears bool `json:"deploy_clears"`
}

// Error names the drift and the way on that clears it.
func (e *LaunchDriftError) Error() string {
	return e.drift() + "; " + strings.Join(e.remedies(), "; ") + "; or launch anyway"
}

// drift is the sentence saying what is out of step.
func (e *LaunchDriftError) drift() string {
	var parts []string
	if n := e.Undeployed - e.StillDeployed; n > 0 {
		parts = append(parts, fmt.Sprintf("%d enabled mod(s) in profile %q of %s are not deployed", n, e.Profile, e.GameID))
	}
	if e.StillDeployed > 0 {
		parts = append(parts, fmt.Sprintf("%d disabled mod(s) in profile %q of %s are still deployed", e.StillDeployed, e.Profile, e.GameID))
	}
	if e.ModPathError != "" {
		parts = append(parts, e.ModPathError)
	}
	return strings.Join(parts, "; ")
}

// remedies is each cause's fix: ModPathError's sentence names its own.
func (e *LaunchDriftError) remedies() []string {
	var out []string
	switch {
	case e.StillDeployed > 0:
		// profile apply also deploys what is enabled.
		out = append(out, fmt.Sprintf("run `lmm profile apply --game %s` to take the disabled mods down", ShellQuoteArg(e.GameID)))
	case e.Undeployed > 0:
		out = append(out, "deploy first (`lmm launch --deploy`)")
	}
	if e.ModPathError != "" {
		out = append(out, "fix the mod_path as above")
	}
	return out
}

// Details is the --json envelope's and the web API's data: the counts a
// frontend renders its Deploy & launch choice from.
func (e *LaunchDriftError) Details() any { return e }

// LaunchEnvironmentError refuses a launch with no display to draw on:
// neither DISPLAY nor WAYLAND_DISPLAY is set for lmm or for the systemd
// user manager that would start the game.
type LaunchEnvironmentError struct {
	// Missing is the variables, any one of which would do.
	Missing []string
}

// Error names the variables and the fix.
func (e *LaunchEnvironmentError) Error() string {
	return fmt.Sprintf("no display to launch the game on: neither %s is set for lmm or for the systemd user manager; run `dbus-update-activation-environment --systemd DISPLAY WAYLAND_DISPLAY` in your desktop session, or set them in an environment.d file",
		strings.Join(e.Missing, " nor "))
}

// SteamApp is the Steam app a game's install directory belongs to, as
// Launcher.SteamApp finds it.
type SteamApp struct {
	AppID string
	// Flatpak is true when the Flatpak Steam owns the game's library.
	Flatpak bool
}

// Launcher is how lmm starts a game: every field is a seam a test
// replaces, like UserUnits'. NewLauncher fills them from the machine.
type Launcher struct {
	// LookPath finds a program on lmm's PATH.
	LookPath func(file string) (string, error)
	// Getenv reads lmm's own environment.
	Getenv func(key string) string
	// Environ is lmm's whole environment, as KEY=VALUE - what a detached
	// game inherits, less every API key (launchEnviron).
	Environ func() []string
	// ManagerEnv returns the names of the variables the systemd user
	// manager hands its services (UserUnits.ManagerEnvironmentNames). An
	// error means no user manager answers.
	ManagerEnv func(ctx context.Context) (map[string]bool, error)
	// Run runs a command to completion - systemd-run, which returns once
	// the unit has started.
	Run func(ctx context.Context, name string, args ...string) ([]byte, error)
	// Detach starts argv in its own session with stdio on /dev/null, with
	// env as its whole environment, in dir when it is not empty, and does
	// not wait for it.
	Detach func(argv, env []string, dir string) error
	// SteamApp finds the Steam app installed at an install path; nil finds
	// none.
	SteamApp func(installPath string) (SteamApp, bool)
	// UnitSuffix returns the random part of a transient unit's name.
	UnitSuffix func() string
}

// NewLauncher returns a Launcher for this machine. steamApp is the Steam
// library lookup, which core cannot import (the composition root passes
// it).
func NewLauncher(steamApp func(installPath string) (SteamApp, bool)) *Launcher {
	return &Launcher{
		LookPath: exec.LookPath,
		Getenv:   os.Getenv,
		Environ:  os.Environ,
		ManagerEnv: func(ctx context.Context) (map[string]bool, error) {
			units := &UserUnits{Run: runCommand}
			return units.ManagerEnvironmentNames(ctx)
		},
		Run:        runCommand,
		Detach:     detach,
		SteamApp:   steamApp,
		UnitSuffix: randomUnitSuffix,
	}
}

// detach is Launcher.Detach's real implementation.
func detach(argv, env []string, dir string) error {
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer devNull.Close() //nolint:errcheck // the child has its own copy
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devNull, devNull, devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// randomUnitSuffix is Launcher.UnitSuffix's real implementation.
func randomUnitSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b) //nolint:errcheck // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}

// SetLauncher registers how this Service starts games. The composition
// root calls it (app.RegisterLaunchers); until it does, Launch refuses
// with ErrLaunchUnavailable and GameStatus carries no launch member.
func (s *Service) SetLauncher(l *Launcher) {
	s.launcherPtr.Store(l)
}

// launcher is the registered Launcher, or nil.
func (s *Service) launcher() *Launcher {
	return s.launcherPtr.Load()
}

// LaunchMethod is one way to start a game. Resolve says whether it applies
// to the request (ok), and if it does, the argv - or why it cannot run
// (err, whose text becomes the refusal's reason).
type LaunchMethod struct {
	Name string
	// Requests is true when the argv only asks another program to start
	// the game (LaunchResult.Requested).
	Requests bool
	Resolve  func(req *LaunchRequest) (argv []string, ok bool, err error)
}

// LaunchRequest is what a LaunchMethod resolves from.
type LaunchRequest struct {
	Game    *domain.Game
	Profile string
	// LookPath finds a program on lmm's PATH.
	LookPath func(file string) (string, error)
	// Env is what the argv needs in its environment, as KEY=VALUE - a Wine
	// prefix, for the battlenet method (#545). A method's Resolve sets it
	// when it applies; it reaches the game the way LMM_GAME_ID does, never
	// a credential variable.
	Env []string

	launcher *Launcher
	steam    *SteamApp
}

// Steam is the game's Steam app; AppID is empty when none is known. It is
// found on first use - the library scan reads every app manifest, and a
// command that uses no app id never needs it (GameStatus resolves on every
// read).
func (r *LaunchRequest) Steam() SteamApp {
	if r.steam == nil {
		app := r.launcher.steamApp(r.Game)
		r.steam = &app
	}
	return *r.steam
}

// builtinLaunchMethods are the methods lmm tries, in order, when games.yaml
// names none: the user's own command first, then Steam.
var builtinLaunchMethods = []LaunchMethod{
	{Name: domain.LaunchViaCommand, Resolve: resolveCommandLaunch},
	{Name: "steam", Requests: true, Resolve: resolveSteamLaunch},
}

// RegisterLaunchMethod adds a launch method after the built-in ones -
// BattleNetLaunchMethod (#545). The composition root calls it. A method
// registered again under the same name replaces the earlier one.
func (s *Service) RegisterLaunchMethod(m LaunchMethod) {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	if i := slices.IndexFunc(s.launchMethods, func(r LaunchMethod) bool { return r.Name == m.Name }); i >= 0 {
		s.launchMethods[i] = m
		return
	}
	s.launchMethods = append(s.launchMethods, m)
}

// allLaunchMethods is every method, in the order they are tried.
func (s *Service) allLaunchMethods() []LaunchMethod {
	s.launchMu.Lock()
	defer s.launchMu.Unlock()
	return append(slices.Clone(builtinLaunchMethods), s.launchMethods...)
}

// resolveCommandLaunch is the `command` method: games.yaml's
// launch.command, with its placeholders expanded and its program found.
func resolveCommandLaunch(req *LaunchRequest) ([]string, bool, error) {
	if req.Game.Launch == nil || len(req.Game.Launch.Command) == 0 {
		return nil, false, nil
	}
	cmd := req.Game.Launch.Command
	values := map[string]string{
		"game_id":      req.Game.ID,
		"install_path": req.Game.InstallPath,
		"mod_path":     req.Game.ModPath,
		"profile":      req.Profile,
	}
	if slices.Contains(domain.LaunchPlaceholdersUsed(cmd), "steam_app_id") {
		if values["steam_app_id"] = req.Steam().AppID; values["steam_app_id"] == "" {
			return nil, true, errors.New("launch.command uses {steam_app_id}, but lmm knows no Steam app id for it; set launch.steam_app_id")
		}
	}
	argv := make([]string, len(cmd))
	for i, arg := range cmd {
		argv[i] = domain.ExpandLaunchPlaceholders(arg, values)
	}
	argv[0] = config.ExpandPath(argv[0])
	// A bare name is found on lmm's PATH now: the user manager runs the
	// unit with its own PATH, which need not be the shell's.
	if !strings.Contains(argv[0], "/") {
		path, err := req.LookPath(argv[0])
		if err != nil {
			return nil, true, fmt.Errorf("launch.command's program %q is not on PATH", argv[0])
		}
		argv[0] = path
	}
	return argv, true, nil
}

// resolveSteamLaunch is the `steam` method: steam://rungameid/<id>, handed
// to the Flatpak Steam for a game in its library, else to the steam
// binary, else to xdg-open.
func resolveSteamLaunch(req *LaunchRequest) ([]string, bool, error) {
	app := req.Steam()
	if app.AppID == "" {
		return nil, false, nil
	}
	url := "steam://rungameid/" + app.AppID
	if app.Flatpak {
		if path, err := req.LookPath("flatpak"); err == nil {
			return []string{path, "run", "com.valvesoftware.Steam", url}, true, nil
		}
		return nil, true, errors.New("the game is in the Flatpak Steam's library, but flatpak is not on PATH")
	}
	for _, prog := range []string{"steam", "xdg-open"} {
		if path, err := req.LookPath(prog); err == nil {
			return []string{path, url}, true, nil
		}
	}
	return nil, true, errors.New("its Steam app id is " + app.AppID + ", but neither steam nor xdg-open is on PATH")
}

// steamApp is game's Steam app: its id from launch.steam_app_id, else the
// steamworkshop source's mapping, else the library scan - which also says
// whether the Flatpak Steam owns it.
func (l *Launcher) steamApp(game *domain.Game) SteamApp {
	var app SteamApp
	if l.SteamApp != nil {
		app, _ = l.SteamApp(game.InstallPath)
	}
	if game.Launch != nil && game.Launch.SteamAppID != "" {
		app.AppID = game.Launch.SteamAppID
	} else if id := game.SourceIDs["steamworkshop"]; id != "" && strings.Trim(id, "0123456789") == "" {
		app.AppID = id
	}
	return app
}

// resolvedLaunch is a resolution: the method and argv, or the refusal.
type resolvedLaunch struct {
	method LaunchMethod
	argv   []string
	env    []string
	err    *LaunchUnconfiguredError
}

// resolveLaunch picks game's launch method: the one launch.via names, or
// the first in order that applies.
func (s *Service) resolveLaunch(l *Launcher, game *domain.Game, profile string) resolvedLaunch {
	req := &LaunchRequest{Game: game, Profile: profile, LookPath: l.LookPath, launcher: l}
	methods := s.allLaunchMethods()
	refuse := func(reason string) resolvedLaunch {
		return resolvedLaunch{err: &LaunchUnconfiguredError{GameID: game.ID, Reason: reason}}
	}
	if game.Launch != nil && game.Launch.Via != "" {
		i := slices.IndexFunc(methods, func(m LaunchMethod) bool { return m.Name == game.Launch.Via })
		if i < 0 {
			names := make([]string, len(methods))
			for j, m := range methods {
				names[j] = m.Name
			}
			slices.Sort(names)
			return refuse(fmt.Sprintf("launch.via %q is not a launch method this lmm knows (known: %s)", game.Launch.Via, strings.Join(names, ", ")))
		}
		methods = methods[i : i+1]
	}
	for _, m := range methods {
		req.Env = nil
		argv, ok, err := m.Resolve(req)
		if !ok {
			continue
		}
		if err != nil {
			return refuse(err.Error())
		}
		return resolvedLaunch{method: m, argv: argv, env: req.Env}
	}
	if game.Launch != nil && game.Launch.Via != "" {
		return refuse(fmt.Sprintf("launch.via is %q, but that method does not apply to this game (for steam: no Steam app id is known; set launch.steam_app_id)", game.Launch.Via))
	}
	return refuse("it has no launch.command in games.yaml and lmm found no Steam app id for it; set launch.command (a list: the program, then its arguments) under the game in games.yaml")
}

// ResolveLaunch says how game would launch for profile, without starting
// anything: the method and the game's argv, or - with a nil argv - the
// reason it cannot (LaunchInfo.Reason). The error is ErrLaunchUnavailable
// on a Service with no Launcher.
func (s *Service) ResolveLaunch(ctx context.Context, game *domain.Game, profile string) (*LaunchInfo, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	l := s.launcher()
	if l == nil {
		return nil, nil, ErrLaunchUnavailable
	}
	r := s.resolveLaunch(l, game, profile)
	if r.err != nil {
		return &LaunchInfo{Reason: r.err.Reason}, nil, nil
	}
	return &LaunchInfo{Method: r.method.Name}, r.argv, nil
}

// launchForwardedEnv is every variable lmm hands the game's unit from its
// own environment, when it has it: what a desktop program needs to find
// the session.
// XAUTHORITY matters on an X11 session whose display manager keeps the
// cookie somewhere other than ~/.Xauthority: DISPLAY alone cannot connect.
var launchForwardedEnv = []string{"DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "XDG_CURRENT_DESKTOP", "XAUTHORITY"}

// isCredentialEnv reports whether the environment variable name can carry a
// mod source's API key, which no game - nor any of its mods' code - is
// given: the LMM_<ID>_API_KEY any source reads (app.EnvKeyForSourceID), or
// a registered source's own variable (source.EnvKeyProvider, such as
// NEXUSMODS_API_KEY).
func (s *Service) isCredentialEnv() func(name string) bool {
	own := map[string]bool{}
	for _, src := range s.ListSources() {
		if p, ok := src.(source.EnvKeyProvider); ok && p.EnvKey() != "" {
			own[p.EnvKey()] = true
		}
	}
	return func(name string) bool {
		return own[name] || strings.HasPrefix(name, "LMM_") && strings.HasSuffix(name, "_API_KEY")
	}
}

// launchEnviron is a detached game's environment: lmm's own - the PATH,
// HOME, display, locale and Steam/Proton variables a game needs, which an
// allow-list could not name - less every credential variable, plus extra.
func launchEnviron(environ []string, isCredential func(string) bool, extra []string) []string {
	env := make([]string, 0, len(environ)+len(extra))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if !isCredential(name) {
			env = append(env, kv)
		}
	}
	return append(env, extra...)
}

// launchDisplayEnv are the variables of which at least one must reach the
// game.
var launchDisplayEnv = []string{"DISPLAY", "WAYLAND_DISPLAY"}

// Launch starts game for its active profile (#501), as a transient systemd
// user service, and returns at once - it never waits for the game.
//
// In order, it refuses: a profile that is not the active one
// (ErrProfileNotActive); a game no method can launch
// (*LaunchUnconfiguredError); undeployed changes without opts.Force
// (*LaunchDriftError); no systemd user manager without opts.AllowDetached
// (*SystemdUnavailableError); and no display (*LaunchEnvironmentError).
//
// It takes the mutation slot (beginOp), so it cannot start the game while
// a deploy is changing its files; a DryRun, which starts nothing, does
// not.
func (s *Service) Launch(ctx context.Context, game *domain.Game, opts LaunchOptions) (*LaunchResult, error) {
	l := s.launcher()
	if l == nil {
		return nil, ErrLaunchUnavailable
	}
	if !opts.DryRun {
		release, err := s.beginOp(ctx)
		if err != nil {
			return nil, err
		}
		defer release()
	}

	profile := opts.Profile
	if profile == "" {
		live, err := s.liveProfile(ctx, game.ID)
		if err != nil {
			return nil, err
		}
		profile = live
	} else if err := s.refuseInactive(ctx, game.ID, profile, "launch"); err != nil {
		return nil, err
	}

	r := s.resolveLaunch(l, game, profile)
	if r.err != nil {
		return nil, r.err
	}
	result := &LaunchResult{
		Game: game.ID, Profile: profile, Method: r.method.Name, Argv: r.argv,
		Requested: r.method.Requests, DryRun: opts.DryRun,
	}

	drift, err := s.launchDrift(ctx, game, profile)
	if err != nil {
		return nil, err
	}
	if drift != nil {
		if !opts.Force {
			return nil, drift
		}
		result.Undeployed = drift.Undeployed
		result.Warnings = append(result.Warnings, "launching anyway: "+drift.drift()+"; "+strings.Join(drift.remedies(), "; "))
	}

	// A command starts in the game's folder, as a Wine or EXE-style
	// command expects; Steam needs no directory.
	var gameEnv []string
	var dir string
	if r.method.Name == domain.LaunchViaCommand {
		dir = game.InstallPath
		gameEnv = []string{"LMM_GAME_ID=" + game.ID, "LMM_GAME_PATH=" + game.InstallPath, "LMM_MOD_PATH=" + game.ModPath, "LMM_PROFILE=" + profile}
	}
	isCredential := s.isCredentialEnv()
	for _, kv := range r.env {
		if name, _, _ := strings.Cut(kv, "="); !isCredential(name) {
			gameEnv = append(gameEnv, kv)
		}
	}

	manager, systemdErr := l.systemd(ctx)
	if systemdErr != nil && !opts.AllowDetached {
		return nil, systemdErr
	}
	if !l.hasDisplay(manager) {
		return nil, &LaunchEnvironmentError{Missing: slices.Clone(launchDisplayEnv)}
	}

	if systemdErr != nil {
		result.Command = r.argv
		if opts.DryRun {
			return result, nil
		}
		if err := l.Detach(r.argv, launchEnviron(l.Environ(), isCredential, gameEnv), dir); err != nil {
			return nil, fmt.Errorf("starting %s: %w", r.argv[0], err)
		}
		return result, nil
	}

	result.Unit = launchUnitName(game.ID, l.UnitSuffix())
	// Type=exec (systemd 240) has systemd-run return once the program has
	// really been executed, not at fork: a program that cannot start, or a
	// working directory that is gone, is systemd-run failing - and this
	// launch failing - rather than "Started" over a unit that died. It
	// still never waits for the game.
	args := []string{"--user", "--collect", "--service-type=exec", "--unit=" + result.Unit, "--description=lmm: " + game.Name}
	for _, k := range launchForwardedEnv {
		if v := l.Getenv(k); v != "" && !isCredential(k) {
			args = append(args, "--setenv="+k+"="+v)
		}
	}
	for _, kv := range gameEnv {
		args = append(args, "--setenv="+kv)
	}
	if dir != "" {
		args = append(args, "--working-directory="+dir)
	}
	// The service manager expands $VAR in a unit's arguments; "$$" is a
	// literal "$", so the game gets its argv as games.yaml wrote it. The
	// program is not expanded: systemd-run looks it up as written (a "$$"
	// there names a file that does not exist) and the manager executes
	// that path. Escaping works on every systemd, where
	// --expand-environment=no needs 254.
	args = append(args, "--", r.argv[0])
	for _, a := range r.argv[1:] {
		args = append(args, strings.ReplaceAll(a, "$", "$$"))
	}
	result.Command = append([]string{"systemd-run"}, args...)
	if opts.DryRun {
		return result, nil
	}
	if _, err := l.Run(ctx, "systemd-run", args...); err != nil {
		return nil, fmt.Errorf("starting %s (%s) as systemd user unit %s: %w; see `journalctl --user -u %s`", game.Name, r.argv[0], result.Unit, err, result.Unit)
	}
	return result, nil
}

// systemd returns the user manager's variable names, or a
// *SystemdUnavailableError when systemd-run is not installed or no user
// manager answers.
func (l *Launcher) systemd(ctx context.Context) (map[string]bool, error) {
	if _, err := l.LookPath("systemd-run"); err != nil {
		return nil, &SystemdUnavailableError{Reason: "systemd-run is not installed", Err: err}
	}
	names, err := l.ManagerEnv(ctx)
	if err != nil {
		return nil, &SystemdUnavailableError{Reason: "no systemd user manager is running for this session (" + err.Error() + ")", Err: err}
	}
	return names, nil
}

// hasDisplay reports whether the game will see a display variable: from
// lmm, which forwards it, or from the user manager (nil when the game is
// started detached, inheriting lmm's environment alone).
func (l *Launcher) hasDisplay(manager map[string]bool) bool {
	for _, k := range launchDisplayEnv {
		if l.Getenv(k) != "" || manager[k] {
			return true
		}
	}
	return false
}

// launchUnitName is lmm-launch-<game>-<suffix>, with every character a
// unit name cannot carry in the game id replaced by "_".
func launchUnitName(gameID, suffix string) string {
	id := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			return r
		default:
			return '_'
		}
	}, gameID)
	return "lmm-launch-" + id + "-" + suffix
}

// launchDrift is the cheap pre-launch check: how many of profile's mods
// are enabled but not deployed or deployed but disabled - the web UI's
// undeployed-changes rule, CountUndeployed - and whether the mod_path
// needs attention. nil when everything is in step. It never verifies files
// on disk; that is `lmm verify`, and too slow for every launch.
func (s *Service) launchDrift(ctx context.Context, game *domain.Game, profile string) (*LaunchDriftError, error) {
	mods, err := s.GetInstalledMods(ctx, game.ID, profile)
	if err != nil {
		return nil, fmt.Errorf("reading installed mods: %w", err)
	}
	modPathErr, err := s.modPathError(ctx, game)
	if err != nil {
		return nil, err
	}
	n := CountUndeployed(mods)
	if n == 0 && modPathErr == "" {
		return nil, nil
	}
	still := 0
	for _, m := range mods {
		if !m.External && m.Deployed && !m.Enabled {
			still++
		}
	}
	return &LaunchDriftError{
		GameID: game.ID, Profile: profile, Undeployed: n, StillDeployed: still, ModPathError: modPathErr,
		DeployClears: still == 0 && modPathErr == "",
	}, nil
}

// CountUndeployed counts the mods whose desired state (Enabled) disagrees
// with what is on disk (Deployed). An external mod never counts: lmm does
// not deploy it, and its Deployed never changes. It is the rule the web
// UI's undeployed-changes indicator uses (modrows.js countUndeployed).
func CountUndeployed(mods []domain.InstalledMod) int {
	n := 0
	for _, m := range mods {
		if !m.External && m.Enabled != m.Deployed {
			n++
		}
	}
	return n
}
