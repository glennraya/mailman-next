// Package web serves the compiled inbox UI baked into the binary.
//
// The Go file sits beside the React source because go:embed cannot reach
// outside its own package directory.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist is the output of `npm run build` in web/. A placeholder is committed
// so the binary compiles whether or not the UI has been built.
//
//go:embed all:dist
var dist embed.FS

// Assets returns the compiled UI rooted at the app itself.
func Assets() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		// Only reachable if the embed directive stops matching, which is a
		// build-time mistake rather than a runtime condition.
		panic(err)
	}
	return sub
}

// Handler serves the single-page app. Real files are returned as they are;
// anything else falls through to index.html so a client-side route survives a
// reload. Vite fingerprints everything under assets/, so those can be cached
// forever while index.html never is.
func Handler() http.Handler {
	assets := Assets()
	files := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

		if name == "" || !exists(assets, name) {
			w.Header().Set("Cache-Control", "no-cache")
			serveIndex(w, r, assets)
			return
		}

		if strings.HasPrefix(name, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}

		files.ServeHTTP(w, r)
	})
}

func exists(assets fs.FS, name string) bool {
	info, err := fs.Stat(assets, name)
	return err == nil && !info.IsDir()
}

func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(w, "the inbox UI was not built into this binary", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	w.Write(index)
}
