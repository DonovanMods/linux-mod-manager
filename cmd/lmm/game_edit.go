package main

import (
	"context"
	"fmt"
	"maps"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"

	"github.com/spf13/cobra"
)

var gameEditCmd = &cobra.Command{
	Use:   "edit <game-id>",
	Short: "Edit a configured game's source mappings",
	Long: `Change which mod sources a configured game maps, and what this game's
identifier is with each of them - the games.yaml "sources:" map.

Until now that map could only be set when the game was created, so a
custom source added later (see 'lmm source add' and the web UI's Setup ->
Custom sources editor) could never be attached to an existing game
without hand-editing games.yaml.

--source takes "<source-id>=<identifier>" and adds or replaces one entry;
the identifier is whatever that source knows this game by (a NexusMods
slug, a CurseForge numeric game id, a custom source's own key) and may be
empty for a source that needs none. --remove-source drops one entry by
its source id. Both are repeatable, and removals are applied before
additions, so the same id can be re-pointed in one run.

The source must already be registered ('lmm source list' shows every one,
built-in or custom). A game must keep at least one source, so removing
the last one is refused.

Examples:
  lmm game edit skyrim-se --source curseforge=skyrim
  lmm game edit icarus --source local-mods= --remove-source nexusmods
  lmm game edit skyrim-se --source nexusmods=skyrimspecialedition --json`,
	Args: cobra.ExactArgs(1),
	RunE: runGameEdit,
}

var (
	gameEditSources []string
	gameEditRemove  []string
)

func init() {
	gameCmd.AddCommand(gameEditCmd)

	gameEditCmd.Flags().StringArrayVar(&gameEditSources, "source", nil,
		`add or replace a source mapping, as "<source-id>=<identifier>" (repeatable)`)
	gameEditCmd.Flags().StringArrayVar(&gameEditRemove, "remove-source", nil,
		"drop a source mapping by its source id (repeatable)")
}

func runGameEdit(cmd *cobra.Command, args []string) error {
	return withService(cmd, func(ctx context.Context, service *core.Service) error {
		return doGameEdit(ctx, service, args[0])
	})
}

// doGameEdit resolves the flags into the FULL source map the game should
// end up with and hands it to core.Service.UpdateGameSources, which owns
// replace semantics, the registry check and the write.
//
// The flags are a DELTA against what the game currently maps - which is
// what a command line wants - while core's seam is a replacement, which is
// what `PUT /api/v1/games/{id}` wants; resolving the one into the other is
// this function's entire job, and it is the only difference between the
// two frontends' paths.
func doGameEdit(ctx context.Context, service *core.Service, gameID string) error {
	if len(gameEditSources) == 0 && len(gameEditRemove) == 0 {
		return fmt.Errorf("nothing to edit: pass --source <id>=<identifier> or --remove-source <id>")
	}

	game, err := service.GetGame(gameID)
	if err != nil {
		return fmt.Errorf("game not found: %s", gameID)
	}

	sources := maps.Clone(game.SourceIDs)
	if sources == nil {
		sources = map[string]string{}
	}

	// Removals first, so "--remove-source x --source x=y" re-points x
	// rather than deleting what the same run just set.
	for _, id := range gameEditRemove {
		id = strings.TrimSpace(id)
		if _, ok := sources[id]; !ok {
			return fmt.Errorf("game %s does not map source %q", gameID, id)
		}
		delete(sources, id)
	}

	for _, pair := range gameEditSources {
		id, identifier, ok := strings.Cut(pair, "=")
		if !ok {
			return fmt.Errorf("--source %q must be <source-id>=<identifier> (the identifier may be empty)", pair)
		}
		sources[strings.TrimSpace(id)] = strings.TrimSpace(identifier)
	}

	entry, err := service.UpdateGameSources(ctx, gameID, sources)
	if err != nil {
		return err
	}

	// Ruling 15: the GameListEntry document - the same row `lmm game list
	// --json` emits for this game - in place of the console lines.
	if jsonOutput {
		return emitJSON(entry)
	}

	fmt.Printf("%s %s sources updated: %s\n", colorGreen("✓"), entry.Name, formatGameSources(entry.SourceIDs))
	return nil
}
