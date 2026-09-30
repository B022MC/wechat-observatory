package bridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Module diagnostics let a phone report why it cannot register or poll, and
// let the server record register attempts it rejected. Both paths are purely
// additive: they never change a module-facing response, and a diagnostics
// failure is only logged.

const (
	DiagnosticSourceModule = "module"
	DiagnosticSourceServer = "server"

	DiagnosticKeyMissing  = "missing"
	DiagnosticKeyUnknown  = "unknown"
	DiagnosticKeyDisabled = "disabled"
	DiagnosticKeyValid    = "valid"

	diagnosticDedupeWindow   = 10 * time.Minute
	diagnosticDedupeCapacity = 4096
	maxDiagnosticMessage     = 1000
)

// ModuleDiagnosticReport is the body a module uploads to /module/diagnostics.
// The API key only identifies the device; it is never stored or echoed.
type ModuleDiagnosticReport struct {
	APIKey        string `json:"api_key"`
	Device        string `json:"device"`
	WxID          string `json:"wxid"`
	Stage         string `json:"stage"`
	Message       string `json:"message"`
	ModuleVersion string `json:"module_version"`
	WeChatVersion string `json:"wechat_version"`
	Android       string `json:"android"`
}

// ModuleDiagnostic is one normalized, storable diagnostic record.
type ModuleDiagnostic struct {
	Source         string
	Device         string
	ReportedDevice string
	KeyStatus      string
	KeyFingerprint string
	Stage          string
	Message        string
	WxID           string
	ModuleVersion  string
	WeChatVersion  string
	Android        string
	ClientIP       string
}

type ModuleDiagnosticView struct {
	ID             int64  `json:"id"`
	Source         string `json:"source"`
	Device         string `json:"device,omitempty"`
	ReportedDevice string `json:"reported_device,omitempty"`
	KeyStatus      string `json:"key_status"`
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	Stage          string `json:"stage"`
	Message        string `json:"message"`
	WxID           string `json:"wxid,omitempty"`
	ModuleVersion  string `json:"module_version,omitempty"`
	WeChatVersion  string `json:"wechat_version,omitempty"`
	Android        string `json:"android,omitempty"`
	ClientIP       string `json:"client_ip,omitempty"`
	CreatedAt      string `json:"created_at"`
}

type ModuleDiagnosticStore interface {
	RecordModuleDiagnostic(ctx context.Context, diagnostic ModuleDiagnostic) error
	ListModuleDiagnostics(ctx context.Context, device string, limit int) ([]ModuleDiagnosticView, error)
}

// diagnosticDeduper suppresses an identical report from the same origin for a
// window, so a module retrying every few seconds produces one record.
type diagnosticDeduper struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

func (d *diagnosticDeduper) allow(key string, now time.Time) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil || len(d.seen) >= diagnosticDedupeCapacity {
		d.seen = make(map[string]time.Time)
	}
	if last, ok := d.seen[key]; ok && now.Sub(last) < diagnosticDedupeWindow {
		return false
	}
	d.seen[key] = now
	return true
}

// RecordModuleDiagnostic normalizes and stores one diagnostic. It returns
// whether a new record was accepted (false when deduplicated).
func (s *Service) RecordModuleDiagnostic(ctx context.Context, source string, report ModuleDiagnosticReport, clientIP string) (bool, error) {
	diagnostic := ModuleDiagnostic{
		Source:         source,
		ReportedDevice: cleanDiagnosticField(report.Device, 128),
		KeyStatus:      DiagnosticKeyMissing,
		Stage:          cleanDiagnosticStage(report.Stage),
		Message:        cleanDiagnosticField(report.Message, maxDiagnosticMessage),
		WxID:           cleanDiagnosticField(report.WxID, 191),
		ModuleVersion:  cleanDiagnosticField(report.ModuleVersion, 64),
		WeChatVersion:  cleanDiagnosticField(report.WeChatVersion, 64),
		Android:        cleanDiagnosticField(report.Android, 128),
		ClientIP:       cleanDiagnosticField(clientIP, 64),
	}
	if diagnostic.Message == "" {
		diagnostic.Message = "(empty)"
	}
	if apiKey := strings.TrimSpace(report.APIKey); apiKey != "" {
		digest := sha256.Sum256([]byte(apiKey))
		diagnostic.KeyFingerprint = hex.EncodeToString(digest[:])[:12]
		diagnostic.KeyStatus = DiagnosticKeyUnknown
		key, ok, err := s.lookupAPIKey(ctx, apiKey)
		switch {
		case err != nil:
			// Keep the report; the key state is simply not known.
		case ok && key.Disabled:
			diagnostic.KeyStatus = DiagnosticKeyDisabled
			diagnostic.Device = apiKeyDeviceName(key)
		case ok:
			diagnostic.KeyStatus = DiagnosticKeyValid
			diagnostic.Device = apiKeyDeviceName(key)
		}
	}
	origin := diagnostic.KeyFingerprint
	if origin == "" {
		origin = diagnostic.ReportedDevice + "|" + diagnostic.ClientIP
	}
	if !s.diagnostics.allow(diagnostic.Source+"\x00"+origin+"\x00"+diagnostic.Stage+"\x00"+diagnostic.Message, time.Now()) {
		return false, nil
	}
	log.Printf("module diagnostic source=%s device=%s reported_device=%s key=%s stage=%s module=%s wechat=%s message=%q",
		diagnostic.Source, fallbackDiagnostic(diagnostic.Device), fallbackDiagnostic(diagnostic.ReportedDevice),
		diagnostic.KeyStatus, diagnostic.Stage, fallbackDiagnostic(diagnostic.ModuleVersion),
		fallbackDiagnostic(diagnostic.WeChatVersion), diagnostic.Message)
	store, ok := s.persistence.(ModuleDiagnosticStore)
	if !ok {
		return true, nil
	}
	return true, store.RecordModuleDiagnostic(ctx, diagnostic)
}

// RecordRegisterFailure stores a rejected /module/register attempt. It works
// for every module version because it needs nothing from the phone beyond the
// registration it already sends.
func (s *Service) RecordRegisterFailure(ctx context.Context, req ModuleRegistrationRequest, cause error, clientIP string) {
	if cause == nil {
		return
	}
	message := cause.Error()
	if errors.Is(cause, ErrAccountSession) {
		message = "account session rejected: " + message
	}
	_, err := s.RecordModuleDiagnostic(ctx, DiagnosticSourceServer, ModuleDiagnosticReport{
		APIKey:  req.APIKey,
		Device:  req.Device,
		WxID:    req.WxID,
		Stage:   "register",
		Message: message,
	}, clientIP)
	if err != nil {
		log.Printf("record register diagnostic failed: %v", err)
	}
}

func (s *Service) ListModuleDiagnostics(ctx context.Context, device string, limit int) ([]ModuleDiagnosticView, error) {
	store, ok := s.persistence.(ModuleDiagnosticStore)
	if !ok {
		return []ModuleDiagnosticView{}, nil
	}
	return store.ListModuleDiagnostics(ctx, strings.TrimSpace(device), limit)
}

func cleanDiagnosticField(value string, maxRunes int) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	value = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, value)
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes])
}

func cleanDiagnosticStage(stage string) string {
	stage = strings.ToLower(strings.TrimSpace(stage))
	var out strings.Builder
	for _, r := range stage {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.' {
			out.WriteRune(r)
		}
		if out.Len() >= 48 {
			break
		}
	}
	if out.Len() == 0 {
		return "unspecified"
	}
	return out.String()
}

func fallbackDiagnostic(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
