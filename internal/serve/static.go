package serve

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/app.css static/app.js
var staticFS embed.FS

// staticHandler serves the embedded static assets under /static/: app.css
// (the committed Tailwind build output described by the Makefile's `css`
// target) and app.js (the progressive-enhancement script - Task 10; every
// page and mutation works with it absent, per WEBUI.md).
func staticHandler() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// static/app.css is embedded at compile time; a broken embed is a
		// build-time defect, not a runtime condition to recover from.
		panic(err)
	}
	return http.FileServerFS(sub)
}
