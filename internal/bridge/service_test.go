package bridge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"wechat-observatory/internal/config"
)

const testAPIKey = "wechat-a-key"

func TestNewServiceDefaultsOutboxPollIntervalToThreeSeconds(t *testing.T) {
	service := NewService(Config{})
	if got := service.OutboxPollInterval(); got != 3*time.Second {
		t.Fatalf("outbox poll default = %s, want 3s", got)
	}
}

func TestContactQueryAllowsCompleteSnapshotsWithoutRaisingOtherEndpointLimits(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/module-contacts?limit=10000", nil)
	if got := queryLimit(req, 100); got != 100 {
		t.Fatalf("default endpoint limit = %d, want fallback 100", got)
	}
	if got := queryLimitUpTo(req, 100, 10000); got != 10000 {
		t.Fatalf("contact endpoint limit = %d, want 10000", got)
	}

	overLimit := httptest.NewRequest(http.MethodGet, "/api/module-contacts?limit=10001", nil)
	if got := queryLimitUpTo(overLimit, 100, 10000); got != 100 {
		t.Fatalf("over-limit contact query = %d, want fallback 100", got)
	}
}

func TestIngestPublishesAndPersistsWithoutBusinessReply(t *testing.T) {
	outbox := &fakeOutbox{}
	persistence := &fakePersistence{}
	service := newTestService("", WithOutbox(outbox), WithPersistence(persistence))

	result, err := service.Ingest(t.Context(), MessageEvent{
		APIKey:    testAPIKey,
		ID:        "101",
		Device:    "phone-a",
		From:      "wxid_friend",
		To:        "wxid_self",
		Text:      "ping",
		Direction: DirectionRecv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Published || result.PersistenceError != "" {
		t.Fatalf("unexpected ingest result: %+v", result)
	}
	if len(persistence.inboundEvents) != 1 {
		t.Fatalf("expected one persisted inbound event, got %+v", persistence.inboundEvents)
	}
	if len(outbox.items) != 0 {
		t.Fatalf("webhook ingest must not enqueue automatic replies: %+v", outbox.items)
	}
	if got := service.Hub().Recent(1); len(got) != 1 || got[0].Text != "ping" || got[0].ChatID() != "wxid_friend" {
		t.Fatalf("unexpected hub event: %+v", got)
	}
}

func TestIngestV2IdentityIsServerOwnedAndStableAcrossRetry(t *testing.T) {
	persistence := &fakePersistence{}
	service := newTestService("", WithPersistence(persistence))
	service.cfg.EventIdentityV2Devices = map[string]struct{}{"phone-a": {}}
	event := MessageEvent{
		APIKey: testAPIKey, EventKey: "evt_v2_" + strings.Repeat("f", 64),
		ID: "882", EventID: 882, ChatRecordID: 882, Device: "forged-device",
		From: "wxid_self", To: "filehelper", Text: "涓?9", Direction: DirectionSent,
		MessageType: 1, CreateTime: 1_786_530_000,
	}
	if _, err := service.Ingest(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Ingest(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	differentSourceTime := event
	differentSourceTime.CreateTime++
	if _, err := service.Ingest(t.Context(), differentSourceTime); err != nil {
		t.Fatal(err)
	}
	if len(persistence.inboundEvents) != 3 {
		t.Fatalf("persist calls = %d, want 2 retries plus one distinct source time", len(persistence.inboundEvents))
	}
	first, second, later := persistence.inboundEvents[0], persistence.inboundEvents[1], persistence.inboundEvents[2]
	if !IsCanonicalEventKeyV2(first.EventKey) || first.EventKey != second.EventKey {
		t.Fatalf("unstable v2 identity: %q %q", first.EventKey, second.EventKey)
	}
	if later.EventKey == first.EventKey {
		t.Fatalf("different source times shared v2 identity: %q", first.EventKey)
	}
	if first.EventKey == event.EventKey {
		t.Fatal("forged ingress event key was trusted")
	}
	if first.Device != "phone-a" {
		t.Fatalf("API key device was not authoritative: %q", first.Device)
	}
}

func TestIngestV2RejectsMissingSourceTimeWithoutPersistenceOrPublish(t *testing.T) {
	for _, createTime := range []int64{0, -1} {
		t.Run(strconv.FormatInt(createTime, 10), func(t *testing.T) {
			persistence := &fakePersistence{}
			service := newTestService("", WithPersistence(persistence))
			service.cfg.EventIdentityV2Devices = map[string]struct{}{"phone-a": {}}
			_, err := service.Ingest(t.Context(), MessageEvent{
				APIKey: testAPIKey, ID: "882", Device: "phone-a", From: "wxid_friend", To: "wxid_self",
				Text: "ping", Direction: DirectionRecv, CreateTime: createTime,
			})
			var validationError *IngestValidationError
			if !errors.As(err, &validationError) || validationError.Field != "create_time" {
				t.Fatalf("non-positive v2 source time error = %v", err)
			}
			if len(persistence.inboundEvents) != 0 {
				t.Fatalf("invalid v2 event reached persistence: %+v", persistence.inboundEvents)
			}
			if got := service.Hub().Recent(1); len(got) != 0 {
				t.Fatalf("invalid v2 event was published: %+v", got)
			}
		})
	}

	persistence := &fakePersistence{}
	service := newTestService("", WithPersistence(persistence))
	service.cfg.EventIdentityV2Devices = map[string]struct{}{"phone-a": {}}
	server := NewHTTPServer(service, "admin").Handler()
	body := []byte(`{"api_key":"wechat-a-key","device":"phone-a","direction":"recv","from":"wxid_friend","to":"wxid_self","text":"ping","create_time":0}`)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/webhook/lsposed/message", bytes.NewReader(body)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing v2 source time status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(persistence.inboundEvents) != 0 {
		t.Fatalf("HTTP-invalid v2 event reached persistence: %+v", persistence.inboundEvents)
	}
}

func TestIngestLegacyPersistenceFailurePreservesExistingPublishContract(t *testing.T) {
	persistence := &fakePersistence{inboundErr: errors.New("database unavailable")}
	service := newTestService("", WithPersistence(persistence))
	result, err := service.Ingest(t.Context(), MessageEvent{
		APIKey: testAPIKey, ID: "101", Device: "phone-a", From: "wxid_friend", To: "wxid_self",
		Text: "ping", Direction: DirectionRecv, CreateTime: 1_786_530_000,
	})
	if err != nil || result == nil || !result.Published || result.PersistenceError != "database unavailable" {
		t.Fatalf("legacy result=%+v err=%v", result, err)
	}
	if got := service.Hub().Recent(1); len(got) != 1 {
		t.Fatalf("legacy persistence failure should retain publish behavior: %+v", got)
	}

	server := NewHTTPServer(service, "admin").Handler()
	body := []byte(`{"api_key":"wechat-a-key","device":"phone-a","direction":"recv","from":"wxid_friend","to":"wxid_self","text":"ping","create_time":1786530000}`)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/webhook/lsposed/message", bytes.NewReader(body)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"persistence_error":"database unavailable"`) {
		t.Fatalf("legacy persistence HTTP status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestIngestV2PersistenceFailureDoesNotPublishAndReturnsRetryableHTTPStatus(t *testing.T) {
	databaseError := errors.New("database unavailable")
	service := newTestService("", WithPersistence(&fakePersistence{inboundErr: databaseError}))
	service.cfg.EventIdentityV2Devices = map[string]struct{}{"phone-a": {}}
	result, err := service.Ingest(t.Context(), MessageEvent{
		APIKey: testAPIKey, ID: "101", Device: "phone-a", From: "wxid_friend", To: "wxid_self",
		Text: "ping", Direction: DirectionRecv, CreateTime: 1_786_530_000,
	})
	var persistenceError *IngestPersistenceError
	if !errors.As(err, &persistenceError) || !errors.Is(err, databaseError) || result != nil {
		t.Fatalf("v2 result=%+v err=%v, want persistence failure", result, err)
	}
	if got := service.Hub().Recent(1); len(got) != 0 {
		t.Fatalf("failed v2 persistence published a live event: %+v", got)
	}
	server := NewHTTPServer(service, "admin").Handler()
	body := []byte(`{"api_key":"wechat-a-key","device":"phone-a","direction":"recv","from":"wxid_friend","to":"wxid_self","text":"ping","create_time":1786530000}`)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/webhook/lsposed/message", bytes.NewReader(body)))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("persistence failure status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestAcquireOutboxSessionRejectsConcurrentDeviceSession(t *testing.T) {
	leaser := &fakeSessionPersistence{leases: map[string]ModuleSessionLease{}}
	first := newTestService("", WithPersistence(leaser))
	first.instanceID = "pod-a"
	second := newTestService("", WithPersistence(leaser))
	second.instanceID = "pod-b"

	lease, err := first.AcquireOutboxSession(t.Context(), testAPIKey, "phone-a", "wxid_self")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.AcquireOutboxSession(t.Context(), testAPIKey, "phone-a", "wxid_self"); err != ErrModuleSessionActive {
		t.Fatalf("expected active-session rejection, got %v", err)
	}
	first.ReleaseOutboxSession(t.Context(), lease)
	if _, err := second.AcquireOutboxSession(t.Context(), testAPIKey, "phone-a", "wxid_self"); err != nil {
		t.Fatalf("expected session claim after release, got %v", err)
	}
}

func TestIngestUsesDatabaseAuthoritativeModuleConfiguration(t *testing.T) {
	persistence := &fakeDynamicConfigPersistence{
		fakePersistence: &fakePersistence{},
		keys: map[string]config.APIKey{
			testAPIKey: {Code: testAPIKey, Device: "phone-b"},
		},
		devices: map[string]config.Device{
			"phone-b": {Name: "phone-b", WxID: "wxid_database_current"},
		},
	}
	service := newTestService("", WithPersistence(persistence))
	_, err := service.Ingest(t.Context(), MessageEvent{
		APIKey:    testAPIKey,
		Device:    "phone-a",
		From:      "wxid_friend",
		To:        "wxid_database_current",
		Text:      "database authority",
		Direction: DirectionRecv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(persistence.inboundEvents) != 1 {
		t.Fatalf("expected one persisted event, got %+v", persistence.inboundEvents)
	}
	event := persistence.inboundEvents[0]
	if event.Device != "phone-b" || event.OwnerWxID != "wxid_database_current" {
		t.Fatalf("event should use database device identity, got %+v", event)
	}
}

func TestLiveEventsReplaysDurableCursor(t *testing.T) {
	tailer := &fakeEventTailReader{
		fakeAdminReader: &fakeAdminReader{},
		latest:          4,
		events: []MessageEvent{{
			Sequence:  4,
			EventKey:  "evt_4",
			ID:        "source-4",
			Device:    "phone-a",
			From:      "wxid_friend",
			To:        "wxid_self",
			Text:      "replayed",
			Direction: DirectionRecv,
		}, {
			Sequence: 5, EventKey: "evt_5", ID: "source-5", Device: "phone-b",
			From: "wxid_other", To: "wxid_self", Text: "other device", Direction: DirectionRecv,
		}},
	}
	service := newTestService("", WithAdminReader(tailer))
	server := NewHTTPServer(service, "admin").Handler()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/live/events?device=phone-a", nil).WithContext(ctx)
	req.Header.Set("X-Bridge-Password", "admin")
	req.Header.Set("Last-Event-ID", "3")
	rec := newSSERecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(rec, req)
		close(done)
	}()

	deadline := time.After(time.Second)
	for !strings.Contains(rec.String(), "id: 4\n") {
		select {
		case <-deadline:
			t.Fatalf("durable event was not replayed: %s", rec.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if strings.Contains(rec.String(), "other device") {
		t.Fatalf("durable stream leaked another device event: %s", rec.String())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("live event handler did not stop after cancellation")
	}
}

func TestIngestDiscardsMediaPayload(t *testing.T) {
	persistence := &fakePersistence{}
	service := newTestService("", WithPersistence(persistence))
	raw := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}

	result, err := service.Ingest(t.Context(), MessageEvent{
		APIKey:      testAPIKey,
		ID:          "media-101",
		Device:      "phone-a",
		From:        "wxid_friend",
		To:          "wxid_self",
		Text:        "[鍥剧墖]",
		MessageType: 3,
		Direction:   DirectionRecv,
		MediaKind:   "image",
		MediaMime:   "image/png",
		MediaName:   "photo.png",
		MediaBase64: base64.StdEncoding.EncodeToString(raw),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Published || result.PersistenceError != "" {
		t.Fatalf("unexpected ingest result: %+v", result)
	}
	if len(persistence.inboundEvents) != 1 {
		t.Fatalf("expected one inbound event, got %+v", persistence.inboundEvents)
	}
	event := persistence.inboundEvents[0]
	if event.MediaBase64 != "" || event.MediaURL != "" || event.MediaKind != "image" || event.MediaMime != "image/png" {
		t.Fatalf("unexpected persisted media fields: %+v", event)
	}
}

func TestLsposedWebhookStoresInboundMessageOnly(t *testing.T) {
	outbox := NewMemoryOutbox()
	service := newTestService("", WithOutbox(outbox))
	server := NewHTTPServer(service, "admin").Handler()

	body, err := json.Marshal(MessageEvent{
		APIKey:    testAPIKey,
		Device:    "phone-a",
		Direction: DirectionRecv,
		From:      "wxid_friend",
		To:        "wxid_self",
		Text:      "ping",
	})
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/webhook/lsposed/message", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}

	var payload struct {
		OK     bool         `json:"ok"`
		Result IngestResult `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || !payload.Result.Published || payload.Result.PersistenceError != "" {
		t.Fatalf("unexpected webhook response: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "business") || strings.Contains(rec.Body.String(), "command") || strings.Contains(rec.Body.String(), "scene") {
		t.Fatalf("webhook response still exposes business fields: %s", rec.Body.String())
	}
	items := pollOutbox(t, service, "phone-a", 10)
	if len(items) != 0 {
		t.Fatalf("inbound webhook should not enqueue outbox replies: %+v", items)
	}
}

func TestMediaRouteIsAbsentWhenMediaStorageIsDisabled(t *testing.T) {
	server := NewHTTPServer(newTestService(""), "admin").Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/media/phone-a/file.png", nil)
	req.Header.Set("X-Bridge-Password", "admin")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected disabled media route to be absent, got status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminRedirectsStayRelativeForReverseProxyMounts(t *testing.T) {
	server := NewHTTPServer(newTestService(""), "admin").Handler()
	for _, path := range []string{"/", "/admin"} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusPermanentRedirect || recorder.Header().Get("Location") != "admin/" {
			t.Fatalf("redirect %s = %d location=%q", path, recorder.Code, recorder.Header().Get("Location"))
		}
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/missing", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown route should remain 404, got %d", recorder.Code)
	}
}

func TestHealthIncludesDatabaseSizeWithoutMakingMetricsAReadinessDependency(t *testing.T) {
	reader := &fakeMetricsAdminReader{fakeAdminReader: &fakeAdminReader{}, databaseBytes: 123456}
	server := NewHTTPServer(newTestService("", WithAdminReader(reader)), "admin").Handler()

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"database_bytes":123456`) {
		t.Fatalf("health response=%d %s", recorder.Code, recorder.Body.String())
	}

	reader.metricsErr = errors.New("metrics unavailable")
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"metrics_error":"metrics unavailable"`) {
		t.Fatalf("metrics failure should not fail health: %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestLiveEventsStreamsPublishedMessages(t *testing.T) {
	service := newTestService("")
	server := httptest.NewServer(NewHTTPServer(service, "admin").Handler())
	defer server.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/live/events?password=admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected status %d", resp.StatusCode)
	}

	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		var out strings.Builder
		deadline := time.After(2 * time.Second)
		for {
			select {
			case <-deadline:
				got <- out.String()
				return
			default:
			}
			n, err := resp.Body.Read(buf)
			if n > 0 {
				out.Write(buf[:n])
				if strings.Contains(out.String(), "live ping") {
					got <- out.String()
					return
				}
			}
			if err != nil {
				got <- out.String()
				return
			}
		}
	}()

	body, err := json.Marshal(MessageEvent{
		APIKey:    testAPIKey,
		Device:    "phone-a",
		Direction: DirectionRecv,
		From:      "wxid_friend",
		To:        "wxid_self",
		Text:      "live ping",
	})
	if err != nil {
		t.Fatal(err)
	}
	postReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/webhook/lsposed/message", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	postResp, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusOK {
		t.Fatalf("unexpected post status %d", postResp.StatusCode)
	}

	select {
	case stream := <-got:
		if !strings.Contains(stream, "event: message") || !strings.Contains(stream, "live ping") {
			t.Fatalf("stream did not contain message event: %s", stream)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for live event")
	}
}

func TestSentMessagesAreObservedWithoutBusinessRouting(t *testing.T) {
	outbox := &fakeOutbox{}
	service := newTestService("", WithOutbox(outbox))
	result, err := service.Ingest(t.Context(), MessageEvent{
		APIKey:    testAPIKey,
		ID:        "sent-101",
		Device:    "phone-a",
		From:      "wxid_self",
		To:        "wxid_friend",
		Text:      "寮€澶氬彿",
		Direction: DirectionSent,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Published {
		t.Fatalf("unexpected sent event result: %+v", result)
	}
	if len(outbox.items) != 0 {
		t.Fatalf("sent observation must not re-enter business or enqueue replies: %+v", outbox.items)
	}
}

func TestAdminSendTextRequiresCurrentOwnerWxID(t *testing.T) {
	service := newTestService("")
	server := NewHTTPServer(service, "admin").Handler()

	body := []byte(`{"device":"phone-a","owner_wxid":"wxid_self","wx_ids":["wxid_friend"],"text":"manual reply"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/send/text", bytes.NewReader(body))
	req.Header.Set("X-Bridge-Password", "admin")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}
	var sendPayload struct {
		OK       bool  `json:"ok"`
		OutboxID int64 `json:"outbox_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &sendPayload); err != nil {
		t.Fatal(err)
	}
	if !sendPayload.OK || sendPayload.OutboxID != 1 || bytes.Contains(rec.Body.Bytes(), []byte("chat_record_id")) {
		t.Fatalf("send response must expose only the queue id: %s", rec.Body.String())
	}
	items := pollOutbox(t, service, "phone-a", 10)
	if len(items) != 1 || items[0].OwnerWxID != "wxid_self" || items[0].WxID != "wxid_friend" || items[0].Text != "manual reply" {
		t.Fatalf("unexpected outbox items: %+v", items)
	}

	staleBody := []byte(`{"device":"phone-a","owner_wxid":"wxid_stale","wx_ids":["wxid_friend"],"text":"stale reply"}`)
	staleReq := httptest.NewRequest(http.MethodPost, "/api/send/text", bytes.NewReader(staleBody))
	staleReq.Header.Set("X-Bridge-Password", "admin")
	staleRec := httptest.NewRecorder()
	server.ServeHTTP(staleRec, staleReq)
	if staleRec.Code != http.StatusBadRequest {
		t.Fatalf("stale owner should be rejected, got status %d body=%s", staleRec.Code, staleRec.Body.String())
	}

	missingOwnerBody := []byte(`{"device":"phone-a","wx_ids":["wxid_friend"],"text":"missing owner"}`)
	missingOwnerReq := httptest.NewRequest(http.MethodPost, "/api/send/text", bytes.NewReader(missingOwnerBody))
	missingOwnerReq.Header.Set("X-Bridge-Password", "admin")
	missingOwnerRec := httptest.NewRecorder()
	server.ServeHTTP(missingOwnerRec, missingOwnerReq)
	if missingOwnerRec.Code != http.StatusBadRequest {
		t.Fatalf("missing owner should be rejected, got status %d body=%s", missingOwnerRec.Code, missingOwnerRec.Body.String())
	}
}

func TestSendTextRejectsOfflinePersistentModuleBeforeEnqueue(t *testing.T) {
	outbox := NewMemoryOutbox()
	persistence := &fakeLivenessPersistence{fakePersistence: &fakePersistence{}, online: false}
	service := newTestService("", WithPersistence(persistence), WithOutbox(outbox))

	_, err := service.SendText(t.Context(), SendTextRequest{
		Device: "phone-a", OwnerWxID: "wxid_self", WxIDs: []string{"wxid_friend"}, Text: "must not queue",
	})
	if !errors.Is(err, ErrModuleOffline) {
		t.Fatalf("offline send error=%v", err)
	}
	if len(snapshotMemoryOutbox(outbox)) != 0 {
		t.Fatalf("offline send created outbox rows: %+v", snapshotMemoryOutbox(outbox))
	}
	if persistence.device != "phone-a" || persistence.ownerWxID != "wxid_self" || persistence.offlineAfter != 5*time.Minute {
		t.Fatalf("liveness request=%+v", persistence)
	}

	persistence.online = true
	if _, err := service.SendText(t.Context(), SendTextRequest{
		Device: "phone-a", OwnerWxID: "wxid_self", WxIDs: []string{"wxid_friend"}, Text: "queue when online",
	}); err != nil {
		t.Fatal(err)
	}
	items := snapshotMemoryOutbox(outbox)
	if len(items) != 1 || items[0].Status != "pending" || items[0].Text != "queue when online" {
		t.Fatalf("online send outbox=%+v", items)
	}
}

func TestRegisterModuleKeepsIdentityStableAcrossWeChatSwitch(t *testing.T) {
	service := newTestService("http://127.0.0.1:1")

	first, err := service.RegisterModule(t.Context(), ModuleRegistrationRequest{
		APIKey:   "wechat-a-key",
		Device:   "phone-a",
		WxID:     "wxid_wechat_a1",
		Nickname: "WeChat A1",
	})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := service.RegisterModule(t.Context(), ModuleRegistrationRequest{
		APIKey:   "wechat-a-key",
		Device:   "phone-a",
		WxID:     "wxid_wechat_a2",
		Nickname: "WeChat A2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Device.Name != "phone-a" || first.Device.WxID != "wxid_wechat_a1" {
		t.Fatalf("unexpected first registration: %+v", first)
	}
	if moved.Device.Name != "phone-a" || moved.Device.WxID != "wxid_wechat_a2" {
		t.Fatalf("unexpected moved registration: %+v", moved)
	}
	if device, ok := service.Device("phone-a"); !ok || device.WxID != "wxid_wechat_a2" {
		t.Fatalf("device wxid should follow the latest registration: ok=%v device=%+v", ok, device)
	}
	other, err := service.RegisterModule(t.Context(), ModuleRegistrationRequest{
		APIKey:   "wechat-b-key",
		Device:   "phone-a",
		WxID:     "wxid_wechat_b",
		Nickname: "WeChat B",
	})
	if err != nil {
		t.Fatal(err)
	}
	if other.Device.Name != "device-wechat-b-key" || other.Device.WxID != "wxid_wechat_b" {
		t.Fatalf("unexpected separate API key registration: %+v", other)
	}
}

func TestRegisterModuleKeepsAPIKeyDeviceWhenWxIDWasSeenOnAnotherDevice(t *testing.T) {
	persistence := &fakePersistence{
		deviceByWxID: map[string]config.Device{
			"wxid_self": {
				Name:     "phone-a",
				WxID:     "wxid_self",
				Nickname: "WeChat Phone",
			},
		},
	}
	service := newTestService("http://127.0.0.1:1", WithPersistence(persistence))

	result, err := service.RegisterModule(t.Context(), ModuleRegistrationRequest{
		APIKey:   "wechat-b-key",
		WxID:     "wxid_self",
		Nickname: "Same WeChat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Device.Name != "device-wechat-b-key" {
		t.Fatalf("api key should keep its own device, got %+v", result.Device)
	}
	keys := service.APIKeys()
	var bound config.APIKey
	for _, key := range keys {
		if key.Code == "wechat-b-key" {
			bound = key
			break
		}
	}
	if bound.Device != "device-wechat-b-key" {
		t.Fatalf("api key should not be rebound by wxid lookup, got %+v", bound)
	}
}

func TestModuleOutboxIgnoresStaleOwnerWxIDAfterSwitch(t *testing.T) {
	outbox := &fakeOutbox{}
	service := newTestService("http://127.0.0.1:1", WithOutbox(outbox))
	if _, err := service.SendText(t.Context(), SendTextRequest{
		Device:    "phone-a",
		OwnerWxID: "wxid_self",
		WxIDs:     []string{"wxid_friend"},
		Text:      "old owner queued",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterModule(t.Context(), ModuleRegistrationRequest{
		APIKey:   "wechat-a-key",
		Device:   "phone-a",
		WxID:     "wxid_self_new",
		Nickname: "WeChat New",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PollOutbox(t.Context(), ModulePollRequest{
		APIKey: testAPIKey,
		Device: "phone-a",
		WxID:   "wxid_self",
		Limit:  1,
	}); err == nil || !strings.Contains(err.Error(), "not current device wxid") {
		t.Fatalf("expected stale owner poll rejection, got %v", err)
	}
	items, err := service.PollOutbox(t.Context(), ModulePollRequest{
		APIKey: testAPIKey,
		Device: "phone-a",
		WxID:   "wxid_self_new",
		Limit:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("old-owner outbox item should not be leased by new login: %+v", items)
	}
	if _, err := service.SendText(t.Context(), SendTextRequest{
		Device:    "phone-a",
		OwnerWxID: "wxid_self",
		WxIDs:     []string{"wxid_friend"},
		Text:      "stale admin send",
	}); err == nil || !strings.Contains(err.Error(), "not current device wxid") {
		t.Fatalf("expected stale owner send rejection, got %v", err)
	}
}

func TestRegisterModulePersistsStableIdentity(t *testing.T) {
	persistence := &fakePersistence{}
	service := newTestService("http://127.0.0.1:1", WithPersistence(persistence))

	result, err := service.RegisterModule(t.Context(), ModuleRegistrationRequest{
		APIKey:   "wechat-a-key",
		Device:   "phone-a",
		WxID:     "wxid_new_self",
		Nickname: "New WeChat",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Device.Name != "phone-a" || result.Device.WxID != "wxid_new_self" || result.Device.Nickname != "WeChat Phone" || result.Device.WeChatNickname != "New WeChat" {
		t.Fatalf("unexpected registration device: %+v", result.Device)
	}
	if device, ok := service.Device("phone-a"); !ok || device.Nickname != "WeChat Phone" || device.WeChatNickname != "New WeChat" {
		t.Fatalf("registration should keep the device label and track the WeChat nickname: ok=%v device=%+v", ok, device)
	}
	if persistence.deviceName != "phone-a" || persistence.deviceWxID != "wxid_new_self" || persistence.deviceNickname != "WeChat Phone" || persistence.wechatNickname != "New WeChat" {
		t.Fatalf("device identities were not persisted: name=%q wxid=%q device_nickname=%q wechat_nickname=%q", persistence.deviceName, persistence.deviceWxID, persistence.deviceNickname, persistence.wechatNickname)
	}
	if len(persistence.moduleActivities) != 1 || persistence.moduleActivities[0].Kind != "register" || persistence.moduleActivities[0].APIKey != "wechat-a-key" {
		t.Fatalf("module register activity was not recorded: %+v", persistence.moduleActivities)
	}
}

func TestIngestRecordsPersistenceChain(t *testing.T) {
	persistence := &fakePersistence{}
	outbox := &fakeOutbox{}
	service := newTestService("", WithPersistence(persistence), WithOutbox(outbox))

	result, err := service.Ingest(t.Context(), MessageEvent{
		APIKey:    testAPIKey,
		ID:        "persist-101",
		Device:    "phone-a",
		From:      "wxid_friend",
		To:        "wxid_self",
		Text:      "ping",
		Direction: DirectionRecv,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || !result.Published || result.PersistenceError != "" {
		t.Fatalf("unexpected ingest result: %+v", result)
	}
	if strings.Join(persistence.calls, ",") != "inbound" {
		t.Fatalf("unexpected persistence calls: %+v", persistence.calls)
	}
	if len(outbox.items) != 0 {
		t.Fatalf("pure gateway ingest should not enqueue replies: %+v", outbox.items)
	}
}

func TestModuleRegisterEndpointUsesAPIKey(t *testing.T) {
	service := newTestService("")
	server := NewHTTPServer(service, "admin").Handler()

	body := []byte(`{"api_key":"wechat-a-key","device":"phone-a","wxid":"wxid_module","nickname":"Module WeChat"}`)
	req := httptest.NewRequest(http.MethodPost, "/module/register", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		OK     bool `json:"ok"`
		Result struct {
			Device struct {
				Name     string `json:"name"`
				WxID     string `json:"wxid"`
				Nickname string `json:"nickname"`
			} `json:"device"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Result.Device.Name != "phone-a" || payload.Result.Device.WxID != "wxid_module" {
		t.Fatalf("unexpected register payload: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"Name"`) || strings.Contains(rec.Body.String(), `"WxID"`) || strings.Contains(rec.Body.String(), `"Timeout"`) {
		t.Fatalf("register response leaked Go device field names: %s", rec.Body.String())
	}
	if device, ok := service.Device("phone-a"); !ok || device.WxID != "wxid_module" {
		t.Fatalf("device wxid was not updated: ok=%v device=%+v", ok, device)
	}
}

func TestModuleRegisterEndpointRejectsBadCode(t *testing.T) {
	service := newTestService("")
	server := NewHTTPServer(service, "admin").Handler()

	req := httptest.NewRequest(http.MethodPost, "/module/register", bytes.NewReader([]byte(`{"api_key":"bad","device":"phone-a","wxid":"wxid_module"}`)))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAPIKeyDisableStopsAndEnableRestoresModuleAuth(t *testing.T) {
	service := newTestService("")
	server := NewHTTPServer(service, "admin").Handler()

	disableReq := httptest.NewRequest(http.MethodPost, "/api/api-keys/wechat-a-key/disable", nil)
	disableReq.Header.Set("X-Bridge-Password", "admin")
	disableRec := httptest.NewRecorder()
	server.ServeHTTP(disableRec, disableReq)
	if disableRec.Code != http.StatusOK || !strings.Contains(disableRec.Body.String(), `"enabled":false`) {
		t.Fatalf("unexpected disable response status=%d body=%s", disableRec.Code, disableRec.Body.String())
	}

	_, err := service.RegisterModule(t.Context(), ModuleRegistrationRequest{
		APIKey: "wechat-a-key",
		WxID:   "wxid_module",
	})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("disabled api key should reject module auth, got %v", err)
	}

	enableReq := httptest.NewRequest(http.MethodPost, "/api/api-keys/wechat-a-key/enable", nil)
	enableReq.Header.Set("X-Bridge-Password", "admin")
	enableRec := httptest.NewRecorder()
	server.ServeHTTP(enableRec, enableReq)
	if enableRec.Code != http.StatusOK || !strings.Contains(enableRec.Body.String(), `"enabled":true`) {
		t.Fatalf("unexpected enable response status=%d body=%s", enableRec.Code, enableRec.Body.String())
	}
	if _, err := service.RegisterModule(t.Context(), ModuleRegistrationRequest{
		APIKey: "wechat-a-key",
		WxID:   "wxid_module",
	}); err != nil {
		t.Fatalf("enabled api key should register again: %v", err)
	}
}

func TestAPIKeyIntrospectionUsesOpaqueReferenceAndTracksAuthorityVersion(t *testing.T) {
	service := newTestService("")
	server := NewHTTPServer(service, "admin").Handler()

	loginReq := httptest.NewRequest(http.MethodPost, "/internal/api-key/introspect", strings.NewReader(`{"api_key":"wechat-a-key"}`))
	loginReq.Header.Set("X-Bridge-Password", "admin")
	loginRec := httptest.NewRecorder()
	server.ServeHTTP(loginRec, loginReq)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("unexpected introspection status=%d body=%s", loginRec.Code, loginRec.Body.String())
	}
	var login APIKeyIntrospection
	if err := json.Unmarshal(loginRec.Body.Bytes(), &login); err != nil {
		t.Fatal(err)
	}
	if !login.Active || login.CredentialRef == "" || login.AuthVersion != 1 || login.Device != "phone-a" {
		t.Fatalf("unexpected introspection result: %+v", login)
	}
	for _, private := range []string{"wechat-a-key", "wxid_self", "WeChat Phone"} {
		if strings.Contains(loginRec.Body.String(), private) {
			t.Fatalf("introspection leaked %q: %s", private, loginRec.Body.String())
		}
	}
	if loginRec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("introspection must be no-store: %v", loginRec.Header())
	}

	refBody := fmt.Sprintf(`{"credential_ref":%q}`, login.CredentialRef)
	refReq := httptest.NewRequest(http.MethodPost, "/internal/api-key/introspect", strings.NewReader(refBody))
	refReq.Header.Set("X-Bridge-Password", "admin")
	refRec := httptest.NewRecorder()
	server.ServeHTTP(refRec, refReq)
	if refRec.Code != http.StatusOK || !strings.Contains(refRec.Body.String(), `"active":true`) {
		t.Fatalf("reference revalidation failed: %d %s", refRec.Code, refRec.Body.String())
	}

	if _, err := service.SetAPIKeyEnabled(t.Context(), "wechat-a-key", false); err != nil {
		t.Fatal(err)
	}
	disabledReq := httptest.NewRequest(http.MethodPost, "/internal/api-key/introspect", strings.NewReader(refBody))
	disabledReq.Header.Set("X-Bridge-Password", "admin")
	disabledRec := httptest.NewRecorder()
	server.ServeHTTP(disabledRec, disabledReq)
	if disabledRec.Code != http.StatusOK || disabledRec.Body.String() != "{\"active\":false}\n" {
		t.Fatalf("disabled reference should be generically inactive: %d %s", disabledRec.Code, disabledRec.Body.String())
	}

	if _, err := service.SetAPIKeyEnabled(t.Context(), "wechat-a-key", true); err != nil {
		t.Fatal(err)
	}
	reenabled, err := service.IntrospectAPIKey(t.Context(), APIKeyIntrospectionRequest{APIKey: "wechat-a-key"})
	if err != nil {
		t.Fatal(err)
	}
	if !reenabled.Active || reenabled.CredentialRef != login.CredentialRef || reenabled.AuthVersion <= login.AuthVersion {
		t.Fatalf("reenabled authority should keep ref and advance version: before=%+v after=%+v", login, reenabled)
	}
}

func TestAPIKeyIntrospectionFailsClosed(t *testing.T) {
	service := newTestService("")
	server := NewHTTPServer(service, "admin").Handler()

	for _, body := range []string{
		`{"api_key":"unknown"}`,
		`{"api_key":"wechat-b-key"}`,
		`{"credential_ref":"ak_unknown"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/internal/api-key/introspect", strings.NewReader(body))
		req.Header.Set("X-Bridge-Password", "admin")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "{\"active\":false}\n" {
			t.Fatalf("inactive introspection body=%s status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}

	badShape := httptest.NewRequest(http.MethodPost, "/internal/api-key/introspect", strings.NewReader(`{"api_key":"wechat-a-key","credential_ref":"ak_conflict"}`))
	badShape.Header.Set("X-Bridge-Password", "admin")
	badShapeRec := httptest.NewRecorder()
	server.ServeHTTP(badShapeRec, badShape)
	if badShapeRec.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous introspection request should fail: %d %s", badShapeRec.Code, badShapeRec.Body.String())
	}

	unauthorized := httptest.NewRequest(http.MethodPost, "/internal/api-key/introspect", strings.NewReader(`{"api_key":"wechat-a-key"}`))
	unauthorizedRec := httptest.NewRecorder()
	server.ServeHTTP(unauthorizedRec, unauthorized)
	if unauthorizedRec.Code != http.StatusUnauthorized {
		t.Fatalf("introspection should require admin auth: %d %s", unauthorizedRec.Code, unauthorizedRec.Body.String())
	}
}

func TestAdminReadEndpointsUsePersistentReader(t *testing.T) {
	reader := &fakeAdminReader{
		keys: []APIKeyView{
			{Code: "wechat-a-key", APIKey: "wechat-a-key"},
		},
		events: []StoredEventView{
			{ID: 7, Device: "phone-a", Text: "hello"},
		},
		messages: []StoredEventView{
			{ID: 9, Device: "phone-a", Text: "chat"},
		},
		modules: []ModuleStatusView{
			{Device: "phone-a", RuntimeStatus: "ready"},
		},
		contacts: []ModuleContactView{
			{Device: "phone-a", WxID: "wxid_friend", Nickname: "Friend"},
		},
	}
	service := newTestService("http://127.0.0.1:1", WithAdminReader(reader))
	server := NewHTTPServer(service, "admin").Handler()

	cases := []struct {
		path string
		want string
	}{
		{path: "/api/api-keys?limit=1", want: `"api_keys"`},
		{path: "/api/stored-events?limit=1", want: `"events"`},
		{path: "/api/messages?device=phone-a&wxid=wxid_friend&after_id=8&limit=1", want: `"messages"`},
		{path: "/api/modules/status", want: `"modules"`},
		{path: "/api/module-contacts?device=phone-a&wxid=wxid_friend&q=Friend&limit=1", want: `"contacts"`},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("X-Bridge-Password", "admin")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), tc.want) {
			t.Fatalf("%s unexpected status=%d body=%s", tc.path, rec.Code, rec.Body.String())
		}
	}
	if got := strings.Join(reader.calls, ","); !strings.Contains(got, "keys:1") || !strings.Contains(got, "events:1") || !strings.Contains(got, "messages:phone-a:wxid_friend:1") || !strings.Contains(got, "modules") || !strings.Contains(got, "contacts:phone-a:Friend:1") {
		t.Fatalf("persistent reader was not used as expected: %+v", reader.calls)
	}
	if !reader.lastMessageFilter.AfterIDSet || reader.lastMessageFilter.AfterID != 8 {
		t.Fatalf("message cursor filter was not forwarded: %+v", reader.lastMessageFilter)
	}
	if reader.lastContactFilter.WxID != "wxid_friend" {
		t.Fatalf("exact contact filter was not forwarded: %+v", reader.lastContactFilter)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/messages?after_id=-1", nil)
	req.Header.Set("X-Bridge-Password", "admin")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("negative after_id status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminCanGenerateAPIKeyAndRenameDevice(t *testing.T) {
	service := newTestService("http://127.0.0.1:1")
	server := NewHTTPServer(service, "admin").Handler()

	body := []byte(`{"api_key":"wg_web_key","device":"phone-web","nickname":"Web WeChat"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/api-keys", bytes.NewReader(body))
	req.Header.Set("X-Bridge-Password", "admin")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected api key status %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"api_key"`) || !strings.Contains(rec.Body.String(), `"phone-web"`) {
		t.Fatalf("unexpected api key response: %s", rec.Body.String())
	}

	deviceBody := []byte(`{"name":"phone-a","nickname":"Web Phone A"}`)
	deviceReq := httptest.NewRequest(http.MethodPost, "/api/devices", bytes.NewReader(deviceBody))
	deviceReq.Header.Set("X-Bridge-Password", "admin")
	deviceRec := httptest.NewRecorder()
	server.ServeHTTP(deviceRec, deviceReq)
	if deviceRec.Code != http.StatusOK {
		t.Fatalf("unexpected device status %d body=%s", deviceRec.Code, deviceRec.Body.String())
	}
	if !strings.Contains(deviceRec.Body.String(), `"device_nickname":"Web Phone A"`) {
		t.Fatalf("unexpected device response: %s", deviceRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/api-keys/wg_web_key", nil)
	deleteReq.Header.Set("X-Bridge-Password", "admin")
	deleteRec := httptest.NewRecorder()
	server.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("unexpected delete api key status %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	for _, code := range service.APIKeys() {
		if code.Code == "wg_web_key" {
			t.Fatalf("api key was not removed")
		}
	}
}

func TestLegacyBusinessAdminEndpointsAreGone(t *testing.T) {
	service := newTestService("")
	server := NewHTTPServer(service, "admin").Handler()
	for _, path := range []string{"/api/commands", "/api/replies"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-Bridge-Password", "admin")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s should be removed, got status=%d body=%s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestModuleContactsSnapshotEndpointPersistsContacts(t *testing.T) {
	persistence := &fakePersistence{}
	service := newTestService("http://127.0.0.1:1", WithPersistence(persistence))
	server := NewHTTPServer(service, "admin").Handler()

	body := []byte(`{"api_key":"wechat-a-key","device":"phone-a","wxid":"wxid_self","complete":true,"contacts":[{"wxid":"wxid_friend","nickname":"Friend"},{"wxid":"","nickname":"ignored"}]}`)
	req := httptest.NewRequest(http.MethodPost, "/module/contacts/snapshot", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":1`) {
		t.Fatalf("unexpected contacts response status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(persistence.contactSnapshots) != 1 || len(persistence.contactSnapshots[0].Contacts) != 1 || persistence.contactSnapshots[0].Contacts[0].WxID != "wxid_friend" {
		t.Fatalf("unexpected persisted contact snapshot: %+v", persistence.contactSnapshots)
	}
}

func TestModuleStatusEndpointFallsBackToRuntimeSnapshot(t *testing.T) {
	service := newTestService("")
	server := NewHTTPServer(service, "admin").Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/modules/status", nil)
	req.Header.Set("X-Bridge-Password", "admin")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"device":"phone-a"`) || !strings.Contains(rec.Body.String(), `"runtime_status":"ready"`) {
		t.Fatalf("unexpected status response %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestModuleStatusOfflinePrecedesOutboxCountsAtFiveMinutes(t *testing.T) {
	now := time.Date(2026, time.July, 28, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		status ModuleStatusView
		want   string
	}{
		{name: "disabled wins", status: ModuleStatusView{Enabled: false, Registered: true, RuntimeUpdatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), PendingOutbox: 2}, want: "disabled"},
		{name: "unregistered wins", status: ModuleStatusView{Enabled: true, Registered: false, RuntimeUpdatedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), PendingOutbox: 2}, want: "unregistered"},
		{name: "exact cutoff is online pending", status: ModuleStatusView{Enabled: true, Registered: true, RuntimeUpdatedAt: now.Add(-5 * time.Minute).Format(time.RFC3339Nano), PendingOutbox: 2}, want: "pending"},
		{name: "older than cutoff is offline", status: ModuleStatusView{Enabled: true, Registered: true, RuntimeUpdatedAt: now.Add(-5*time.Minute - time.Nanosecond).Format(time.RFC3339Nano), PendingOutbox: 2, LeasedOutbox: 1}, want: "offline"},
		{name: "fresh lease is sending", status: ModuleStatusView{Enabled: true, Registered: true, RuntimeUpdatedAt: now.Add(-time.Minute).Format(time.RFC3339Nano), LeasedOutbox: 1}, want: "sending"},
		{name: "fresh ready", status: ModuleStatusView{Enabled: true, Registered: true, RuntimeUpdatedAt: now.Add(-time.Minute).Format(time.RFC3339Nano)}, want: "ready"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.status.NormalizeRuntimeStatusAt(now, 5*time.Minute)
			if test.status.RuntimeStatus != test.want {
				t.Fatalf("runtime status=%q want=%q", test.status.RuntimeStatus, test.want)
			}
		})
	}
}

func TestModuleStatusEndpointProjectsOfflineFromPersistentActivity(t *testing.T) {
	reader := &fakeAdminReader{modules: []ModuleStatusView{{
		Device: "phone-a", Enabled: true, Registered: true,
		RuntimeUpdatedAt: time.Now().Add(-6 * time.Minute).UTC().Format(time.RFC3339Nano),
		PendingOutbox:    2,
	}}}
	service := newTestService("", WithAdminReader(reader))
	server := NewHTTPServer(service, "admin").Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/modules/status", nil)
	req.Header.Set("X-Bridge-Password", "admin")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"runtime_status":"offline"`) {
		t.Fatalf("offline module status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminReadEndpointsRequireAdminPassword(t *testing.T) {
	service := newTestService("http://127.0.0.1:1")
	server := NewHTTPServer(service, "admin").Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/modules/status", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestAdminReadEndpointsRejectBridgeTokenHeader(t *testing.T) {
	service := newTestService("http://127.0.0.1:1")
	server := NewHTTPServer(service, "admin").Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/modules/status", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestModuleOutboxPollAndAckEndpoints(t *testing.T) {
	outbox := &fakeOutbox{}
	persistence := &fakePersistence{}
	service := newTestService("http://127.0.0.1:1", WithOutbox(outbox), WithPersistence(persistence))
	server := NewHTTPServer(service, "admin").Handler()

	if _, err := service.SendText(t.Context(), SendTextRequest{
		Device: "phone-a",
		WxIDs:  []string{"wxid_friend"},
		Text:   "queued reply",
	}); err != nil {
		t.Fatal(err)
	}

	pollBody := []byte(`{"api_key":"wechat-a-key","device":"phone-a","limit":10}`)
	pollReq := httptest.NewRequest(http.MethodPost, "/module/outbox/poll", bytes.NewReader(pollBody))
	pollRec := httptest.NewRecorder()
	server.ServeHTTP(pollRec, pollReq)
	if pollRec.Code != http.StatusOK {
		t.Fatalf("unexpected poll status %d body=%s", pollRec.Code, pollRec.Body.String())
	}
	var pollPayload struct {
		OK    bool               `json:"ok"`
		Items []ModuleOutboxItem `json:"items"`
	}
	if err := json.Unmarshal(pollRec.Body.Bytes(), &pollPayload); err != nil {
		t.Fatal(err)
	}
	if !pollPayload.OK || len(pollPayload.Items) != 1 || pollPayload.Items[0].Text != "queued reply" || pollPayload.Items[0].Status != "leased" {
		t.Fatalf("unexpected poll payload: %s", pollRec.Body.String())
	}

	ackBody := []byte(`{"api_key":"wechat-a-key","device":"phone-a","items":[{"id":1,"status":"sent","chat_record_id":9001}]}`)
	ackReq := httptest.NewRequest(http.MethodPost, "/module/outbox/ack", bytes.NewReader(ackBody))
	ackRec := httptest.NewRecorder()
	server.ServeHTTP(ackRec, ackReq)
	if ackRec.Code != http.StatusOK {
		t.Fatalf("unexpected ack status %d body=%s", ackRec.Code, ackRec.Body.String())
	}
	if len(persistence.outboundEvents) != 1 ||
		persistence.outboundEvents[0].ChatRecordID != 9001 ||
		persistence.outboundEvents[0].RawProvider != RawProviderModuleAck ||
		persistence.outboundEvents[0].OwnerWxID != "wxid_self" {
		t.Fatalf("ack did not record outbound event: %+v", persistence.outboundEvents)
	}
	if len(persistence.moduleActivities) != 2 ||
		persistence.moduleActivities[0].Kind != "poll" || persistence.moduleActivities[0].PollItemCount != 1 ||
		persistence.moduleActivities[1].Kind != "ack" || persistence.moduleActivities[1].AckSentCount != 1 {
		t.Fatalf("module poll/ack activity was not recorded: %+v", persistence.moduleActivities)
	}
}

func TestModuleOutboxWebSocketPushAndAck(t *testing.T) {
	outbox := NewMemoryOutbox()
	persistence := &fakePersistence{}
	service := newTestService("http://127.0.0.1:1", WithOutbox(outbox), WithPersistence(persistence))
	server := httptest.NewServer(NewHTTPServer(service, "admin").Handler())
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL, "/module/outbox/ws?api_key=wechat-a-key&device=phone-a&wxid=wxid_self")
	defer conn.close()

	ready := readTestWSMessage(t, conn)
	if ready.Type != "ready" || !ready.OK {
		t.Fatalf("unexpected ready message: %+v", ready)
	}
	if _, err := service.SendText(t.Context(), SendTextRequest{
		Device: "phone-a",
		WxIDs:  []string{"wxid_friend"},
		Text:   "queued through ws",
	}); err != nil {
		t.Fatal(err)
	}
	outboxMsg := readTestWSMessage(t, conn)
	if outboxMsg.Type != "outbox" || len(outboxMsg.Items) != 1 || outboxMsg.Items[0].Text != "queued through ws" || outboxMsg.Items[0].Status != "leased" {
		t.Fatalf("unexpected outbox message: %+v", outboxMsg)
	}
	ack := ModuleAckRequest{
		Items: []ModuleAckItem{{
			ID:           outboxMsg.Items[0].ID,
			Status:       "sent",
			ChatRecordID: 9101,
		}},
	}
	if !conn.writeJSON(outboxWSMessage{Type: "ack", Ack: &ack}) {
		t.Fatal("failed to write websocket ack")
	}
	ackMsg := readTestWSMessageOfType(t, conn, "ack")
	if ackMsg.Type != "ack" || !ackMsg.OK || len(ackMsg.Items) != 1 || ackMsg.Items[0].Status != "sent" {
		t.Fatalf("unexpected ack message: %+v", ackMsg)
	}
	outboundEvents, moduleActivities := persistence.activitySnapshot()
	if len(outboundEvents) != 1 ||
		outboundEvents[0].ChatRecordID != 9101 ||
		outboundEvents[0].RawProvider != RawProviderModuleAck ||
		outboundEvents[0].OwnerWxID != "wxid_self" {
		t.Fatalf("ack did not record outbound event: %+v", outboundEvents)
	}
	hasAckActivity := false
	for _, activity := range moduleActivities {
		if activity.Kind == "ack" && activity.AckSentCount == 1 {
			hasAckActivity = true
		}
	}
	if !hasAckActivity {
		t.Fatalf("module websocket activity was not recorded: %+v", moduleActivities)
	}
}

func TestModuleOutboxWebSocketProbesBeforeLeasing(t *testing.T) {
	outbox := NewMemoryOutbox()
	service := newTestService("http://127.0.0.1:1", WithOutbox(outbox))
	server := httptest.NewServer(NewHTTPServer(service, "admin").Handler())
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL, "/module/outbox/ws?api_key=wechat-a-key&device=phone-a&wxid=wxid_self")
	defer conn.close()
	if ready := readTestWSMessage(t, conn); ready.Type != "ready" || !ready.OK {
		t.Fatalf("unexpected ready message: %+v", ready)
	}
	if _, err := service.SendText(t.Context(), SendTextRequest{
		Device: "phone-a", WxIDs: []string{"wxid_friend"}, Text: "probe before lease",
	}); err != nil {
		t.Fatal(err)
	}

	if err := conn.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload, op, err := conn.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if op != wsOpPing {
		t.Fatalf("first delivery frame opcode = %d, want ping", op)
	}
	items := snapshotMemoryOutbox(outbox)
	if len(items) != 1 || items[0].Status != "pending" || items[0].AttemptCount != 0 {
		t.Fatalf("outbox leased before liveness proof: %+v", items)
	}
	if !conn.writeControl(wsOpPong, payload) {
		t.Fatal("failed to answer delivery probe")
	}
	outboxMsg := readTestWSMessageOfType(t, conn, "outbox")
	if len(outboxMsg.Items) != 1 || outboxMsg.Items[0].Status != "leased" || outboxMsg.Items[0].AttemptCount != 1 {
		t.Fatalf("unexpected outbox after liveness proof: %+v", outboxMsg)
	}
}

func TestModuleOutboxWebSocketProbeTimeoutLeavesItemPending(t *testing.T) {
	outbox := NewMemoryOutbox()
	service := newTestService("http://127.0.0.1:1", WithOutbox(outbox))
	server := httptest.NewServer(NewHTTPServer(service, "admin").Handler())
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL, "/module/outbox/ws?api_key=wechat-a-key&device=phone-a&wxid=wxid_self")
	defer conn.close()
	if ready := readTestWSMessage(t, conn); ready.Type != "ready" || !ready.OK {
		t.Fatalf("unexpected ready message: %+v", ready)
	}
	if _, err := service.SendText(t.Context(), SendTextRequest{
		Device: "phone-a", WxIDs: []string{"wxid_friend"}, Text: "keep pending on stale socket",
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, op, err := conn.readFrame()
	if err != nil {
		t.Fatal(err)
	}
	if op != wsOpPing {
		t.Fatalf("first delivery frame opcode = %d, want ping", op)
	}
	started := time.Now()
	if _, _, err := conn.readFrame(); err == nil {
		t.Fatal("stale websocket remained open after probe timeout")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("stale websocket close took %s", elapsed)
	}
	items := snapshotMemoryOutbox(outbox)
	if len(items) != 1 || items[0].Status != "pending" || items[0].AttemptCount != 0 {
		t.Fatalf("probe timeout leased pending outbox: %+v", items)
	}
}

func TestModuleOutboxWebSocketKeepsSecondItemPendingUntilAck(t *testing.T) {
	outbox := NewMemoryOutbox()
	service := newTestService("http://127.0.0.1:1", WithOutbox(outbox))
	server := httptest.NewServer(NewHTTPServer(service, "admin").Handler())
	defer server.Close()

	conn := dialTestWebSocket(t, server.URL, "/module/outbox/ws?api_key=wechat-a-key&device=phone-a&wxid=wxid_self")
	defer conn.close()
	if ready := readTestWSMessage(t, conn); ready.Type != "ready" || !ready.OK {
		t.Fatalf("unexpected ready message: %+v", ready)
	}
	if _, err := service.SendText(t.Context(), SendTextRequest{
		Device: "phone-a", WxIDs: []string{"wxid_friend"}, Text: "first single-flight reply",
	}); err != nil {
		t.Fatal(err)
	}
	first := readTestWSMessageOfType(t, conn, "outbox")
	if len(first.Items) != 1 || first.Items[0].Text != "first single-flight reply" {
		t.Fatalf("unexpected first outbox message: %+v", first)
	}
	if _, err := service.SendText(t.Context(), SendTextRequest{
		Device: "phone-a", WxIDs: []string{"wxid_friend"}, Text: "second single-flight reply",
	}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	items := snapshotMemoryOutbox(outbox)
	if len(items) != 2 || items[0].Status != "leased" || items[1].Status != "pending" || items[1].AttemptCount != 0 {
		t.Fatalf("second outbox item leased before first ACK: %+v", items)
	}

	ack := ModuleAckRequest{Items: []ModuleAckItem{{ID: first.Items[0].ID, Status: "sent", ChatRecordID: 9201}}}
	if !conn.writeJSON(outboxWSMessage{Type: "ack", Ack: &ack}) {
		t.Fatal("failed to write first ACK")
	}
	ackMsg := readTestWSMessageOfType(t, conn, "ack")
	if !ackMsg.OK || len(ackMsg.Items) != 1 || ackMsg.Items[0].Status != "sent" {
		t.Fatalf("unexpected first ACK response: %+v", ackMsg)
	}
	second := readTestWSMessageOfType(t, conn, "outbox")
	if len(second.Items) != 1 || second.Items[0].Text != "second single-flight reply" || second.Items[0].AttemptCount != 1 {
		t.Fatalf("unexpected second outbox message: %+v", second)
	}
}

func snapshotMemoryOutbox(outbox *MemoryOutbox) []ModuleOutboxItem {
	outbox.mu.Lock()
	defer outbox.mu.Unlock()
	items := make([]ModuleOutboxItem, 0, len(outbox.items))
	for _, item := range outbox.items {
		items = append(items, item.ModuleOutboxItem)
	}
	return items
}

func TestModuleOutboxPollIsSerializedForWeChatSender(t *testing.T) {
	service := newTestService("")
	for _, text := range []string{"first queued reply", "second queued reply"} {
		if _, err := service.SendText(t.Context(), SendTextRequest{
			Device: "phone-a",
			WxIDs:  []string{"wxid_friend"},
			Text:   text,
		}); err != nil {
			t.Fatal(err)
		}
	}

	items, err := service.PollOutbox(t.Context(), ModulePollRequest{
		APIKey: testAPIKey,
		Device: "phone-a",
		Limit:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Text != "first queued reply" {
		t.Fatalf("unexpected serialized poll items: %+v", items)
	}
}

func TestModuleRegisterUsesWebBoundDevice(t *testing.T) {
	service := newTestService("http://127.0.0.1:1")
	result, err := service.RegisterModule(t.Context(), ModuleRegistrationRequest{
		APIKey: "wechat-phone-b-key",
		Device: "phone-a",
		WxID:   "wxid_self_wrong_device",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Device.Name != "phone-b" || result.Device.WxID != "wxid_self_wrong_device" {
		t.Fatalf("web-bound device should win over module payload: %+v", result)
	}
}

func pollOutbox(t *testing.T, service *Service, device string, limit int) []ModuleOutboxItem {
	t.Helper()
	items, err := service.PollOutbox(t.Context(), ModulePollRequest{
		APIKey: testAPIKey,
		Device: device,
		Limit:  limit,
	})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func newTestService(legacyEndpoint string, opts ...Option) *Service {
	return NewService(Config{
		DefaultDevice: "phone-a",
		Devices: map[string]config.Device{
			"phone-a": {
				Name:     "phone-a",
				WxID:     "wxid_self",
				Nickname: "WeChat Phone",
				Timeout:  time.Second,
			},
			"phone-b": {
				Name:     "phone-b",
				WxID:     "wxid_unbound_phone_b",
				Nickname: "WeChat Phone B",
				Timeout:  time.Second,
			},
		},
		APIKeys: map[string]config.APIKey{
			"wechat-a-key": {
				Code:   "wechat-a-key",
				Device: "phone-a",
			},
			"wechat-b-key": {
				Code: "wechat-b-key",
			},
			"wechat-phone-b-key": {
				Code:   "wechat-phone-b-key",
				Device: "phone-b",
			},
		},
	}, opts...)
}

type fakePersistence struct {
	mu               sync.Mutex
	deviceName       string
	deviceWxID       string
	deviceNickname   string
	wechatNickname   string
	deviceByWxID     map[string]config.Device
	inboundEvents    []MessageEvent
	outboundEvents   []MessageEvent
	moduleActivities []ModuleActivity
	contactSnapshots []ModuleContactSnapshotRequest
	calls            []string
	inboundErr       error
}

type fakeSessionPersistence struct {
	*fakePersistence
	mu     sync.Mutex
	leases map[string]ModuleSessionLease
}

type fakeLivenessPersistence struct {
	*fakePersistence
	online       bool
	err          error
	device       string
	ownerWxID    string
	offlineAfter time.Duration
}

type fakeDynamicConfigPersistence struct {
	*fakePersistence
	keys    map[string]config.APIKey
	devices map[string]config.Device
}

func (p *fakeLivenessPersistence) ModuleOnline(_ context.Context, device string, ownerWxID string, offlineAfter time.Duration) (bool, error) {
	p.device = device
	p.ownerWxID = ownerWxID
	p.offlineAfter = offlineAfter
	return p.online, p.err
}

func (p *fakeDynamicConfigPersistence) LookupAPIKey(_ context.Context, code string) (config.APIKey, bool, error) {
	key, ok := p.keys[code]
	return key, ok, nil
}

func (p *fakeDynamicConfigPersistence) LookupDevice(_ context.Context, name string) (config.Device, bool, error) {
	device, ok := p.devices[name]
	return device, ok, nil
}

func (p *fakeSessionPersistence) ClaimModuleSession(_ context.Context, lease ModuleSessionLease) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := lease.Device + "\x00" + lease.OwnerWxID
	if _, exists := p.leases[key]; exists {
		return false, nil
	}
	p.leases[key] = lease
	return true, nil
}

func (p *fakeSessionPersistence) RenewModuleSession(_ context.Context, lease ModuleSessionLease) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current, ok := p.leases[lease.Device+"\x00"+lease.OwnerWxID]
	return ok && current.HolderID == lease.HolderID && current.Token == lease.Token, nil
}

func (p *fakeSessionPersistence) ReleaseModuleSession(_ context.Context, lease ModuleSessionLease) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := lease.Device + "\x00" + lease.OwnerWxID
	current, ok := p.leases[key]
	if ok && current.HolderID == lease.HolderID && current.Token == lease.Token {
		delete(p.leases, key)
	}
	return nil
}

func (p *fakePersistence) UpdateDeviceIdentity(_ context.Context, deviceName string, wxid string, nickname string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deviceName = deviceName
	p.deviceWxID = wxid
	p.deviceNickname = nickname
	return nil
}

func (p *fakePersistence) UpdateDeviceWeChatIdentity(_ context.Context, _ string, _ string, nickname string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wechatNickname = nickname
	return nil
}

func (p *fakePersistence) LookupDeviceByWxID(_ context.Context, wxid string) (config.Device, bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deviceByWxID == nil {
		return config.Device{}, false, nil
	}
	device, ok := p.deviceByWxID[wxid]
	return device, ok, nil
}

func (p *fakePersistence) UpsertAPIKey(_ context.Context, key config.APIKey) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "upsert-key:"+key.Code)
	return nil
}

func (p *fakePersistence) DeleteAPIKey(_ context.Context, code string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "delete-key:"+code)
	return nil
}

func (p *fakePersistence) SetAPIKeyEnabled(_ context.Context, code string, enabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, fmt.Sprintf("key-enabled:%s:%t", code, enabled))
	return nil
}

func (p *fakePersistence) RecordInboundEvent(_ context.Context, event MessageEvent) (MessageEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "inbound")
	p.inboundEvents = append(p.inboundEvents, event)
	return event, p.inboundErr
}

func (p *fakePersistence) RecordOutboundEvent(_ context.Context, event MessageEvent) (MessageEvent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "outbound")
	p.outboundEvents = append(p.outboundEvents, event)
	return event, nil
}

func (p *fakePersistence) RecordModuleActivity(_ context.Context, activity ModuleActivity) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "module:"+activity.Kind)
	p.moduleActivities = append(p.moduleActivities, activity)
	return nil
}

func (p *fakePersistence) RecordModuleContacts(_ context.Context, snapshot ModuleContactSnapshotRequest) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, "contacts")
	p.contactSnapshots = append(p.contactSnapshots, snapshot)
	return nil
}

func (p *fakePersistence) activitySnapshot() ([]MessageEvent, []ModuleActivity) {
	p.mu.Lock()
	defer p.mu.Unlock()
	outbound := append([]MessageEvent(nil), p.outboundEvents...)
	activities := append([]ModuleActivity(nil), p.moduleActivities...)
	return outbound, activities
}

type sseRecorder struct {
	mu     sync.Mutex
	header http.Header
	body   bytes.Buffer
	status int
}

func newSSERecorder() *sseRecorder {
	return &sseRecorder{header: make(http.Header)}
}

func (r *sseRecorder) Header() http.Header {
	return r.header
}

func (r *sseRecorder) Write(value []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(value)
}

func (r *sseRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = status
	}
}

func (r *sseRecorder) Flush() {}

func (r *sseRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.body.String()
}

type fakeAdminReader struct {
	keys              []APIKeyView
	events            []StoredEventView
	messages          []StoredEventView
	modules           []ModuleStatusView
	contacts          []ModuleContactView
	calls             []string
	lastMessageFilter MessageFilter
	lastContactFilter ModuleContactFilter
}

type fakeMetricsAdminReader struct {
	*fakeAdminReader
	databaseBytes int64
	metricsErr    error
}

func (r *fakeMetricsAdminReader) DatabaseSizeBytes(context.Context) (int64, error) {
	return r.databaseBytes, r.metricsErr
}

type fakeEventTailReader struct {
	*fakeAdminReader
	latest int64
	events []MessageEvent
}

func (r *fakeEventTailReader) LatestLiveEventID(context.Context) (int64, error) {
	return r.latest, nil
}

func (r *fakeEventTailReader) ListLiveEventsAfter(_ context.Context, afterID int64, device string, _ int) ([]MessageEvent, error) {
	out := make([]MessageEvent, 0, len(r.events))
	for _, event := range r.events {
		if event.Sequence > afterID && (device == "" || event.Device == device) {
			out = append(out, event)
		}
	}
	return out, nil
}

func (r *fakeAdminReader) ListAPIKeys(_ context.Context, limit int) ([]APIKeyView, error) {
	r.calls = append(r.calls, "keys:"+strconv.Itoa(limit))
	return r.keys, nil
}

func (r *fakeAdminReader) ListStoredEvents(_ context.Context, limit int) ([]StoredEventView, error) {
	r.calls = append(r.calls, "events:"+strconv.Itoa(limit))
	return r.events, nil
}

func (r *fakeAdminReader) ListMessages(_ context.Context, filter MessageFilter) ([]StoredEventView, error) {
	r.lastMessageFilter = filter
	r.calls = append(r.calls, "messages:"+filter.Device+":"+filter.WxID+":"+strconv.Itoa(filter.Limit))
	return r.messages, nil
}

func (r *fakeAdminReader) ListModuleStatuses(_ context.Context) ([]ModuleStatusView, error) {
	r.calls = append(r.calls, "modules")
	return r.modules, nil
}

func (r *fakeAdminReader) ListModuleContacts(_ context.Context, filter ModuleContactFilter) ([]ModuleContactView, error) {
	r.lastContactFilter = filter
	r.calls = append(r.calls, "contacts:"+filter.Device+":"+filter.Query+":"+strconv.Itoa(filter.Limit))
	return r.contacts, nil
}

type fakeOutbox struct {
	nextID int64
	items  []ModuleOutboxItem
}

func (o *fakeOutbox) EnqueueReply(_ context.Context, action ReplyAction) (ModuleOutboxItem, error) {
	o.nextID++
	item := ModuleOutboxItem{
		ID:        o.nextID,
		Device:    action.Device,
		OwnerWxID: action.OwnerWxID,
		WxID:      action.WxID,
		Text:      action.Text,
		Status:    "pending",
	}
	o.items = append(o.items, item)
	return item, nil
}

func (o *fakeOutbox) PollReplyActions(_ context.Context, req ModulePollRequest) ([]ModuleOutboxItem, error) {
	limit := req.Limit
	if limit <= 0 || limit > len(o.items) {
		limit = len(o.items)
	}
	out := []ModuleOutboxItem{}
	for i := range o.items {
		if len(out) >= limit {
			break
		}
		if o.items[i].Device != req.Device {
			continue
		}
		if req.WxID != "" && o.items[i].OwnerWxID != "" && o.items[i].OwnerWxID != req.WxID {
			continue
		}
		if o.items[i].Status != "pending" {
			continue
		}
		o.items[i].Status = "leased"
		o.items[i].AttemptCount++
		out = append(out, o.items[i])
	}
	return out, nil
}

func (o *fakeOutbox) AckReplyActions(_ context.Context, req ModuleAckRequest) ([]ModuleOutboxItem, error) {
	byID := map[int64]ModuleAckItem{}
	for _, ack := range req.Items {
		byID[ack.ID] = ack
	}
	out := []ModuleOutboxItem{}
	for i := range o.items {
		ack, ok := byID[o.items[i].ID]
		if !ok || o.items[i].Device != req.Device {
			continue
		}
		if req.WxID != "" && o.items[i].OwnerWxID != "" && o.items[i].OwnerWxID != req.WxID {
			continue
		}
		o.items[i].Status = ack.Status
		o.items[i].LastError = ack.Error
		o.items[i].ChatRecordID = ack.ChatRecordID
		out = append(out, o.items[i])
	}
	return out, nil
}

func dialTestWebSocket(t *testing.T, serverURL string, path string) *wsConn {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	request := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n", path, parsed.Host, key)
	if _, err := conn.Write([]byte(request)); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if !strings.Contains(status, "101") {
		_ = conn.Close()
		t.Fatalf("unexpected websocket status: %s", status)
	}
	accept := ""
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			t.Fatal(err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(name), "Sec-WebSocket-Accept") {
			accept = strings.TrimSpace(value)
		}
	}
	if accept != websocketAccept(key) {
		_ = conn.Close()
		t.Fatalf("unexpected websocket accept %q", accept)
	}
	return &wsConn{
		conn: conn,
		rw:   bufio.NewReadWriter(reader, bufio.NewWriter(conn)),
	}
}

func readTestWSMessage(t *testing.T, conn *wsConn) outboxWSMessage {
	t.Helper()
	if err := conn.conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for {
		payload, op, err := conn.readFrame()
		if err != nil {
			t.Fatal(err)
		}
		switch op {
		case wsOpText:
			var msg outboxWSMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				t.Fatal(err)
			}
			return msg
		case wsOpPing:
			if !conn.writeControl(wsOpPong, payload) {
				t.Fatal("failed to write pong")
			}
		}
	}
}

func readTestWSMessageOfType(t *testing.T, conn *wsConn, typ string) outboxWSMessage {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		msg := readTestWSMessage(t, conn)
		if msg.Type == typ {
			return msg
		}
	}
	t.Fatalf("websocket message type %q was not received", typ)
	return outboxWSMessage{}
}
