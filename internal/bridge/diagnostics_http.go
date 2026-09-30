package bridge

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	diagnosticUploadLimitPerMinute = 30
	diagnosticUploadMaxBytes       = 8 << 10
)

// ipRateLimiter is a fixed one-minute window per client IP. The upload
// endpoint accepts reports from modules whose API key may be wrong, so it is
// intentionally unauthenticated and needs its own abuse bound.
type ipRateLimiter struct {
	mu     sync.Mutex
	window time.Time
	counts map[string]int
}

func (l *ipRateLimiter) allow(ip string, limit int, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	window := now.Truncate(time.Minute)
	if !window.Equal(l.window) || l.counts == nil {
		l.window = window
		l.counts = make(map[string]int)
	}
	if l.counts[ip] >= limit {
		return false
	}
	l.counts[ip]++
	return true
}

func requestClientIP(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
		if first != "" {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// uploadModuleDiagnostic accepts a best-effort report from a module. It always
// answers 202 for a well-formed body, whether the report was stored, deduped
// or rate limited, so the endpoint never reveals whether an API key exists.
func (s *HTTPServer) uploadModuleDiagnostic(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, diagnosticUploadMaxBytes)
	var report ModuleDiagnosticReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid diagnostic report")
		return
	}
	ip := requestClientIP(r)
	if s.diagnosticLimit.allow(ip, diagnosticUploadLimitPerMinute, time.Now()) {
		if _, err := s.service.RecordModuleDiagnostic(r.Context(), DiagnosticSourceModule, report, ip); err != nil {
			// Reporting must never fail a module; keep only a server log line.
			log.Printf("store module diagnostic failed: %v", err)
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"ok": true})
}

func (s *HTTPServer) moduleDiagnostics(w http.ResponseWriter, r *http.Request) {
	items, err := s.service.ListModuleDiagnostics(r.Context(), r.URL.Query().Get("device"), queryLimit(r, 100))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "diagnostics_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "diagnostics": items})
}

// recordRegisterFailure runs detached from the request so a slow diagnostics
// write can never delay or alter the register response.
func (s *HTTPServer) recordRegisterFailure(r *http.Request, req ModuleRegistrationRequest, cause error) {
	ip := requestClientIP(r)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.service.RecordRegisterFailure(ctx, req, cause, ip)
	}()
}
