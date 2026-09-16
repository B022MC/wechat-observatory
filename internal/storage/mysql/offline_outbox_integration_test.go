package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"wechat-observatory/internal/bridge"
)

const offlineOutboxIntegrationDSNEnv = "WECHAT_OBSERVATORY_MYSQL_TEST_DSN"

func TestOfflineOutboxMySQLIntegration(t *testing.T) {
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
	if _, err := store.db.ExecContext(ctx, `SET timestamp = UNIX_TIMESTAMP('2026-07-28 12:00:00')`); err != nil {
		t.Fatal(err)
	}

	prefix := fmt.Sprintf("offline_it_%d", time.Now().UnixNano())
	exactDevice := prefix + "_exact"
	offlineDevice := prefix + "_offline"
	onlineDevice := prefix + "_online"
	devices := []string{exactDevice, offlineDevice, onlineDevice}
	for _, device := range devices {
		wxid := device + "_wxid"
		apiKey := device + "_key"
		credentialID := "ak_" + device
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO bridge_devices (name, wxid, nickname) VALUES (?, ?, ?)`,
			device, wxid, device); err != nil {
			t.Fatal(err)
		}
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO bridge_api_keys (code, credential_id, device, nickname, enabled)
			VALUES (?, ?, ?, ?, TRUE)`, apiKey, credentialID, device, device); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(devices)), ",")
		args := make([]any, len(devices))
		for index, device := range devices {
			args[index] = device
		}
		for _, statement := range []string{
			`DELETE FROM bridge_module_outbox WHERE device IN (` + placeholders + `)`,
			`DELETE FROM bridge_module_runtime WHERE device IN (` + placeholders + `)`,
			`DELETE FROM bridge_api_keys WHERE device IN (` + placeholders + `)`,
			`DELETE FROM bridge_devices WHERE name IN (` + placeholders + `)`,
		} {
			_, _ = store.db.ExecContext(cleanupCtx, statement, args...)
		}
	})

	seedRuntime := func(device string, ageSeconds int) {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO bridge_module_runtime (
				device, wxid, api_key, last_register_at, updated_at
			) VALUES (
				?, ?, ?, DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL ? SECOND),
				DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL ? SECOND)
			)`, device, device+"_wxid", device+"_key", ageSeconds, ageSeconds); err != nil {
			t.Fatal(err)
		}
	}
	seedRuntime(exactDevice, 300)
	seedRuntime(offlineDevice, 301)
	seedRuntime(onlineDevice, 0)

	exactOnline, err := store.ModuleOnline(ctx, exactDevice, exactDevice+"_wxid", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !exactOnline {
		t.Fatal("activity at the exact five-minute cutoff must remain online")
	}
	offlineOnline, err := store.ModuleOnline(ctx, offlineDevice, offlineDevice+"_wxid", 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if offlineOnline {
		t.Fatal("activity older than five minutes must be offline")
	}

	type seededOutbox struct {
		name         string
		device       string
		status       string
		leaseSQL     string
		attemptCount int
	}
	seeded := []seededOutbox{
		{name: "offline-pending", device: offlineDevice, status: "pending"},
		{name: "offline-expired-lease", device: offlineDevice, status: "leased", leaseSQL: "DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL 1 SECOND)", attemptCount: 1},
		{name: "offline-active-lease", device: offlineDevice, status: "leased", leaseSQL: "DATE_ADD(CURRENT_TIMESTAMP(6), INTERVAL 60 SECOND)", attemptCount: 1},
		{name: "offline-sent", device: offlineDevice, status: "sent"},
		{name: "offline-failed", device: offlineDevice, status: "failed"},
		{name: "offline-already-cancelled", device: offlineDevice, status: "cancelled"},
		{name: "online-pending", device: onlineDevice, status: "pending"},
	}
	ids := make(map[string]int64, len(seeded))
	for _, item := range seeded {
		leaseExpression := "NULL"
		if item.leaseSQL != "" {
			leaseExpression = item.leaseSQL
		}
		result, err := store.db.ExecContext(ctx, fmt.Sprintf(`
			INSERT INTO bridge_module_outbox (
				device, owner_wxid, wxid, text, status, attempt_count, lease_until
			) VALUES (?, ?, ?, ?, ?, ?, %s)`, leaseExpression),
			item.device, item.device+"_wxid", "wxid_friend", item.name, item.status, item.attemptCount)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		ids[item.name] = id
	}

	cancelled, err := store.CancelOfflineOutbox(ctx, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if cancelled != 2 {
		t.Fatalf("cancelled rows=%d want=2", cancelled)
	}

	type outboxState struct {
		status       string
		attemptCount int
		lastError    sql.NullString
		leaseUntil   sql.NullTime
	}
	loadState := func(name string) outboxState {
		t.Helper()
		var state outboxState
		if err := store.db.QueryRowContext(ctx, `
			SELECT status, attempt_count, last_error, lease_until
			FROM bridge_module_outbox WHERE id = ?`, ids[name]).Scan(
			&state.status, &state.attemptCount, &state.lastError, &state.leaseUntil,
		); err != nil {
			t.Fatal(err)
		}
		return state
	}
	for _, name := range []string{"offline-pending", "offline-expired-lease"} {
		state := loadState(name)
		if state.status != "cancelled" || state.lastError.String != offlineOutboxReason || state.leaseUntil.Valid {
			t.Fatalf("%s state=%+v", name, state)
		}
	}
	for name, want := range map[string]string{
		"offline-active-lease":      "leased",
		"offline-sent":              "sent",
		"offline-failed":            "failed",
		"offline-already-cancelled": "cancelled",
		"online-pending":            "pending",
	} {
		if state := loadState(name); state.status != want {
			t.Fatalf("%s status=%q want=%q", name, state.status, want)
		}
	}
	if state := loadState("offline-expired-lease"); state.attemptCount != 1 {
		t.Fatalf("cancelled audit lost attempt count: %+v", state)
	}
	lateAck, err := store.AckReplyActions(ctx, bridge.ModuleAckRequest{
		Device: offlineDevice,
		WxID:   offlineDevice + "_wxid",
		Items: []bridge.ModuleAckItem{{
			ID: ids["offline-expired-lease"], Status: "sent", ChatRecordID: 987654,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lateAck) != 0 {
		t.Fatalf("late ACK changed terminal cancellation: %+v", lateAck)
	}
	if state := loadState("offline-expired-lease"); state.status != "cancelled" || state.lastError.String != offlineOutboxReason {
		t.Fatalf("late ACK overwrote terminal cancellation: %+v", state)
	}

	if _, err := store.db.ExecContext(ctx, `
		UPDATE bridge_module_runtime
		SET last_poll_at = CURRENT_TIMESTAMP(6), updated_at = CURRENT_TIMESTAMP(6)
		WHERE device = ?`, offlineDevice); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE bridge_module_outbox SET status = 'sent', lease_until = NULL WHERE id = ?`,
		ids["offline-active-lease"]); err != nil {
		t.Fatal(err)
	}
	polled, err := store.PollReplyActions(ctx, bridge.ModulePollRequest{
		Device: offlineDevice, WxID: offlineDevice + "_wxid", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(polled) != 0 {
		t.Fatalf("cancelled rows were leased after reconnect: %+v", polled)
	}

	newItem, err := store.EnqueueReply(ctx, bridge.ReplyAction{
		Device: offlineDevice, OwnerWxID: offlineDevice + "_wxid", WxID: "wxid_friend", Text: "new-online-row",
	})
	if err != nil {
		t.Fatal(err)
	}
	polled, err = store.PollReplyActions(ctx, bridge.ModulePollRequest{
		Device: offlineDevice, WxID: offlineDevice + "_wxid", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(polled) != 1 || polled[0].ID != newItem.ID || polled[0].Status != "leased" {
		t.Fatalf("new online row did not use normal delivery flow: %+v", polled)
	}
}
