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

func TestModuleContactSnapshotMySQLIntegration(t *testing.T) {
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

	prefix := fmt.Sprintf("contact_it_%d", time.Now().UnixNano())
	device := prefix + "_device"
	rollbackDevice := prefix + "_rollback"
	ownerA := prefix + "_owner_a"
	ownerB := prefix + "_owner_b"
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = store.db.ExecContext(cleanupCtx, `DELETE FROM bridge_module_contacts WHERE device IN (?, ?)`, device, rollbackDevice)
	})

	for _, seed := range []struct {
		owner    string
		wxid     string
		nickname string
		deleted  bool
	}{
		{owner: ownerA, wxid: "keep", nickname: "old", deleted: false},
		{owner: ownerA, wxid: "missing", nickname: "missing", deleted: false},
		{owner: ownerA, wxid: "already-deleted", nickname: "deleted", deleted: true},
		{owner: ownerB, wxid: "other-owner", nickname: "other", deleted: false},
	} {
		if _, err := store.db.ExecContext(ctx, `
			INSERT INTO bridge_module_contacts (
				device, owner_wxid, wxid, nickname, is_deleted, last_seen_at
			) VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
			device, seed.owner, seed.wxid, seed.nickname, seed.deleted); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.RecordModuleContacts(ctx, bridge.ModuleContactSnapshotRequest{
		Device: device, WxID: ownerA, Complete: true,
		Contacts: []bridge.ModuleContact{
			{WxID: "keep", Nickname: "new"},
			{WxID: "new", Nickname: "new contact"},
			{WxID: "explicit-deleted", Nickname: "explicit", Deleted: true},
		},
	}); err != nil {
		t.Fatal(err)
	}

	type contactState struct {
		nickname string
		deleted  bool
	}
	states := map[string]contactState{}
	rows, err := store.db.QueryContext(ctx, `
		SELECT owner_wxid, wxid, COALESCE(nickname, ''), is_deleted
		FROM bridge_module_contacts WHERE device = ?`, device)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var owner, wxid string
		var state contactState
		if err := rows.Scan(&owner, &wxid, &state.nickname, &state.deleted); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		states[owner+"/"+wxid] = state
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]contactState{
		ownerA + "/keep":             {nickname: "new", deleted: false},
		ownerA + "/missing":          {nickname: "missing", deleted: true},
		ownerA + "/already-deleted":  {nickname: "deleted", deleted: true},
		ownerA + "/new":              {nickname: "new contact", deleted: false},
		ownerA + "/explicit-deleted": {nickname: "explicit", deleted: true},
		ownerB + "/other-owner":      {nickname: "other", deleted: false},
	}
	if len(states) != len(want) {
		t.Fatalf("contact states=%+v, want %+v", states, want)
	}
	for key, wantState := range want {
		if got := states[key]; got != wantState {
			t.Fatalf("contact %s=%+v, want %+v", key, got, wantState)
		}
	}

	rollbackContacts := make([]bridge.ModuleContact, 501)
	for index := range 500 {
		rollbackContacts[index].WxID = fmt.Sprintf("rollback-%04d", index)
	}
	rollbackContacts[500].WxID = strings.Repeat("x", 192)
	if err := store.RecordModuleContacts(ctx, bridge.ModuleContactSnapshotRequest{
		Device: rollbackDevice, WxID: ownerA, Contacts: rollbackContacts,
	}); err == nil {
		t.Fatal("oversized second-batch contact should fail")
	}
	var rollbackRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_module_contacts WHERE device = ?`, rollbackDevice).Scan(&rollbackRows); err != nil {
		t.Fatal(err)
	}
	if rollbackRows != 0 {
		t.Fatalf("failed snapshot left %d rows from its first batch", rollbackRows)
	}
}
