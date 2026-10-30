# Launch Game (#501)

Issue #501 asks for launching the game from lmm, in both the CLI and the
web UI. The decisions below were confirmed with the owner on 2026-10-30.

## What the code gives us today

- lmm stores no Steam app id. Neither `domain.Game` nor
  `config.GameConfig` (`internal/storage/config/games.go`) has one, and
  neither has anything about launching. The id exists only on
  `domain.DetectedGame.SteamAppID` during detection, and on
  `Sources["steamworkshop"]` for games that use the Workshop source.
- Hooks (`internal/core/hooks.go`) already run external programs directly,
  without a shell, with `os.Environ()` plus the `LMM_*` variables. The web UI
  cannot edit hooks: `core.GameEdit` has no hooks field.
- `packaging/systemd/lmm-serve.service` sets `NoNewPrivileges`,
  `LockPersonality` and `RestrictRealtime`, and `UserUnits.RestartServeIfActive`
  restarts the service on self-update.

## Decisions

### 1. How a game is launched: Steam, else a custom command

lmm picks the first of these that applies:

1. `launch.command` in games.yaml: run it.
2. A Steam app id is known: `steam steam://rungameid/<id>`. If the game is in
   a Flatpak Steam library, run `flatpak run com.valvesoftware.Steam …`. If no
   `steam` binary is found, use `xdg-open`.
3. Neither: refuse with `LaunchUnconfiguredError`, which names
   `launch.command` as the fix.

The app id comes from the first of these:

1. `launch.steam_app_id`;
2. `Sources["steamworkshop"]`;
3. a new `steam.AppIDForInstallPath`, which matches the game's `InstallPath`
   against the `installdir` in `appmanifest_*.acf` across `GetLibraryPaths()`.

So every game added from Steam can launch with no change to games.yaml.

Launching through Steam handles Proton, its prefix and runtime, and the user's
own launch options. That includes the BepInEx `%command%` launch option from
`docs/plans/2026-09-09-bepinex-spike.md`.

Running a game's executable directly is not a built-in method. lmm doesn't
know which file is the executable, and a Proton game run outside Steam loses
its runtime. Anyone who wants that can put it in `launch.command`. That also
covers non-Steam games (Lutris, Heroic, Bottles, plain Wine), for example
`["lutris", "lutris:rungame/wow"]`.

### 2. A profile that isn't fully deployed: warn, and offer to deploy

- **Which profile:** lmm launches the active profile only, by the same rule
  as `lmm deploy --profile`. `--profile X` for a profile that isn't active is
  refused, with a hint to run `lmm profile switch`.
- **The drift check:** a cheap database query. It counts mods where enabled
  ≠ deployed, using the `countUndeployed` rule in `spa/app/modrows.js`, which
  moves into core. It also checks `ModPathError`. When anything is
  undeployed, Launch returns `LaunchDriftError`, whose `Details` carries the
  undeployed count.
- **CLI:** prints a warning and launches anyway. `--deploy` runs
  `PlanDeploy`/`ApplyDeploy` first. `--strict` refuses.
- **Web UI:** the 409 opens a choice between **Deploy & launch** (the
  existing deploy job, then the launch call) and **Launch anyway**.
- **What it doesn't check:** there is no full verify on each launch, because
  it's too slow.
- **Hooks:** none in v1. A wrapper script in `launch.command` covers that
  need. If a hook is wanted later, it goes in a `hooks.launch.before` slot.

### 3. Who owns the process: a transient systemd user service

lmm always starts the game with:

`systemd-run --user --collect --unit=lmm-launch-<game>-<rand> --description="lmm: <name>" [--setenv=K=V …] -- <argv>`

It returns straight away and never waits. With Steam, success only means
"launch requested", and the result says so.

Why not run the game as lmm's own child, or use `--scope`:

- **The cgroup:** a child of `lmm serve` sits in `lmm-serve.service`'s
  cgroup. A self-update or `systemctl --user restart lmm-serve` would kill
  the game, and also the Steam client if lmm had just started it.
- **The service's restrictions:** `NoNewPrivileges` and the others carry over
  to any process forked from lmm, even under `--scope`. That breaks file
  capabilities (gamescope's `cap_sys_nice`) and can upset Wine.
- **What a transient service avoids:** the user manager starts it, not lmm,
  so it inherits neither the cgroup nor the restrictions.

Environment:

- **Variables passed through:** lmm passes `--setenv` for each of `DISPLAY`,
  `WAYLAND_DISPLAY`, `XDG_RUNTIME_DIR`, `DBUS_SESSION_BUS_ADDRESS` and
  `XDG_CURRENT_DESKTOP` that its own process has. From the CLI, the shell's
  values win.
- **Under `lmm serve`:** the service has the manager's environment and
  nothing else, which is the same gap as #582.
- **No display at all:** if neither display variable is set in lmm's
  environment or in the manager's (`UserUnits.ManagerEnvironmentNames`,
  from #582), Launch refuses with `LaunchEnvironmentError`. The fix it names
  is `dbus-update-activation-environment --systemd DISPLAY WAYLAND_DISPLAY`,
  or an `environment.d` file.
- **No systemd:** the CLI falls back to a detached spawn (`Setsid`, stdio to
  `/dev/null`). `lmm serve` refuses.

### 4. Surfaces: `lmm launch` and a Play button in the top bar

- **CLI:** `lmm launch [--game] [--profile] [--deploy] [--strict]
  [--dry-run] [--json]`, a top-level command next to `deploy`.
  - `--dry-run` prints the method and the full command line without running
    anything.
  - `--json` emits `LaunchResult`, with a golden.
- **API:** `POST /api/v1/games/{id}/launch` with `{profile, force}`. It is a
  single beginOp-gated call, not a job: there is nothing to preview or
  stream.
- **Web UI:** a **Play** button in `topbar.js` next to Deploy. When
  `GameStatus.Launch.Method` is empty, the button is disabled, with the
  reason as its tooltip. The game chooser cards get no Play button.

### 5. Config

Per game, in games.yaml. `GameConfig.Launch` is a pointer field
(`*GameLaunchYAML`), so files that don't set it are saved unchanged, as with
`Loader`.

```yaml
launch:
  via: steam | command   # optional; default is the order in §1
  steam_app_id: "489830" # optional override; digits only
  command: ["~/bin/run.sh", "--profile", "{profile}"] # argv, never a shell string
```

- **Placeholders:** `{game_id}`, `{install_path}`, `{mod_path}`, `{profile}`
  and `{steam_app_id}`. The `LMM_*` environment variables are set as they are
  for hooks.
- **Validation:** in `loadGamesLocked`, failing loudly as #172 does. Refused:
  an unknown placeholder, an empty `command[0]`, `via: command` with no
  command, and a non-numeric app id.
- **`~` expansion:** applies to `command[0]`, through `ExpandPath`.
- **Per-profile override:** deferred.

### 6. Security

- The request carries only the game id, the profile and `force`. The command
  line and environment come only from games.yaml.
- `GameEdit` and the web UI must never be able to write `launch.command`,
  the same rule as for hooks. Otherwise a web request could make lmm run any
  program. `steam_app_id` is plain data and may be edited.
- CSRF and the Host allow-list already cover the POST.
- The warning for a non-loopback `--addr` adds "and launch programs".

## Core API (`internal/core/launch.go`)

```go
type LaunchOptions struct{ Profile string; Force, Strict, DryRun bool }
type LaunchResult struct {
	Game, Profile, Method string // method: "steam" | "command"
	Argv                  []string
	Unit                  string
	Undeployed            int
	Warnings              []string
	DryRun                bool
}
type LaunchInfo struct{ Method, Reason string } // on GameStatus as `launch,omitzero`

func (s *Service) ResolveLaunch(ctx context.Context, game *domain.Game, profile string) (*LaunchInfo, []string, error) // read-only
func (s *Service) Launch(ctx context.Context, game *domain.Game, opts LaunchOptions) (*LaunchResult, error)
```

`Launch` takes `beginOp`, so it can't race a deploy job.

Process spawning goes through a `Launcher` with function-field seams (`LookPath`,
`Getenv`, `ManagerEnv`, `Run`), shaped like `UserUnits`. It is registered by
`app.RegisterLaunchers(svc)` the way adapters are, which keeps
`source/steam`, which core may not import, behind `app`.

Launch methods form a small registry, `LaunchMethod{Name; Resolve(game) (argv, ok)}`,
so #545 can add a `battlenet` method (Wine prefix plus product code, prefilled
by detection as `launch: {via: battlenet, id: …}`). Lutris and Heroic need
nothing new; they use `via: command`.

## Tests

No test may start a real process.

- **The `Run` seam:** tests record argv and `--setenv` through it. They cover:
  - the unit name;
  - which environment variables are forwarded;
  - the order lmm tries launch methods in;
  - the Flatpak and no-systemd branches;
  - placeholder expansion.
- **App id lookup:** fixture `appmanifest` files under
  `internal/source/steam/testdata`.
- **Core:** the drift refusal and the `--deploy` ordering, on the
  fake-source fixtures.
- **Config:** load, round-trip and preserve-unknown tests.
- **Goldens:** `LaunchResult` and `GameStatus.launch`.
- **Serve:** httptest for the 409 `Details`, a CSRF refusal, and that a
  request body can't inject argv.
- **E2E:** the Play button is disabled with its reason, and the
  Deploy & launch choice.

## Build order

1. Config schema and validation, and `steam.AppIDForInstallPath`.
2. Core `ResolveLaunch`/`Launch` with the `Launcher` seam: the transient
   unit, the environment check, the drift warning.
3. `lmm launch`, with `--dry-run`, `--json`, `--deploy` and `--strict`.
4. The serve endpoint, `GameStatus.launch`, and the Play button with
   Deploy & launch.
5. Docs: a guide page, the man page, and the service-environment section of
   background.md.
6. Later: a per-profile override, a launch hook, and the #545 `battlenet`
   method.
