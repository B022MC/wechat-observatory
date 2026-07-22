package mysql

import (
	"strings"
	"testing"

	"wechat-observatory/internal/bridge"
)

func TestStoreImplementsBridgePersistence(t *testing.T) {
	var _ bridge.Persistence = (*Store)(nil)
	var _ bridge.Outbox = (*Store)(nil)
	var _ bridge.AdminReader = (*Store)(nil)
	var _ bridge.EventTailReader = (*Store)(nil)
	var _ bridge.ModuleConfigReader = (*Store)(nil)
	var _ bridge.ModuleSessionLeaser = (*Store)(nil)
}

func TestMigrationsCoverCoreTables(t *testing.T) {
	joined := strings.Join(Migrations(), "\n")
	for _, table := range []string{
		"bridge_api_keys",
		"bridge_devices",
		"bridge_message_events",
		"bridge_module_outbox",
		"bridge_module_runtime",
		"bridge_module_contacts",
	} {
		if !strings.Contains(joined, table) {
			t.Fatalf("migration does not include %s", table)
		}
	}
	if !strings.Contains(joined, "enabled BOOLEAN NOT NULL DEFAULT TRUE") {
		t.Fatalf("bridge_api_keys migration should include enabled state: %s", joined)
	}
	for _, want := range []string{"event_key VARCHAR(191) NOT NULL", "bridge_device_session_lease"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("migration missing %q: %s", want, joined)
		}
	}
}

func TestListMessagesQueryExcludesModuleAckEvents(t *testing.T) {
	query, args := listMessagesQuery(bridge.MessageFilter{
		Device: "phone-a",
		WxID:   "wxid_friend",
		Limit:  25,
	})
	if !strings.Contains(query, "raw_provider IS NULL OR raw_provider <> ?") {
		t.Fatalf("message query does not exclude module ack events: %s", query)
	}
	if len(args) != 7 {
		t.Fatalf("unexpected args: %#v", args)
	}
	if args[0] != bridge.RawProviderModuleAck {
		t.Fatalf("first arg should exclude module ack provider, got %#v", args[0])
	}
	if args[1] != "phone-a" {
		t.Fatalf("device arg mismatch: %#v", args)
	}
	for i := 2; i <= 5; i++ {
		if args[i] != "wxid_friend" {
			t.Fatalf("wxid arg %d mismatch: %#v", i, args)
		}
	}
	if args[6] != 25 {
		t.Fatalf("limit arg mismatch: %#v", args)
	}
}

func TestListMessagesQueryFiltersByOwnerWxID(t *testing.T) {
	query, args := listMessagesQuery(bridge.MessageFilter{
		Device:    "phone-a",
		OwnerWxID: "wxid_current",
		Limit:     25,
	})
	if !strings.Contains(query, "owner_wxid = ?") {
		t.Fatalf("message query does not filter by owner_wxid: %s", query)
	}
	if strings.Contains(query, "from_wxid = ? OR to_wxid = ? OR sender_wxid = ?") {
		t.Fatalf("owner filter should not fall back to participant matching: %s", query)
	}
	if len(args) != 4 {
		t.Fatalf("unexpected args: %#v", args)
	}
	if args[0] != bridge.RawProviderModuleAck || args[1] != "phone-a" || args[2] != "wxid_current" || args[3] != 25 {
		t.Fatalf("owner filter args mismatch: %#v", args)
	}
}

func TestMessageEventOwnerBackfillUsesCurrentDeviceWxID(t *testing.T) {
	query := strings.Join(strings.Fields(messageEventOwnerBackfillStatement), " ")
	for _, want := range []string{
		"UPDATE bridge_message_events e",
		"JOIN bridge_devices d ON d.name = e.device",
		"SET e.owner_wxid = d.wxid",
		"e.owner_wxid IS NULL",
		"e.from_wxid = d.wxid OR e.to_wxid = d.wxid OR e.sender_wxid = d.wxid",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("owner backfill query missing %q: %s", want, query)
		}
	}
}

func TestListMessagesQuerySupportsChatRooms(t *testing.T) {
	query, args := listMessagesQuery(bridge.MessageFilter{
		Device:   "phone-a",
		ChatID:   "wxid_room@chatroom",
		ChatKind: string(bridge.ChatKindRoom),
		Limit:    25,
	})
	if !strings.Contains(query, "room_id = ? OR from_wxid = ? OR to_wxid = ?") {
		t.Fatalf("message query does not target chat rooms: %s", query)
	}
	if len(args) != 6 {
		t.Fatalf("unexpected args: %#v", args)
	}
	if args[1] != "phone-a" || args[2] != "wxid_room@chatroom" || args[3] != "wxid_room@chatroom" || args[4] != "wxid_room@chatroom" {
		t.Fatalf("chat room args mismatch: %#v", args)
	}
	if args[5] != 25 {
		t.Fatalf("limit arg mismatch: %#v", args)
	}
}

func TestQuoteIdentifierEscapesBackticks(t *testing.T) {
	if got := quoteIdentifier("wechat`gateway"); got != "`wechat``gateway`" {
		t.Fatalf("unexpected quoted identifier: %s", got)
	}
}
