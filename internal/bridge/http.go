package bridge

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type HTTPServer struct {
	service         *Service
	adminPass       string
	deviceAdminPass string
}

type HTTPServerOption func(*HTTPServer)

func WithDeviceAdminPassword(password string) HTTPServerOption {
	return func(server *HTTPServer) {
		server.deviceAdminPass = strings.TrimSpace(password)
	}
}

func NewHTTPServer(service *Service, adminPassword string, options ...HTTPServerOption) *HTTPServer {
	server := &HTTPServer{
		service:   service,
		adminPass: adminPassword,
	}
	for _, option := range options {
		if option != nil {
			option(server)
		}
	}
	return server
}

func (s *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.root)
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/devices", s.requireAdmin(s.devices))
	mux.HandleFunc("POST /api/devices", s.requireAdmin(s.upsertDevice))
	mux.HandleFunc("GET /api/api-keys", s.requireAdmin(s.apiKeys))
	mux.HandleFunc("POST /api/api-keys", s.requireAdmin(s.upsertAPIKey))
	mux.HandleFunc("POST /api/api-keys/", s.requireAdmin(s.updateAPIKeyState))
	mux.HandleFunc("DELETE /api/api-keys/", s.requireAdmin(s.deleteAPIKey))
	mux.HandleFunc("POST /internal/api-key/introspect", s.requireAdmin(s.introspectAPIKey))
	mux.HandleFunc("GET /api/events", s.requireAdmin(s.events))
	mux.HandleFunc("GET /api/stored-events", s.requireAdmin(s.storedEvents))
	mux.HandleFunc("GET /api/messages", s.requireAdmin(s.messages))
	mux.HandleFunc("GET /api/live/events", s.requireAdmin(s.liveEvents))
	mux.HandleFunc("GET /api/modules/status", s.requireAdmin(s.moduleStatuses))
	mux.HandleFunc("GET /api/module-contacts", s.requireAdmin(s.moduleContacts))
	mux.HandleFunc("POST /api/send/text", s.requireAdmin(s.sendText))
	mux.HandleFunc("GET /admin", s.adminPage)
	mux.HandleFunc("GET /admin/", s.adminPage)
	mux.HandleFunc("GET /device", s.devicePage)
	mux.HandleFunc("GET /device/", s.devicePage)
	mux.HandleFunc("GET /device-assets/", s.deviceAssets)
	mux.HandleFunc("GET /device-manifest.webmanifest", s.devicePWAAsset)
	mux.HandleFunc("GET /device-sw.js", s.devicePWAAsset)
	mux.HandleFunc("GET /device-offline.html", s.devicePWAAsset)
	mux.HandleFunc("GET /device-icons/", s.devicePWAAsset)
	mux.HandleFunc("GET /api/device-admin/modules", s.requireDeviceAdmin(s.deviceAdminModules))
	mux.HandleFunc("GET /api/device-admin/api-keys", s.requireDeviceAdmin(s.deviceAdminAPIKeys))
	mux.HandleFunc("POST /api/device-admin/api-keys", s.requireDeviceAdmin(s.deviceAdminUpsertAPIKey))
	mux.HandleFunc("POST /api/device-admin/api-keys/", s.requireDeviceAdmin(s.deviceAdminUpdateAPIKeyState))
	mux.HandleFunc("DELETE /api/device-admin/api-keys/", s.requireDeviceAdmin(s.deviceAdminDeleteAPIKey))
	mux.HandleFunc("POST /api/device-admin/devices", s.requireDeviceAdmin(s.deviceAdminUpsertDevice))
	mux.HandleFunc("POST /module/register", s.registerModule)
	mux.HandleFunc("POST /module/contacts/snapshot", s.recordContacts)
	mux.HandleFunc("POST /module/outbox/poll", s.pollOutbox)
	mux.HandleFunc("POST /module/outbox/ack", s.ackOutbox)
	mux.HandleFunc("GET /module/outbox/ws", s.outboxWebSocket)
	mux.HandleFunc("POST /webhook/lsposed/message", s.ingestMessageFrom("lsposed"))
	mux.HandleFunc("POST /webhook/module/message", s.ingestMessageFrom("module"))
	return mux
}

func (s *HTTPServer) introspectAPIKey(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req APIKeyIntrospectionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid introspection request")
		return
	}
	result, err := s.service.IntrospectAPIKey(r.Context(), req)
	if err != nil {
		if strings.Contains(err.Error(), "exactly one") {
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "introspection_failed", "credential authority is unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, http.StatusOK, result)
}

func (s *HTTPServer) health(w http.ResponseWriter, r *http.Request) {
	payload := map[string]any{"ok": true}
	if reader, ok := s.service.AdminReader().(StorageMetricsReader); ok {
		if databaseBytes, err := reader.DatabaseSizeBytes(r.Context()); err != nil {
			payload["metrics_error"] = err.Error()
		} else {
			payload["database_bytes"] = databaseBytes
		}
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *HTTPServer) devices(w http.ResponseWriter, r *http.Request) {
	type deviceResponse struct {
		Name     string `json:"name"`
		WxID     string `json:"wxid"`
		Nickname string `json:"nickname"`
	}
	out := []deviceResponse{}
	if reader := s.service.AdminReader(); reader != nil {
		statuses, err := reader.ListModuleStatuses(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_read_failed", err.Error())
			return
		}
		for _, status := range statuses {
			out = append(out, deviceResponse{
				Name:     status.Device,
				WxID:     status.DeviceWxID,
				Nickname: status.DeviceNickname,
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"default_device": s.service.DefaultDevice(),
			"devices":        out,
		})
		return
	}
	for _, device := range s.service.Devices() {
		out = append(out, deviceResponse{
			Name:     device.Name,
			WxID:     device.WxID,
			Nickname: device.Nickname,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default_device": s.service.DefaultDevice(),
		"devices":        out,
	})
}

func (s *HTTPServer) apiKeys(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 200)
	if reader := s.service.AdminReader(); reader != nil {
		keys, err := reader.ListAPIKeys(r.Context(), limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_read_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"api_keys": keys})
		return
	}
	keys := s.apiKeyViews(limit)
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": keys})
}

func (s *HTTPServer) apiKeyViews(limit int) []APIKeyView {
	keys := s.service.APIKeys()
	if len(keys) > limit {
		keys = keys[:limit]
	}
	out := make([]APIKeyView, 0, len(keys))
	for _, key := range keys {
		out = append(out, apiKeyView(key))
	}
	return out
}

func (s *HTTPServer) upsertAPIKey(w http.ResponseWriter, r *http.Request) {
	var req APIKeyUpsertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	key, err := s.service.UpsertAPIKey(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "api_key_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "api_key": key})
}

func (s *HTTPServer) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	apiKey := strings.TrimPrefix(r.URL.Path, "/api/api-keys/")
	if apiKey == "" || apiKey == r.URL.Path {
		writeError(w, http.StatusBadRequest, "api_key_failed", "api key is required")
		return
	}
	if err := s.service.DeleteAPIKey(r.Context(), apiKey); err != nil {
		writeError(w, http.StatusBadRequest, "api_key_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *HTTPServer) updateAPIKeyState(w http.ResponseWriter, r *http.Request) {
	apiKey, action, ok := parseAPIKeyActionPath(r.URL.Path)
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

func parseAPIKeyActionPath(path string) (string, string, bool) {
	return parseAPIKeyActionPathWithPrefix(path, "/api/api-keys/")
}

func parseAPIKeyActionPathWithPrefix(path, prefix string) (string, string, bool) {
	suffix := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	parts := strings.Split(suffix, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || suffix == path {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s *HTTPServer) upsertDevice(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "device": device})
}

func (s *HTTPServer) events(w http.ResponseWriter, r *http.Request) {
	limit := queryLimit(r, 50)
	writeJSON(w, http.StatusOK, map[string]any{
		"events": s.service.Hub().Recent(limit),
	})
}

func (s *HTTPServer) storedEvents(w http.ResponseWriter, r *http.Request) {
	reader := s.service.AdminReader()
	if reader == nil {
		writeJSON(w, http.StatusOK, map[string]any{"events": []StoredEventView{}})
		return
	}
	events, err := reader.ListStoredEvents(r.Context(), queryLimit(r, 50))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_read_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (s *HTTPServer) messages(w http.ResponseWriter, r *http.Request) {
	reader := s.service.AdminReader()
	if reader == nil {
		writeJSON(w, http.StatusOK, map[string]any{"messages": []StoredEventView{}})
		return
	}
	filter := MessageFilter{
		Device:    strings.TrimSpace(r.URL.Query().Get("device")),
		WxID:      strings.TrimSpace(r.URL.Query().Get("wxid")),
		OwnerWxID: strings.TrimSpace(r.URL.Query().Get("owner_wxid")),
		ChatID:    strings.TrimSpace(r.URL.Query().Get("chat_id")),
		ChatKind:  strings.TrimSpace(r.URL.Query().Get("chat_kind")),
		Limit:     queryLimit(r, 100),
	}
	if filter.OwnerWxID == "" && filter.Device != "" {
		filter.OwnerWxID = s.service.deviceWxID(r.Context(), filter.Device)
	}
	messages, err := reader.ListMessages(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_read_failed", err.Error())
		return
	}
	if messages == nil {
		messages = []StoredEventView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages})
}

func (s *HTTPServer) liveEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "streaming is not supported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	tailer, durable := s.service.AdminReader().(EventTailReader)
	if durable {
		s.liveDurableEvents(w, r, flusher, tailer)
		return
	}

	writeSSE(w, "ready", map[string]any{"ok": true, "time": time.Now().Unix()})
	flusher.Flush()

	events := s.service.Hub().Subscribe(r.Context())
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-events:
			if !ok {
				return
			}
			writeSSE(w, "message", event)
			flusher.Flush()
		case <-ticker.C:
			writeSSE(w, "ping", map[string]any{"time": time.Now().Unix()})
			flusher.Flush()
		}
	}
}

func (s *HTTPServer) liveDurableEvents(w http.ResponseWriter, r *http.Request, flusher http.Flusher, tailer EventTailReader) {
	cursor, supplied, err := liveEventCursor(r)
	if err != nil {
		writeSSE(w, "error", map[string]any{"code": "invalid_cursor", "message": err.Error()})
		flusher.Flush()
		return
	}
	if !supplied {
		cursor, err = tailer.LatestLiveEventID(r.Context())
		if err != nil {
			writeSSE(w, "error", map[string]any{"code": "event_tail_failed", "message": err.Error()})
			flusher.Flush()
			return
		}
	}
	writeSSE(w, "ready", map[string]any{"ok": true, "time": time.Now().Unix(), "cursor": cursor})
	flusher.Flush()

	wake := s.service.Hub().Subscribe(r.Context())
	poll := time.NewTicker(time.Second)
	ping := time.NewTicker(20 * time.Second)
	defer poll.Stop()
	defer ping.Stop()

	drain := func() bool {
		for {
			events, err := tailer.ListLiveEventsAfter(r.Context(), cursor, 100)
			if err != nil {
				writeSSE(w, "error", map[string]any{"code": "event_tail_failed", "message": err.Error()})
				flusher.Flush()
				return false
			}
			for _, event := range events {
				if event.Sequence <= cursor {
					continue
				}
				writeSSEID(w, event.Sequence, "message", event)
				cursor = event.Sequence
			}
			if len(events) < 100 {
				flusher.Flush()
				return true
			}
		}
	}
	if !drain() {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case _, ok := <-wake:
			if !ok || !drain() {
				return
			}
		case <-poll.C:
			if !drain() {
				return
			}
		case <-ping.C:
			writeSSE(w, "ping", map[string]any{"time": time.Now().Unix(), "cursor": cursor})
			flusher.Flush()
		}
	}
}

func liveEventCursor(r *http.Request) (int64, bool, error) {
	raw := strings.TrimSpace(r.Header.Get("Last-Event-ID"))
	if raw == "" {
		raw = strings.TrimSpace(r.URL.Query().Get("cursor"))
	}
	if raw == "" {
		return 0, false, nil
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cursor < 0 {
		return 0, true, fmt.Errorf("cursor must be a non-negative integer")
	}
	return cursor, true, nil
}

func (s *HTTPServer) moduleStatuses(w http.ResponseWriter, r *http.Request) {
	reader := s.service.AdminReader()
	if reader != nil {
		statuses, err := reader.ListModuleStatuses(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "admin_read_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"modules": statuses})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"modules": s.moduleStatusViews()})
}

func (s *HTTPServer) moduleContacts(w http.ResponseWriter, r *http.Request) {
	reader := s.service.AdminReader()
	if reader == nil {
		writeJSON(w, http.StatusOK, map[string]any{"contacts": []ModuleContactView{}})
		return
	}
	filter := ModuleContactFilter{
		Device:         strings.TrimSpace(r.URL.Query().Get("device")),
		OwnerWxID:      strings.TrimSpace(r.URL.Query().Get("owner_wxid")),
		Query:          strings.TrimSpace(r.URL.Query().Get("q")),
		IncludeDeleted: parseBoolQuery(r.URL.Query().Get("include_deleted")),
		Limit:          queryLimit(r, 100),
	}
	contacts, err := reader.ListModuleContacts(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "admin_read_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"contacts": contacts})
}

func (s *HTTPServer) moduleStatusViews() []ModuleStatusView {
	devices := s.service.Devices()
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].Name < devices[j].Name
	})
	out := make([]ModuleStatusView, 0, len(devices))
	for _, device := range devices {
		item := ModuleStatusView{
			Device:         device.Name,
			DeviceWxID:     device.WxID,
			DeviceNickname: device.Nickname,
			Enabled:        true,
		}
		if strings.TrimSpace(device.WxID) != "" {
			item.Registered = true
		}
		item.NormalizeRuntimeStatus()
		out = append(out, item)
	}
	return out
}

func (s *HTTPServer) sendText(w http.ResponseWriter, r *http.Request) {
	var req SendTextRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.OwnerWxID) == "" {
		writeError(w, http.StatusBadRequest, "owner_wxid_required", "owner_wxid is required for admin sends")
		return
	}
	outboxID, err := s.service.SendText(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "send_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "outbox_id": outboxID})
}

func (s *HTTPServer) registerModule(w http.ResponseWriter, r *http.Request) {
	var req ModuleRegistrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	result, err := s.service.RegisterModule(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "register_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func (s *HTTPServer) recordContacts(w http.ResponseWriter, r *http.Request) {
	var req ModuleContactSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	count, err := s.service.RecordModuleContacts(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "contacts_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "count": count})
}

func (s *HTTPServer) pollOutbox(w http.ResponseWriter, r *http.Request) {
	var req ModulePollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	items, err := s.service.PollOutbox(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "poll_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

func (s *HTTPServer) ackOutbox(w http.ResponseWriter, r *http.Request) {
	var req ModuleAckRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	items, err := s.service.AckOutbox(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ack_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "items": items})
}

func (s *HTTPServer) ingestMessageFrom(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
		var event MessageEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
			return
		}
		if event.Device == "" {
			event.Device = s.service.DefaultDevice()
		}
		event.RawProvider = provider
		result, err := s.service.Ingest(r.Context(), event)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ingest_failed", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
	}
}

func (s *HTTPServer) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(r.Header.Get("X-Bridge-Password"))
		if got == "" {
			got = strings.TrimSpace(r.URL.Query().Get("password"))
		}
		if got != s.adminPass {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid admin password")
			return
		}
		next(w, r)
	}
}

func queryLimit(r *http.Request, fallback int) int {
	limit := fallback
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}
	return limit
}

func parseBoolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"ok":      false,
		"code":    code,
		"message": message,
	})
}

func writeSSE(w http.ResponseWriter, event string, payload any) {
	writeSSEID(w, 0, event, payload)
}

func writeSSEID(w http.ResponseWriter, id int64, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(`{"error":"marshal_failed"}`)
	}
	if event != "" {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
	}
	if id > 0 {
		_, _ = fmt.Fprintf(w, "id: %d\n", id)
	}
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}
