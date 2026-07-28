package bridge

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
)

const deviceAdminAPIKeyPrefix = "/api/device-admin/api-keys/"

type DeviceAdminModuleView struct {
	Device         string `json:"device"`
	DeviceWxID     string `json:"device_wxid,omitempty"`
	DeviceNickname string `json:"device_nickname,omitempty"`
	Enabled        bool   `json:"enabled"`
	RuntimeStatus  string `json:"runtime_status"`
	LastSeenAt     string `json:"last_seen_at,omitempty"`
}

func (s *HTTPServer) requireDeviceAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.deviceAdminPass == "" {
			http.NotFound(w, r)
			return
		}
		got := strings.TrimSpace(r.Header.Get("X-Bridge-Device-Password"))
		if len(got) != len(s.deviceAdminPass) || subtle.ConstantTimeCompare([]byte(got), []byte(s.deviceAdminPass)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid device admin password")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next(w, r)
	}
}

func (s *HTTPServer) deviceAdminModules(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.loadModuleStatuses(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_read_failed", err.Error())
		return
	}
	modules := make([]DeviceAdminModuleView, 0, len(statuses))
	for _, status := range statuses {
		modules = append(modules, newDeviceAdminModuleView(status))
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": modules})
}

func (s *HTTPServer) loadModuleStatuses(r *http.Request) ([]ModuleStatusView, error) {
	if reader := s.service.AdminReader(); reader != nil {
		return reader.ListModuleStatuses(r.Context())
	}
	return s.moduleStatusViews(), nil
}

func newDeviceAdminModuleView(status ModuleStatusView) DeviceAdminModuleView {
	runtimeStatus := "online"
	switch {
	case !status.Enabled:
		runtimeStatus = "disabled"
	case !status.Registered:
		runtimeStatus = "unregistered"
	}
	lastSeenAt := status.RuntimeUpdatedAt
	if lastSeenAt == "" {
		lastSeenAt = status.LastRegisterAt
	}
	return DeviceAdminModuleView{
		Device:         status.Device,
		DeviceWxID:     status.DeviceWxID,
		DeviceNickname: status.DeviceNickname,
		Enabled:        status.Enabled,
		RuntimeStatus:  runtimeStatus,
		LastSeenAt:     lastSeenAt,
	}
}

func (s *HTTPServer) deviceAdminAPIKeys(w http.ResponseWriter, r *http.Request) {
	s.apiKeys(w, r)
}

func (s *HTTPServer) deviceAdminUpsertAPIKey(w http.ResponseWriter, r *http.Request) {
	s.upsertAPIKey(w, r)
}

func (s *HTTPServer) deviceAdminDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	apiKey := strings.Trim(strings.TrimPrefix(r.URL.Path, deviceAdminAPIKeyPrefix), "/")
	if apiKey == "" || apiKey == r.URL.Path || strings.Contains(apiKey, "/") {
		writeError(w, http.StatusBadRequest, "api_key_failed", "api key is required")
		return
	}
	if err := s.service.DeleteAPIKey(r.Context(), apiKey); err != nil {
		writeError(w, http.StatusBadRequest, "api_key_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *HTTPServer) deviceAdminUpdateAPIKeyState(w http.ResponseWriter, r *http.Request) {
	apiKey, action, ok := parseAPIKeyActionPathWithPrefix(r.URL.Path, deviceAdminAPIKeyPrefix)
	if !ok {
		writeError(w, http.StatusBadRequest, "api_key_failed", "api key action is required")
		return
	}
	var enabled bool
	switch action {
	case "enable":
		enabled = true
	case "disable":
		enabled = false
	default:
		writeError(w, http.StatusBadRequest, "api_key_failed", "unknown api key action")
		return
	}
	key, err := s.service.SetAPIKeyEnabled(r.Context(), apiKey, enabled)
	if err != nil {
		writeError(w, http.StatusBadRequest, "api_key_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "api_key": key})
}

func (s *HTTPServer) deviceAdminUpsertDevice(w http.ResponseWriter, r *http.Request) {
	var req DeviceUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	device, err := s.service.UpsertDevice(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "device_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": newDeviceAdminModuleView(device)})
}
