package main

import (
	"fmt"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
)

// renderPluginList prints what a deploy or a purge did to the game's plugin
// list (#616): one line, and the file's path (and Skyrim LE's
// loadorder.txt beside it). The findings themselves were
// printed as they happened, as warnings, so they are not repeated here.
func renderPluginList(res *core.PluginListResult) {
	if res == nil {
		return
	}
	var line string
	switch res.Status {
	case core.PluginListWritten, core.PluginListUnchanged:
		verb := "written"
		if res.Status == core.PluginListUnchanged {
			verb = "unchanged"
		}
		line = fmt.Sprintf("Plugins.txt %s: %d plugin(s), %d active", verb, res.Plugins, res.Active)
		if res.External > 0 {
			line += fmt.Sprintf(" (%d kept that lmm did not deploy)", res.External)
		}
	case core.PluginListRestored:
		line = "Plugins.txt restored to the list you had before lmm wrote it"
	case core.PluginListRemoved:
		line = "Plugins.txt removed (there was none before lmm wrote it)"
	default:
		line = colorYellow("Plugins.txt not written") + " - see the warnings above"
	}
	if res.HasErrors() && res.Status != core.PluginListSkipped {
		line += " - " + colorRed("with errors, see the warnings above")
	}
	fmt.Println(line)
	if res.ListFile != "" {
		fmt.Printf("  %s\n", res.ListFile)
		if res.OrderFile != "" {
			fmt.Printf("  %s\n", res.OrderFile)
		}
	}
}
