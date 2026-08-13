package config

import (
	"testing"
	"time"
)

func TestLoadFromEnvParsesRuntimeConfig(t *testing.T) {
	t.Setenv("BRIDGE_HTTP_ADDR", ":8088")
	t.Setenv("BRIDGE_ADMIN_PASSWORD", "admin")
	t.Setenv("BRIDGE_DEVICE_ADMIN_PASSWORD", "device-admin-test")
	t.Setenv("BRIDGE_DEFAULT_DEVICE", "phone-a")
	t.Setenv("BRIDGE_DEVICES", "phone-a||wechat-phone|5s")
	t.Setenv("BRIDGE_API_KEYS", "wg_wechat_a|phone-a|WeChat Account")
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")
	t.Setenv("BRIDGE_HISTORY_RETENTION_DAYS", "21")
	t.Setenv("BRIDGE_HISTORY_RETENTION_INTERVAL", "2h")
	t.Setenv("BRIDGE_OUTBOX_POLL_INTERVAL", "4s")
	t.Setenv("BRIDGE_MODULE_OFFLINE_AFTER", "7m")
	t.Setenv("BRIDGE_OFFLINE_OUTBOX_SWEEP_INTERVAL", "45s")
	t.Setenv("BRIDGE_EVENT_IDENTITY_V2_DEVICES", " 61497f;phone-b,61497f ")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys["wg_wechat_a"].Device != "phone-a" {
		t.Fatalf("unexpected api keys: %+v", cfg.APIKeys)
	}
	if cfg.RetentionDays != 21 || cfg.RetentionPoll != 2*time.Hour {
		t.Fatalf("unexpected retention config: days=%d interval=%s", cfg.RetentionDays, cfg.RetentionPoll)
	}
	if cfg.PollInterval != 4*time.Second {
		t.Fatalf("unexpected outbox poll interval: %s", cfg.PollInterval)
	}
	if cfg.ModuleOfflineAfter != 7*time.Minute || cfg.OfflineOutboxSweep != 45*time.Second {
		t.Fatalf("unexpected offline config: after=%s sweep=%s", cfg.ModuleOfflineAfter, cfg.OfflineOutboxSweep)
	}
	if cfg.DeviceAdminPassword != "device-admin-test" {
		t.Fatal("device admin password was not loaded")
	}
	if len(cfg.EventIdentityV2) != 2 {
		t.Fatalf("unexpected v2 identity devices: %+v", cfg.EventIdentityV2)
	}
	if _, ok := cfg.EventIdentityV2["61497f"]; !ok {
		t.Fatal("61497f was not enabled for v2 event identity")
	}
}

func TestEventIdentityV2DevicesDefaultOffAndMatchExactly(t *testing.T) {
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")
	t.Setenv("BRIDGE_EVENT_IDENTITY_V2_DEVICES", "")
	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.EventIdentityV2) != 0 {
		t.Fatalf("v2 identity should default off: %+v", cfg.EventIdentityV2)
	}
	values := parseStringSet("phone-a Phone-A")
	if _, ok := values["phone-a"]; !ok {
		t.Fatal("exact lower-case device was not parsed")
	}
	if _, ok := values["PHONE-A"]; ok {
		t.Fatal("device matching must remain case-sensitive")
	}
}

func TestLoadFromEnvDefaultsOutboxPollIntervalToThreeSeconds(t *testing.T) {
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")
	t.Setenv("BRIDGE_OUTBOX_POLL_INTERVAL", "")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != 3*time.Second {
		t.Fatalf("unexpected outbox poll default: %s", cfg.PollInterval)
	}
}

func TestLoadFromEnvDefaultsHistoryRetentionToFifteenDays(t *testing.T) {
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")
	t.Setenv("BRIDGE_HISTORY_RETENTION_DAYS", "")
	t.Setenv("BRIDGE_HISTORY_RETENTION_INTERVAL", "")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RetentionDays != 15 || cfg.RetentionPoll != time.Hour {
		t.Fatalf("unexpected defaults: days=%d interval=%s", cfg.RetentionDays, cfg.RetentionPoll)
	}
}

func TestLoadFromEnvDefaultsInvalidOfflineDurations(t *testing.T) {
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")
	t.Setenv("BRIDGE_MODULE_OFFLINE_AFTER", "invalid")
	t.Setenv("BRIDGE_OFFLINE_OUTBOX_SWEEP_INTERVAL", "0s")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModuleOfflineAfter != 5*time.Minute || cfg.OfflineOutboxSweep != 30*time.Second {
		t.Fatalf("offline defaults: after=%s sweep=%s", cfg.ModuleOfflineAfter, cfg.OfflineOutboxSweep)
	}
}

func TestLoadFromEnvAllowsDeviceWithoutWxID(t *testing.T) {
	t.Setenv("BRIDGE_HTTP_ADDR", ":8088")
	t.Setenv("BRIDGE_ADMIN_PASSWORD", "admin")
	t.Setenv("BRIDGE_DEFAULT_DEVICE", "phone-a")
	t.Setenv("BRIDGE_DEVICES", "phone-a")
	t.Setenv("BRIDGE_API_KEYS", "wg_wechat_a|phone-a|WeChat Account")
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Devices["phone-a"].WxID != "" {
		t.Fatalf("expected module-owned wxid to start empty, got %+v", cfg.Devices["phone-a"])
	}
}

func TestLoadFromEnvParsesMySQLConfigWithoutRuntimeSeeds(t *testing.T) {
	t.Setenv("BRIDGE_ADMIN_PASSWORD", "admin")
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")
	t.Setenv("BRIDGE_MYSQL_AUTO_MIGRATE", "yes")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.MySQL.Enabled() || cfg.MySQL.DSN == "" || !cfg.MySQL.AutoMigrate {
		t.Fatalf("unexpected mysql config: %+v", cfg.MySQL)
	}
	if len(cfg.Devices) != 0 {
		t.Fatalf("mysql bootstrap should not require env devices, got %+v", cfg.Devices)
	}
}

func TestLoadFromEnvRejectsInvalidMySQLAutoMigrate(t *testing.T) {
	t.Setenv("BRIDGE_ADMIN_PASSWORD", "admin")
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")
	t.Setenv("BRIDGE_MYSQL_AUTO_MIGRATE", "maybe")

	if _, err := LoadFromEnv(); err == nil {
		t.Fatal("expected invalid auto migrate error")
	}
}

func TestLoadFromEnvRequiresMySQL(t *testing.T) {
	t.Setenv("BRIDGE_MYSQL_DSN", "")
	if _, err := LoadFromEnv(); err == nil || err.Error() != "BRIDGE_MYSQL_DSN is required" {
		t.Fatalf("expected required mysql error, got %v", err)
	}
}

func TestLoadFromEnvDefaultsAdminPassword(t *testing.T) {
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminPassword != DefaultAdminPassword {
		t.Fatalf("unexpected default admin password: %q", cfg.AdminPassword)
	}
}

func TestLoadFromEnvLeavesDeviceAdminDisabledByDefault(t *testing.T) {
	t.Setenv("BRIDGE_DEVICE_ADMIN_PASSWORD", "")
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeviceAdminPassword != "" {
		t.Fatal("device admin must remain disabled without explicit configuration")
	}
}

func TestLoadFromEnvRejectsSharedAdminPasswords(t *testing.T) {
	t.Setenv("BRIDGE_ADMIN_PASSWORD", "shared-password")
	t.Setenv("BRIDGE_DEVICE_ADMIN_PASSWORD", "shared-password")
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")

	if _, err := LoadFromEnv(); err == nil || err.Error() != "BRIDGE_DEVICE_ADMIN_PASSWORD must differ from BRIDGE_ADMIN_PASSWORD" {
		t.Fatalf("expected independent password error, got %v", err)
	}
}

func TestLoadFromEnvReadsAdminPassword(t *testing.T) {
	t.Setenv("BRIDGE_ADMIN_PASSWORD", "new-password")
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AdminPassword != "new-password" {
		t.Fatalf("unexpected admin password: %q", cfg.AdminPassword)
	}
}

func TestParseAPIKeysSupportsAPIKeys(t *testing.T) {
	keys, err := parseAPIKeys("wg_a|phone-a|WeChat Account,wg_b")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("expected 2 api keys, got %+v", keys)
	}
	if keys["wg_a"].Device != "phone-a" || keys["wg_a"].Nickname != "WeChat Account" {
		t.Fatalf("unexpected api key: %+v", keys["wg_a"])
	}
	if keys["wg_b"].Device != "" {
		t.Fatalf("unexpected generated-account api key: %+v", keys["wg_b"])
	}
}

func TestParseAPIKeysRejectsMalformedAPIKey(t *testing.T) {
	if _, err := parseAPIKeys("|wechat-a"); err == nil {
		t.Fatal("expected empty api key error")
	}
	if _, err := parseAPIKeys("code|device|nickname|extra"); err == nil {
		t.Fatal("expected api key entries with too many fields to fail")
	}
}
