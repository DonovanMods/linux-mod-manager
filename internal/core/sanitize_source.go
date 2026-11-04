package core

import (
	"slices"

	"github.com/DonovanMods/linux-mod-manager/v2/internal/domain"
)

// #594: every value core receives from a source.ModSource passes through
// one of these on its way in - searchSource, GetMod, GetModFiles,
// GetDependencies, the update check, ModDetail's description and
// changelog fetches, and the Workshop adopt/collection describers - so
// neither frontend, nor the installed_mods row an
// install writes, ever carries a raw terminal control sequence a mod page
// put in its metadata. Each returns a copy: a source may hand back a value
// it also keeps (a cache, a fixture), and core must not write into it.

// sanitizeSourceMod is a cleaned copy of m (nil for nil).
func sanitizeSourceMod(m *domain.Mod) *domain.Mod {
	if m == nil {
		return nil
	}
	c := *m
	c.Dependencies = slices.Clone(m.Dependencies)
	c.SanitizeSourceText()
	return &c
}

// sanitizeSourceMods is sanitizeSourceMod over a search page.
func sanitizeSourceMods(mods []domain.Mod) []domain.Mod {
	if mods == nil {
		return nil
	}
	out := make([]domain.Mod, len(mods))
	for i := range mods {
		out[i] = *sanitizeSourceMod(&mods[i])
	}
	return out
}

// sanitizeSourceFiles is a cleaned copy of a source's file list.
func sanitizeSourceFiles(files []domain.DownloadableFile) []domain.DownloadableFile {
	if files == nil {
		return nil
	}
	out := slices.Clone(files)
	for i := range out {
		out[i].SanitizeSourceText()
	}
	return out
}

// sanitizeSourceRefs is a cleaned copy of a source's dependency references.
func sanitizeSourceRefs(refs []domain.ModReference) []domain.ModReference {
	if refs == nil {
		return nil
	}
	out := slices.Clone(refs)
	for i := range out {
		out[i].Version = domain.SanitizeLine(out[i].Version)
	}
	return out
}

// sanitizeSourceUpdates is a cleaned copy of an update check's answer.
func sanitizeSourceUpdates(updates []domain.Update) []domain.Update {
	if updates == nil {
		return nil
	}
	out := slices.Clone(updates)
	for i := range out {
		out[i].InstalledMod.Dependencies = slices.Clone(out[i].InstalledMod.Dependencies)
		out[i].SanitizeSourceText()
	}
	return out
}
