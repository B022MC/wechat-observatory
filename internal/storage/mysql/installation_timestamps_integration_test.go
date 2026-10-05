package mysql

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Aliyun RDS runs with explicit_defaults_for_timestamp=0. A first attempt of
// the multi-phone migration there created bridge_module_installations with an
// implicit ON UPDATE CURRENT_TIMESTAMP on last_seen_at, then failed on the
// switch-request table (error 1067). Re-running the fixed migration must finish
// and strip the implicit ON UPDATE so a state change never moves the heartbeat.
func TestInstallationTimestampsRepairMySQLIntegration(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv(offlineOutboxIntegrationDSNEnv))
	if dsn == "" {
		t.Skip("set " + offlineOutboxIntegrationDSNEnv + " to a disposable test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var database string
	if err := store.db.QueryRowContext(ctx, "SELECT DATABASE()").Scan(&database); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(database), "test") {
		t.Fatal("integration requires a test database")
	}
	// Recreate the half-applied production shape under the legacy timestamp mode.
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, statement := range []string{
		"SET SESSION explicit_defaults_for_timestamp = 0",
		"DROP TABLE IF EXISTS bridge_module_switch_requests",
		"DROP TABLE IF EXISTS bridge_module_installations",
		`CREATE TABLE bridge_module_installations (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			device VARCHAR(128) NOT NULL,
			instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
			last_state VARCHAR(16) NOT NULL DEFAULT 'standby',
			last_seen_at TIMESTAMP(6) NOT NULL,
			last_active_at TIMESTAMP(6) NULL,
			UNIQUE KEY uniq_bridge_module_installation (device, instance_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	if extra := installationLastSeenExtra(t, store); !strings.Contains(strings.ToLower(extra), "on update") {
		t.Skipf("server did not reproduce the implicit ON UPDATE (extra=%q)", extra)
	}
	// Repair the column in place, then drop the abbreviated table and prove the
	// full migration creates a correct one and stays repeatable.
	if err := store.ensureInstallationTimestamps(ctx); err != nil {
		t.Fatal(err)
	}
	if extra := installationLastSeenExtra(t, store); strings.Contains(strings.ToLower(extra), "on update") {
		t.Fatalf("implicit ON UPDATE survived the repair: %q", extra)
	}
	if err := store.ensureInstallationTimestamps(ctx); err != nil {
		t.Fatalf("repair is not idempotent: %v", err)
	}
	if _, err := conn.ExecContext(ctx, "DROP TABLE bridge_module_installations"); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyMigrations(ctx); err != nil {
		t.Fatalf("migration after repair: %v", err)
	}
	if err := store.ApplyMigrations(ctx); err != nil {
		t.Fatalf("migration is not repeatable: %v", err)
	}
	if extra := installationLastSeenExtra(t, store); strings.Contains(strings.ToLower(extra), "on update") {
		t.Fatalf("fresh installations table has an implicit ON UPDATE: %q", extra)
	}
}

func installationLastSeenExtra(t *testing.T, store *Store) string {
	t.Helper()
	var extra string
	if err := store.db.QueryRow(`SELECT COALESCE(EXTRA, '') FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'bridge_module_installations' AND column_name = 'last_seen_at'`).Scan(&extra); err != nil {
		t.Fatal(err)
	}
	return extra
}
