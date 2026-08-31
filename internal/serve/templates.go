package serve

import (
	"embed"
	"html/template"
)

//go:embed templates/*.gohtml
var templateFS embed.FS

// parsePage builds the *template.Template for one page: the shared layout
// plus the named page's own templates/<name>.gohtml. Each page gets its own
// *template.Template (rather than one shared tree holding every page)
// because every page defines a template named "content" - html/template
// has no block-inheritance mechanism, so two "content" definitions loaded
// into the same tree would silently overwrite one another.
func parsePage(name string) *template.Template {
	return template.Must(template.ParseFS(templateFS, "templates/layout.gohtml", "templates/"+name+".gohtml"))
}

// statusTemplate renders "/" - the status dashboard
// (docs/plans/2026-08-30-serve-design.md §HTTP surface).
var statusTemplate = parsePage("status")

// modsTemplate renders "/mods" - the installed mods list
// (docs/plans/2026-08-30-serve-design.md §HTTP surface).
var modsTemplate = parsePage("mods")
