package bridge

import (
	"strings"
	"testing"
)

func TestCanonicalEventKeyIsStableAndSensitiveToSourceIdentity(t *testing.T) {
	base := MessageEvent{
		ID:           "source-101",
		Device:       "phone-a",
		OwnerWxID:    "wxid_self",
		From:         "wxid_friend",
		To:           "wxid_self",
		Text:         "hello",
		Direction:    DirectionRecv,
		MessageType:  1,
		RawProvider:  "lsposed",
		ChatRecordID: 101,
	}
	if first, second := base.CanonicalEventKey(), base.CanonicalEventKey(); first != second {
		t.Fatalf("canonical key must be stable: %q != %q", first, second)
	}
	changed := base
	changed.ID = "source-102"
	if base.CanonicalEventKey() == changed.CanonicalEventKey() {
		t.Fatal("different source identities must not share an event key")
	}
}

func TestCanonicalEventKeyV2SeparatesReusedLocalIDBySourceTime(t *testing.T) {
	base := MessageEvent{
		ID: "882", EventID: 882, ChatRecordID: 882, Device: "61497f",
		OwnerWxID: "wxid_self", From: "wxid_self", To: "filehelper", Text: "下39",
		Direction: DirectionSent, MessageType: 1, RawProvider: "lsposed", CreateTime: 1_786_530_000,
	}
	first := base.CanonicalEventKeyV2()
	if !IsCanonicalEventKeyV2(first) || !strings.HasPrefix(first, "evt_v2_") {
		t.Fatalf("invalid v2 key: %q", first)
	}
	changed := base
	changed.CreateTime++
	if first == changed.CanonicalEventKeyV2() {
		t.Fatal("reused local ID at a different source time must get a different v2 key")
	}
	if got := (MessageEvent{}).CanonicalEventKeyV2(); got != "" {
		t.Fatalf("missing source time must not invent a v2 identity: %q", got)
	}
}

func TestCanonicalEventKeyIgnoresIngressEventKey(t *testing.T) {
	event := MessageEvent{EventKey: "evt_v2_" + strings.Repeat("a", 64), Device: "phone-a", Text: "x"}
	if event.CanonicalEventKey() == event.EventKey {
		t.Fatal("module-supplied event_key must not select the persisted identity")
	}
}
