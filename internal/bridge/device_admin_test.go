package bridge

import (
	"bytes"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testDeviceAdminPassword = "device-admin-test"

func newDeviceAdminTestHandler(service *Service) http.Handler {
	return NewHTTPServer(service, "full-admin", WithDeviceAdminPassword(testDeviceAdminPassword)).Handler()
}

func deviceAdminRequest(method, path string, body []byte) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-Bridge-Device-Password", testDeviceAdminPassword)
	return req
}

func TestDeviceAdminIsDisabledWithoutExplicitPassword(t *testing.T) {
	handler := NewHTTPServer(newTestService(""), "full-admin").Handler()
	for _, path := range []string{"/device", "/device/", "/api/device-admin/modules"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("disabled route %s = %d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestDeviceAdminStaticPageUsesExactCanonicalPath(t *testing.T) {
	handler := newDeviceAdminTestHandler(newTestService(""))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/device", nil))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(body, "<title>设备管理</title>") || !strings.Contains(body, `./device-assets/`) {
		t.Fatalf("device page = %d body=%s", recorder.Code, recorder.Body.String())
	}
	assetStart := strings.Index(body, `src="./device-assets/`)
	if assetStart < 0 {
		t.Fatalf("device page has no script asset: %s", body)
	}
	assetValue := body[assetStart+len(`src="`):]
	assetEnd := strings.Index(assetValue, `"`)
	if assetEnd < 0 {
		t.Fatalf("device page has no script asset: %s", body)
	}
	assetPath := "/" + strings.TrimPrefix(assetValue[:assetEnd], "./")
	assetRecorder := httptest.NewRecorder()
	handler.ServeHTTP(assetRecorder, httptest.NewRequest(http.MethodGet, assetPath, nil))
	if assetRecorder.Code != http.StatusOK || assetRecorder.Body.Len() == 0 {
		t.Fatalf("device asset %s = %d", assetPath, assetRecorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/device/", nil))
	if recorder.Code != http.StatusPermanentRedirect || recorder.Header().Get("Location") != "../device" {
		t.Fatalf("device slash redirect = %d location=%q", recorder.Code, recorder.Header().Get("Location"))
	}
}

func TestDeviceAdminBundleContainsNoExcludedEndpoint(t *testing.T) {
	forbidden := []string{
		"/api/messages", "/api/live/events", "/api/send/text",
		"/api/module-contacts", "/api/media/", "/module/outbox/",
	}
	err := fs.WalkDir(deviceDist, "device_dist", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := deviceDist.ReadFile(path)
		if err != nil {
			return err
		}
		for _, value := range forbidden {
			if bytes.Contains(content, []byte(value)) {
				t.Fatalf("device bundle %s contains excluded endpoint %s", path, value)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDeviceAdminAuthenticationIsIndependentAndHeaderOnly(t *testing.T) {
	handler := newDeviceAdminTestHandler(newTestService(""))

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/device-admin/modules", nil),
		httptest.NewRequest(http.MethodGet, "/api/device-admin/modules?password="+testDeviceAdminPassword, nil),
	} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("limited request without dedicated header = %d body=%s", recorder.Code, recorder.Body.String())
		}
	}

	fullCredential := httptest.NewRequest(http.MethodGet, "/api/device-admin/modules", nil)
	fullCredential.Header.Set("X-Bridge-Device-Password", "full-admin")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, fullCredential)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("full admin credential entered limited namespace: %d", recorder.Code)
	}

	fullAdminRequests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/messages"},
		{method: http.MethodGet, path: "/api/module-contacts"},
		{method: http.MethodGet, path: "/api/live/events"},
		{method: http.MethodPost, path: "/api/send/text"},
	}
	for _, item := range fullAdminRequests {
		req := httptest.NewRequest(item.method, item.path, nil)
		req.Header.Set("X-Bridge-Password", testDeviceAdminPassword)
		recorder = httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("limited credential accessed %s: %d body=%s", item.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestDeviceAdminRejectsMalformedAndUnsupportedRoutes(t *testing.T) {
	handler := newDeviceAdminTestHandler(newTestService(""))
	tests := []struct {
		method string
		path   string
		status int
	}{
		{method: http.MethodPost, path: "/api/device-admin/api-keys/missing-action", status: http.StatusBadRequest},
		{method: http.MethodPost, path: "/api/device-admin/api-keys/key/enable/extra", status: http.StatusBadRequest},
		{method: http.MethodDelete, path: "/api/device-admin/api-keys/key/extra", status: http.StatusBadRequest},
		{method: http.MethodGet, path: "/api/device-admin/devices", status: http.StatusMethodNotAllowed},
		{method: http.MethodPost, path: "/api/device-admin/modules", status: http.StatusMethodNotAllowed},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, deviceAdminRequest(test.method, test.path, nil))
		if recorder.Code != test.status {
			t.Fatalf("%s %s = %d body=%s", test.method, test.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestDeviceAdminModuleProjectionExcludesMessageAndOutboxMetadata(t *testing.T) {
	reader := &fakeAdminReader{modules: []ModuleStatusView{{
		Device: "phone-a", DeviceWxID: "wxid_device", DeviceNickname: "Device A",
		Enabled: true, Registered: true, RuntimeStatus: "sending",
		PendingOutbox: 7, LeasedOutbox: 1, SentOutbox: 9, FailedOutbox: 2,
		LastOutboxID: 99, LastOutboxStatus: "failed", LastOutboxError: "private-outbox-error",
		LastEventAt: "private-event-time", LastInboundAt: "private-inbound-time",
		LastOutboundAckAt: "private-outbound-time", LastPollAt: "private-poll-time",
		LastAckAt: "private-ack-time", RuntimeUpdatedAt: "2026-07-27T10:00:00Z",
	}}}
	handler := newDeviceAdminTestHandler(newTestService("", WithAdminReader(reader)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, deviceAdminRequest(http.MethodGet, "/api/device-admin/modules", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("module status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	for _, want := range []string{`"device":"phone-a"`, `"runtime_status":"online"`, `"last_seen_at":"2026-07-27T10:00:00Z"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("limited module response missing %s: %s", want, body)
		}
	}
	for _, forbidden := range []string{"outbox", "last_event", "inbound", "outbound", "last_poll", "last_ack", "private-"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("limited module response exposed %q: %s", forbidden, body)
		}
	}
}

func TestDeviceAdminModuleProjectionUsesRegistrationAndRuntimeActivity(t *testing.T) {
	tests := []struct {
		name         string
		status       ModuleStatusView
		runtimeState string
		lastSeenAt   string
	}{
		{
			name:         "disabled wins",
			status:       ModuleStatusView{Enabled: false, Registered: true, RuntimeUpdatedAt: "2026-07-27T10:01:00Z"},
			runtimeState: "disabled",
			lastSeenAt:   "2026-07-27T10:01:00Z",
		},
		{
			name:         "runtime key has not registered",
			status:       ModuleStatusView{Enabled: true, Registered: false, LastRegisterAt: "2026-07-27T10:02:00Z"},
			runtimeState: "unregistered",
			lastSeenAt:   "2026-07-27T10:02:00Z",
		},
		{
			name: "registered ignores outbox state",
			status: ModuleStatusView{
				Enabled: true, Registered: true, RuntimeStatus: "sending",
				RuntimeUpdatedAt: "2026-07-27T10:03:00Z", LastRegisterAt: "2026-07-27T09:00:00Z",
			},
			runtimeState: "online",
			lastSeenAt:   "2026-07-27T10:03:00Z",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			view := newDeviceAdminModuleView(test.status)
			if view.RuntimeStatus != test.runtimeState || view.LastSeenAt != test.lastSeenAt {
				t.Fatalf("view = %+v, want state=%q last_seen_at=%q", view, test.runtimeState, test.lastSeenAt)
			}
		})
	}
}

func TestDeviceAdminCanManageAPIKeysAndDeviceNames(t *testing.T) {
	service := newTestService("http://127.0.0.1:1")
	handler := newDeviceAdminTestHandler(service)

	create := httptest.NewRecorder()
	handler.ServeHTTP(create, deviceAdminRequest(http.MethodPost, "/api/device-admin/api-keys", []byte(`{"api_key":"wg_device_page","device":"phone-a","nickname":"Device page"}`)))
	if create.Code != http.StatusOK || !strings.Contains(create.Body.String(), `"api_key":"wg_device_page"`) {
		t.Fatalf("create key = %d body=%s", create.Code, create.Body.String())
	}

	disable := httptest.NewRecorder()
	handler.ServeHTTP(disable, deviceAdminRequest(http.MethodPost, "/api/device-admin/api-keys/wg_device_page/disable", nil))
	if disable.Code != http.StatusOK || !strings.Contains(disable.Body.String(), `"enabled":false`) {
		t.Fatalf("disable key = %d body=%s", disable.Code, disable.Body.String())
	}
	enable := httptest.NewRecorder()
	handler.ServeHTTP(enable, deviceAdminRequest(http.MethodPost, "/api/device-admin/api-keys/wg_device_page/enable", nil))
	if enable.Code != http.StatusOK || !strings.Contains(enable.Body.String(), `"enabled":true`) {
		t.Fatalf("enable key = %d body=%s", enable.Code, enable.Body.String())
	}

	rename := httptest.NewRecorder()
	handler.ServeHTTP(rename, deviceAdminRequest(http.MethodPost, "/api/device-admin/devices", []byte(`{"name":"phone-a","nickname":"Limited Device"}`)))
	if rename.Code != http.StatusOK || !strings.Contains(rename.Body.String(), `"device_nickname":"Limited Device"`) {
		t.Fatalf("rename device = %d body=%s", rename.Code, rename.Body.String())
	}
	for _, forbidden := range []string{"outbox", "last_event", "inbound", "outbound"} {
		if strings.Contains(strings.ToLower(rename.Body.String()), forbidden) {
			t.Fatalf("rename response exposed %q: %s", forbidden, rename.Body.String())
		}
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, deviceAdminRequest(http.MethodGet, "/api/device-admin/api-keys", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), "wg_device_page") {
		t.Fatalf("list keys = %d body=%s", list.Code, list.Body.String())
	}

	remove := httptest.NewRecorder()
	handler.ServeHTTP(remove, deviceAdminRequest(http.MethodDelete, "/api/device-admin/api-keys/wg_device_page", nil))
	if remove.Code != http.StatusOK {
		t.Fatalf("delete key = %d body=%s", remove.Code, remove.Body.String())
	}
	for _, key := range service.APIKeys() {
		if key.Code == "wg_device_page" {
			t.Fatal("limited delete did not remove API key")
		}
	}
}
