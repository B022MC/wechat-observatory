package config

import "testing"

func TestLoadFromEnvParsesRuntimeConfig(t *testing.T) {
	t.Setenv("BRIDGE_HTTP_ADDR", ":8088")
	t.Setenv("BRIDGE_ADMIN_PASSWORD", "admin")
	t.Setenv("BRIDGE_DEFAULT_DEVICE", "phone-a")
	t.Setenv("BRIDGE_DEVICES", "phone-a||wechat-phone|5s")
	t.Setenv("BRIDGE_API_KEYS", "wg_wechat_a|phone-a|WeChat Account")
	t.Setenv("BRIDGE_MYSQL_DSN", "wechat:secret@tcp(db.example:3306)/wechat_observatory?parseTime=true")

	cfg, err := LoadFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.APIKeys) != 1 || cfg.APIKeys["wg_wechat_a"].Device != "phone-a" {
		t.Fatalf("unexpected api keys: %+v", cfg.APIKeys)
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
