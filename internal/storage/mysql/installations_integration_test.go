package mysql

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"wechat-observatory/internal/bridge"
)

// TestMultiPhoneTakeoverMySQLIntegration covers standby, foreground takeover,
// operator switch, session reactivation and the epoch across two replicas.
func TestMultiPhoneTakeoverMySQLIntegration(t *testing.T) {
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
	if err := second.ApplyMigrations(ctx); err != nil {
		t.Fatalf("migrations must be repeatable: %v", err)
	}
	device := fmt.Sprintf("multi_phone_%d", time.Now().UnixNano())
	key := device + "_key"
	if _, err := first.db.ExecContext(ctx, "INSERT INTO bridge_api_keys (code,credential_id,device,nickname) VALUES (?,?,?,?)", key, key, device, device); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		for _, table := range []string{"bridge_module_account_history", "bridge_module_account_current", "bridge_module_installations",
			"bridge_module_switch_requests", "bridge_module_outbox", "bridge_module_runtime", "bridge_device_session_lease", "bridge_api_keys"} {
			if _, err := first.db.ExecContext(cleanup, "DELETE FROM "+table+" WHERE device=?", device); err != nil {
				t.Error(err)
			}
		}
		_, _ = first.db.ExecContext(cleanup, "DELETE FROM bridge_devices WHERE name=?", device)
	}()
	s1 := bridge.NewService(bridge.Config{DefaultDevice: device}, bridge.WithPersistence(first), bridge.WithOutbox(first), bridge.WithAdminReader(first))
	s2 := bridge.NewService(bridge.Config{DefaultDevice: device}, bridge.WithPersistence(second), bridge.WithOutbox(second), bridge.WithAdminReader(second))
	phone := func(instance, owner string, generation int64) bridge.ModuleRegistrationRequest {
		return bridge.ModuleRegistrationRequest{APIKey: key, WxID: owner, Nickname: owner,
			InstanceID:     "installation-" + instance + "-0000",
			AccountSession: fmt.Sprintf("session-%s-%012d", instance, generation), AccountGeneration: generation,
			DeviceModel: "Model " + instance, AndroidVersion: "13", WeChatVersion: "8.0.78", ModuleVersion: "0.1.12"}
	}
	status := func(s *bridge.Service) bridge.ModuleStatusView {
		t.Helper()
		statuses, err := first.ListModuleStatuses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		statuses = s.NormalizeModuleStatuses(statuses)
		if err := s.AttachInstallations(ctx, statuses); err != nil {
			t.Fatal(err)
		}
		for _, item := range statuses {
			if item.Device == device {
				return item
			}
		}
		t.Fatal("missing status")
		return bridge.ModuleStatusView{}
	}

	a := phone("a", "wxid_A", 3)
	if _, err := s1.RegisterModule(ctx, a); err != nil {
		t.Fatal(err)
	}
	epoch := status(s1).AccountGeneration
	send := bridge.SendTextRequest{Device: device, OwnerWxID: a.WxID, AccountGeneration: &epoch, WxIDs: []string{"friend"}, Text: "reply"}
	leasedID, err := s1.SendText(ctx, send)
	if err != nil {
		t.Fatal(err)
	}
	if items, err := s1.PollOutbox(ctx, bridge.ModulePollRequest{APIKey: key, WxID: a.WxID, AccountSession: a.AccountSession}); err != nil || len(items) != 1 {
		t.Fatalf("lease: %+v %v", items, err)
	}
	pendingID, err := s1.SendText(ctx, send)
	if err != nil {
		t.Fatal(err)
	}

	b := phone("b", "wxid_A", 1)
	if _, err := s2.RegisterModule(ctx, b); !errors.Is(err, bridge.ErrDeviceStandby) {
		t.Fatalf("standby: %v", err)
	}
	current := status(s2)
	if len(current.Installations) != 2 || current.Installations[0].State != bridge.InstallationStateActive ||
		current.Installations[1].State != bridge.InstallationStateStandby || current.Installations[1].DeviceModel != "Model b" {
		t.Fatalf("installations: %+v", current.Installations)
	}
	b.Takeover = bridge.TakeoverForeground
	if result, err := s2.RegisterModule(ctx, b); err != nil || result.Takeover != bridge.TakeoverForeground {
		t.Fatalf("foreground takeover: %+v %v", result, err)
	}
	var leasedStatus, pendingStatus string
	if err := first.db.QueryRowContext(ctx, "SELECT status FROM bridge_module_outbox WHERE id=?", leasedID).Scan(&leasedStatus); err != nil {
		t.Fatal(err)
	}
	if err := first.db.QueryRowContext(ctx, "SELECT status FROM bridge_module_outbox WHERE id=?", pendingID).Scan(&pendingStatus); err != nil {
		t.Fatal(err)
	}
	if leasedStatus != "cancelled" || pendingStatus != "pending" {
		t.Fatalf("same-owner takeover outbox: leased=%s pending=%s", leasedStatus, pendingStatus)
	}
	if items, err := s2.PollOutbox(ctx, bridge.ModulePollRequest{APIKey: key, WxID: b.WxID, AccountSession: b.AccountSession}); err != nil || len(items) != 1 || items[0].ID != pendingID {
		t.Fatalf("moved reply: %+v %v", items, err)
	}
	if _, err := s1.SendText(ctx, send); !errors.Is(err, bridge.ErrAccountSession) {
		t.Fatalf("pre-takeover epoch accepted: %v", err)
	}
	if after := status(s1); after.AccountGeneration != epoch+1 || after.Installations[0].DeviceModel != "Model b" {
		t.Fatalf("epoch after takeover: %+v", after)
	}

	// Operator switch back to phone a reactivates its retried original session.
	if _, err := s1.RegisterModule(ctx, a); !errors.Is(err, bridge.ErrDeviceStandby) {
		t.Fatalf("displaced phone: %v", err)
	}
	standbyID := status(s1).Installations[1].ID
	if _, err := s2.RequestInstallationSwitch(ctx, device, standbyID); err != nil {
		t.Fatal(err)
	}
	if pending := status(s1); pending.SwitchRequest == nil || pending.SwitchRequest.InstallationID != standbyID {
		t.Fatalf("switch request: %+v", pending.SwitchRequest)
	}
	if result, err := s1.RegisterModule(ctx, a); err != nil || result.AccountGeneration != a.AccountGeneration {
		t.Fatalf("reactivate: %+v %v", result, err)
	}
	if done := status(s2); done.SwitchRequest != nil || done.AccountGeneration != epoch+2 || done.Installations[0].ID != standbyID {
		t.Fatalf("after switch: %+v", done)
	}
	var history int
	if err := first.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM bridge_module_account_history WHERE device=?", device).Scan(&history); err != nil || history != 2 {
		t.Fatalf("history rows %d %v", history, err)
	}
	stale := phone("a", "wxid_A", 2)
	stale.Takeover = bridge.TakeoverForeground
	if _, err := s2.RegisterModule(ctx, stale); !errors.Is(err, bridge.ErrAccountSession) {
		t.Fatalf("older generation accepted: %v", err)
	}
	if _, err := s2.RequestInstallationSwitch(ctx, device, standbyID); !errors.Is(err, bridge.ErrInstallationActive) {
		t.Fatalf("switch to active: %v", err)
	}
}
