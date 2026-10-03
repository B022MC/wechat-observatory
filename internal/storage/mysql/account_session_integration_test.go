package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"wechat-observatory/internal/bridge"
	"wechat-observatory/internal/config"
)

func TestAccountSessionsMySQLIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(offlineOutboxIntegrationDSNEnv))
	if dsn == "" {
		t.Skip("set " + offlineOutboxIntegrationDSNEnv + " to a disposable test database")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	first, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	// A lock-owning operation must reuse its connection for all nested reads/writes.
	for _, store := range []*Store{first, second} {
		store.db.SetMaxOpenConns(1)
		store.db.SetMaxIdleConns(1)
	}
	var database string
	if err := first.db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(database), "test") {
		t.Fatal("integration requires a test database")
	}
	if err := first.ApplyMigrations(ctx); err != nil {
		t.Fatal(err)
	}
	device := fmt.Sprintf("account_it_%d", time.Now().UnixNano())
	key := device + "_key"
	if _, err := first.db.ExecContext(ctx, "INSERT INTO bridge_api_keys (code,credential_id,device,nickname) VALUES (?,?,?,?)", key, key, device, device); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		for _, table := range []string{"bridge_module_account_history", "bridge_module_account_current", "bridge_module_outbox", "bridge_module_contacts", "bridge_message_events", "bridge_module_runtime", "bridge_device_session_lease", "bridge_module_installations", "bridge_module_switch_requests", "bridge_api_keys"} {
			if _, err := first.db.ExecContext(cleanup, "DELETE FROM "+table+" WHERE device=?", device); err != nil {
				t.Error(err)
			}
		}
		_, _ = first.db.ExecContext(cleanup, "DELETE FROM bridge_devices WHERE name=?", device)
	}()
	makeService := func(store *Store) *bridge.Service {
		return bridge.NewService(bridge.Config{DefaultDevice: device}, bridge.WithPersistence(store), bridge.WithOutbox(store))
	}
	s1, s2 := makeService(first), makeService(second)
	reg := func(owner string, generation int64) bridge.ModuleRegistrationRequest {
		return bridge.ModuleRegistrationRequest{APIKey: key, WxID: owner, InstanceID: "installation-00001", AccountSession: fmt.Sprintf("session-%016d", generation), AccountGeneration: generation, Nickname: owner}
	}
	register := func(s *bridge.Service, r bridge.ModuleRegistrationRequest) {
		t.Helper()
		if _, err := s.RegisterModule(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	snapshot := func(s *bridge.Service, r bridge.ModuleRegistrationRequest, contacts []bridge.ModuleContact) error {
		_, err := s.RecordModuleContacts(ctx, bridge.ModuleContactSnapshotRequest{APIKey: key, WxID: r.WxID, AccountSession: r.AccountSession, Complete: true, Contacts: contacts})
		return err
	}
	a, b, a3 := reg("wxid_A", 1), reg("wxid_B", 2), reg("wxid_A", 3)
	register(s1, a)
	if err := snapshot(s1, a, []bridge.ModuleContact{{WxID: "shared", Remark: "A remark"}, {WxID: "only_A"}}); err != nil {
		t.Fatal(err)
	}
	generation := a.AccountGeneration
	send := bridge.SendTextRequest{Device: device, OwnerWxID: a.WxID, AccountGeneration: &generation, WxIDs: []string{"shared"}, Text: "queued"}
	leasedID, err := s1.SendText(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := s1.AcquireOutboxSession(ctx, key, device, a.WxID, a.AccountSession)
	if err != nil {
		t.Fatal(err)
	}
	s1.ReleaseOutboxSession(ctx, lease)
	items, err := s1.PollOutbox(ctx, bridge.ModulePollRequest{APIKey: key, WxID: a.WxID, AccountSession: a.AccountSession})
	if err != nil || len(items) != 1 {
		t.Fatalf("poll: %+v %v", items, err)
	}
	if _, err := s1.SendText(ctx, send); err != nil {
		t.Fatal(err)
	}
	register(s2, b)
	if err := snapshot(s2, b, []bridge.ModuleContact{{WxID: "shared", Remark: "B remark"}, {WxID: "only_B"}}); err != nil {
		t.Fatal(err)
	}
	if err := snapshot(s1, a, []bridge.ModuleContact{{WxID: "shared", Remark: "late overwrite"}}); !errors.Is(err, bridge.ErrAccountSession) {
		t.Fatalf("late contacts: %v", err)
	}
	// A new process still knows historical session owners; retries keep the same key.
	s1 = makeService(first)
	event := bridge.MessageEvent{APIKey: key, AccountSession: a.AccountSession, OwnerWxID: a.WxID, From: "shared", To: a.WxID, ID: "17", Text: "late A", CreateTime: 1789780000}
	for range 2 {
		result, err := s1.Ingest(ctx, event)
		if err != nil || result.PersistenceError != "" {
			t.Fatalf("historical ingest: %+v %v", result, err)
		}
	}
	event.AccountSession = b.AccountSession
	event.OwnerWxID = b.WxID
	event.To = b.WxID
	event.Text = "B"
	if _, err := s2.Ingest(ctx, event); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_message_events WHERE device=?", device).Scan(&count); err != nil || count != 2 {
		t.Fatalf("dedup/owner count %d %v", count, err)
	}
	register(s1, a3)
	if err := snapshot(s1, a3, []bridge.ModuleContact{{WxID: "only_A"}}); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		owner, remark string
		deleted       bool
	}{{a.WxID, "A remark", true}, {b.WxID, "B remark", false}} {
		contacts, err := first.ListModuleContacts(ctx, bridge.ModuleContactFilter{Device: device, OwnerWxID: check.owner, WxID: "shared", IncludeDeleted: true})
		if err != nil || len(contacts) != 1 || contacts[0].Remark != check.remark || contacts[0].Deleted != check.deleted {
			t.Fatalf("shared friend crossed owners: %+v %v", contacts, err)
		}
	}
	if _, err := s2.SendText(ctx, send); !errors.Is(err, bridge.ErrAccountSession) {
		t.Fatalf("stale send accepted: %v", err)
	}
	items, err = s1.PollOutbox(ctx, bridge.ModulePollRequest{APIKey: key, WxID: a3.WxID, AccountSession: a3.AccountSession})
	if err != nil || len(items) != 0 {
		t.Fatalf("cancelled work delivered: %+v %v", items, err)
	}
	acked, err := s1.AckOutbox(ctx, bridge.ModuleAckRequest{APIKey: key, WxID: a3.WxID, AccountSession: a3.AccountSession, Items: []bridge.ModuleAckItem{{ID: leasedID, Status: "sent"}}})
	if err != nil || len(acked) != 0 {
		t.Fatalf("cancelled work acknowledged: %+v %v", acked, err)
	}
	if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_module_outbox WHERE device=? AND status='cancelled'", device).Scan(&count); err != nil || count != 2 {
		t.Fatalf("cancellation count %d %v", count, err)
	}
	// Fail late in a registration transaction after history/outbox writes; everything rolls back.
	generation = 3
	if _, err := s1.SendText(ctx, send); err != nil {
		t.Fatal(err)
	}
	rollbackSession := "rollback-session-00001"
	err = first.WithDeviceAccountLock(ctx, device, func(locked context.Context) error {
		_, err := first.RegisterAccountBinding(locked, bridge.AccountBinding{Device: device, InstanceID: a.InstanceID, SessionID: rollbackSession, Generation: 4, OwnerWxID: "wxid_B", CredentialID: key, AuthVersion: 1}, config.Device{Name: device, WxID: "wxid_B", Nickname: device, WeChatNickname: strings.Repeat("x", 256)}, key)
		return err
	})
	if err == nil {
		t.Fatal("expected SQL length failure")
	}
	current, found, err := first.CurrentAccountBinding(ctx, device)
	if err != nil || !found || current.Generation != 3 {
		t.Fatalf("partial registration survived: %+v %v", current, err)
	}
	if _, found, err := first.FindAccountBinding(ctx, device, rollbackSession); err != nil || found {
		t.Fatalf("partial history survived: %v %v", found, err)
	}
	if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_module_outbox WHERE device=? AND status='pending'", device).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback lost pending send: %d %v", count, err)
	}
	// Concurrent replicas cannot move the accepted sequence backwards.
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for generation := int64(4); generation <= 23; generation++ {
		wg.Add(1)
		go func(g int64) {
			defer wg.Done()
			service := s1
			if g%2 == 0 {
				service = s2
			}
			_, err := service.RegisterModule(ctx, reg(fmt.Sprintf("wxid_%d", g), g))
			if err != nil && !errors.Is(err, bridge.ErrAccountSession) {
				errs <- err
			}
		}(generation)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	current, _, err = second.CurrentAccountBinding(ctx, device)
	if err != nil || current.Generation != 23 {
		t.Fatalf("concurrent generation: %+v %v", current, err)
	}
	statuses, err := second.ListModuleStatuses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, status := range statuses {
		if status.Device == device {
			found = true
			if status.AccountGeneration != current.Epoch || status.DeviceWxID != current.OwnerWxID {
				t.Fatalf("status scope: %+v", status)
			}
		}
	}
	if !found {
		t.Fatal("missing module status")
	}
	// Credential revocation also invalidates durable historical sessions after restart.
	if _, err := first.db.ExecContext(ctx, "UPDATE bridge_api_keys SET auth_version=auth_version+1 WHERE code=?", key); err != nil {
		t.Fatal(err)
	}
	if _, err := makeService(second).Ingest(ctx, event); !errors.Is(err, bridge.ErrAccountSession) {
		t.Fatalf("revoked history accepted: %v", err)
	}
}
