package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func accountRegistration(owner string, generation int64) ModuleRegistrationRequest {
	return ModuleRegistrationRequest{APIKey: testAPIKey, WxID: owner, InstanceID: "installation-00001",
		AccountSession: fmt.Sprintf("session-%016d", generation), AccountGeneration: generation}
}

func TestAccountSessionSwitchAndHistoricalMessage(t *testing.T) {
	s := newTestService("")
	a := accountRegistration("wxid_self", 1)
	b := accountRegistration("wxid_other", 2)
	register := func(req ModuleRegistrationRequest) {
		t.Helper()
		got, err := s.RegisterModule(t.Context(), req)
		if err != nil || got.AccountSession != req.AccountSession || got.AccountGeneration != req.AccountGeneration {
			t.Fatalf("registration: %+v %v", got, err)
		}
	}
	register(a)
	e := MessageEvent{APIKey: testAPIKey, ID: "42", From: "shared-friend", To: a.WxID, OwnerWxID: a.WxID, AccountSession: a.AccountSession, Text: "from A", CreateTime: 1789780000}
	if _, err := s.Ingest(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	before := s.Hub().Recent(1)[0]
	register(b)
	if _, err := s.Ingest(t.Context(), e); err != nil {
		t.Fatal(err)
	}
	after := s.Hub().Recent(1)[0]
	if after.OwnerWxID != a.WxID || before.EventKey != after.EventKey {
		t.Fatalf("historical message changed: %+v %+v", before, after)
	}
	raw, _ := json.Marshal(after)
	if strings.Contains(string(raw), a.AccountSession) || strings.Contains(string(raw), testAPIKey) {
		t.Fatal("transport credentials leaked")
	}
	forged := e
	forged.OwnerWxID = b.WxID
	if _, err := s.Ingest(t.Context(), forged); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("forged source: %v", err)
	}
	forged = e
	forged.AccountSession = b.AccountSession
	if _, err := s.Ingest(t.Context(), forged); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("wrong session: %v", err)
	}
	register(accountRegistration(a.WxID, 3))
	for _, old := range []ModuleRegistrationRequest{a, b, {APIKey: testAPIKey, WxID: a.WxID}} {
		if _, err := s.RegisterModule(t.Context(), old); !errors.Is(err, ErrAccountSession) {
			t.Fatalf("old registration accepted: %v", err)
		}
	}
	reused := accountRegistration("wxid_forged", 3)
	if _, err := s.RegisterModule(t.Context(), reused); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("reused session: %v", err)
	}
	other := accountRegistration(a.WxID, 4)
	other.InstanceID = "installation-00002"
	if _, err := s.RegisterModule(t.Context(), other); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("second installation: %v", err)
	}
	for _, session := range []string{a.AccountSession, b.AccountSession, ""} {
		if _, err := s.PollOutbox(t.Context(), ModulePollRequest{APIKey: testAPIKey, WxID: a.WxID, AccountSession: session}); !errors.Is(err, ErrAccountSession) {
			t.Fatalf("old poll: %v", err)
		}
		if _, err := s.RecordModuleContacts(t.Context(), ModuleContactSnapshotRequest{APIKey: testAPIKey, WxID: a.WxID, AccountSession: session}); !errors.Is(err, ErrAccountSession) {
			t.Fatalf("old contacts: %v", err)
		}
		if _, err := s.AckOutbox(t.Context(), ModuleAckRequest{APIKey: testAPIKey, WxID: a.WxID, AccountSession: session}); !errors.Is(err, ErrAccountSession) {
			t.Fatalf("old ack: %v", err)
		}
	}
}

func TestAccountSwitchCancelsPendingAndLeasedAndRejectsStaleSend(t *testing.T) {
	s := newTestService("")
	a := accountRegistration("wxid_self", 1)
	if _, err := s.RegisterModule(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	req := SendTextRequest{Device: "phone-a", OwnerWxID: a.WxID, AccountGeneration: &a.AccountGeneration, WxIDs: []string{"shared-friend"}, Text: "test"}
	id, err := s.SendText(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.PollOutbox(t.Context(), ModulePollRequest{APIKey: testAPIKey, WxID: a.WxID, AccountSession: a.AccountSession})
	if err != nil || len(items) != 1 || items[0].ID != id {
		t.Fatalf("lease: %+v %v", items, err)
	}
	if _, err := s.SendText(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	for _, reg := range []ModuleRegistrationRequest{accountRegistration("wxid_other", 2), accountRegistration(a.WxID, 3)} {
		if _, err := s.RegisterModule(t.Context(), reg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SendText(t.Context(), req); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("delayed UI send accepted: %v", err)
	}
	current := accountRegistration(a.WxID, 3)
	items, err = s.PollOutbox(t.Context(), ModulePollRequest{APIKey: testAPIKey, WxID: a.WxID, AccountSession: current.AccountSession})
	if err != nil || len(items) != 0 {
		t.Fatalf("old sends resurrected: %+v %v", items, err)
	}
	acked, err := s.AckOutbox(t.Context(), ModuleAckRequest{APIKey: testAPIKey, WxID: a.WxID, AccountSession: current.AccountSession, Items: []ModuleAckItem{{ID: id, Status: "sent"}}})
	if err != nil || len(acked) != 0 {
		t.Fatalf("cancelled ACK resurrected: %+v %v", acked, err)
	}
}

func TestAccountSessionCredentialRevocation(t *testing.T) {
	s := newTestService("")
	a := accountRegistration("wxid_self", 1)
	if _, err := s.RegisterModule(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	key, _, _ := s.lookupAPIKey(t.Context(), testAPIKey)
	key.AuthVersion++
	s.setCachedAPIKey(key)
	if _, err := s.Ingest(t.Context(), MessageEvent{APIKey: testAPIKey, From: "peer", To: a.WxID, OwnerWxID: a.WxID, AccountSession: a.AccountSession, Text: "old"}); !errors.Is(err, ErrAccountSession) {
		t.Fatalf("revoked session accepted: %v", err)
	}
}

func TestAccountSessionHTTPConflict(t *testing.T) {
	s := newTestService("")
	a := accountRegistration("wxid_self", 1)
	if _, err := s.RegisterModule(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	server := &HTTPServer{service: s}
	req := httptest.NewRequest(http.MethodPost, "/module/outbox/poll", strings.NewReader(`{"api_key":"wechat-a-key","wxid":"wxid_self"}`))
	recorder := httptest.NewRecorder()
	server.pollOutbox(recorder, req)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
}
