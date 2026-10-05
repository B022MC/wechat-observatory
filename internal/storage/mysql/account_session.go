package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"wechat-observatory/internal/bridge"
	"wechat-observatory/internal/config"
)

var _ bridge.AccountSessionStore = (*Store)(nil)

var accountSessionMigrations = []string{
	`CREATE TABLE IF NOT EXISTS bridge_module_account_current (
		device VARCHAR(128) NOT NULL PRIMARY KEY,
		instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		generation BIGINT NOT NULL,
		owner_wxid VARCHAR(191) NOT NULL,
		credential_id VARCHAR(128) NOT NULL,
		auth_version BIGINT NOT NULL,
		epoch BIGINT NOT NULL DEFAULT 0,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS bridge_module_account_history (
		device VARCHAR(128) NOT NULL,
		instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		generation BIGINT NOT NULL,
		owner_wxid VARCHAR(191) NOT NULL,
		credential_id VARCHAR(128) NOT NULL,
		auth_version BIGINT NOT NULL,
		epoch BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (device, session_id),
		UNIQUE KEY uniq_account_generation (device, instance_id, generation)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS bridge_module_installations (
		id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
		device VARCHAR(128) NOT NULL,
		instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		owner_wxid VARCHAR(191) NULL,
		wechat_nickname VARCHAR(255) NULL,
		device_model VARCHAR(128) NULL,
		android_version VARCHAR(64) NULL,
		wechat_version VARCHAR(64) NULL,
		module_version VARCHAR(64) NULL,
		last_state VARCHAR(16) NOT NULL DEFAULT 'standby',
		last_seen_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		last_active_at TIMESTAMP(6) NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		UNIQUE KEY uniq_bridge_module_installation (device, instance_id),
		KEY idx_bridge_module_installation_seen (last_seen_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS bridge_module_switch_requests (
		device VARCHAR(128) NOT NULL PRIMARY KEY,
		target_instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		requested_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		expires_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
}

// Every TIMESTAMP NOT NULL column above carries an explicit DEFAULT. With
// explicit_defaults_for_timestamp=0 (Aliyun RDS ships that), MySQL otherwise
// gives the first such column an implicit ON UPDATE CURRENT_TIMESTAMP and makes
// the next one an invalid zero-date default (error 1067). A table created
// under that mode before this fix is repaired by ensureInstallationTimestamps.

// ensureInstallationTimestamps removes the implicit ON UPDATE clause that
// explicit_defaults_for_timestamp=0 attached to last_seen_at: a heartbeat must
// be written explicitly, never as a side effect of updating last_state.
func (s *Store) ensureInstallationTimestamps(ctx context.Context) error {
	var extra string
	err := s.executor(ctx).QueryRowContext(ctx, `SELECT COALESCE(EXTRA, '') FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'bridge_module_installations' AND column_name = 'last_seen_at'`).Scan(&extra)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(extra), "on update") {
		return nil
	}
	_, err = s.executor(ctx).ExecContext(ctx, `ALTER TABLE bridge_module_installations
		MODIFY COLUMN last_seen_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)`)
	return err
}

// ensureAccountEpochColumns adds the server epoch to bindings created before
// multi-phone takeover. Existing rows start from their generation so browser
// pages that captured account_generation before the upgrade stay valid.
func (s *Store) ensureAccountEpochColumns(ctx context.Context) error {
	statements := []string{
		`ALTER TABLE bridge_module_account_current ADD COLUMN epoch BIGINT NOT NULL DEFAULT 0 AFTER auth_version`,
		`ALTER TABLE bridge_module_account_history ADD COLUMN epoch BIGINT NOT NULL DEFAULT 0 AFTER auth_version`,
		`UPDATE bridge_module_account_current SET epoch = generation WHERE epoch = 0`,
		`UPDATE bridge_module_account_history SET epoch = generation WHERE epoch = 0`,
	}
	for _, statement := range statements {
		if _, err := s.executor(ctx).ExecContext(ctx, statement); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "duplicate column") {
				continue
			}
			return err
		}
	}
	return nil
}

type accountConnKey struct{}
type accountConnection struct {
	store  *Store
	device string
	conn   *sql.Conn
}
type accountExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

func (s *Store) executor(ctx context.Context) accountExecutor {
	if held, ok := ctx.Value(accountConnKey{}).(accountConnection); ok && held.store == s {
		return held.conn
	}
	return s.db
}

// One reserved connection owns both the lock and all nested store calls. Waiting
// callers never hold a connection while asking the pool for a second connection.
func (s *Store) WithDeviceAccountLock(ctx context.Context, device string, fn func(context.Context) error) error {
	if held, ok := ctx.Value(accountConnKey{}).(accountConnection); ok {
		if held.store != s || held.device != device {
			return bridge.ErrAccountSession
		}
		return fn(ctx)
	}
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	digest := sha256.Sum256([]byte(device))
	lock := fmt.Sprintf("account:%x", digest[:28])
	var acquired sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, 10)", lock).Scan(&acquired); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return errors.New("account operation busy; retry")
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		var released sql.NullInt64
		if err := conn.QueryRowContext(releaseCtx, "SELECT RELEASE_LOCK(?)", lock).Scan(&released); err != nil || !released.Valid || released.Int64 != 1 {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()
	return fn(context.WithValue(ctx, accountConnKey{}, accountConnection{s, device, conn}))
}

func scanAccountBinding(row *sql.Row) (bridge.AccountBinding, bool, error) {
	var b bridge.AccountBinding
	err := row.Scan(&b.Device, &b.InstanceID, &b.SessionID, &b.Generation, &b.OwnerWxID, &b.CredentialID, &b.AuthVersion, &b.Epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return b, false, nil
	}
	return b, err == nil, err
}

const accountBindingColumns = "device,instance_id,session_id,generation,owner_wxid,credential_id,auth_version,epoch"

func (s *Store) CurrentAccountBinding(ctx context.Context, device string) (bridge.AccountBinding, bool, error) {
	return scanAccountBinding(s.executor(ctx).QueryRowContext(ctx, "SELECT "+accountBindingColumns+" FROM bridge_module_account_current WHERE device=?", device))
}

func (s *Store) FindAccountBinding(ctx context.Context, device, session string) (bridge.AccountBinding, bool, error) {
	return scanAccountBinding(s.executor(ctx).QueryRowContext(ctx, "SELECT "+accountBindingColumns+" FROM bridge_module_account_history WHERE device=? AND session_id=?", device, session))
}

func (s *Store) MaxInstallationGeneration(ctx context.Context, device, instanceID string) (int64, bool, error) {
	var max sql.NullInt64
	err := s.executor(ctx).QueryRowContext(ctx, `SELECT MAX(generation) FROM bridge_module_account_history
		WHERE device=? AND instance_id=?`, device, instanceID).Scan(&max)
	return max.Int64, max.Valid, err
}

// RegisterAccountBinding stores a binding the service has already accepted
// (including takeovers by another installation) under the device lock. A
// changed binding gets the next device epoch; pending rows of the same WeChat
// owner move to the new binding, while leased and other-owner rows are cancelled.
func (s *Store) RegisterAccountBinding(ctx context.Context, b bridge.AccountBinding, device config.Device, apiKey string) (bridge.AccountBinding, error) {
	held, ok := ctx.Value(accountConnKey{}).(accountConnection)
	if !ok || held.store != s || held.device != b.Device {
		return b, bridge.ErrAccountSession
	}
	previous, exists, err := s.CurrentAccountBinding(ctx, b.Device)
	if err != nil {
		return b, err
	}
	if exists && previous.InstanceID == b.InstanceID && (previous.Generation > b.Generation ||
		(previous.Generation == b.Generation && !previous.SameSession(b))) {
		return b, bridge.ErrAccountSession
	}
	tx, err := s.executor(ctx).BeginTx(ctx, nil)
	if err != nil {
		return b, err
	}
	defer tx.Rollback()
	if exists && previous.SameSession(b) {
		b.Epoch = previous.Epoch
	} else {
		var epoch int64
		if err := tx.QueryRowContext(ctx, `SELECT GREATEST(
			COALESCE((SELECT MAX(epoch) FROM bridge_module_account_current WHERE device=?), 0),
			COALESCE((SELECT MAX(epoch) FROM bridge_module_account_history WHERE device=?), 0))`, b.Device, b.Device).Scan(&epoch); err != nil {
			return b, err
		}
		b.Epoch = epoch + 1
		if _, err := tx.ExecContext(ctx, `UPDATE bridge_module_outbox SET status='cancelled',
			last_error='account session changed',lease_until=NULL
			WHERE device=? AND (status='leased' OR (status='pending' AND NOT (owner_wxid <=> ?)))`, b.Device, b.OwnerWxID); err != nil {
			return b, err
		}
		// A session already in history is being reactivated by its installation
		// (an older module retrying after another phone was current).
		var known int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM bridge_module_account_history
			WHERE device=? AND session_id=?`, b.Device, b.SessionID).Scan(&known); err != nil {
			return b, err
		}
		if known == 0 {
			if _, err := tx.ExecContext(ctx, "INSERT INTO bridge_module_account_history ("+accountBindingColumns+") VALUES (?,?,?,?,?,?,?,?)",
				b.Device, b.InstanceID, b.SessionID, b.Generation, b.OwnerWxID, b.CredentialID, b.AuthVersion, b.Epoch); err != nil {
				return b, err
			}
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bridge_module_account_current ("+accountBindingColumns+") VALUES (?,?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE "+
			"instance_id=VALUES(instance_id),session_id=VALUES(session_id),generation=VALUES(generation),owner_wxid=VALUES(owner_wxid),credential_id=VALUES(credential_id),auth_version=VALUES(auth_version),epoch=VALUES(epoch)",
			b.Device, b.InstanceID, b.SessionID, b.Generation, b.OwnerWxID, b.CredentialID, b.AuthVersion, b.Epoch); err != nil {
			return b, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bridge_devices (name,wxid,nickname,wechat_nickname,timeout_ms)
		VALUES (?,?,?,?,5000) ON DUPLICATE KEY UPDATE wxid=VALUES(wxid),wechat_nickname=VALUES(wechat_nickname)`,
		device.Name, device.WxID, device.Nickname, device.WeChatNickname); err != nil {
		return b, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bridge_module_runtime (device,wxid,api_key,last_register_at)
		VALUES (?,?,?,CURRENT_TIMESTAMP) ON DUPLICATE KEY UPDATE wxid=VALUES(wxid),api_key=VALUES(api_key),
		last_register_at=CURRENT_TIMESTAMP,last_error=NULL`, b.Device, b.OwnerWxID, apiKey); err != nil {
		return b, err
	}
	return b, tx.Commit()
}
