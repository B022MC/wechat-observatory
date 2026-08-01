package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"wechat-observatory/internal/bridge"
)

func TestStoreImplementsBridgePersistence(t *testing.T) {
	var _ bridge.Persistence = (*Store)(nil)
	var _ bridge.Outbox = (*Store)(nil)
	var _ bridge.AdminReader = (*Store)(nil)
	var _ bridge.EventTailReader = (*Store)(nil)
	var _ bridge.ModuleConfigReader = (*Store)(nil)
	var _ bridge.APIKeyCredentialReader = (*Store)(nil)
	var _ bridge.ModuleSessionLeaser = (*Store)(nil)
	var _ bridge.ModuleLivenessChecker = (*Store)(nil)
}

func TestContactLimitCanCoverCompleteModuleSnapshot(t *testing.T) {
	if got := normalizeLimit(10000); got != 500 {
		t.Fatalf("shared read limit = %d, want 500", got)
	}
	if got := normalizeLimitUpTo(10000, 10000); got != 10000 {
		t.Fatalf("contact read limit = %d, want 10000", got)
	}
	if got := normalizeLimitUpTo(10001, 10000); got != 10000 {
		t.Fatalf("capped contact read limit = %d, want 10000", got)
	}
}

func TestMySQLConfigUsesBeijingTime(t *testing.T) {
	cfg, err := parseMySQLConfig("wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true&loc=UTC")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ParseTime {
		t.Fatal("MySQL time parsing must stay enabled")
	}
	if cfg.Loc.String() != "Asia/Shanghai" {
		t.Fatalf("MySQL location = %q, want Asia/Shanghai", cfg.Loc)
	}
	_, offset := time.Now().In(cfg.Loc).Zone()
	if offset != 8*60*60 {
		t.Fatalf("MySQL location offset = %d, want +08:00", offset)
	}
	if cfg.Params["time_zone"] != "'+08:00'" {
		t.Fatalf("MySQL session time_zone = %q, want '+08:00'", cfg.Params["time_zone"])
	}
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
	for _, want := range []string{
		"credential_id VARCHAR(64) NOT NULL",
		"auth_version BIGINT NOT NULL DEFAULT 1",
		"uniq_bridge_api_keys_credential_id",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bridge_api_keys migration should include %q: %s", want, joined)
		}
	}
	for _, want := range []string{"event_key VARCHAR(191) NOT NULL", "bridge_device_session_lease"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("migration missing %q: %s", want, joined)
		}
	}
	for _, want := range []string{
		"idx_bridge_message_events_retention",
		"idx_bridge_module_outbox_retention",
		"idx_bridge_message_events_device_id",
		"idx_bridge_message_events_device_direction_created",
		"idx_bridge_message_events_device_direction_provider_created",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("migration missing index %q", want)
		}
	}
}

func TestModuleStatusQueryUsesBoundedLatestEventLookups(t *testing.T) {
	baseQuery := strings.Join(strings.Fields(listModuleStatusesStatement), " ")
	if strings.Contains(baseQuery, "bridge_message_events") {
		t.Fatalf("base module status query still reads message history: %s", baseQuery)
	}
	query := strings.Join(strings.Fields(latestModuleEventTimesStatement), " ")
	for _, want := range []string{
		"WHERE latest_event.device = ? ORDER BY latest_event.create_time DESC LIMIT 1",
		"WHERE inbound.device = ? AND inbound.direction = 'recv' ORDER BY inbound.created_at DESC LIMIT 1",
		"outbound.direction = 'sent'",
		"outbound.raw_provider = ?",
		"ORDER BY outbound.created_at DESC LIMIT 1",
	} {
		if !strings.Contains(query, want) {
			t.Fatalf("module status query missing bounded lookup %q: %s", want, query)
		}
	}
	for _, forbidden := range []string{
		"MAX(created_at)",
		"GROUP BY device",
		"MAX(CASE WHEN direction",
	} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("module status query retains global message aggregate %q: %s", forbidden, query)
		}
	}
}

func TestModuleStatusIndexUpgradeStatementsMatchQueryFilters(t *testing.T) {
	joined := strings.Join(messageEventModuleStatusIndexStatements, "\n")
	for _, want := range []string{
		"(device, direction, created_at)",
		"(device, direction, raw_provider, created_at)",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("module status index upgrade missing %q: %s", want, joined)
		}
	}
}

func TestRetentionQueriesUseDatabaseClockAndOnlyDeleteTerminalOutbox(t *testing.T) {
	messages, outbox, err := retentionQueries(15)
	if err != nil {
		t.Fatal(err)
	}
	for name, query := range map[string]string{"messages": messages, "outbox": outbox} {
		normalized := strings.Join(strings.Fields(query), " ")
		if !strings.Contains(normalized, "DATE_SUB(CURRENT_TIMESTAMP, INTERVAL 15 DAY)") || !strings.Contains(normalized, "LIMIT ?") {
			t.Fatalf("%s retention query does not use bounded database time: %s", name, normalized)
		}
	}
	if !strings.Contains(outbox, "status IN ('sent', 'cancelled')") {
		t.Fatalf("outbox cleanup must include only terminal sent/cancelled rows: %s", outbox)
	}
	for _, forbidden := range []string{"'pending'", "'leased'", "'failed'"} {
		if strings.Contains(outbox, forbidden) {
			t.Fatalf("outbox retention must keep %s rows: %s", forbidden, outbox)
		}
	}
	if _, _, err := retentionQueries(0); err == nil {
		t.Fatal("zero retention days should fail")
	}
}

func TestModuleOnlineAndOfflineCancellationUseDatabaseClock(t *testing.T) {
	online := strings.Join(strings.Fields(moduleOnlineStatement), " ")
	for _, want := range []string{
		"rt.updated_at >= DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL ? MICROSECOND)",
		"d.wxid = rt.wxid",
		"ak.code = rt.api_key",
		"ak.enabled = TRUE",
	} {
		if !strings.Contains(online, want) {
			t.Fatalf("online query missing %q: %s", want, online)
		}
	}

	selection := strings.Join(strings.Fields(selectOfflineOutboxIDsStatement), " ")
	for _, want := range []string{
		"o.status = 'pending'",
		"o.status = 'leased'",
		"o.lease_until < CURRENT_TIMESTAMP(6)",
		"rt.updated_at < DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL ? MICROSECOND)",
		"LIMIT ?",
	} {
		if !strings.Contains(selection, want) {
			t.Fatalf("offline selection missing %q: %s", want, selection)
		}
	}

	cancel := strings.Join(strings.Fields(fmt.Sprintf(cancelOfflineOutboxStatement, "?,?")), " ")
	for _, want := range []string{
		"o.status = 'cancelled'",
		"o.lease_until = NULL",
		"o.lease_until < CURRENT_TIMESTAMP(6)",
		"rt.updated_at < DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL ? MICROSECOND)",
	} {
		if !strings.Contains(cancel, want) {
			t.Fatalf("offline cancellation missing %q: %s", want, cancel)
		}
	}
	for _, forbidden := range []string{"o.status = 'sent'", "o.status = 'failed'"} {
		if strings.Contains(cancel, forbidden) {
			t.Fatalf("offline cancellation must preserve %s rows: %s", forbidden, cancel)
		}
	}
}

func TestAckUpdatesOnlyLeasedOutboxRows(t *testing.T) {
	query := strings.Join(strings.Fields(ackOutboxItemStatement), " ")
	if !strings.Contains(query, "AND status = 'leased'") {
		t.Fatalf("ACK must transition only currently leased rows: %s", query)
	}
}

func TestOutboxLeaseUsesMySQLClock(t *testing.T) {
	query := strings.Join(strings.Fields(leaseOutboxItemStatement), " ")
	if !strings.Contains(query, "lease_until = DATE_ADD(CURRENT_TIMESTAMP, INTERVAL 60 SECOND)") {
		t.Fatalf("outbox lease must use the MySQL clock: %s", query)
	}
	if strings.Contains(query, "lease_until = ?") {
		t.Fatalf("outbox lease must not use an application-clock timestamp: %s", query)
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

func TestListMessagesQuerySupportsIncrementalRecovery(t *testing.T) {
	query, args := listMessagesQuery(bridge.MessageFilter{
		Device: "phone-a", AfterID: 123, AfterIDSet: true, Limit: 100,
	})
	if !strings.Contains(query, "id > ?") || !strings.Contains(query, "ORDER BY id ASC") {
		t.Fatalf("incremental message query is not cursor ordered: %s", query)
	}
	if len(args) != 4 || args[0] != bridge.RawProviderModuleAck || args[1] != "phone-a" || args[2] != int64(123) || args[3] != 100 {
		t.Fatalf("incremental message args mismatch: %#v", args)
	}
}

func TestListModuleContactsQuerySupportsExactWxID(t *testing.T) {
	query, args := listModuleContactsQuery(bridge.ModuleContactFilter{
		Device: "phone-a", OwnerWxID: "wxid_owner", WxID: "wxid_friend", Limit: 1,
	})
	if !strings.Contains(query, "wxid = ?") || strings.Contains(query, "wxid LIKE ?") {
		t.Fatalf("exact contact query is not indexable: %s", query)
	}
	if len(args) != 4 || args[0] != "phone-a" || args[1] != "wxid_owner" || args[2] != "wxid_friend" || args[3] != 1 {
		t.Fatalf("exact contact args mismatch: %#v", args)
	}
}

type moduleContactExecCall struct {
	query string
	args  []any
}

type recordingModuleContactExecer struct {
	calls  []moduleContactExecCall
	failAt int
}

func (r *recordingModuleContactExecer) ExecContext(_ context.Context, query string, args ...any) (sql.Result, error) {
	r.calls = append(r.calls, moduleContactExecCall{query: query, args: append([]any(nil), args...)})
	if r.failAt > 0 && len(r.calls) == r.failAt {
		return nil, errors.New("planned contact write failure")
	}
	return driver.RowsAffected(1), nil
}

func TestRecordModuleContactsBatchesAndDeletesOnlyMissingOwnerRows(t *testing.T) {
	contacts := make([]bridge.ModuleContact, 0, 1203)
	for index := range 1201 {
		contacts = append(contacts, bridge.ModuleContact{WxID: fmt.Sprintf("wxid-%04d", index), Nickname: "original"})
	}
	contacts = append(contacts,
		bridge.ModuleContact{WxID: "  ", Nickname: "ignored"},
		bridge.ModuleContact{WxID: " wxid-0000 ", Nickname: " final ", Deleted: true},
	)

	execer := &recordingModuleContactExecer{}
	err := recordModuleContacts(context.Background(), execer, bridge.ModuleContactSnapshotRequest{
		Device: " phone-a ", WxID: " owner-a ", Complete: true, Contacts: contacts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(execer.calls) != 4 {
		t.Fatalf("contact statements=%d, want three upserts and one delete", len(execer.calls))
	}
	for index, wantRows := range []int{500, 500, 201} {
		call := execer.calls[index]
		if !strings.Contains(call.query, "INSERT INTO bridge_module_contacts") {
			t.Fatalf("statement %d is not an upsert: %s", index, call.query)
		}
		if len(call.args) != wantRows*10 {
			t.Fatalf("statement %d args=%d, want %d", index, len(call.args), wantRows*10)
		}
		if placeholders := strings.Count(call.query, "?"); placeholders != len(call.args) {
			t.Fatalf("statement %d placeholders=%d args=%d", index, placeholders, len(call.args))
		}
	}
	first := execer.calls[0].args
	if first[0] != "phone-a" || first[1] != (sql.NullString{String: "owner-a", Valid: true}) ||
		first[2] != "wxid-0000" || first[3] != (sql.NullString{String: "final", Valid: true}) || first[9] != true {
		t.Fatalf("deduplicated first contact args=%#v", first[:10])
	}
	deletion := execer.calls[3]
	normalized := strings.Join(strings.Fields(deletion.query), " ")
	for _, want := range []string{"WHERE device = ?", "owner_wxid = ?", "wxid NOT IN", "is_deleted = FALSE"} {
		if !strings.Contains(normalized, want) {
			t.Fatalf("missing-owner deletion does not contain %q: %s", want, normalized)
		}
	}
	if len(deletion.args) != 1203 || deletion.args[0] != "phone-a" || deletion.args[1] != "owner-a" {
		t.Fatalf("deletion args=%d first=%#v", len(deletion.args), deletion.args[:2])
	}
}

func TestRecordModuleContactsPreservesLegacyEmptyOwnerPreDelete(t *testing.T) {
	execer := &recordingModuleContactExecer{}
	err := recordModuleContacts(context.Background(), execer, bridge.ModuleContactSnapshotRequest{
		Device: "phone-a", Complete: true,
		Contacts: []bridge.ModuleContact{{WxID: "filehelper", Nickname: "File Helper"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(execer.calls) != 2 {
		t.Fatalf("contact statements=%d, want pre-delete and upsert", len(execer.calls))
	}
	deletion := strings.Join(strings.Fields(execer.calls[0].query), " ")
	if !strings.Contains(deletion, "UPDATE bridge_module_contacts") || strings.Contains(deletion, "owner_wxid") || strings.Contains(deletion, "NOT IN") {
		t.Fatalf("legacy empty-owner deletion changed scope: %s", deletion)
	}
	if len(execer.calls[0].args) != 1 || execer.calls[0].args[0] != "phone-a" {
		t.Fatalf("legacy deletion args=%#v", execer.calls[0].args)
	}
	if !strings.Contains(execer.calls[1].query, "INSERT INTO bridge_module_contacts") ||
		execer.calls[1].args[1] != (sql.NullString{}) {
		t.Fatalf("empty-owner upsert=%+v", execer.calls[1])
	}
}

func TestRecordModuleContactsPartialAndEmptyCompleteSnapshots(t *testing.T) {
	partial := &recordingModuleContactExecer{}
	if err := recordModuleContacts(context.Background(), partial, bridge.ModuleContactSnapshotRequest{
		Device: "phone-a", WxID: "owner-a",
		Contacts: []bridge.ModuleContact{{WxID: ""}, {WxID: "deleted-contact", Deleted: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(partial.calls) != 1 || !strings.Contains(partial.calls[0].query, "INSERT INTO bridge_module_contacts") {
		t.Fatalf("partial snapshot statements=%+v", partial.calls)
	}
	if len(partial.calls[0].args) != 10 || partial.calls[0].args[9] != true {
		t.Fatalf("explicit deleted contact args=%#v", partial.calls[0].args)
	}

	emptyComplete := &recordingModuleContactExecer{}
	if err := recordModuleContacts(context.Background(), emptyComplete, bridge.ModuleContactSnapshotRequest{
		Device: "phone-a", WxID: "owner-a", Complete: true,
	}); err != nil {
		t.Fatal(err)
	}
	if len(emptyComplete.calls) != 1 {
		t.Fatalf("empty complete statements=%+v", emptyComplete.calls)
	}
	deletion := strings.Join(strings.Fields(emptyComplete.calls[0].query), " ")
	if !strings.Contains(deletion, "owner_wxid = ?") || strings.Contains(deletion, "NOT IN") || !strings.Contains(deletion, "is_deleted = FALSE") {
		t.Fatalf("empty complete deletion=%s", deletion)
	}
}

func TestRecordModuleContactsStopsBeforeDeletionAfterBatchFailure(t *testing.T) {
	contacts := make([]bridge.ModuleContact, 1201)
	for index := range contacts {
		contacts[index].WxID = fmt.Sprintf("wxid-%04d", index)
	}
	execer := &recordingModuleContactExecer{failAt: 2}
	err := recordModuleContacts(context.Background(), execer, bridge.ModuleContactSnapshotRequest{
		Device: "phone-a", WxID: "owner-a", Complete: true, Contacts: contacts,
	})
	if err == nil || !strings.Contains(err.Error(), "planned contact write failure") {
		t.Fatalf("batch failure err=%v", err)
	}
	if len(execer.calls) != 2 {
		t.Fatalf("statements after failed second batch=%d, want 2", len(execer.calls))
	}
	for _, call := range execer.calls {
		if strings.Contains(call.query, "UPDATE bridge_module_contacts") {
			t.Fatalf("missing-contact deletion ran after a failed batch: %s", call.query)
		}
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
