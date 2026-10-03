package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type testClock struct{ now time.Time }

func (c *testClock) Now() time.Time                 { return c.now }
func (c *testClock) Advance(duration time.Duration) { c.now = c.now.Add(duration) }

func newClockedService(t *testing.T) (*Service, *testClock) {
	t.Helper()
	s := newTestService("")
	clock := &testClock{now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	s.clock = clock.Now
	return s, clock
}

func phoneRegistration(instance, owner string, generation int64) ModuleRegistrationRequest {
	return ModuleRegistrationRequest{APIKey: testAPIKey, WxID: owner, Nickname: owner,
		InstanceID:     fmt.Sprintf("installation-%s-0000", instance),
		AccountSession: fmt.Sprintf("session-%s-%012d", instance, generation), AccountGeneration: generation,
		DeviceModel: "Model " + instance, AndroidVersion: "13", WeChatVersion: "8.0.78", ModuleVersion: "0.1.12"}
}

func moduleStatus(t *testing.T, s *Service, device string) ModuleStatusView {
	t.Helper()
	statuses := s.NormalizeModuleStatuses((&HTTPServer{service: s}).moduleStatusViews())
	if err := s.AttachInstallations(t.Context(), statuses); err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.Device == device {
			return status
		}
	}
	t.Fatalf("missing status for %s", device)
	return ModuleStatusView{}
}

func outboxStatuses(s *Service) map[int64]string {
	out := map[int64]string{}
	memory := s.outbox.(*MemoryOutbox)
	memory.mu.Lock()
	defer memory.mu.Unlock()
	for _, item := range memory.items {
		out[item.ID] = item.Status
	}
	return out
}

func TestForegroundTakeoverKeepsSameOwnerPendingAndCancelsLeased(t *testing.T) {
	s, _ := newClockedService(t)
	phone1 := phoneRegistration("a", "wxid_self", 7)
	if _, err := s.RegisterModule(t.Context(), phone1); err != nil {
		t.Fatal(err)
	}
	epoch := moduleStatus(t, s, "phone-a").AccountGeneration
	send := SendTextRequest{Device: "phone-a", OwnerWxID: "wxid_self", AccountGeneration: &epoch, WxIDs: []string{"friend"}, Text: "reply"}
	leasedID, err := s.SendText(t.Context(), send)
	if err != nil {
		t.Fatal(err)
	}
	if items, err := s.PollOutbox(t.Context(), ModulePollRequest{APIKey: testAPIKey, WxID: phone1.WxID, AccountSession: phone1.AccountSession}); err != nil || len(items) != 1 {
		t.Fatalf("lease: %+v %v", items, err)
	}
	pendingID, err := s.SendText(t.Context(), send)
	if err != nil {
		t.Fatal(err)
	}

	phone2 := phoneRegistration("b", "wxid_self", 1)
	_, err = s.RegisterModule(t.Context(), phone2)
	var standby *StandbyError
	if !errors.As(err, &standby) || standby.Active.DeviceModel != "Model a" || !standby.Active.Active {
		t.Fatalf("second phone should stand by behind phone a: %v %+v", err, standby)
	}
	status := moduleStatus(t, s, "phone-a")
	if len(status.Installations) != 2 || status.Installations[0].State != InstallationStateActive ||
		status.Installations[1].State != InstallationStateStandby || status.Installations[1].DeviceModel != "Model b" {
		t.Fatalf("installations: %+v", status.Installations)
	}

	phone2.Takeover = TakeoverForeground
	result, err := s.RegisterModule(t.Context(), phone2)
	if err != nil || result.Takeover != TakeoverForeground || result.AccountGeneration != 1 {
		t.Fatalf("foreground takeover: %+v %v", result, err)
	}
	statuses := outboxStatuses(s)
	if statuses[leasedID] != "cancelled" || statuses[pendingID] != "pending" {
		t.Fatalf("outbox after same-owner takeover: %+v", statuses)
	}
	items, err := s.PollOutbox(t.Context(), ModulePollRequest{APIKey: testAPIKey, WxID: phone2.WxID, AccountSession: phone2.AccountSession})
	if err != nil || len(items) != 1 || items[0].ID != pendingID {
		t.Fatalf("pending reply should move to the new phone: %+v %v", items, err)
	}
	if _, err := s.PollOutbox(t.Context(), ModulePollRequest{APIKey: testAPIKey, WxID: phone1.WxID, AccountSession: phone1.AccountSession}); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("displaced phone polled: %v", err)
	}
	if _, err := s.RegisterModule(t.Context(), phone1); !errors.Is(err, ErrDeviceStandby) {
		t.Fatalf("displaced phone should stand by: %v", err)
	}
	if _, err := s.SendText(t.Context(), send); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("send with pre-takeover epoch accepted: %v", err)
	}
	after := moduleStatus(t, s, "phone-a")
	if after.AccountGeneration != epoch+1 || !after.Installations[0].Active || after.Installations[0].DeviceModel != "Model b" {
		t.Fatalf("epoch/current after takeover: %+v", after)
	}
}

func TestTakeoverWithAnotherWeChatCancelsAllPending(t *testing.T) {
	s, _ := newClockedService(t)
	if _, err := s.RegisterModule(t.Context(), phoneRegistration("a", "wxid_self", 1)); err != nil {
		t.Fatal(err)
	}
	id, err := s.SendText(t.Context(), SendTextRequest{Device: "phone-a", OwnerWxID: "wxid_self", WxIDs: []string{"friend"}, Text: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	other := phoneRegistration("b", "wxid_other", 1)
	other.Takeover = TakeoverForeground
	if _, err := s.RegisterModule(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if status := outboxStatuses(s)[id]; status != "cancelled" {
		t.Fatalf("other owner's pending reply survived: %s", status)
	}
}

func TestSameInstallationRestartKeepsPendingReplies(t *testing.T) {
	s, _ := newClockedService(t)
	if _, err := s.RegisterModule(t.Context(), phoneRegistration("a", "wxid_self", 1)); err != nil {
		t.Fatal(err)
	}
	id, err := s.SendText(t.Context(), SendTextRequest{Device: "phone-a", OwnerWxID: "wxid_self", WxIDs: []string{"friend"}, Text: "reply"})
	if err != nil {
		t.Fatal(err)
	}
	restarted := phoneRegistration("a", "wxid_self", 2)
	if _, err := s.RegisterModule(t.Context(), restarted); err != nil {
		t.Fatal(err)
	}
	items, err := s.PollOutbox(t.Context(), ModulePollRequest{APIKey: testAPIKey, WxID: restarted.WxID, AccountSession: restarted.AccountSession})
	if err != nil || len(items) != 1 || items[0].ID != id {
		t.Fatalf("restart dropped pending reply: %+v %v", items, err)
	}
}

func TestAdminSwitchRequestHandsOverAtNextHeartbeat(t *testing.T) {
	s, clock := newClockedService(t)
	if _, err := s.RegisterModule(t.Context(), phoneRegistration("a", "wxid_self", 1)); err != nil {
		t.Fatal(err)
	}
	phone2 := phoneRegistration("b", "wxid_self", 1)
	if _, err := s.RegisterModule(t.Context(), phone2); !errors.Is(err, ErrDeviceStandby) {
		t.Fatalf("standby: %v", err)
	}
	status := moduleStatus(t, s, "phone-a")
	activeID, standbyID := status.Installations[0].ID, status.Installations[1].ID
	if _, err := s.RequestInstallationSwitch(t.Context(), "phone-a", activeID); !errors.Is(err, ErrInstallationActive) {
		t.Fatalf("switch to active phone: %v", err)
	}
	if _, err := s.RequestInstallationSwitch(t.Context(), "phone-a", 999); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("unknown installation: %v", err)
	}
	if _, err := s.RequestInstallationSwitch(t.Context(), "phone-b", standbyID); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("installation of another device: %v", err)
	}

	// An expired request is ignored.
	if _, err := s.RequestInstallationSwitch(t.Context(), "phone-a", standbyID); err != nil {
		t.Fatal(err)
	}
	clock.Advance(switchRequestTTL + time.Second)
	if _, err := s.RegisterModule(t.Context(), phoneRegistration("a", "wxid_self", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterModule(t.Context(), phone2); !errors.Is(err, ErrDeviceStandby) {
		t.Fatalf("expired switch honoured: %v", err)
	}

	view, err := s.RequestInstallationSwitch(t.Context(), "phone-a", standbyID)
	if err != nil || view.InstallationID != standbyID {
		t.Fatalf("switch request: %+v %v", view, err)
	}
	if pending := moduleStatus(t, s, "phone-a"); pending.SwitchRequest == nil || !pending.Installations[1].SwitchPending {
		t.Fatalf("pending switch not visible: %+v", pending)
	}
	result, err := s.RegisterModule(t.Context(), phone2)
	if err != nil || result.Takeover != takeoverAdmin {
		t.Fatalf("admin switch: %+v %v", result, err)
	}
	if done := moduleStatus(t, s, "phone-a"); done.SwitchRequest != nil || done.Installations[0].ID != standbyID {
		t.Fatalf("switch not completed: %+v", done)
	}
}

func TestStandbyTakesOverWhenActivePhoneStopsHeartbeating(t *testing.T) {
	s, clock := newClockedService(t)
	phone1 := phoneRegistration("a", "wxid_self", 1)
	if _, err := s.RegisterModule(t.Context(), phone1); err != nil {
		t.Fatal(err)
	}
	phone2 := phoneRegistration("b", "wxid_self", 1)
	clock.Advance(90 * time.Second)
	if _, err := s.RegisterModule(t.Context(), phone1); err != nil { // heartbeat refresh
		t.Fatal(err)
	}
	clock.Advance(90 * time.Second)
	if _, err := s.RegisterModule(t.Context(), phone2); !errors.Is(err, ErrDeviceStandby) {
		t.Fatalf("live active phone replaced: %v", err)
	}
	clock.Advance(DefaultModuleTakeoverAfter)
	result, err := s.RegisterModule(t.Context(), phone2)
	if err != nil || result.Takeover != takeoverStale {
		t.Fatalf("stale takeover: %+v %v", result, err)
	}
	if status := moduleStatus(t, s, "phone-a"); status.Installations[1].State != InstallationStateOffline {
		t.Fatalf("old phone state: %+v", status.Installations)
	}
}

func TestBindingWithoutInstallationRecordGetsOneTakeoverWindow(t *testing.T) {
	s, clock := newClockedService(t)
	if _, err := s.RegisterModule(t.Context(), phoneRegistration("a", "wxid_self", 1)); err != nil {
		t.Fatal(err)
	}
	// Simulate a binding created before installation tracking was deployed.
	s.accountInstallations = map[string]*InstallationRecord{}
	clock.Advance(time.Hour)
	phone2 := phoneRegistration("b", "wxid_self", 1)
	if _, err := s.RegisterModule(t.Context(), phone2); !errors.Is(err, ErrDeviceStandby) {
		t.Fatalf("untracked active phone replaced immediately: %v", err)
	}
	clock.Advance(DefaultModuleTakeoverAfter + time.Second)
	if result, err := s.RegisterModule(t.Context(), phone2); err != nil || result.Takeover != takeoverStale {
		t.Fatalf("untracked phone never expired: %+v %v", result, err)
	}
}

func TestDisplacedOldModuleSessionCanBeReactivatedButNotRewound(t *testing.T) {
	s, _ := newClockedService(t)
	phone1 := phoneRegistration("a", "wxid_self", 5)
	if _, err := s.RegisterModule(t.Context(), phone1); err != nil {
		t.Fatal(err)
	}
	phone2 := phoneRegistration("b", "wxid_self", 1)
	phone2.Takeover = TakeoverForeground
	if _, err := s.RegisterModule(t.Context(), phone2); err != nil {
		t.Fatal(err)
	}
	// An older installation keeps retrying its original session; an operator
	// switch brings that exact session back.
	status := moduleStatus(t, s, "phone-a")
	if _, err := s.RequestInstallationSwitch(t.Context(), "phone-a", status.Installations[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RegisterModule(t.Context(), phone1); err != nil {
		t.Fatalf("reactivate session: %v", err)
	}
	if _, err := s.PollOutbox(t.Context(), ModulePollRequest{APIKey: testAPIKey, WxID: phone1.WxID, AccountSession: phone1.AccountSession}); err != nil {
		t.Fatalf("reactivated session cannot poll: %v", err)
	}
	// A delayed request from an older generation of the same installation, or a
	// new session reusing an accepted generation, never comes back.
	for _, stale := range []ModuleRegistrationRequest{phoneRegistration("a", "wxid_self", 4), {
		APIKey: testAPIKey, WxID: "wxid_self", InstanceID: phone1.InstanceID, AccountSession: "session-a-other-00005", AccountGeneration: 5, Takeover: TakeoverForeground}} {
		if _, err := s.RegisterModule(t.Context(), stale); !errors.Is(err, ErrAccountSession) {
			t.Fatalf("stale generation accepted: %+v %v", stale, err)
		}
	}
	// Even a foreground claim cannot rewind phone b below its accepted generation.
	rewind := phoneRegistration("b", "wxid_self", 0)
	rewind.AccountGeneration = 1
	rewind.AccountSession = "session-b-rewound-0001"
	rewind.Takeover = TakeoverForeground
	if _, err := s.RegisterModule(t.Context(), rewind); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("rewound standby generation accepted: %v", err)
	}
}

func TestRegisterStandbyAndSwitchHTTP(t *testing.T) {
	s, _ := newClockedService(t)
	server := &HTTPServer{service: s}
	post := func(handler http.HandlerFunc, path, body string, setup func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if setup != nil {
			setup(req)
		}
		recorder := httptest.NewRecorder()
		handler(recorder, req)
		return recorder
	}
	encode := func(req ModuleRegistrationRequest) string {
		raw, _ := json.Marshal(req)
		return string(raw)
	}
	if rec := post(server.registerModule, "/module/register", encode(phoneRegistration("a", "wxid_self", 1)), nil); rec.Code != http.StatusOK {
		t.Fatalf("register: %d %s", rec.Code, rec.Body.String())
	}
	rec := post(server.registerModule, "/module/register", encode(phoneRegistration("b", "wxid_self", 1)), nil)
	var body struct {
		Code   string                 `json:"code"`
		Active ModuleInstallationView `json:"active"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusConflict ||
		body.Code != "device_standby" || body.Active.DeviceModel != "Model a" {
		t.Fatalf("standby response: %d %s", rec.Code, rec.Body.String())
	}
	standbyID := moduleStatus(t, s, "phone-a").Installations[1].ID
	withDevice := func(device string) func(*http.Request) {
		return func(req *http.Request) { req.SetPathValue("device", device) }
	}
	if rec := post(server.switchModuleInstallation, "/api/modules/phone-a/switch", fmt.Sprintf(`{"installation_id":%d}`, standbyID), withDevice("phone-a")); rec.Code != http.StatusOK {
		t.Fatalf("switch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(server.switchModuleInstallation, "/api/modules/phone-a/switch", `{"installation_id":12345}`, withDevice("phone-a")); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown switch target: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(server.switchModuleInstallation, "/api/modules/phone-a/switch", `{}`, withDevice("phone-a")); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing switch target: %d %s", rec.Code, rec.Body.String())
	}
}
