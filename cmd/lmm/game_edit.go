package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"

	"github.com/spf13/cobra"
)

var gameEditCmd = &cobra.Command{
	Use:   "edit <game-id>",
	Short: "Edit a configured game's sources, mod path, adapter or loader",
	Long: `Change which mod sources a configured game maps, and what this game's
identifier is with each of them - the games.yaml "sources:" map.

Until now that map could only be set when the game was created, so a
custom source added later (see 'lmm source add' and the web UI's Setup ->
Custom sources editor) could never be attached to an existing game
without hand-editing games.yaml.

--source takes "SOURCE-ID=IDENTIFIER" and adds or replaces one entry;
the identifier is whatever that source knows this game by (a NexusMods
slug, a CurseForge numeric game id, a custom source's own key) and may be
empty for a source that needs none. --remove-source drops one entry by
its source id. Both are repeatable, and removals are applied before
additions, so the same id can be re-pointed in one run.

The source must already be registered ('lmm source list --all' shows every
one, built-in or custom; without --all the list is scoped to the current
game, which is exactly the set you are editing). A game must keep at
least one source, so removing the last one is refused.

--loader declares a mod loader installed in the game directory (today
'bepinex'), with --loader-version, --loader-runtime (mono|il2cpp) and
--loader-bootstrap (native|proton) refining it; leave the last two out and
lmm reads them off the install directory. Declaring a loader is what makes
lmm normalise ambiguous BepInEx archive layouts for this game, refuse to
deploy a plugin the game cannot load, and run 'lmm verify's loader checks.
'--loader ""' removes the declaration. lmm never installs the loader and
never writes a Steam launch option - 'lmm game show' prints the exact
string to paste.

--mod-path sets the directory lmm deploys mods into - the repair for a
mod_path that no longer exists, which 'lmm game show' and 'lmm status'
flag. A relative path is relative to the game's install path, and "~/"
is your home directory. The directory does not have to exist yet (a
deploy creates it), but a file is refused. lmm records every deployed
file relative to the mod_path, so the edit is refused while files are
deployed: run 'lmm purge --game <id>' first, then this, then 'lmm
deploy'. A BepInEx game deploys into its install path, so that is its
mod path; --mod-path and --adapter bepinex can be passed together, and
the mod path is changed first.

Sources and the loader are separate edits: pass one or the other.

Examples:
  lmm game edit skyrim-se --source curseforge=skyrim
  lmm game edit icarus --source local-mods= --remove-source nexusmods
  lmm game edit human-host --mod-path "~/.steam/steam/steamapps/common/Human Host"
  lmm game edit valheim --mod-path /games/valheim --adapter bepinex
  lmm game edit valheim --loader bepinex --loader-version 5.4.23.5 --loader-bootstrap proton
  lmm game edit valheim --loader ""
  lmm game edit skyrim-se --source nexusmods=skyrimspecialedition --json`,
	Args: cobra.ExactArgs(1),
	RunE: runGameEdit,
}

var (
	gameEditSources []string
	gameEditRemove  []string
	gameEditAdapter string
	gameEditModPath string
	// gameEditModPathSet records that --mod-path was passed at all, so
	// `--mod-path ""` reaches core's refusal instead of reading as absent.
	gameEditModPathSet bool

	gameEditLoader          string
	gameEditLoaderVersion   string
	gameEditLoaderRuntime   string
	gameEditLoaderBootstrap string
)

func init() {
	gameCmd.AddCommand(gameEditCmd)

	gameEditCmd.Flags().StringArrayVar(&gameEditSources, "source", nil,
		`add or replace a source mapping, as "SOURCE-ID=IDENTIFIER" (repeatable)`)
	gameEditCmd.Flags().StringArrayVar(&gameEditRemove, "remove-source", nil,
		"drop a source mapping by its source id (repeatable)")
	gameEditCmd.Flags().StringVar(&gameEditModPath, "mod-path", "",
		"set the directory lmm deploys mods into (relative to the install path unless absolute)")
	gameEditCmd.Flags().StringVar(&gameEditAdapter, "adapter", "",
		`set the game adapter; the empty string ("") clears it, so the game uses its derived adapter (generic-files unless deploy_mode or BepInEx selects one)`)
	gameEditCmd.Flags().StringVar(&gameEditLoader, "loader", "",
		`declare a mod loader installed in the game directory (today: bepinex); "" removes the declaration`)
	gameEditCmd.Flags().StringVar(&gameEditLoaderVersion, "loader-version", "",
		"the loader version installed in the game directory, e.g. 5.4.23.5 (checked by 'lmm verify')")
	gameEditCmd.Flags().StringVar(&gameEditLoaderRuntime, "loader-runtime", "",
		"the game's Unity scripting backend: mono or il2cpp (default: read from the install directory)")
	gameEditCmd.Flags().StringVar(&gameEditLoaderBootstrap, "loader-bootstrap", "",
		"how the loader is injected: native or proton (default: read from the install directory)")
}

func runGameEdit(cmd *cobra.Command, args []string) error {
	adapterSet := cmd.Flags().Changed("adapter")
	gameEditModPathSet = cmd.Flags().Changed("mod-path")
	return withGameWriteService(cmd, args[0], func(ctx context.Context, service *core.Service) error {
		// Changed("loader") rather than a non-empty value, so `--loader ""`
		// is an explicit "this game has no loader after all" and reaches
		// core's nil rather than reading as "no loader flag was passed" -
		// the same distinction --id draws in game_add.go (#387).
		if cmd.Flags().Changed("loader") {
			return doGameEditLoader(ctx, service, args[0], adapterSet)
		}
		return doGameEdit(ctx, service, args[0], adapterSet)
	})
}

// doGameEditLoader is the --loader half of `lmm game edit`: one call to
// core.Service.UpdateGameLoader, which owns replace semantics, validation
// and the write.
//
// It is a SEPARATE edit from the source map and from the adapter (#353)
// rather than one call taking all three, because each is a complete
// statement on its own and combining them would make a partial failure -
// sources written, loader not - expressible. The command refuses a run that
// asks for more than one rather than picking an order.
func doGameEditLoader(ctx context.Context, service *core.Service, gameID string, adapterSet bool) error {
	if len(gameEditSources) > 0 || len(gameEditRemove) > 0 || adapterSet || gameEditModPathSet {
		return fmt.Errorf("edit the loader separately: pass --loader, or --source/--remove-source/--adapter/--mod-path, not both")
	}

	spec, err := loaderSpecFromFlags(gameEditLoader, gameEditLoaderVersion, gameEditLoaderRuntime, gameEditLoaderBootstrap)
	if err != nil {
		return err
	}

	entry, err := service.UpdateGameLoader(ctx, gameID, spec)
	if err != nil {
		return err
	}

	// Ruling 15: the GameListEntry document - the same row `lmm game list
	// --json` emits for this game - in place of the console lines.
	if jsonOutput {
		return emitJSON(entry)
	}

	if entry.Loader == nil {
		fmt.Printf("%s %s no longer declares a mod loader\n", colorGreen("✓"), entry.Name)
		return nil
	}
	fmt.Printf("%s %s declares the %s loader\n", colorGreen("✓"), entry.Name, entry.Loader.Kind)
	fmt.Printf("  Run `lmm game show %s` for the Steam launch option it needs.\n", entry.ID)
	return nil
}

// doGameEdit applies the non-loader flags, each through its own gated core
// write, in the only order that always works: the mod path, then the
// adapter (bepinex is refused off the game root, so a run moving a game to
// its root and onto bepinex must move it first), then the source map. A
// failure stops the run with what came before it already written, and says
// which edit failed.
//
// The source flags are a DELTA against what the game currently maps - which
// is what a command line wants - while core's seam is a replacement, which
// is what `PUT /api/v1/games/{id}` wants; resolving the one into the other
// is sourceMapFromFlags' entire job, and it is the only difference between
// the two frontends' paths.
func doGameEdit(ctx context.Context, service *core.Service, gameID string, adapterSet bool) error {
	editsSources := len(gameEditSources) > 0 || len(gameEditRemove) > 0
	if !editsSources && !adapterSet && !gameEditModPathSet {
		return fmt.Errorf("nothing to edit: pass --source <id>=<identifier>, --remove-source <id>, --adapter <name>, or --mod-path <path>")
	}

	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	var (
		entry *core.GameListEntry
		lines []string
	)

	// #427/#456: the repair for a mod_path that no longer exists, and the
	// command form of the BepInEx remedies' mod_path step.
	if gameEditModPathSet {
		entry, err = service.SetGameModPath(ctx, gameID, gameEditModPath)
		if err != nil {
			return err
		}
		lines = append(lines, "mod path set to "+entry.ModPath)
		if entry.ModPathError != "" {
			lines = append(lines, "  "+colorYellow("!")+" "+entry.ModPathError)
		} else if _, err := os.Stat(entry.ModPath); errors.Is(err, fs.ErrNotExist) {
			lines = append(lines, "  "+colorDim("not created yet - the first deploy creates it"))
		}
	}

	// #353: the adapter edit is its own single-step write
	// (core.SetGameAdapter), which owns the registry check and the
	// deploy_mode: compile composition rule.
	if adapterSet {
		if err := validateAdapterFlag(service, gameEditAdapter); err != nil {
			return err
		}
		entry, err = service.SetGameAdapter(ctx, gameID, gameEditAdapter)
		if err != nil {
			return err
		}
		// #426: clearing the key hands the game back to whatever lmm
		// derives for it, which is not always generic-files - so say
		// which adapter is now in use.
		if entry.Adapter == "" {
			lines = append(lines, "adapter cleared; it now uses "+formatGameAdapter(*entry))
		} else {
			lines = append(lines, "adapter set to "+formatAdapterName(entry.Adapter))
		}
	}

	if editsSources {
		sources, err := sourceMapFromFlags(game)
		if err != nil {
			return err
		}
		entry, err = service.UpdateGameSources(ctx, gameID, sources)
		if err != nil {
			return err
		}
		lines = append(lines, "sources updated: "+formatGameSources(entry.SourceIDs))
	}

	// Ruling 15: the GameListEntry document - the same row `lmm game list
	// --json` emits for this game, as the last write left it - in place of
	// the console lines.
	if jsonOutput {
		return emitJSON(entry)
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "  ") {
			fmt.Println(line)
			continue
		}
		fmt.Printf("%s %s %s\n", colorGreen("✓"), entry.Name, line)
	}
	return nil
}

// sourceMapFromFlags resolves --source/--remove-source against game's
// current map into the full map it should end up with.
func sourceMapFromFlags(game *domain.Game) (map[string]string, error) {
	sources := maps.Clone(game.SourceIDs)
	if sources == nil {
		sources = map[string]string{}
	}

	// Removals first, so "--remove-source x --source x=y" re-points x
	// rather than deleting what the same run just set.
	for _, id := range gameEditRemove {
		id = strings.TrimSpace(id)
		if _, ok := sources[id]; !ok {
			return nil, fmt.Errorf("game %s does not map source %q", game.ID, id)
		}
		delete(sources, id)
	}

	for _, pair := range gameEditSources {
		id, identifier, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("--source %q must be <source-id>=<identifier> (the identifier may be empty)", pair)
		}
		sources[strings.TrimSpace(id)] = strings.TrimSpace(identifier)
	}
	return sources, nil
}
