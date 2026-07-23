package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHTTPAddr      = ":8088"
	DefaultAdminPassword = "change-this-password"
	currentAdminPassEnv  = "BRIDGE_ADMIN_PASSWORD"
)

type Config struct {
	HTTPAddr      string
	AdminPassword string
	DefaultDevice string
	InstanceID    string
	SessionTTL    time.Duration
	PollInterval  time.Duration
	RetentionDays int
	RetentionPoll time.Duration
	Devices       map[string]Device
	APIKeys       map[string]APIKey
	MySQL         MySQLConfig
}

type MySQLConfig struct {
	DSN         string
	AutoMigrate bool
}

func (cfg MySQLConfig) Enabled() bool {
	return strings.TrimSpace(cfg.DSN) != ""
}

type Device struct {
	Name     string
	WxID     string
	Nickname string
	Timeout  time.Duration
}

type APIKey struct {
	Code     string `json:"-"`
	Device   string `json:"device,omitempty"`
	Nickname string `json:"nickname,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

func LoadFromEnv() (Config, error) {
	cfg := Config{
		HTTPAddr:      getenv("BRIDGE_HTTP_ADDR", defaultHTTPAddr),
		AdminPassword: adminPasswordFromEnv(),
		DefaultDevice: strings.TrimSpace(os.Getenv("BRIDGE_DEFAULT_DEVICE")),
		InstanceID:    instanceID(),
		SessionTTL:    getenvDuration("BRIDGE_DEVICE_SESSION_LEASE_TTL", 15*time.Second),
		PollInterval:  getenvDuration("BRIDGE_OUTBOX_POLL_INTERVAL", time.Second),
		RetentionDays: getenvPositiveInt("BRIDGE_HISTORY_RETENTION_DAYS", 15),
		RetentionPoll: getenvDuration("BRIDGE_HISTORY_RETENTION_INTERVAL", time.Hour),
		Devices:       map[string]Device{},
		APIKeys:       map[string]APIKey{},
		MySQL: MySQLConfig{
			DSN: strings.TrimSpace(os.Getenv("BRIDGE_MYSQL_DSN")),
		},
	}

	if err := validateListenAddr("BRIDGE_HTTP_ADDR", cfg.HTTPAddr); err != nil {
		return Config{}, err
	}

	autoMigrate, err := getenvBool("BRIDGE_MYSQL_AUTO_MIGRATE", false)
	if err != nil {
		return Config{}, err
	}
	cfg.MySQL.AutoMigrate = autoMigrate

	devices, err := parseDevices(os.Getenv("BRIDGE_DEVICES"))
	if err != nil {
		return Config{}, err
	}
	cfg.Devices = devices

	apiKeys, err := parseAPIKeys(os.Getenv("BRIDGE_API_KEYS"))
	if err != nil {
		return Config{}, err
	}
	cfg.APIKeys = apiKeys

	if !cfg.MySQL.Enabled() {
		return Config{}, errors.New("BRIDGE_MYSQL_DSN is required")
	}

	return cfg, nil
}

func (cfg *Config) EnsureRuntimeReady() error {
	if len(cfg.Devices) == 0 {
		return errors.New("BRIDGE_DEVICES or MySQL bridge_devices is required")
	}
	if cfg.DefaultDevice == "" {
		for name := range cfg.Devices {
			cfg.DefaultDevice = name
			break
		}
	}
	if _, ok := cfg.Devices[cfg.DefaultDevice]; !ok {
		return fmt.Errorf("BRIDGE_DEFAULT_DEVICE %q is not present in configured devices", cfg.DefaultDevice)
	}
	return nil
}

func getenv(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func adminPasswordFromEnv() string {
	if value := strings.TrimSpace(os.Getenv(currentAdminPassEnv)); value != "" {
		return value
	}
	return DefaultAdminPassword
}

func getenvBool(name string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := parseBoolFlag(raw)
	if err != nil {
		return false, fmt.Errorf("%s is invalid: %w", name, err)
	}
	return value, nil
}

func getenvDuration(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func getenvPositiveInt(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func instanceID() string {
	if value := strings.TrimSpace(os.Getenv("BRIDGE_INSTANCE_ID")); value != "" {
		return value
	}
	if hostname, err := os.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
		return hostname
	}
	return "wechat-observatory"
}

func validateListenAddr(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s cannot be empty", name)
	}
	if _, _, err := net.SplitHostPort(value); err != nil {
		if strings.HasPrefix(value, ":") {
			if _, err := strconv.Atoi(strings.TrimPrefix(value, ":")); err == nil {
				return nil
			}
		}
		return fmt.Errorf("%s must be a listen address like :8088 or 127.0.0.1:8088: %w", name, err)
	}
	return nil
}

func parseDevices(raw string) (map[string]Device, error) {
	devices := map[string]Device{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return devices, nil
	}

	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		fields := strings.Split(part, "|")
		if len(fields) < 1 || len(fields) > 4 {
			return nil, fmt.Errorf("invalid device entry %q, want name[|wxid][|nickname][|timeout]", part)
		}

		name := strings.TrimSpace(fields[0])
		wxid := ""
		if len(fields) >= 2 {
			wxid = strings.TrimSpace(fields[1])
		}
		nickname := name
		if len(fields) >= 3 && strings.TrimSpace(fields[2]) != "" {
			nickname = strings.TrimSpace(fields[2])
		}
		timeout := 5 * time.Second
		if len(fields) >= 4 && strings.TrimSpace(fields[3]) != "" {
			parsed, err := time.ParseDuration(strings.TrimSpace(fields[3]))
			if err != nil {
				return nil, fmt.Errorf("invalid timeout for device %q: %w", name, err)
			}
			timeout = parsed
		}

		if name == "" {
			return nil, fmt.Errorf("device entry %q has empty name", part)
		}
		devices[name] = Device{
			Name:     name,
			WxID:     wxid,
			Nickname: nickname,
			Timeout:  timeout,
		}
	}

	return devices, nil
}

func parseAPIKeys(raw string) (map[string]APIKey, error) {
	keys := map[string]APIKey{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return keys, nil
	}

	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		fields := strings.Split(part, "|")
		if len(fields) < 1 || len(fields) > 3 {
			return nil, fmt.Errorf("invalid api key entry %q, want code[|device][|nickname]", part)
		}
		key := APIKey{
			Code: strings.TrimSpace(fields[0]),
		}
		if key.Code == "" {
			return nil, fmt.Errorf("invalid api key %q: code is required", part)
		}
		if len(fields) >= 2 {
			key.Device = strings.TrimSpace(fields[1])
		}
		if len(fields) >= 3 {
			key.Nickname = strings.TrimSpace(fields[2])
		}
		keys[key.Code] = key
	}
	return keys, nil
}

func parseBoolFlag(raw string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("want true/false or 1/0, got %q", raw)
	}
}
