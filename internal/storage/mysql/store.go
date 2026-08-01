package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	mysqldriver "github.com/go-sql-driver/mysql"

	"wechat-observatory/internal/bridge"
	"wechat-observatory/internal/config"
)

var beijingLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

type Store struct {
	db *sql.DB
}

type Snapshot struct {
	Devices map[string]config.Device
	APIKeys map[string]config.APIKey
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func Open(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := parseMySQLConfig(dsn)
	if err != nil {
		return nil, err
	}
	connector, err := mysqldriver.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func parseMySQLConfig(dsn string) (*mysqldriver.Config, error) {
	cfg, err := mysqldriver.ParseDSN(strings.TrimSpace(dsn))
	if err != nil {
		return nil, err
	}
	cfg.ParseTime = true
	cfg.Loc = beijingLocation
	if cfg.Params == nil {
		cfg.Params = make(map[string]string)
	}
	cfg.Params["time_zone"] = "'+08:00'"
	return cfg, nil
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) ApplyMigrations(ctx context.Context) error {
	for _, statement := range Migrations() {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := s.ensureMessageEventMediaColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureMessageEventOwnerColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureMessageEventEventKey(ctx); err != nil {
		return err
	}
	if err := s.backfillMessageEventOwnerWxID(ctx); err != nil {
		return err
	}
	if err := s.ensureOutboxOwnerColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureModuleContactOwnerKey(ctx); err != nil {
		return err
	}
	if err := s.ensureAPIKeyEnabledColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureAPIKeyCredentialColumns(ctx); err != nil {
		return err
	}
	if err := s.ensureDeviceSessionLeaseTable(ctx); err != nil {
		return err
	}
	if err := s.ensureRetentionIndexes(ctx); err != nil {
		return err
	}
	if err := s.ensureMessageEventDeviceCursorIndex(ctx); err != nil {
		return err
	}
	if err := s.ensureMessageEventModuleStatusIndexes(ctx); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureMessageEventDeviceCursorIndex(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE INDEX idx_bridge_message_events_device_id ON bridge_message_events (device, id)`)
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate key name") {
		return nil
	}
	return err
}

var messageEventModuleStatusIndexStatements = []string{
	`CREATE INDEX idx_bridge_message_events_device_direction_created ON bridge_message_events (device, direction, created_at)`,
	`CREATE INDEX idx_bridge_message_events_device_direction_provider_created ON bridge_message_events (device, direction, raw_provider, created_at)`,
}

func (s *Store) ensureMessageEventModuleStatusIndexes(ctx context.Context) error {
	for _, statement := range messageEventModuleStatusIndexStatements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate key name") {
				continue
			}
			return err
		}
	}
	return nil
}

const messageEventOwnerBackfillStatement = `
	UPDATE bridge_message_events e
	JOIN bridge_devices d ON d.name = e.device
	SET e.owner_wxid = d.wxid
	WHERE (e.owner_wxid IS NULL OR e.owner_wxid = '')
		AND d.wxid IS NOT NULL
		AND d.wxid <> ''
		AND (e.from_wxid = d.wxid OR e.to_wxid = d.wxid OR e.sender_wxid = d.wxid)`

func (s *Store) ensureMessageEventMediaColumns(ctx context.Context) error {
	statements := []string{
		`ALTER TABLE bridge_message_events ADD COLUMN media_kind VARCHAR(32) NULL AFTER message_type`,
		`ALTER TABLE bridge_message_events ADD COLUMN media_mime VARCHAR(128) NULL AFTER media_kind`,
		`ALTER TABLE bridge_message_events ADD COLUMN media_name VARCHAR(255) NULL AFTER media_mime`,
		`ALTER TABLE bridge_message_events ADD COLUMN media_url TEXT NULL AFTER media_name`,
		`ALTER TABLE bridge_message_events ADD COLUMN media_size BIGINT NULL AFTER media_url`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Store) ensureMessageEventOwnerColumns(ctx context.Context) error {
	statements := []string{
		`ALTER TABLE bridge_message_events ADD COLUMN owner_wxid VARCHAR(191) NULL AFTER device`,
		`CREATE INDEX idx_bridge_message_events_owner_time ON bridge_message_events (device, owner_wxid, id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "duplicate key name") {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Store) ensureMessageEventEventKey(ctx context.Context) error {
	statements := []string{
		`ALTER TABLE bridge_message_events ADD COLUMN event_key VARCHAR(191) NULL AFTER id`,
		`UPDATE bridge_message_events SET event_key = CONCAT('legacy_', id) WHERE event_key IS NULL OR event_key = ''`,
		`ALTER TABLE bridge_message_events MODIFY event_key VARCHAR(191) NOT NULL`,
		`CREATE UNIQUE INDEX uniq_bridge_message_events_event_key ON bridge_message_events (event_key)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "duplicate key name") {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Store) backfillMessageEventOwnerWxID(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, messageEventOwnerBackfillStatement)
	return err
}

func (s *Store) ensureOutboxOwnerColumns(ctx context.Context) error {
	statements := []string{
		`ALTER TABLE bridge_module_outbox ADD COLUMN owner_wxid VARCHAR(191) NULL AFTER device`,
		`CREATE INDEX idx_bridge_module_outbox_owner_status ON bridge_module_outbox (device, owner_wxid, status, id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "duplicate key name") {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Store) ensureModuleContactOwnerKey(ctx context.Context) error {
	statements := []string{
		`DELETE c1 FROM bridge_module_contacts c1
			JOIN bridge_module_contacts c2
				ON c1.device = c2.device
				AND COALESCE(c1.owner_wxid, '') = COALESCE(c2.owner_wxid, '')
				AND c1.wxid = c2.wxid
				AND c1.id < c2.id`,
		`ALTER TABLE bridge_module_contacts DROP INDEX uniq_bridge_module_contacts_device_wxid`,
		`ALTER TABLE bridge_module_contacts ADD UNIQUE KEY uniq_bridge_module_contacts_owner_wxid (device, owner_wxid, wxid)`,
		`CREATE INDEX idx_bridge_module_contacts_owner_deleted ON bridge_module_contacts (device, owner_wxid, is_deleted, updated_at)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "can't drop") ||
				strings.Contains(lower, "check that column/key exists") ||
				strings.Contains(lower, "duplicate key name") {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Store) ensureAPIKeyEnabledColumn(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `ALTER TABLE bridge_api_keys ADD COLUMN enabled BOOLEAN NOT NULL DEFAULT TRUE AFTER nickname`)
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
		return nil
	}
	return err
}

func (s *Store) ensureAPIKeyCredentialColumns(ctx context.Context) error {
	statements := []string{
		`ALTER TABLE bridge_api_keys ADD COLUMN credential_id VARCHAR(64) NULL AFTER code`,
		`ALTER TABLE bridge_api_keys ADD COLUMN auth_version BIGINT NOT NULL DEFAULT 1 AFTER credential_id`,
		`UPDATE bridge_api_keys SET credential_id = CONCAT('ak_', REPLACE(UUID(), '-', '')) WHERE credential_id IS NULL OR credential_id = ''`,
		`ALTER TABLE bridge_api_keys MODIFY credential_id VARCHAR(64) NOT NULL`,
		`CREATE UNIQUE INDEX uniq_bridge_api_keys_credential_id ON bridge_api_keys (credential_id)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			lower := strings.ToLower(err.Error())
			if strings.Contains(lower, "duplicate column") || strings.Contains(lower, "duplicate key name") {
				continue
			}
			return err
		}
	}
	return nil
}

func (s *Store) ensureDeviceSessionLeaseTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bridge_device_session_lease (
		device VARCHAR(128) NOT NULL,
		owner_wxid VARCHAR(191) NOT NULL,
		holder_id VARCHAR(191) NOT NULL,
		lease_token VARCHAR(191) NOT NULL,
		lease_until TIMESTAMP(6) NOT NULL,
		updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
		PRIMARY KEY (device, owner_wxid),
		KEY idx_bridge_device_session_lease_until (lease_until)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`)
	return err
}

func (s *Store) ensureRetentionIndexes(ctx context.Context) error {
	statements := []string{
		`CREATE INDEX idx_bridge_message_events_retention ON bridge_message_events (created_at)`,
		`CREATE INDEX idx_bridge_module_outbox_retention ON bridge_module_outbox (status, updated_at)`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate key name") {
				continue
			}
			return err
		}
	}
	return nil
}

func Migrations() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS bridge_api_keys (
			code VARCHAR(128) NOT NULL PRIMARY KEY,
			credential_id VARCHAR(64) NOT NULL,
			auth_version BIGINT NOT NULL DEFAULT 1,
			device VARCHAR(128) NULL,
			nickname VARCHAR(255) NULL,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			UNIQUE KEY uniq_bridge_api_keys_credential_id (credential_id)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS bridge_devices (
			name VARCHAR(128) NOT NULL PRIMARY KEY,
			wxid VARCHAR(191) NOT NULL,
			nickname VARCHAR(255) NOT NULL,
			timeout_ms BIGINT NOT NULL DEFAULT 5000,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			KEY idx_bridge_devices_wxid (wxid)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS bridge_message_events (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			event_key VARCHAR(191) NOT NULL,
			source_id VARCHAR(191) NULL,
			event_id BIGINT NULL,
			chat_record_id BIGINT NULL,
			device VARCHAR(128) NOT NULL,
			owner_wxid VARCHAR(191) NULL,
			direction VARCHAR(16) NOT NULL,
			from_wxid VARCHAR(191) NULL,
			to_wxid VARCHAR(191) NULL,
			room_id VARCHAR(191) NULL,
			sender_wxid VARCHAR(191) NULL,
			text TEXT NOT NULL,
			message_type INT NOT NULL,
			media_kind VARCHAR(32) NULL,
			media_mime VARCHAR(128) NULL,
			media_name VARCHAR(255) NULL,
			media_url TEXT NULL,
			media_size BIGINT NULL,
			raw_provider VARCHAR(64) NULL,
			create_time BIGINT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			KEY idx_bridge_message_events_device_time (device, create_time),
			KEY idx_bridge_message_events_device_id (device, id),
			KEY idx_bridge_message_events_device_direction_created (device, direction, created_at),
			KEY idx_bridge_message_events_device_direction_provider_created (device, direction, raw_provider, created_at),
			KEY idx_bridge_message_events_owner_time (device, owner_wxid, id),
			KEY idx_bridge_message_events_chat_record (chat_record_id),
			KEY idx_bridge_message_events_direction (direction),
			KEY idx_bridge_message_events_retention (created_at),
			UNIQUE KEY uniq_bridge_message_events_event_key (event_key)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS bridge_module_outbox (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			device VARCHAR(128) NOT NULL,
			owner_wxid VARCHAR(191) NULL,
			wxid VARCHAR(191) NOT NULL,
			text TEXT NOT NULL,
			chat_record_id BIGINT NULL,
			status VARCHAR(32) NOT NULL DEFAULT 'pending',
			attempt_count INT NOT NULL DEFAULT 0,
			last_error TEXT NULL,
			lease_until TIMESTAMP NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			KEY idx_bridge_module_outbox_device_status (device, status, id),
			KEY idx_bridge_module_outbox_owner_status (device, owner_wxid, status, id),
			KEY idx_bridge_module_outbox_lease (lease_until),
			KEY idx_bridge_module_outbox_retention (status, updated_at)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS bridge_module_runtime (
			device VARCHAR(128) NOT NULL PRIMARY KEY,
			wxid VARCHAR(191) NULL,
			api_key VARCHAR(128) NULL,
			last_register_at TIMESTAMP NULL,
			last_poll_at TIMESTAMP NULL,
			last_poll_limit INT NOT NULL DEFAULT 0,
			last_poll_item_count INT NOT NULL DEFAULT 0,
			last_ack_at TIMESTAMP NULL,
			last_ack_sent_count INT NOT NULL DEFAULT 0,
			last_ack_failed_count INT NOT NULL DEFAULT 0,
			last_error TEXT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			KEY idx_bridge_module_runtime_wxid (wxid),
			KEY idx_bridge_module_runtime_api_key (api_key)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS bridge_module_contacts (
			id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
			device VARCHAR(128) NOT NULL,
			owner_wxid VARCHAR(191) NULL,
			wxid VARCHAR(191) NOT NULL,
			nickname VARCHAR(255) NULL,
			remark VARCHAR(255) NULL,
			contact_alias VARCHAR(255) NULL,
			contact_type INT NOT NULL DEFAULT 0,
			verify_flag INT NOT NULL DEFAULT 0,
			is_chatroom BOOLEAN NOT NULL DEFAULT FALSE,
			is_deleted BOOLEAN NOT NULL DEFAULT FALSE,
			last_seen_at TIMESTAMP NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			UNIQUE KEY uniq_bridge_module_contacts_owner_wxid (device, owner_wxid, wxid),
			KEY idx_bridge_module_contacts_device_deleted (device, is_deleted, updated_at),
			KEY idx_bridge_module_contacts_owner_deleted (device, owner_wxid, is_deleted, updated_at),
			KEY idx_bridge_module_contacts_wxid (wxid)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
		`CREATE TABLE IF NOT EXISTS bridge_device_session_lease (
			device VARCHAR(128) NOT NULL,
			owner_wxid VARCHAR(191) NOT NULL,
			holder_id VARCHAR(191) NOT NULL,
			lease_token VARCHAR(191) NOT NULL,
			lease_until TIMESTAMP(6) NOT NULL,
			updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6),
			PRIMARY KEY (device, owner_wxid),
			KEY idx_bridge_device_session_lease_until (lease_until)
		) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	}
}

type RetentionCleanup struct {
	MessageEvents  int64
	TerminalOutbox int64
}

func (s *Store) PurgeExpiredHistory(ctx context.Context, retentionDays int) (RetentionCleanup, error) {
	const batchSize = 1000
	messageQuery, outboxQuery, err := retentionQueries(retentionDays)
	if err != nil {
		return RetentionCleanup{}, err
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return RetentionCleanup{}, err
	}
	defer conn.Close()
	var acquired int
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('wechat_observatory_history_retention', 0)`).Scan(&acquired); err != nil {
		return RetentionCleanup{}, err
	}
	if acquired != 1 {
		return RetentionCleanup{}, nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		_ = conn.QueryRowContext(releaseCtx, `SELECT RELEASE_LOCK('wechat_observatory_history_retention')`).Scan(&released)
	}()

	messages, err := purgeBatches(ctx, conn, messageQuery, batchSize)
	if err != nil {
		return RetentionCleanup{}, err
	}
	outbox, err := purgeBatches(ctx, conn, outboxQuery, batchSize)
	if err != nil {
		return RetentionCleanup{MessageEvents: messages}, err
	}
	return RetentionCleanup{MessageEvents: messages, TerminalOutbox: outbox}, nil
}

func retentionQueries(retentionDays int) (string, string, error) {
	if retentionDays <= 0 {
		return "", "", errors.New("retention days must be positive")
	}
	messageQuery := fmt.Sprintf(`DELETE FROM bridge_message_events
		WHERE created_at < DATE_SUB(CURRENT_TIMESTAMP, INTERVAL %d DAY)
		ORDER BY created_at LIMIT ?`, retentionDays)
	outboxQuery := fmt.Sprintf(`DELETE FROM bridge_module_outbox
		WHERE status IN ('sent', 'cancelled')
			AND updated_at < DATE_SUB(CURRENT_TIMESTAMP, INTERVAL %d DAY)
		ORDER BY updated_at LIMIT ?`, retentionDays)
	return messageQuery, outboxQuery, nil
}

type retentionExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func purgeBatches(ctx context.Context, execer retentionExecer, query string, batchSize int) (int64, error) {
	var total int64
	for {
		result, err := execer.ExecContext(ctx, query, batchSize)
		if err != nil {
			return total, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += count
		if count < int64(batchSize) {
			return total, nil
		}
	}
}

func (s *Store) DatabaseSizeBytes(ctx context.Context) (int64, error) {
	var size int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(data_length + index_length), 0)
		FROM information_schema.tables WHERE table_schema = DATABASE()`).Scan(&size)
	return size, err
}

func (s *Store) SeedFromConfig(ctx context.Context, cfg config.Config) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	for _, key := range cfg.APIKeys {
		if err := upsertAPIKey(ctx, tx, key); err != nil {
			return err
		}
	}
	for _, device := range cfg.Devices {
		if err := upsertDevice(ctx, tx, device); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoadSnapshot(ctx context.Context) (Snapshot, error) {
	snapshot := Snapshot{
		Devices: map[string]config.Device{},
		APIKeys: map[string]config.APIKey{},
	}
	if err := s.loadAPIKeys(ctx, snapshot.APIKeys); err != nil {
		return Snapshot{}, err
	}
	if err := s.loadDevices(ctx, snapshot.Devices); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func (s *Store) UpsertAPIKey(ctx context.Context, key config.APIKey) error {
	return upsertAPIKey(ctx, s.db, key)
}

func (s *Store) UpsertDevice(ctx context.Context, device config.Device) error {
	return upsertDevice(ctx, s.db, device)
}

func (s *Store) UpdateDeviceIdentity(ctx context.Context, deviceName string, wxid string, nickname string) error {
	deviceName = strings.TrimSpace(deviceName)
	nickname = firstNonEmpty(strings.TrimSpace(nickname), deviceName)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO bridge_devices (name, wxid, nickname, timeout_ms)
		VALUES (?, ?, ?, 5000)
		ON DUPLICATE KEY UPDATE
			wxid = VALUES(wxid)`,
		deviceName,
		strings.TrimSpace(wxid),
		nickname,
	)
	return err
}

func (s *Store) LookupAPIKey(ctx context.Context, code string) (config.APIKey, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT code, credential_id, auth_version, device, nickname, enabled
		FROM bridge_api_keys
		WHERE code = ?`, strings.TrimSpace(code))
	return scanAPIKey(row)
}

func (s *Store) LookupAPIKeyByCredentialRef(ctx context.Context, credentialRef string) (config.APIKey, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT code, credential_id, auth_version, device, nickname, enabled
		FROM bridge_api_keys
		WHERE credential_id = ?`, strings.TrimSpace(credentialRef))
	return scanAPIKey(row)
}

func scanAPIKey(row interface{ Scan(...any) error }) (config.APIKey, bool, error) {
	var key config.APIKey
	var device, nickname sql.NullString
	var enabled bool
	if err := row.Scan(&key.Code, &key.CredentialID, &key.AuthVersion, &device, &nickname, &enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return config.APIKey{}, false, nil
		}
		return config.APIKey{}, false, err
	}
	key.Device = device.String
	key.Nickname = nickname.String
	key.Disabled = !enabled
	return key, true, nil
}

func (s *Store) LookupDevice(ctx context.Context, name string) (config.Device, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, wxid, nickname, timeout_ms
		FROM bridge_devices
		WHERE name = ?`, strings.TrimSpace(name))
	var device config.Device
	var timeoutMS int64
	if err := row.Scan(&device.Name, &device.WxID, &device.Nickname, &timeoutMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return config.Device{}, false, nil
		}
		return config.Device{}, false, err
	}
	device.Timeout = time.Duration(timeoutMS) * time.Millisecond
	return device, true, nil
}

func (s *Store) ClaimModuleSession(ctx context.Context, lease bridge.ModuleSessionLease) (bool, error) {
	if err := validateModuleSessionLease(lease); err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	var holderID, token string
	var expired bool
	err = tx.QueryRowContext(ctx, `
		SELECT holder_id, lease_token, lease_until < CURRENT_TIMESTAMP(6)
		FROM bridge_device_session_lease
		WHERE device = ? AND owner_wxid = ?
		FOR UPDATE`, lease.Device, lease.OwnerWxID).Scan(&holderID, &token, &expired)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO bridge_device_session_lease (
				device, owner_wxid, holder_id, lease_token, lease_until
			) VALUES (?, ?, ?, ?, DATE_ADD(CURRENT_TIMESTAMP(6), INTERVAL ? MICROSECOND))`,
			lease.Device, lease.OwnerWxID, lease.HolderID, lease.Token, leaseMicroseconds(lease.TTL))
		if err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	if err != nil {
		return false, err
	}
	if !expired && (holderID != lease.HolderID || token != lease.Token) {
		return false, tx.Commit()
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE bridge_device_session_lease
		SET holder_id = ?, lease_token = ?,
			lease_until = DATE_ADD(CURRENT_TIMESTAMP(6), INTERVAL ? MICROSECOND)
		WHERE device = ? AND owner_wxid = ?`,
		lease.HolderID, lease.Token, leaseMicroseconds(lease.TTL), lease.Device, lease.OwnerWxID)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s *Store) RenewModuleSession(ctx context.Context, lease bridge.ModuleSessionLease) (bool, error) {
	if err := validateModuleSessionLease(lease); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE bridge_device_session_lease
		SET lease_until = DATE_ADD(CURRENT_TIMESTAMP(6), INTERVAL ? MICROSECOND)
		WHERE device = ? AND owner_wxid = ? AND holder_id = ? AND lease_token = ?
			AND lease_until >= CURRENT_TIMESTAMP(6)`,
		leaseMicroseconds(lease.TTL), lease.Device, lease.OwnerWxID, lease.HolderID, lease.Token)
	if err != nil {
		return false, err
	}
	updated, err := result.RowsAffected()
	return updated == 1, err
}

func (s *Store) ReleaseModuleSession(ctx context.Context, lease bridge.ModuleSessionLease) error {
	if err := validateModuleSessionLease(lease); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM bridge_device_session_lease
		WHERE device = ? AND owner_wxid = ? AND holder_id = ? AND lease_token = ?`,
		lease.Device, lease.OwnerWxID, lease.HolderID, lease.Token)
	return err
}

func validateModuleSessionLease(lease bridge.ModuleSessionLease) error {
	if strings.TrimSpace(lease.Device) == "" || strings.TrimSpace(lease.OwnerWxID) == "" ||
		strings.TrimSpace(lease.HolderID) == "" || strings.TrimSpace(lease.Token) == "" {
		return errors.New("module session lease device, owner_wxid, holder_id, and token are required")
	}
	if lease.TTL <= 0 {
		return errors.New("module session lease ttl must be positive")
	}
	return nil
}

func leaseMicroseconds(ttl time.Duration) int64 {
	if ttl <= 0 {
		return int64(time.Second / time.Microsecond)
	}
	return int64(ttl / time.Microsecond)
}

func (s *Store) LookupDeviceByWxID(ctx context.Context, wxid string) (config.Device, bool, error) {
	wxid = strings.TrimSpace(wxid)
	if wxid == "" {
		return config.Device{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT d.name, d.wxid, d.nickname, d.timeout_ms
		FROM bridge_devices d
		WHERE d.wxid = ?
		ORDER BY
			EXISTS(
				SELECT 1
				FROM bridge_module_contacts c
				WHERE c.device = d.name
					AND c.owner_wxid = d.wxid
					AND c.is_deleted = 0
			) DESC,
			EXISTS(
				SELECT 1
				FROM bridge_message_events m
				WHERE m.device = d.name
					AND m.owner_wxid = d.wxid
			) DESC,
			d.updated_at DESC,
			d.name ASC
		LIMIT 1`,
		wxid)
	var device config.Device
	var timeoutMS int64
	if err := row.Scan(&device.Name, &device.WxID, &device.Nickname, &timeoutMS); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return config.Device{}, false, nil
		}
		return config.Device{}, false, err
	}
	device.Timeout = time.Duration(timeoutMS) * time.Millisecond
	return device, true, nil
}

func (s *Store) RecordInboundEvent(ctx context.Context, event bridge.MessageEvent) (bridge.MessageEvent, error) {
	return s.recordMessageEvent(ctx, event)
}

func (s *Store) RecordOutboundEvent(ctx context.Context, event bridge.MessageEvent) (bridge.MessageEvent, error) {
	return s.recordMessageEvent(ctx, event)
}

func (s *Store) RecordModuleActivity(ctx context.Context, activity bridge.ModuleActivity) error {
	switch strings.TrimSpace(activity.Kind) {
	case "register":
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO bridge_module_runtime (
				device, wxid, api_key, last_register_at, last_error
			) VALUES (?, ?, ?, CURRENT_TIMESTAMP, NULL)
			ON DUPLICATE KEY UPDATE
				wxid = VALUES(wxid),
				api_key = VALUES(api_key),
				last_register_at = CURRENT_TIMESTAMP,
				last_error = NULL`,
			strings.TrimSpace(activity.Device),
			nullString(activity.WxID),
			nullString(activity.APIKey),
		)
		return err
	case "poll":
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO bridge_module_runtime (
				device, wxid, last_poll_at, last_poll_limit, last_poll_item_count
			) VALUES (?, ?, CURRENT_TIMESTAMP, ?, ?)
			ON DUPLICATE KEY UPDATE
				wxid = COALESCE(VALUES(wxid), wxid),
				last_poll_at = CURRENT_TIMESTAMP,
				last_poll_limit = VALUES(last_poll_limit),
				last_poll_item_count = VALUES(last_poll_item_count)`,
			strings.TrimSpace(activity.Device),
			nullString(activity.WxID),
			activity.PollLimit,
			activity.PollItemCount,
		)
		return err
	case "ack":
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO bridge_module_runtime (
				device, last_ack_at, last_ack_sent_count, last_ack_failed_count, last_error
			) VALUES (?, CURRENT_TIMESTAMP, ?, ?, ?)
			ON DUPLICATE KEY UPDATE
				last_ack_at = CURRENT_TIMESTAMP,
				last_ack_sent_count = VALUES(last_ack_sent_count),
				last_ack_failed_count = VALUES(last_ack_failed_count),
				last_error = VALUES(last_error)`,
			strings.TrimSpace(activity.Device),
			activity.AckSentCount,
			activity.AckFailedCount,
			nullString(activity.LastError),
		)
		return err
	default:
		return nil
	}
}

func (s *Store) RecordModuleContacts(ctx context.Context, snapshot bridge.ModuleContactSnapshotRequest) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	device := strings.TrimSpace(snapshot.Device)
	if snapshot.Complete {
		query := `
			UPDATE bridge_module_contacts
			SET is_deleted = TRUE
			WHERE device = ?`
		args := []any{device}
		if ownerWxID := strings.TrimSpace(snapshot.WxID); ownerWxID != "" {
			query += ` AND owner_wxid = ?`
			args = append(args, ownerWxID)
		}
		if _, err := tx.ExecContext(ctx, query, args...); err != nil {
			return err
		}
	}
	for _, contact := range snapshot.Contacts {
		if strings.TrimSpace(contact.WxID) == "" {
			continue
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO bridge_module_contacts (
				device, owner_wxid, wxid, nickname, remark, contact_alias, contact_type,
				verify_flag, is_chatroom, is_deleted, last_seen_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
			ON DUPLICATE KEY UPDATE
				owner_wxid = VALUES(owner_wxid),
				nickname = VALUES(nickname),
				remark = VALUES(remark),
				contact_alias = VALUES(contact_alias),
				contact_type = VALUES(contact_type),
				verify_flag = VALUES(verify_flag),
				is_chatroom = VALUES(is_chatroom),
				is_deleted = VALUES(is_deleted),
				last_seen_at = CURRENT_TIMESTAMP`,
			device,
			nullString(snapshot.WxID),
			strings.TrimSpace(contact.WxID),
			nullString(contact.Nickname),
			nullString(contact.Remark),
			nullString(contact.Alias),
			contact.Type,
			contact.VerifyFlag,
			contact.Chatroom,
			contact.Deleted,
		)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

const (
	offlineOutboxBatchSize = 1000
	offlineOutboxLockName  = "wechat_observatory_offline_outbox"
	offlineOutboxReason    = "device offline"
)

func (s *Store) EnqueueReply(ctx context.Context, action bridge.ReplyAction) (bridge.ModuleOutboxItem, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO bridge_module_outbox (device, owner_wxid, wxid, text, chat_record_id, status)
		VALUES (?, ?, ?, ?, ?, 'pending')`,
		strings.TrimSpace(action.Device),
		nullString(action.OwnerWxID),
		strings.TrimSpace(action.WxID),
		action.Text,
		nullInt64(action.ChatRecordID),
	)
	if err != nil {
		return bridge.ModuleOutboxItem{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return bridge.ModuleOutboxItem{}, err
	}
	return s.findOutboxItem(ctx, id)
}

const moduleOnlineStatement = `
	SELECT EXISTS (
		SELECT 1
		FROM bridge_module_runtime rt
		JOIN bridge_devices d ON d.name = rt.device AND d.wxid = rt.wxid
		JOIN bridge_api_keys ak ON ak.device = rt.device AND ak.code = rt.api_key AND ak.enabled = TRUE
		WHERE rt.device = ? AND rt.wxid = ?
			AND rt.updated_at >= DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL ? MICROSECOND)
	)`

func (s *Store) ModuleOnline(ctx context.Context, device string, ownerWxID string, offlineAfter time.Duration) (bool, error) {
	device = strings.TrimSpace(device)
	ownerWxID = strings.TrimSpace(ownerWxID)
	if device == "" || ownerWxID == "" || offlineAfter <= 0 {
		return false, nil
	}
	var online bool
	err := s.db.QueryRowContext(ctx, moduleOnlineStatement, device, ownerWxID, leaseMicroseconds(offlineAfter)).Scan(&online)
	return online, err
}

func (s *Store) CancelOfflineOutbox(ctx context.Context, offlineAfter time.Duration) (int64, error) {
	if offlineAfter <= 0 {
		return 0, errors.New("module offline duration must be positive")
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	var acquired int
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 0)`, offlineOutboxLockName).Scan(&acquired); err != nil {
		return 0, err
	}
	if acquired != 1 {
		return 0, nil
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var released sql.NullInt64
		_ = conn.QueryRowContext(releaseCtx, `SELECT RELEASE_LOCK(?)`, offlineOutboxLockName).Scan(&released)
	}()

	offlineMicros := leaseMicroseconds(offlineAfter)
	total := int64(0)
	for {
		rows, err := conn.QueryContext(ctx, selectOfflineOutboxIDsStatement, offlineMicros, offlineOutboxBatchSize)
		if err != nil {
			return total, err
		}
		ids := make([]int64, 0, offlineOutboxBatchSize)
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return total, err
			}
			ids = append(ids, id)
		}
		if err := rows.Close(); err != nil {
			return total, err
		}
		if err := rows.Err(); err != nil {
			return total, err
		}
		if len(ids) == 0 {
			return total, nil
		}

		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		query := fmt.Sprintf(cancelOfflineOutboxStatement, placeholders)
		args := make([]any, 0, len(ids)+2)
		args = append(args, offlineOutboxReason)
		for _, id := range ids {
			args = append(args, id)
		}
		args = append(args, offlineMicros)
		result, err := conn.ExecContext(ctx, query, args...)
		if err != nil {
			return total, err
		}
		cancelled, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += cancelled
		if len(ids) < offlineOutboxBatchSize {
			return total, nil
		}
	}
}

const selectOfflineOutboxIDsStatement = `
	SELECT o.id
	FROM bridge_module_outbox o
	LEFT JOIN bridge_module_runtime rt ON rt.device = o.device
	WHERE (
			o.status = 'pending'
			OR (o.status = 'leased' AND (o.lease_until IS NULL OR o.lease_until < CURRENT_TIMESTAMP(6)))
		)
		AND (rt.updated_at IS NULL OR rt.updated_at < DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL ? MICROSECOND))
	ORDER BY o.id
	LIMIT ?`

const cancelOfflineOutboxStatement = `
	UPDATE bridge_module_outbox o
	LEFT JOIN bridge_module_runtime rt ON rt.device = o.device
	SET o.status = 'cancelled', o.last_error = ?, o.lease_until = NULL
	WHERE o.id IN (%s)
		AND (
			o.status = 'pending'
			OR (o.status = 'leased' AND (o.lease_until IS NULL OR o.lease_until < CURRENT_TIMESTAMP(6)))
		)
		AND (rt.updated_at IS NULL OR rt.updated_at < DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL ? MICROSECOND))`

func (s *Store) PollReplyActions(ctx context.Context, req bridge.ModulePollRequest) ([]bridge.ModuleOutboxItem, error) {
	limit := normalizeLimit(req.Limit)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	rows, err := tx.QueryContext(ctx, `
		SELECT id
		FROM bridge_module_outbox
		WHERE device = ?
			AND owner_wxid = ?
			AND (
				status = 'pending'
				OR (status = 'leased' AND (lease_until IS NULL OR lease_until < CURRENT_TIMESTAMP))
			)
		ORDER BY id ASC
		LIMIT ? FOR UPDATE`,
		strings.TrimSpace(req.Device),
		strings.TrimSpace(req.WxID),
		limit,
	)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, leaseOutboxItemStatement, id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []bridge.ModuleOutboxItem{}, nil
	}
	return s.listOutboxItems(ctx, ids)
}

const leaseOutboxItemStatement = `
	UPDATE bridge_module_outbox
	SET status = 'leased', attempt_count = attempt_count + 1,
		lease_until = DATE_ADD(CURRENT_TIMESTAMP, INTERVAL 60 SECOND)
	WHERE id = ?`

const ackOutboxItemStatement = `
	UPDATE bridge_module_outbox
	SET status = ?, last_error = ?, chat_record_id = COALESCE(?, chat_record_id), lease_until = NULL
	WHERE id = ? AND device = ? AND (? = '' OR owner_wxid = ?)
		AND status = 'leased'`

func (s *Store) AckReplyActions(ctx context.Context, req bridge.ModuleAckRequest) ([]bridge.ModuleOutboxItem, error) {
	ids := make([]int64, 0, len(req.Items))
	for _, item := range req.Items {
		result, err := s.db.ExecContext(ctx, ackOutboxItemStatement,
			item.Status,
			nullString(item.Error),
			nullInt64(item.ChatRecordID),
			item.ID,
			strings.TrimSpace(req.Device),
			strings.TrimSpace(req.WxID),
			strings.TrimSpace(req.WxID),
		)
		if err != nil {
			return nil, err
		}
		updated, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if updated == 1 && item.ID > 0 {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) == 0 {
		return []bridge.ModuleOutboxItem{}, nil
	}
	return s.listOutboxItemsForDevice(ctx, ids, req.Device)
}

func (s *Store) findOutboxItem(ctx context.Context, id int64) (bridge.ModuleOutboxItem, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, device, owner_wxid, wxid, text, chat_record_id, status, attempt_count, last_error, created_at, updated_at
		FROM bridge_module_outbox
		WHERE id = ?`,
		id,
	)
	if err != nil {
		return bridge.ModuleOutboxItem{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return bridge.ModuleOutboxItem{}, sql.ErrNoRows
	}
	item, err := scanOutboxItem(rows)
	if err != nil {
		return bridge.ModuleOutboxItem{}, err
	}
	return item, rows.Err()
}

func (s *Store) listOutboxItems(ctx context.Context, ids []int64) ([]bridge.ModuleOutboxItem, error) {
	return s.listOutboxItemsForDevice(ctx, ids, "")
}

func (s *Store) listOutboxItemsForDevice(ctx context.Context, ids []int64, device string) ([]bridge.ModuleOutboxItem, error) {
	ids = positiveIDs(ids)
	if len(ids) == 0 {
		return []bridge.ModuleOutboxItem{}, nil
	}
	placeholders := make([]string, 0, len(ids))
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	deviceFilter := ""
	if device = strings.TrimSpace(device); device != "" {
		deviceFilter = " AND device = ?"
		args = append(args, device)
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, device, owner_wxid, wxid, text, chat_record_id, status, attempt_count, last_error, created_at, updated_at
		FROM bridge_module_outbox
		WHERE id IN (%s)
			%s
		ORDER BY id ASC`, strings.Join(placeholders, ","), deviceFilter), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []bridge.ModuleOutboxItem
	for rows.Next() {
		item, err := scanOutboxItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) recordMessageEvent(ctx context.Context, event bridge.MessageEvent) (bridge.MessageEvent, error) {
	event = event.Normalize()
	event.EventKey = event.CanonicalEventKey()
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO bridge_message_events (
			event_key, source_id, event_id, chat_record_id, device, owner_wxid, direction, from_wxid,
			to_wxid, room_id, sender_wxid, text, message_type, media_kind,
			media_mime, media_name, media_url, media_size, raw_provider, create_time
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id)`,
		event.EventKey,
		nullString(event.ID),
		nullInt64(event.EventID),
		nullInt64(event.ChatRecordID),
		strings.TrimSpace(event.Device),
		nullString(event.OwnerWxID),
		string(event.Direction),
		nullString(event.From),
		nullString(event.To),
		nullString(event.RoomID),
		nullString(event.Sender),
		event.Text,
		event.MessageType,
		nullString(event.MediaKind),
		nullString(event.MediaMime),
		nullString(event.MediaName),
		nullString(event.MediaURL),
		nullInt64(event.MediaSize),
		nullString(event.RawProvider),
		event.Timestamp(),
	)
	if err != nil {
		return bridge.MessageEvent{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return bridge.MessageEvent{}, err
	}
	return s.messageEventByID(ctx, id)
}

func (s *Store) messageEventByID(ctx context.Context, id int64) (bridge.MessageEvent, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, event_key, source_id, event_id, chat_record_id, device, owner_wxid,
			direction, from_wxid, to_wxid, room_id, sender_wxid, text, message_type,
			media_kind, media_mime, media_name, media_url, media_size, raw_provider, create_time
		FROM bridge_message_events
		WHERE id = ?`, id)
	return scanMessageEvent(row)
}

func upsertAPIKey(ctx context.Context, exec sqlExecutor, key config.APIKey) error {
	version := key.AuthVersion
	if version <= 0 {
		version = 1
	}
	_, err := exec.ExecContext(ctx, `
		INSERT INTO bridge_api_keys (code, credential_id, auth_version, device, nickname, enabled)
		VALUES (?, COALESCE(NULLIF(?, ''), CONCAT('ak_', REPLACE(UUID(), '-', ''))), ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			auth_version = CASE
				WHEN NOT (device <=> VALUES(device)) OR enabled <> VALUES(enabled) THEN auth_version + 1
				ELSE auth_version
			END,
			device = VALUES(device),
			nickname = VALUES(nickname),
			enabled = VALUES(enabled)`,
		strings.TrimSpace(key.Code),
		strings.TrimSpace(key.CredentialID),
		version,
		nullString(key.Device),
		nullString(key.Nickname),
		!key.Disabled,
	)
	return err
}

func (s *Store) DeleteAPIKey(ctx context.Context, code string) error {
	code = strings.TrimSpace(code)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE bridge_module_runtime
		SET api_key = NULL,
			last_error = 'api key revoked',
			updated_at = CURRENT_TIMESTAMP
		WHERE api_key = ?`,
		code,
	); err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM bridge_api_keys
		WHERE code = ?`,
		code,
	); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (s *Store) SetAPIKeyEnabled(ctx context.Context, code string, enabled bool) error {
	code = strings.TrimSpace(code)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM bridge_api_keys
		WHERE code = ?`,
		code,
	).Scan(&exists); err != nil {
		_ = tx.Rollback()
		return err
	}
	if exists == 0 {
		_ = tx.Rollback()
		return fmt.Errorf("api key %q not found", code)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE bridge_api_keys
		SET auth_version = auth_version + IF(enabled = ?, 0, 1),
			enabled = ?
		WHERE code = ?`,
		enabled,
		enabled,
		code,
	); err != nil {
		_ = tx.Rollback()
		return err
	}
	if !enabled {
		if _, err := tx.ExecContext(ctx, `
			UPDATE bridge_module_runtime
			SET api_key = NULL,
				last_error = 'api key disabled',
				updated_at = CURRENT_TIMESTAMP
			WHERE api_key = ?`,
			code,
		); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func upsertDevice(ctx context.Context, exec sqlExecutor, device config.Device) error {
	_, err := exec.ExecContext(ctx, `
		INSERT INTO bridge_devices (name, wxid, nickname, timeout_ms)
		VALUES (?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE
			wxid = CASE
				WHEN VALUES(wxid) <> '' THEN VALUES(wxid)
				ELSE wxid
			END,
			nickname = VALUES(nickname),
			timeout_ms = VALUES(timeout_ms)`,
		strings.TrimSpace(device.Name),
		strings.TrimSpace(device.WxID),
		strings.TrimSpace(device.Nickname),
		device.Timeout.Milliseconds(),
	)
	return err
}

func (s *Store) loadAPIKeys(ctx context.Context, out map[string]config.APIKey) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT code, credential_id, auth_version, device, nickname, enabled
		FROM bridge_api_keys`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key config.APIKey
		var device, nickname sql.NullString
		var enabled bool
		if err := rows.Scan(&key.Code, &key.CredentialID, &key.AuthVersion, &device, &nickname, &enabled); err != nil {
			return err
		}
		key.Device = device.String
		key.Nickname = nickname.String
		key.Disabled = !enabled
		out[key.Code] = key
	}
	return rows.Err()
}

func (s *Store) loadDevices(ctx context.Context, out map[string]config.Device) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, wxid, nickname, timeout_ms
		FROM bridge_devices`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var device config.Device
		var timeoutMS int64
		if err := rows.Scan(&device.Name, &device.WxID, &device.Nickname, &timeoutMS); err != nil {
			return err
		}
		device.Timeout = time.Duration(timeoutMS) * time.Millisecond
		out[device.Name] = device
	}
	return rows.Err()
}

type sqlExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

type scanner interface {
	Scan(dest ...any) error
}

func scanOutboxItem(row scanner) (bridge.ModuleOutboxItem, error) {
	var item bridge.ModuleOutboxItem
	var ownerWxID sql.NullString
	var chatRecordID sql.NullInt64
	var lastError sql.NullString
	var createdAt, updatedAt time.Time
	if err := row.Scan(
		&item.ID,
		&item.Device,
		&ownerWxID,
		&item.WxID,
		&item.Text,
		&chatRecordID,
		&item.Status,
		&item.AttemptCount,
		&lastError,
		&createdAt,
		&updatedAt,
	); err != nil {
		return bridge.ModuleOutboxItem{}, err
	}
	item.OwnerWxID = ownerWxID.String
	item.ChatRecordID = chatRecordID.Int64
	item.LastError = lastError.String
	item.CreatedAt = formatTime(createdAt)
	item.UpdatedAt = formatTime(updatedAt)
	return item, nil
}

func nullString(value string) sql.NullString {
	value = strings.TrimSpace(value)
	return sql.NullString{String: value, Valid: value != ""}
}

func nullInt64(value int64) sql.NullInt64 {
	return sql.NullInt64{Int64: value, Valid: value > 0}
}

func positiveIDs(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := map[int64]struct{}{}
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
