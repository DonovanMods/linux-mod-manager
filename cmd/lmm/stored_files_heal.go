package main

import (
	"fmt"
	"strings"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/core"
	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// storedFileLines renders a plan's #662 entries: each mod whose stored
// main files are of more than one version - the copies of one file it
// drops and the version it keeps, and any separate component it keeps at
// another version - then each collection mod whose curator's copies of
// one file span versions and that installs as chosen. Nil for neither.
// Names and file names go through domain.SanitizeLine, as every name the
// CLI prints from a source is.
func storedFileLines(heals []core.StoredFilesHeal, curator []core.CuratorMixedFiles) []string {
	var lines []string
	files := func(versions []core.MixedVersion) string {
		var out []string
		for _, v := range versions {
			for _, f := range v.Files {
				out = append(out, fmt.Sprintf("%s (%s)", domain.SanitizeLine(fileLabel(f)), domain.SanitizeLine(v.Version)))
			}
		}
		return strings.Join(out, ", ")
	}
	if len(heals) > 0 {
		lines = append(lines, fmt.Sprintf("%d mod(s) have stored main files of more than one version:", len(heals)))
		dropping := false
		for _, h := range heals {
			var parts []string
			mark := "="
			if len(h.Dropped) > 0 {
				dropping, mark = true, "~"
				keep := "keeps version " + domain.SanitizeLine(h.Version)
				if h.RecordedVersion != "" {
					keep = fmt.Sprintf("keeps the newest, %s (none is at the recorded %s)", domain.SanitizeLine(h.Version), domain.SanitizeLine(h.RecordedVersion))
				}
				parts = append(parts, keep+", drops "+files(h.Dropped))
			}
			if len(h.Kept) > 0 {
				parts = append(parts, "keeps "+files(h.Kept)+", a separate component")
			}
			lines = append(lines, fmt.Sprintf("  %s %s: %s", mark, modLabel(h.Name, h.SourceID, h.ModID), strings.Join(parts, "; ")))
		}
		if dropping {
			lines = append(lines, "  (a dropped file is a copy of a kept one at another version; reinstall the mod at its version to use it instead)")
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
