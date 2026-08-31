package serve

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/app.css
var staticFS embed.FS

// staticHandler serves the embedded static assets (app.css, the committed
// Tailwind build output described by the Makefile's `css` target) under
// /static/.
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// static/app.css is embedded at compile time; a broken embed is a
		// build-time defect, not a runtime condition to recover from.
		panic(err)
	}
	return http.FileServerFS(sub)
}
