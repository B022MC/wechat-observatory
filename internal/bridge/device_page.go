package bridge

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

//go:embed device_dist/*
var deviceDist embed.FS

func (s *HTTPServer) devicePage(w http.ResponseWriter, r *http.Request) {
	if s.deviceAdminPass == "" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Path == "/device/" {
		w.Header().Set("Location", "../device")
		w.WriteHeader(http.StatusPermanentRedirect)
		return
	}
	staticFiles, err := deviceStaticFiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "device_assets_failed", err.Error())
		return
	}
	if r.URL.Path == "/device" {
		serveDeviceIndex(w, staticFiles)
		return
	}
	http.NotFound(w, r)
}

func (s *HTTPServer) deviceAssets(w http.ResponseWriter, r *http.Request) {
	if s.deviceAdminPass == "" {
		http.NotFound(w, r)
		return
	}
	staticFiles, err := deviceStaticFiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "device_assets_failed", err.Error())
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/device-assets/")
	if path == "" || path == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	path = "device-assets/" + path
	if _, err := fs.Stat(staticFiles, path); err != nil {
		http.NotFound(w, r)
		return
	}
	http.FileServer(http.FS(staticFiles)).ServeHTTP(w, r)
}

func (s *HTTPServer) devicePWAAsset(w http.ResponseWriter, r *http.Request) {
	if s.deviceAdminPass == "" {
		http.NotFound(w, r)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	allowed := map[string]string{
		"device-manifest.webmanifest": "application/manifest+json; charset=utf-8",
		"device-sw.js":                "text/javascript; charset=utf-8",
		"device-offline.html":         "text/html; charset=utf-8",
		"device-icons/icon-180.png":   "image/png",
		"device-icons/icon-192.png":   "image/png",
		"device-icons/icon-512.png":   "image/png",
	}
	contentType, ok := allowed[path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	staticFiles, err := deviceStaticFiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "device_assets_failed", err.Error())
		return
	}
	content, err := fs.ReadFile(staticFiles, path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(path))
	}
	w.Header().Set("Content-Type", contentType)
	if path == "device-sw.js" || path == "device-manifest.webmanifest" || path == "device-offline.html" {
		w.Header().Set("Cache-Control", "no-cache")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func deviceStaticFiles() (fs.FS, error) {
	return fs.Sub(deviceDist, "device_dist")
}

func serveDeviceIndex(w http.ResponseWriter, staticFiles fs.FS) {
	index, err := fs.ReadFile(staticFiles, "index.html")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "device_index_failed", err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(index)
}
