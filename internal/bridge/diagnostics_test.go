package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"wechat-observatory/internal/config"
)

type diagnosticRecorder struct {
	mu    sync.Mutex
	items []ModuleDiagnostic
}

func (r *diagnosticRecorder) UpdateDeviceIdentity(context.Context, string, string, string) error {
	return nil
}
func (r *diagnosticRecorder) RecordInboundEvent(_ context.Context, e MessageEvent) (MessageEvent, error) {
	return e, nil
}
func (r *diagnosticRecorder) RecordOutboundEvent(_ context.Context, e MessageEvent) (MessageEvent, error) {
	return e, nil
}
func (r *diagnosticRecorder) RecordModuleDiagnostic(_ context.Context, d ModuleDiagnostic) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items = append(r.items, d)
	return nil
}
func (r *diagnosticRecorder) ListModuleDiagnostics(_ context.Context, device string, limit int) ([]ModuleDiagnosticView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []ModuleDiagnosticView{}
	for i := len(r.items) - 1; i >= 0 && len(out) < limit; i-- {
		d := r.items[i]
		if device != "" && d.Device != device && d.ReportedDevice != device {
			continue
		}
		out = append(out, ModuleDiagnosticView{ID: int64(i + 1), Source: d.Source, Device: d.Device,
			ReportedDevice: d.ReportedDevice, KeyStatus: d.KeyStatus, KeyFingerprint: d.KeyFingerprint,
			Stage: d.Stage, Message: d.Message})
	}
	return out, nil
}
func (r *diagnosticRecorder) snapshot() []ModuleDiagnostic {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ModuleDiagnostic(nil), r.items...)
}

func newDiagnosticService() (*Service, *diagnosticRecorder) {
	recorder := &diagnosticRecorder{}
	service := NewService(Config{
		DefaultDevice: "61538M",
		Devices: map[string]config.Device{
			"61538M": {Name: "61538M", Nickname: "61538M", Timeout: time.Second},
		},
		APIKeys: map[string]config.APIKey{
			"m-key-live": {Code: "m-key-live", Device: "61538M"},
			"m-key-off":  {Code: "m-key-off", Device: "61538H", Disabled: true},
		},
	}, WithPersistence(recorder))
	return service, recorder
}

func TestModuleDiagnosticResolvesKeyWithoutStoringIt(t *testing.T) {
	service, recorder := newDiagnosticService()
	cases := []struct {
		key, wantStatus, wantDevice string
	}{
		{"m-key-live", DiagnosticKeyValid, "61538M"},
		{"m-key-off", DiagnosticKeyDisabled, "61538H"},
		{"typo-key", DiagnosticKeyUnknown, ""},
		{"", DiagnosticKeyMissing, ""},
	}
	for i, tc := range cases {
		stored, err := service.RecordModuleDiagnostic(t.Context(), DiagnosticSourceModule, ModuleDiagnosticReport{
			APIKey: tc.key, Device: "phone-typed", Stage: "register", Message: "attempt " + string(rune('a'+i)),
		}, "203.0.113.9")
		if err != nil || !stored {
			t.Fatalf("%q stored=%v err=%v", tc.key, stored, err)
		}
		got := recorder.snapshot()[i]
		if got.KeyStatus != tc.wantStatus || got.Device != tc.wantDevice || got.ReportedDevice != "phone-typed" {
			t.Fatalf("%q diagnostic=%+v", tc.key, got)
		}
		if tc.key == "" && got.KeyFingerprint != "" || tc.key != "" && len(got.KeyFingerprint) != 12 {
			t.Fatalf("%q fingerprint=%q", tc.key, got.KeyFingerprint)
		}
		raw, _ := json.Marshal(got)
		if tc.key != "" && strings.Contains(string(raw), tc.key) {
			t.Fatalf("api key persisted: %s", raw)
		}
	}
}

func TestModuleDiagnosticDedupesRepeatsAndCleansFields(t *testing.T) {
	service, recorder := newDiagnosticService()
	report := ModuleDiagnosticReport{APIKey: "m-key-live", Stage: "  Identity Pending!\n", Message: "line1\nline2\x00" + strings.Repeat("长", 1200)}
	for i := 0; i < 3; i++ {
		if _, err := service.RecordModuleDiagnostic(t.Context(), DiagnosticSourceModule, report, "203.0.113.9"); err != nil {
			t.Fatal(err)
		}
	}
	items := recorder.snapshot()
	if len(items) != 1 {
		t.Fatalf("repeated report stored %d times", len(items))
	}
	got := items[0]
	if got.Stage != "identitypending" || strings.ContainsAny(got.Message, "\n\x00") ||
		len([]rune(got.Message)) != maxDiagnosticMessage || !strings.HasPrefix(got.Message, "line1 line2") {
		t.Fatalf("cleaned diagnostic stage=%q message prefix=%q runes=%d", got.Stage, got.Message[:12], len([]rune(got.Message)))
	}
	report.Stage = "worker"
	if stored, _ := service.RecordModuleDiagnostic(t.Context(), DiagnosticSourceModule, report, "203.0.113.9"); !stored {
		t.Fatal("a different stage was suppressed")
	}
}

func TestDiagnosticUploadEndpointIsBoundedAndAlwaysAccepted(t *testing.T) {
	service, recorder := newDiagnosticService()
	handler := NewHTTPServer(service, "admin").Handler()
	post := func(body string) int {
		req := httptest.NewRequest(http.MethodPost, "/module/diagnostics", strings.NewReader(body))
		req.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.2")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post(`{"api_key":"typo-key","device":"61538W","stage":"register","message":"invalid api key"}`); code != http.StatusAccepted {
		t.Fatalf("upload status=%d", code)
	}
	items := recorder.snapshot()
	if len(items) != 1 || items[0].ClientIP != "198.51.100.7" || items[0].KeyStatus != DiagnosticKeyUnknown {
		t.Fatalf("stored=%+v", items)
	}
	if code := post(`{bad json`); code != http.StatusBadRequest {
		t.Fatalf("invalid json status=%d", code)
	}
	if code := post(`{"message":"` + strings.Repeat("x", diagnosticUploadMaxBytes) + `"}`); code != http.StatusBadRequest {
		t.Fatalf("oversized status=%d", code)
	}
	for i := 0; i < diagnosticUploadLimitPerMinute+5; i++ {
		body, _ := json.Marshal(ModuleDiagnosticReport{Stage: "flood", Message: strings.Repeat("m", i+1)})
		if code := post(string(body)); code != http.StatusAccepted {
			t.Fatalf("flood upload %d status=%d", i, code)
		}
	}
	if stored := len(recorder.snapshot()); stored > diagnosticUploadLimitPerMinute {
		t.Fatalf("per-ip limit not enforced: stored=%d", stored)
	}
}

func TestRegisterFailureIsRecordedWithoutChangingResponse(t *testing.T) {
	service, recorder := newDiagnosticService()
	handler := NewHTTPServer(service, "admin").Handler()
	body, _ := json.Marshal(ModuleRegistrationRequest{APIKey: "typo-key", Device: "61538M", WxID: "wxid_princess"})
	req := httptest.NewRequest(http.MethodPost, "/module/register", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	// Byte-for-byte the response the pre-diagnostics server returned.
	if rec.Code != http.StatusBadRequest || response["ok"] != false || response["code"] != "register_failed" ||
		response["message"] != "invalid api key" || len(response) != 3 {
		t.Fatalf("register response changed: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(recorder.snapshot()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	items := recorder.snapshot()
	if len(items) != 1 || items[0].Source != DiagnosticSourceServer || items[0].Stage != "register" ||
		items[0].Message != "invalid api key" || items[0].WxID != "wxid_princess" || items[0].KeyStatus != DiagnosticKeyUnknown {
		t.Fatalf("server diagnostic=%+v", items)
	}
}

func TestDiagnosticListRequiresAdmin(t *testing.T) {
	service, _ := newDiagnosticService()
	if _, err := service.RecordModuleDiagnostic(t.Context(), DiagnosticSourceModule, ModuleDiagnosticReport{
		APIKey: "m-key-live", Stage: "identity", Message: "current account pending",
	}, "203.0.113.9"); err != nil {
		t.Fatal(err)
	}
	handler := NewHTTPServer(service, "admin").Handler()
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/module-diagnostics?device=61538M", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list status=%d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/module-diagnostics?device=61538M", nil)
	req.Header.Set("X-Bridge-Password", "admin")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	var body struct {
		Diagnostics []ModuleDiagnosticView `json:"diagnostics"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK ||
		len(body.Diagnostics) != 1 || body.Diagnostics[0].Stage != "identity" {
		t.Fatalf("admin list status=%d body=%s err=%v", rec.Code, rec.Body.String(), err)
	}
}
