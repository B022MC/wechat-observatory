package mysql

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"wechat-observatory/internal/bridge"
)

const installationColumns = `i.id, i.device, i.instance_id, COALESCE(i.owner_wxid, ''), COALESCE(i.wechat_nickname, ''),
	COALESCE(i.device_model, ''), COALESCE(i.android_version, ''), COALESCE(i.wechat_version, ''), COALESCE(i.module_version, ''),
	i.last_state, i.last_seen_at, i.last_active_at, ac.device IS NOT NULL`

const installationFrom = ` FROM bridge_module_installations i
	LEFT JOIN bridge_module_account_current ac ON ac.device = i.device AND ac.instance_id = i.instance_id`

func scanInstallation(row rowScanner) (bridge.InstallationRecord, error) {
	var rec bridge.InstallationRecord
	var lastActive sql.NullTime
	err := row.Scan(&rec.ID, &rec.Device, &rec.InstanceID, &rec.OwnerWxID, &rec.WeChatNickname,
		&rec.Info.DeviceModel, &rec.Info.AndroidVersion, &rec.Info.WeChatVersion, &rec.Info.ModuleVersion,
		&rec.LastState, &rec.LastSeenAt, &lastActive, &rec.Current)
	if lastActive.Valid {
		rec.LastActiveAt = lastActive.Time
	}
	return rec, err
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// TouchInstallation records a registration heartbeat. Empty descriptive fields
// never erase what an earlier (newer) module reported.
func (s *Store) TouchInstallation(ctx context.Context, seen bridge.InstallationSeen) error {
	if seen.OnlyIfAbsent {
		_, err := s.executor(ctx).ExecContext(ctx, `INSERT IGNORE INTO bridge_module_installations
			(device, instance_id, owner_wxid, last_state, last_seen_at, last_active_at) VALUES (?,?,?,?,?,?)`,
			seen.Device, seen.InstanceID, nullableText(seen.OwnerWxID), seen.State, seen.Seen, seen.Seen)
		return err
	}
	var lastActive any
	if seen.State == bridge.InstallationStateActive {
		lastActive = seen.Seen
	}
	_, err := s.executor(ctx).ExecContext(ctx, `INSERT INTO bridge_module_installations
		(device, instance_id, owner_wxid, wechat_nickname, device_model, android_version, wechat_version, module_version,
		 last_state, last_seen_at, last_active_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON DUPLICATE KEY UPDATE
			owner_wxid = COALESCE(VALUES(owner_wxid), owner_wxid),
			wechat_nickname = COALESCE(VALUES(wechat_nickname), wechat_nickname),
			device_model = COALESCE(VALUES(device_model), device_model),
			android_version = COALESCE(VALUES(android_version), android_version),
			wechat_version = COALESCE(VALUES(wechat_version), wechat_version),
			module_version = COALESCE(VALUES(module_version), module_version),
			last_state = VALUES(last_state),
			last_seen_at = VALUES(last_seen_at),
			last_active_at = COALESCE(VALUES(last_active_at), last_active_at)`,
		seen.Device, seen.InstanceID, nullableText(seen.OwnerWxID), nullableText(seen.WeChatNickname),
		nullableText(seen.Info.DeviceModel), nullableText(seen.Info.AndroidVersion), nullableText(seen.Info.WeChatVersion),
		nullableText(seen.Info.ModuleVersion), seen.State, seen.Seen, lastActive)
	return err
}

func (s *Store) FindInstallation(ctx context.Context, device, instanceID string) (bridge.InstallationRecord, bool, error) {
	rec, err := scanInstallation(s.executor(ctx).QueryRowContext(ctx, "SELECT "+installationColumns+installationFrom+
		" WHERE i.device=? AND i.instance_id=?", device, instanceID))
	if errors.Is(err, sql.ErrNoRows) {
		return bridge.InstallationRecord{}, false, nil
	}
	return rec, err == nil, err
}

func (s *Store) InstallationByID(ctx context.Context, device string, id int64) (bridge.InstallationRecord, bool, error) {
	rec, err := scanInstallation(s.executor(ctx).QueryRowContext(ctx, "SELECT "+installationColumns+installationFrom+
		" WHERE i.device=? AND i.id=?", device, id))
	if errors.Is(err, sql.ErrNoRows) {
		return bridge.InstallationRecord{}, false, nil
	}
	return rec, err == nil, err
}

// ListInstallations returns phones seen since the cut-off plus every current one.
func (s *Store) ListInstallations(ctx context.Context, since time.Time) ([]bridge.InstallationRecord, error) {
	rows, err := s.executor(ctx).QueryContext(ctx, "SELECT "+installationColumns+installationFrom+
		" WHERE i.last_seen_at >= ? OR ac.device IS NOT NULL ORDER BY i.device, i.last_seen_at DESC", since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bridge.InstallationRecord{}
	for rows.Next() {
		rec, err := scanInstallation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (s *Store) CurrentSwitchRequest(ctx context.Context, device string) (bridge.AccountSwitchRequest, bool, error) {
	var req bridge.AccountSwitchRequest
	err := s.executor(ctx).QueryRowContext(ctx, `SELECT device, target_instance_id, expires_at
		FROM bridge_module_switch_requests WHERE device=?`, device).Scan(&req.Device, &req.InstanceID, &req.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return bridge.AccountSwitchRequest{}, false, nil
	}
	return req, err == nil, err
}

func (s *Store) ListSwitchRequests(ctx context.Context) ([]bridge.AccountSwitchRequest, error) {
	rows, err := s.executor(ctx).QueryContext(ctx, `SELECT device, target_instance_id, expires_at FROM bridge_module_switch_requests`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bridge.AccountSwitchRequest{}
	for rows.Next() {
		var req bridge.AccountSwitchRequest
		if err := rows.Scan(&req.Device, &req.InstanceID, &req.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, req)
	}
	return out, rows.Err()
}

func (s *Store) SetSwitchRequest(ctx context.Context, req bridge.AccountSwitchRequest) error {
	_, err := s.executor(ctx).ExecContext(ctx, `INSERT INTO bridge_module_switch_requests (device, target_instance_id, requested_at, expires_at)
		VALUES (?,?,CURRENT_TIMESTAMP(6),?) ON DUPLICATE KEY UPDATE target_instance_id=VALUES(target_instance_id),
		requested_at=VALUES(requested_at), expires_at=VALUES(expires_at)`, req.Device, req.InstanceID, req.ExpiresAt)
	return err
}

// ClearSwitchRequest removes the device request; with an instance it only
// removes a request targeting that installation.
func (s *Store) ClearSwitchRequest(ctx context.Context, device, instanceID string) error {
	if instanceID == "" {
		_, err := s.executor(ctx).ExecContext(ctx, `DELETE FROM bridge_module_switch_requests WHERE device=?`, device)
		return err
	}
	_, err := s.executor(ctx).ExecContext(ctx, `DELETE FROM bridge_module_switch_requests WHERE device=? AND target_instance_id=?`, device, instanceID)
	return err
}
