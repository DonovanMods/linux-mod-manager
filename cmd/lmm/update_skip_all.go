package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// doUpdateSkipAll is `lmm update --skip-all` / `--unskip-all` (#667): #542's
// per-mod skip for every offered update at once, or every skip cleared.
//
// They are flags of `lmm update` rather than `lmm mod skip-update --all`
// because they act on the update list - the same check `lmm update` prints
// - not on a mod the user names; the per-mod commands stay under `lmm mod`.
func doUpdateSkipAll(ctx context.Context, service *core.Service, game *domain.Game, args []string) error {
	switch {
	case updateSkipAll && updateUnskipAll:
		return errors.New("--skip-all and --unskip-all cannot be combined")
	case len(args) > 0:
		return fmt.Errorf("--skip-all/--unskip-all act on every update; to skip one mod's, use 'lmm mod skip-update %s'", args[0])
	case updateAll, updateDryRun, updateFromFile != "":
		return errors.New("--skip-all/--unskip-all cannot be combined with --all, --dry-run or --from-file")
	}
	profileName, err := resolveProfile(ctx, service, game.ID, updateProfile)
	if err != nil {
		return err
	}

	var result *core.UpdateSkipAllResult
	if updateSkipAll {
		result, err = service.SkipAllUpdates(ctx, game, profileName)
	} else {
		result, err = service.UnskipAllUpdates(ctx, game.ID, profileName)
	}
	if err != nil {
		if errors.Is(err, domain.ErrAuthRequired) {
			return authPromptError(core.AuthRequiredSource(err))
		}
		return err
	}
	if jsonOutput {
		return emitJSON(result)
	}

	n := len(result.Mods)
	switch {
	case updateSkipAll && n == 0:
		fmt.Println("No updates to skip.")
	case updateSkipAll:
		fmt.Printf("%s Skipped %d update%s — hidden until a newer version appears ('lmm update --unskip-all' to show them again):\n",
			colorGreen("✓"), n, plural(n))
	case n == 0:
		fmt.Println("No skipped updates to clear.")
	default:
		fmt.Printf("%s Cleared %d skipped update%s — 'lmm update' offers them again:\n", colorGreen("✓"), n, plural(n))
	}
	for _, m := range result.Mods {
		fmt.Printf("  %s %s\n", m.Name, m.Version)
	}
	return nil
}

// cliArg renders s as one shell argument in a suggested command: as is
// when it needs no quoting, else double-quoted.
func cliArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}()<>|&;#~") {
		return fmt.Sprintf("%q", s)
	}
	return s
}
