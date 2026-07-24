package bridge

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed admin_dist/*
var adminDist embed.FS

func (s *HTTPServer) root(w http.ResponseWriter, _ *http.Request) {
	redirectToAdmin(w)
}

func (s *HTTPServer) adminPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/admin" {
		redirectToAdmin(w)
		return
	}
	staticFiles, err := fs.Sub(adminDist, "admin_dist")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_assets_failed", err.Error())
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/admin/")
	if path == "" {
		serveAdminIndex(w, r, staticFiles)
		return
	}
	if _, err := fs.Stat(staticFiles, path); err != nil {
		serveAdminIndex(w, r, staticFiles)
		return
	}
	http.StripPrefix("/admin/", http.FileServer(http.FS(staticFiles))).ServeHTTP(w, r)
}

func redirectToAdmin(w http.ResponseWriter) {
	w.Header().Set("Location", "admin/")
	w.WriteHeader(http.StatusPermanentRedirect)
}

func serveAdminIndex(w http.ResponseWriter, r *http.Request, staticFiles fs.FS) {
	index, err := fs.ReadFile(staticFiles, "index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_index_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(index)
}
