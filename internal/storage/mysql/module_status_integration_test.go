package mysql

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"wechat-observatory/internal/bridge"
)

func TestModuleStatusMySQLIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(offlineOutboxIntegrationDSNEnv))
	if dsn == "" {
		t.Skipf("set %s to run the disposable MySQL integration test", offlineOutboxIntegrationDSNEnv)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	store.db.SetMaxOpenConns(1)
	store.db.SetMaxIdleConns(1)

	var database string
	if err := store.db.QueryRowContext(ctx, `SELECT DATABASE()`).Scan(&database); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(database), "test") {
		t.Fatalf("refusing to run integration cleanup against non-test database %q", database)
	}
	if err := store.ApplyMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyMigrations(ctx); err != nil {
		t.Fatalf("repeated migration failed: %v", err)
	}

	for _, index := range []string{
		"idx_bridge_message_events_device_direction_created",
		"idx_bridge_message_events_device_direction_provider_created",
	} {
		var count int
		if err := store.db.QueryRowContext(ctx, `
			SELECT COUNT(*)
			FROM information_schema.statistics
			WHERE table_schema = DATABASE()
				AND table_name = 'bridge_message_events'
				AND index_name = ?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatalf("migration did not create %s", index)
		}
	}

	prefix := fmt.Sprintf("module_status_it_%d", time.Now().UnixNano())
	emptyDevice := prefix + "_empty"
	inboundDevice := prefix + "_inbound"
	ackDevice := prefix + "_ack"
	disabledDevice := prefix + "_disabled"
	devices := []string{emptyDevice, inboundDevice, ackDevice, disabledDevice}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(devices)), ",")
		args := make([]any, len(devices))
		for index, device := range devices {
			args[index] = device
		}
		for _, statement := range []string{
			`DELETE FROM bridge_message_events WHERE device IN (` + placeholders + `)`,
			`DELETE FROM bridge_module_outbox WHERE device IN (` + placeholders + `)`,
			`DELETE FROM bridge_module_runtime WHERE device IN (` + placeholders + `)`,
			`DELETE FROM bridge_api_keys WHERE device IN (` + placeholders + `)`,
			`DELETE FROM bridge_devices WHERE name IN (` + placeholders + `)`,
		} {
			_, _ = store.db.ExecContext(cleanupCtx, statement, args...)
		}
	})

	for _, device := range devices {
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO bridge_devices (name, wxid, nickname) VALUES (?, ?, ?)`,
			device, device+"_wxid", device); err != nil {
			t.Fatal(err)
		}
	}
	insertKey := func(code, device string, enabled bool) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO bridge_api_keys (code, credential_id, device, nickname, enabled)
			VALUES (?, ?, ?, ?, ?)`, code, "ak_"+code, device, device, enabled); err != nil {
			t.Fatal(err)
		}
	}
	insertKey(emptyDevice+"_key", emptyDevice, true)
	insertKey(inboundDevice+"_key", inboundDevice, true)
	insertKey(ackDevice+"_old_key", ackDevice, false)
	insertKey(ackDevice+"_key", ackDevice, true)
	insertKey(disabledDevice+"_key", disabledDevice, false)

	for _, item := range []struct {
		device   string
		code     string
		activity string
	}{
		{device: inboundDevice, code: inboundDevice + "_key", activity: "2026-08-01 10:00:00"},
		{device: ackDevice, code: ackDevice + "_key", activity: "2026-08-01 10:01:00"},
	} {
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO bridge_module_runtime (
				device, wxid, api_key, last_register_at, updated_at
			) VALUES (?, ?, ?, ?, ?)`,
			item.device, item.device+"_wxid", item.code, item.activity, item.activity); err != nil {
			t.Fatal(err)
		}
	}

	insertEvent := func(eventKey, device, direction, provider, createdAt string, createTime int64) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO bridge_message_events (
				event_key, device, owner_wxid, direction, from_wxid, to_wxid,
				text, message_type, raw_provider, create_time, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, 1, NULLIF(?, ''), ?, ?)`,
			eventKey, device, device+"_wxid", direction, "wxid_friend", device+"_wxid",
			eventKey, provider, createTime, createdAt); err != nil {
			t.Fatal(err)
		}
	}
	insertEvent(prefix+"_inbound", inboundDevice, "recv", "lsposed", "2026-08-01 10:02:00", 100)
	insertEvent(prefix+"_ack_inbound", ackDevice, "recv", "lsposed", "2026-08-01 10:03:00", 101)
	insertEvent(prefix+"_ack", ackDevice, "sent", bridge.RawProviderModuleAck, "2026-08-01 10:04:00", 102)

	var messageCount int64
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_message_events`).Scan(&messageCount); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	statuses, err := store.ListModuleStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(startedAt)
	t.Logf("module status read scanned a database with %d messages in %s", messageCount, elapsed)
	if messageCount >= 100000 && elapsed >= 100*time.Millisecond {
		t.Fatalf("production-sized module status read took %s, want <100ms", elapsed)
	}
	byDevice := make(map[string][]bridge.ModuleStatusView)
	for _, status := range statuses {
		if strings.HasPrefix(status.Device, prefix) {
			byDevice[status.Device] = append(byDevice[status.Device], status)
		}
	}
	if rows := byDevice[emptyDevice]; len(rows) != 1 || rows[0].LastEventAt != "" || rows[0].LastInboundAt != "" || rows[0].LastOutboundAckAt != "" {
		t.Fatalf("empty device status=%+v", rows)
	}
	if rows := byDevice[inboundDevice]; len(rows) != 1 || rows[0].LastEventAt == "" || rows[0].LastInboundAt != rows[0].LastEventAt || rows[0].LastOutboundAckAt != "" || !rows[0].Registered {
		t.Fatalf("inbound device status=%+v", rows)
	}
	ackRows := byDevice[ackDevice]
	if len(ackRows) != 2 {
		t.Fatalf("multi-key device rows=%+v", ackRows)
	}
	registered := 0
	for _, status := range ackRows {
		if status.LastEventAt == "" || status.LastInboundAt == "" || status.LastOutboundAckAt == "" {
			t.Fatalf("ACK device lost event timestamps: %+v", status)
		}
		if status.Registered {
			registered++
		}
	}
	if registered != 1 {
		t.Fatalf("multi-key device registered rows=%d statuses=%+v", registered, ackRows)
	}
	if rows := byDevice[disabledDevice]; len(rows) != 1 || rows[0].Enabled || rows[0].Registered {
		t.Fatalf("disabled device status=%+v", rows)
	}
}
