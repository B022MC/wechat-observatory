package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"wechat-observatory/internal/bridge"
)

// Diagnostics live in their own table so the feature adds schema without
// touching any table an existing module or gateway reads.
var moduleDiagnosticMigrations = []string{
	`CREATE TABLE IF NOT EXISTS bridge_module_diagnostics (
		id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
		source VARCHAR(16) NOT NULL,
		device VARCHAR(128) NULL,
		reported_device VARCHAR(128) NULL,
		key_status VARCHAR(16) NOT NULL,
		key_fingerprint VARCHAR(16) NULL,
		stage VARCHAR(48) NOT NULL,
		message VARCHAR(1024) NOT NULL,
		wxid VARCHAR(191) NULL,
		module_version VARCHAR(64) NULL,
		wechat_version VARCHAR(64) NULL,
		android VARCHAR(128) NULL,
		client_ip VARCHAR(64) NULL,
		created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
		KEY idx_bridge_module_diagnostics_device (device, id),
		KEY idx_bridge_module_diagnostics_reported (reported_device, id),
		KEY idx_bridge_module_diagnostics_created (created_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
}

func (s *Store) RecordModuleDiagnostic(ctx context.Context, d bridge.ModuleDiagnostic) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO bridge_module_diagnostics
		(source, device, reported_device, key_status, key_fingerprint, stage, message,
		 wxid, module_version, wechat_version, android, client_ip)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.Source, nullString(d.Device), nullString(d.ReportedDevice), d.KeyStatus,
		nullString(d.KeyFingerprint), d.Stage, d.Message, nullString(d.WxID),
		nullString(d.ModuleVersion), nullString(d.WeChatVersion), nullString(d.Android),
		nullString(d.ClientIP))
	return err
}

func (s *Store) ListModuleDiagnostics(ctx context.Context, device string, limit int) ([]bridge.ModuleDiagnosticView, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT id, source, device, reported_device, key_status, key_fingerprint, stage, message,
		wxid, module_version, wechat_version, android, client_ip, created_at
		FROM bridge_module_diagnostics`
	args := []any{}
	if device != "" {
		query += ` WHERE device = ? OR reported_device = ?`
		args = append(args, device, device)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]bridge.ModuleDiagnosticView, 0)
	for rows.Next() {
		var item bridge.ModuleDiagnosticView
		var dev, reported, fingerprint, wxid, moduleVersion, wechatVersion, android, clientIP sql.NullString
		var created time.Time
		if err := rows.Scan(&item.ID, &item.Source, &dev, &reported, &item.KeyStatus, &fingerprint,
			&item.Stage, &item.Message, &wxid, &moduleVersion, &wechatVersion, &android, &clientIP, &created); err != nil {
			return nil, err
		}
		item.Device, item.ReportedDevice, item.KeyFingerprint = dev.String, reported.String, fingerprint.String
		item.WxID, item.ModuleVersion, item.WeChatVersion = wxid.String, moduleVersion.String, wechatVersion.String
		item.Android, item.ClientIP = android.String, clientIP.String
		item.CreatedAt = created.Format(time.RFC3339)
		items = append(items, item)
	}
	return items, rows.Err()
}

// PurgeExpiredModuleDiagnostics deletes diagnostics older than retentionDays
// in bounded batches.
func (s *Store) PurgeExpiredModuleDiagnostics(ctx context.Context, retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, errors.New("diagnostic retention days must be positive")
	}
	query := fmt.Sprintf(`DELETE FROM bridge_module_diagnostics
		WHERE created_at < DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL %d DAY)
		ORDER BY id LIMIT 1000`, retentionDays)
	var total int64
	for {
		result, err := s.db.ExecContext(ctx, query)
		if err != nil {
			return total, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
		if affected < 1000 {
			return total, nil
		}
	}
}
