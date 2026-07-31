package mysql

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"wechat-observatory/internal/bridge"
)

func (s *Store) ListAPIKeys(ctx context.Context, limit int) ([]bridge.APIKeyView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT code, device, nickname, enabled, created_at, updated_at
		FROM bridge_api_keys
		ORDER BY updated_at DESC, code ASC
		LIMIT ?`, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []bridge.APIKeyView
	for rows.Next() {
		var item bridge.APIKeyView
		var device, nickname sql.NullString
		var enabled bool
		var createdAt, updatedAt time.Time
		if err := rows.Scan(
			&item.Code,
			&device,
			&nickname,
			&enabled,
			&createdAt,
			&updatedAt,
		); err != nil {
			return nil, err
		}
		item.Device = device.String
		item.Nickname = nickname.String
		item.APIKey = item.Code
		item.Enabled = enabled
		item.CreatedAt = formatTime(createdAt)
		item.UpdatedAt = formatTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListStoredEvents(ctx context.Context, limit int) ([]bridge.StoredEventView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_key, source_id, event_id, chat_record_id, device, owner_wxid, direction, from_wxid,
			to_wxid, room_id, sender_wxid, text, message_type, media_kind,
			media_mime, media_name, media_url, media_size, raw_provider, create_time, created_at
		FROM bridge_message_events
		ORDER BY id DESC
		LIMIT ?`, normalizeLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanStoredEventViews(rows)
}

func (s *Store) LatestLiveEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM bridge_message_events`).Scan(&id)
	return id, err
}

func (s *Store) ListLiveEventsAfter(ctx context.Context, afterID int64, device string, limit int) ([]bridge.MessageEvent, error) {
	conditions := []string{"id > ?"}
	args := []any{afterID}
	if device = strings.TrimSpace(device); device != "" {
		conditions = append(conditions, "device = ?")
		args = append(args, device)
	}
	args = append(args, normalizeLimit(limit))
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, event_key, source_id, event_id, chat_record_id, device, owner_wxid,
			direction, from_wxid, to_wxid, room_id, sender_wxid, text, message_type,
			media_kind, media_mime, media_name, media_url, media_size, raw_provider, create_time
		FROM bridge_message_events
		WHERE `+strings.Join(conditions, " AND ")+`
		ORDER BY id ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]bridge.MessageEvent, 0)
	for rows.Next() {
		event, err := scanMessageEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) ListMessages(ctx context.Context, filter bridge.MessageFilter) ([]bridge.StoredEventView, error) {
	query, args := listMessagesQuery(filter)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanStoredEventViews(rows)
}

func listMessagesQuery(filter bridge.MessageFilter) (string, []any) {
	conditions := []string{"(raw_provider IS NULL OR raw_provider <> ?)"}
	args := []any{bridge.RawProviderModuleAck}
	if device := strings.TrimSpace(filter.Device); device != "" {
		conditions = append(conditions, "device = ?")
		args = append(args, device)
	}
	if ownerWxID := strings.TrimSpace(filter.OwnerWxID); ownerWxID != "" {
		conditions = append(conditions, "owner_wxid = ?")
		args = append(args, ownerWxID)
	}
	if filter.AfterIDSet {
		conditions = append(conditions, "id > ?")
		args = append(args, filter.AfterID)
	}
	chatID := strings.TrimSpace(filter.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(filter.WxID)
	}
	if chatID != "" {
		chatKind := strings.ToLower(strings.TrimSpace(filter.ChatKind))
		switch {
		case chatKind == string(bridge.ChatKindRoom) || strings.Contains(strings.ToLower(chatID), "@chatroom"):
			conditions = append(conditions, "(room_id = ? OR from_wxid = ? OR to_wxid = ?)")
			args = append(args, chatID, chatID, chatID)
		case chatKind == string(bridge.ChatKindDirect):
			conditions = append(conditions, "((room_id IS NULL OR room_id = '') AND (from_wxid = ? OR to_wxid = ? OR sender_wxid = ?))")
			args = append(args, chatID, chatID, chatID)
		default:
			conditions = append(conditions, "(from_wxid = ? OR to_wxid = ? OR room_id = ? OR sender_wxid = ?)")
			args = append(args, chatID, chatID, chatID, chatID)
		}
	} else if wxid := strings.TrimSpace(filter.WxID); wxid != "" {
		conditions = append(conditions, "(from_wxid = ? OR to_wxid = ? OR room_id = ? OR sender_wxid = ?)")
		args = append(args, wxid, wxid, wxid, wxid)
	}
	order := "DESC"
	if filter.AfterIDSet {
		order = "ASC"
	}
	args = append(args, normalizeLimit(filter.Limit))
	return `
		SELECT id, event_key, source_id, event_id, chat_record_id, device, owner_wxid, direction, from_wxid,
			to_wxid, room_id, sender_wxid, text, message_type, media_kind,
			media_mime, media_name, media_url, media_size, raw_provider, create_time, created_at
		FROM bridge_message_events
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY id ` + order + `
		LIMIT ?`, args
}

func scanStoredEventViews(rows *sql.Rows) ([]bridge.StoredEventView, error) {
	var out []bridge.StoredEventView
	for rows.Next() {
		var item bridge.StoredEventView
		var sourceID, ownerWxID, fromWxID, toWxID, roomID, senderWxID, rawProvider sql.NullString
		var mediaKind, mediaMime, mediaName, mediaURL sql.NullString
		var eventID, chatRecordID, mediaSize sql.NullInt64
		var createdAt time.Time
		if err := rows.Scan(
			&item.ID,
			&item.EventKey,
			&sourceID,
			&eventID,
			&chatRecordID,
			&item.Device,
			&ownerWxID,
			&item.Direction,
			&fromWxID,
			&toWxID,
			&roomID,
			&senderWxID,
			&item.Text,
			&item.MessageType,
			&mediaKind,
			&mediaMime,
			&mediaName,
			&mediaURL,
			&mediaSize,
			&rawProvider,
			&item.CreateTime,
			&createdAt,
		); err != nil {
			return nil, err
		}
		item.SourceID = sourceID.String
		item.EventID = eventID.Int64
		item.ChatRecordID = chatRecordID.Int64
		item.OwnerWxID = ownerWxID.String
		item.FromWxID = fromWxID.String
		item.ToWxID = toWxID.String
		item.RoomID = roomID.String
		item.SenderWxID = senderWxID.String
		item.MediaKind = mediaKind.String
		item.MediaMime = mediaMime.String
		item.MediaName = mediaName.String
		item.MediaURL = mediaURL.String
		item.MediaSize = mediaSize.Int64
		item.RawProvider = rawProvider.String
		event := bridge.MessageEvent{
			Direction: bridge.Direction(item.Direction),
			From:      item.FromWxID,
			To:        item.ToWxID,
			RoomID:    item.RoomID,
			Sender:    item.SenderWxID,
		}.Normalize()
		item.ChatID = event.ChatID()
		item.ChatKind = string(event.Kind())
		item.SequenceID = item.ID
		item.CreatedAt = formatTime(createdAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanMessageEvent(row rowScanner) (bridge.MessageEvent, error) {
	var event bridge.MessageEvent
	var sourceID, ownerWxID, fromWxID, toWxID, roomID, senderWxID, rawProvider sql.NullString
	var mediaKind, mediaMime, mediaName, mediaURL sql.NullString
	var eventID, chatRecordID, mediaSize sql.NullInt64
	var direction string
	err := row.Scan(
		&event.Sequence,
		&event.EventKey,
		&sourceID,
		&eventID,
		&chatRecordID,
		&event.Device,
		&ownerWxID,
		&direction,
		&fromWxID,
		&toWxID,
		&roomID,
		&senderWxID,
		&event.Text,
		&event.MessageType,
		&mediaKind,
		&mediaMime,
		&mediaName,
		&mediaURL,
		&mediaSize,
		&rawProvider,
		&event.CreateTime,
	)
	if err != nil {
		return bridge.MessageEvent{}, err
	}
	event.ID = sourceID.String
	event.EventID = eventID.Int64
	event.ChatRecordID = chatRecordID.Int64
	event.OwnerWxID = ownerWxID.String
	event.Direction = bridge.Direction(direction)
	event.From = fromWxID.String
	event.To = toWxID.String
	event.RoomID = roomID.String
	event.Sender = senderWxID.String
	event.MediaKind = mediaKind.String
	event.MediaMime = mediaMime.String
	event.MediaName = mediaName.String
	event.MediaURL = mediaURL.String
	event.MediaSize = mediaSize.Int64
	event.RawProvider = rawProvider.String
	return event.Normalize(), nil
}

func (s *Store) ListModuleStatuses(ctx context.Context) ([]bridge.ModuleStatusView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ak.device, d.wxid, COALESCE(d.nickname, ak.nickname, ak.device), ak.enabled, d.updated_at,
			rt.last_register_at,
			rt.last_poll_at,
			rt.last_ack_at,
			rt.last_poll_limit,
			rt.last_poll_item_count,
			rt.last_ack_sent_count,
			rt.last_ack_failed_count,
			rt.last_error,
			rt.updated_at,
			rt.api_key,
			ak.code,
			COALESCE(obs.pending_count, 0),
			COALESCE(obs.leased_count, 0),
			COALESCE(obs.sent_count, 0),
			COALESCE(obs.failed_count, 0),
			obs.last_outbox_id,
			last_ob.status,
			last_ob.last_error,
			last_ob.updated_at,
			ev.last_event_at,
			ev.last_inbound_at,
			ev.last_outbound_ack_at
		FROM bridge_api_keys ak
		LEFT JOIN bridge_devices d
			ON d.name = ak.device
		LEFT JOIN bridge_module_runtime rt
			ON rt.device = ak.device
		LEFT JOIN (
			SELECT device, owner_wxid,
				SUM(CASE WHEN status = 'pending' THEN 1 ELSE 0 END) AS pending_count,
				SUM(CASE WHEN status = 'leased' THEN 1 ELSE 0 END) AS leased_count,
				SUM(CASE WHEN status = 'sent' THEN 1 ELSE 0 END) AS sent_count,
				SUM(CASE WHEN status = 'failed' THEN 1 ELSE 0 END) AS failed_count,
				MAX(id) AS last_outbox_id
			FROM bridge_module_outbox
			WHERE owner_wxid IS NOT NULL AND owner_wxid <> ''
			GROUP BY device, owner_wxid
		) obs ON obs.device = ak.device AND obs.owner_wxid = d.wxid
		LEFT JOIN bridge_module_outbox last_ob
			ON last_ob.id = obs.last_outbox_id
		LEFT JOIN (
			SELECT device,
				MAX(created_at) AS last_event_at,
				MAX(CASE WHEN direction = 'recv' THEN created_at ELSE NULL END) AS last_inbound_at,
				MAX(CASE WHEN direction = 'sent' AND raw_provider = ? THEN created_at ELSE NULL END) AS last_outbound_ack_at
			FROM bridge_message_events
			GROUP BY device
		) ev ON ev.device = ak.device
		WHERE ak.device IS NOT NULL AND ak.device <> ''
		ORDER BY ak.device ASC`, bridge.RawProviderModuleAck)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []bridge.ModuleStatusView{}
	for rows.Next() {
		var item bridge.ModuleStatusView
		var deviceWxID, deviceNickname sql.NullString
		var enabled bool
		var runtimeError, runtimeAPIKey, activeAPIKey sql.NullString
		var deviceUpdatedAt sql.NullTime
		var lastRegisterAt, lastPollAt, lastAckAt, runtimeUpdatedAt sql.NullTime
		var lastOutboxUpdated, lastEventAt, lastInboundAt, lastOutboundAckAt sql.NullTime
		var lastPollLimit, lastPollItemCount, lastAckSentCount, lastAckFailedCount sql.NullInt64
		var pending, leased, sent, failed int64
		var lastOutboxID sql.NullInt64
		var lastOutboxStatus, lastOutboxError sql.NullString
		if err := rows.Scan(
			&item.Device,
			&deviceWxID,
			&deviceNickname,
			&enabled,
			&deviceUpdatedAt,
			&lastRegisterAt,
			&lastPollAt,
			&lastAckAt,
			&lastPollLimit,
			&lastPollItemCount,
			&lastAckSentCount,
			&lastAckFailedCount,
			&runtimeError,
			&runtimeUpdatedAt,
			&runtimeAPIKey,
			&activeAPIKey,
			&pending,
			&leased,
			&sent,
			&failed,
			&lastOutboxID,
			&lastOutboxStatus,
			&lastOutboxError,
			&lastOutboxUpdated,
			&lastEventAt,
			&lastInboundAt,
			&lastOutboundAckAt,
		); err != nil {
			return nil, err
		}
		item.DeviceWxID = deviceWxID.String
		item.DeviceNickname = deviceNickname.String
		item.Enabled = enabled
		item.Registered = item.Enabled &&
			strings.TrimSpace(item.DeviceWxID) != "" &&
			activeAPIKey.Valid &&
			runtimeAPIKey.Valid &&
			strings.TrimSpace(runtimeAPIKey.String) == strings.TrimSpace(activeAPIKey.String)
		item.LastRegisterAt = formatNullTime(lastRegisterAt)
		item.LastPollAt = formatNullTime(lastPollAt)
		item.LastAckAt = formatNullTime(lastAckAt)
		item.LastPollLimit = int(lastPollLimit.Int64)
		item.LastPollItemCount = int(lastPollItemCount.Int64)
		item.LastAckSentCount = int(lastAckSentCount.Int64)
		item.LastAckFailedCount = int(lastAckFailedCount.Int64)
		item.PendingOutbox = pending
		item.LeasedOutbox = leased
		item.SentOutbox = sent
		item.FailedOutbox = failed
		item.LastOutboxID = lastOutboxID.Int64
		item.LastOutboxStatus = lastOutboxStatus.String
		item.LastOutboxError = lastOutboxError.String
		item.LastOutboxUpdated = formatNullTime(lastOutboxUpdated)
		item.LastEventAt = formatNullTime(lastEventAt)
		item.LastInboundAt = formatNullTime(lastInboundAt)
		item.LastOutboundAckAt = formatNullTime(lastOutboundAckAt)
		item.RuntimeUpdatedAt = formatNullTime(runtimeUpdatedAt)
		item.DeviceUpdatedAt = formatNullTime(deviceUpdatedAt)
		if item.LastOutboxError == "" {
			item.LastOutboxError = runtimeError.String
		}
		item.NormalizeRuntimeStatus()
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) ListModuleContacts(ctx context.Context, filter bridge.ModuleContactFilter) ([]bridge.ModuleContactView, error) {
	query, args := listModuleContactsQuery(filter)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []bridge.ModuleContactView
	for rows.Next() {
		var item bridge.ModuleContactView
		var ownerWxID, nickname, remark, alias sql.NullString
		var lastSeenAt sql.NullTime
		var updatedAt time.Time
		if err := rows.Scan(
			&item.ID,
			&item.Device,
			&ownerWxID,
			&item.WxID,
			&nickname,
			&remark,
			&alias,
			&item.Type,
			&item.VerifyFlag,
			&item.Chatroom,
			&item.Deleted,
			&lastSeenAt,
			&updatedAt,
		); err != nil {
			return nil, err
		}
		item.OwnerWxID = ownerWxID.String
		item.Nickname = nickname.String
		item.Remark = remark.String
		item.Alias = alias.String
		item.LastSeenAt = formatNullTime(lastSeenAt)
		item.UpdatedAt = formatTime(updatedAt)
		out = append(out, item)
	}
	return out, rows.Err()
}

func listModuleContactsQuery(filter bridge.ModuleContactFilter) (string, []any) {
	limit := normalizeLimitUpTo(filter.Limit, 10000)
	conditions := []string{"1=1"}
	args := []any{}
	if device := strings.TrimSpace(filter.Device); device != "" {
		conditions = append(conditions, "device = ?")
		args = append(args, device)
	}
	if ownerWxID := strings.TrimSpace(filter.OwnerWxID); ownerWxID != "" {
		conditions = append(conditions, "owner_wxid = ?")
		args = append(args, ownerWxID)
	}
	if wxid := strings.TrimSpace(filter.WxID); wxid != "" {
		conditions = append(conditions, "wxid = ?")
		args = append(args, wxid)
	}
	if !filter.IncludeDeleted {
		conditions = append(conditions, "is_deleted = FALSE")
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		like := "%" + query + "%"
		conditions = append(conditions, "(wxid LIKE ? OR nickname LIKE ? OR remark LIKE ? OR contact_alias LIKE ?)")
		args = append(args, like, like, like, like)
	}
	args = append(args, limit)
	return `
		SELECT id, device, owner_wxid, wxid, nickname, remark, contact_alias,
			contact_type, verify_flag, is_chatroom, is_deleted, last_seen_at, updated_at
		FROM bridge_module_contacts
		WHERE ` + strings.Join(conditions, " AND ") + `
		ORDER BY is_deleted ASC,
			COALESCE(NULLIF(remark, ''), NULLIF(nickname, ''), wxid) ASC,
			id ASC
		LIMIT ?`, args
}

func normalizeLimit(limit int) int {
	return normalizeLimitUpTo(limit, 500)
}

func normalizeLimitUpTo(limit, maximum int) int {
	if limit <= 0 {
		return 50
	}
	if limit > maximum {
		return maximum
	}
	return limit
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatNullTime(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return formatTime(value.Time)
}
