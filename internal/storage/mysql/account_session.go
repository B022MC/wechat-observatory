package mysql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"

	"wechat-observatory/internal/bridge"
	"wechat-observatory/internal/config"
)

var accountSessionMigrations = []string{
	`CREATE TABLE IF NOT EXISTS bridge_module_account_current (
		device VARCHAR(128) NOT NULL PRIMARY KEY,
		instance_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
		generation BIGINT NOT NULL,
		owner_wxid VARCHAR(191) NOT NULL,
		credential_id VARCHAR(128) NOT NULL,
		auth_version BIGINT NOT NULL,
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
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (device, session_id),
		UNIQUE KEY uniq_account_generation (device, instance_id, generation)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
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
	err := row.Scan(&b.Device, &b.InstanceID, &b.SessionID, &b.Generation, &b.OwnerWxID, &b.CredentialID, &b.AuthVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return b, false, nil
	}
	return b, err == nil, err
}

const accountBindingColumns = "device,instance_id,session_id,generation,owner_wxid,credential_id,auth_version"

func (s *Store) CurrentAccountBinding(ctx context.Context, device string) (bridge.AccountBinding, bool, error) {
	return scanAccountBinding(s.executor(ctx).QueryRowContext(ctx, "SELECT "+accountBindingColumns+" FROM bridge_module_account_current WHERE device=?", device))
}

func (s *Store) FindAccountBinding(ctx context.Context, device, session string) (bridge.AccountBinding, bool, error) {
	return scanAccountBinding(s.executor(ctx).QueryRowContext(ctx, "SELECT "+accountBindingColumns+" FROM bridge_module_account_history WHERE device=? AND session_id=?", device, session))
}

func (s *Store) RegisterAccountBinding(ctx context.Context, b bridge.AccountBinding, device config.Device, apiKey string) error {
	held, ok := ctx.Value(accountConnKey{}).(accountConnection)
	if !ok || held.store != s || held.device != b.Device {
		return bridge.ErrAccountSession
	}
	previous, exists, err := s.CurrentAccountBinding(ctx, b.Device)
	if err != nil {
		return err
	}
	if exists && (previous.InstanceID != b.InstanceID || previous.Generation > b.Generation ||
		(previous.Generation == b.Generation && previous != b)) {
		return bridge.ErrAccountSession
	}
	tx, err := s.executor(ctx).BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !exists || previous != b {
		if _, err := tx.ExecContext(ctx, `UPDATE bridge_module_outbox SET status='cancelled',
			last_error='account session changed',lease_until=NULL WHERE device=? AND status IN ('pending','leased')`, b.Device); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bridge_module_account_history ("+accountBindingColumns+") VALUES (?,?,?,?,?,?,?)",
			b.Device, b.InstanceID, b.SessionID, b.Generation, b.OwnerWxID, b.CredentialID, b.AuthVersion); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO bridge_module_account_current ("+accountBindingColumns+") VALUES (?,?,?,?,?,?,?) ON DUPLICATE KEY UPDATE "+
			"instance_id=VALUES(instance_id),session_id=VALUES(session_id),generation=VALUES(generation),owner_wxid=VALUES(owner_wxid),credential_id=VALUES(credential_id),auth_version=VALUES(auth_version)",
			b.Device, b.InstanceID, b.SessionID, b.Generation, b.OwnerWxID, b.CredentialID, b.AuthVersion); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bridge_devices (name,wxid,nickname,wechat_nickname,timeout_ms)
		VALUES (?,?,?,?,5000) ON DUPLICATE KEY UPDATE wxid=VALUES(wxid),wechat_nickname=VALUES(wechat_nickname)`,
		device.Name, device.WxID, device.Nickname, device.WeChatNickname); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bridge_module_runtime (device,wxid,api_key,last_register_at)
		VALUES (?,?,?,CURRENT_TIMESTAMP) ON DUPLICATE KEY UPDATE wxid=VALUES(wxid),api_key=VALUES(api_key),
		last_register_at=CURRENT_TIMESTAMP,last_error=NULL`, b.Device, b.OwnerWxID, apiKey); err != nil {
		return err
	}
	return tx.Commit()
}
