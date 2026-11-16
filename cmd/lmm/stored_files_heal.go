package main

import (
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// storedFileLines renders a plan's #662 entries: each mod whose stored
// main files mix versions and that the run heals to one (the version kept
// and every file dropped), then each collection mod whose curator's files
// mix versions and that installs as chosen. Nil for neither. Names and
// file names go through domain.SanitizeLine, as every name the CLI prints
// from a source is.
func storedFileLines(heals []core.StoredFilesHeal, curator []core.CuratorMixedFiles) []string {
	var lines []string
	if len(heals) > 0 {
		lines = append(lines, fmt.Sprintf("Will heal %d mod(s) whose stored main files are of more than one version:", len(heals)))
		for _, h := range heals {
			keep := "keeps version " + domain.SanitizeLine(h.Version)
			if h.RecordedVersion != "" {
				keep = fmt.Sprintf("keeps the newest, %s (none is at the recorded %s)", domain.SanitizeLine(h.Version), domain.SanitizeLine(h.RecordedVersion))
			}
			var dropped []string
			for _, v := range h.Dropped {
				for _, f := range v.Files {
					dropped = append(dropped, fmt.Sprintf("%s (%s)", domain.SanitizeLine(fileLabel(f)), domain.SanitizeLine(v.Version)))
				}
			}
			lines = append(lines, fmt.Sprintf("  ~ %s: %s, drops %s", modLabel(h.Name, h.SourceID, h.ModID), keep, strings.Join(dropped, ", ")))
		}
	}
	if len(curator) > 0 {
		lines = append(lines, fmt.Sprintf("%d collection mod(s) install the curator's files, which mix versions:", len(curator)))
		for _, c := range curator {
			var versions []string
			for _, v := range c.Versions {
				var names []string
				for _, f := range v.Files {
					names = append(names, domain.SanitizeLine(fileLabel(f)))
				}
				versions = append(versions, fmt.Sprintf("%s (%s)", domain.SanitizeLine(v.Version), strings.Join(names, ", ")))
			}
			lines = append(lines, fmt.Sprintf("  = %s: %s", modLabel(c.Name, c.SourceID, c.ModID), strings.Join(versions, "; ")))
		}
	}
	return lines
}

// fileLabel is a mixed-version file's name, else its id.
func fileLabel(f core.MixedVersionFile) string {
	if f.FileName != "" {
		return f.FileName
	}
	return f.ID
}

// modLabel is a mod's name, else its source:id.
func modLabel(name, sourceID, modID string) string {
	if name != "" {
		return domain.SanitizeLine(name)
	}
	return domain.SanitizeLine(domain.ModKey(sourceID, modID))
}

// printStoredFiles prints storedFileLines, each line after indent.
func printStoredFiles(indent string, heals []core.StoredFilesHeal, curator []core.CuratorMixedFiles) {
	for _, line := range storedFileLines(heals, curator) {
		fmt.Println(indent + line)
	}
}
