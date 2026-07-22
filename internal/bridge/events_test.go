package bridge

import "testing"

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
