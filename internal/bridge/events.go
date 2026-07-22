package bridge

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

type Direction string

type ChatKind string

const (
	DirectionRecv Direction = "recv"
	DirectionSent Direction = "sent"

	ChatKindDirect  ChatKind = "direct"
	ChatKindRoom    ChatKind = "room"
	ChatKindUnknown ChatKind = "unknown"

	RawProviderModuleAck = "module_ack"
)

type MessageEvent struct {
	Sequence     int64     `json:"-"`
	EventKey     string    `json:"event_key,omitempty"`
	APIKey       string    `json:"api_key,omitempty"`
	ID           string    `json:"id"`
	EventID      int64     `json:"event_id"`
	ChatRecordID int64     `json:"chat_record_id"`
	Device       string    `json:"device"`
	OwnerWxID    string    `json:"owner_wxid,omitempty"`
	From         string    `json:"from"`
	To           string    `json:"to"`
	RoomID       string    `json:"room_id"`
	Sender       string    `json:"sender"`
	Text         string    `json:"text"`
	MessageType  int32     `json:"message_type"`
	MediaKind    string    `json:"media_kind,omitempty"`
	MediaMime    string    `json:"media_mime,omitempty"`
	MediaName    string    `json:"media_name,omitempty"`
	MediaURL     string    `json:"media_url,omitempty"`
	MediaSize    int64     `json:"media_size,omitempty"`
	MediaBase64  string    `json:"media_base64,omitempty"`
	CreateTime   int64     `json:"create_time"`
	Direction    Direction `json:"direction"`
	RawProvider  string    `json:"raw_provider"`
	ChatKind     ChatKind  `json:"chat_kind,omitempty"`
	Conversation string    `json:"chat_id,omitempty"`
}

func (e MessageEvent) Validate() error {
	if strings.TrimSpace(e.Device) == "" {
		return errors.New("device is required")
	}
	if strings.TrimSpace(e.Text) == "" && strings.TrimSpace(e.MediaURL) == "" {
		return errors.New("text or media_url is required")
	}
	if e.Direction != DirectionRecv && e.Direction != DirectionSent {
		return errors.New("direction must be recv or sent")
	}
	if e.ChatID() == "" {
		return errors.New("one of from, to, room_id, or chat_id is required")
	}
	return nil
}

func (e MessageEvent) ChatID() string {
	if strings.TrimSpace(e.Conversation) != "" {
		return strings.TrimSpace(e.Conversation)
	}
	if strings.TrimSpace(e.RoomID) != "" {
		return strings.TrimSpace(e.RoomID)
	}
	if e.Direction == DirectionSent && strings.TrimSpace(e.To) != "" {
		return strings.TrimSpace(e.To)
	}
	if strings.TrimSpace(e.From) != "" {
		return strings.TrimSpace(e.From)
	}
	return strings.TrimSpace(e.To)
}

func (e MessageEvent) Kind() ChatKind {
	if strings.TrimSpace(string(e.ChatKind)) != "" {
		return e.ChatKind
	}
	if strings.TrimSpace(e.RoomID) != "" || strings.Contains(strings.ToLower(strings.TrimSpace(e.ChatID())), "@chatroom") {
		return ChatKindRoom
	}
	return ChatKindDirect
}

func (e MessageEvent) Normalize() MessageEvent {
	if strings.TrimSpace(e.Conversation) == "" {
		e.Conversation = e.ChatID()
	}
	if strings.TrimSpace(string(e.ChatKind)) == "" {
		e.ChatKind = e.Kind()
	}
	if e.ChatKind == ChatKindRoom && strings.TrimSpace(e.RoomID) == "" {
		e.RoomID = e.ChatID()
	}
	return e
}

func (e MessageEvent) Timestamp() int64 {
	if e.CreateTime > 0 {
		return e.CreateTime
	}
	return time.Now().Unix()
}

// CanonicalEventKey identifies one source event across replay and replicas.
// It deliberately uses a digest so raw message text and identifiers do not
// become index keys or operational labels.
func (e MessageEvent) CanonicalEventKey() string {
	if key := strings.TrimSpace(e.EventKey); key != "" {
		return key
	}
	parts := []string{
		strings.TrimSpace(e.Device),
		strings.TrimSpace(e.OwnerWxID),
		string(e.Direction),
		strings.TrimSpace(e.ID),
		strconv.FormatInt(e.EventID, 10),
		strconv.FormatInt(e.ChatRecordID, 10),
		strings.TrimSpace(e.From),
		strings.TrimSpace(e.To),
		strings.TrimSpace(e.RoomID),
		strings.TrimSpace(e.Sender),
		strings.TrimSpace(e.Text),
		strconv.FormatInt(int64(e.MessageType), 10),
		strings.TrimSpace(e.MediaKind),
		strings.TrimSpace(e.MediaMime),
		strings.TrimSpace(e.MediaName),
		strconv.FormatInt(e.MediaSize, 10),
		strings.TrimSpace(e.RawProvider),
	}
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "evt_" + hex.EncodeToString(digest[:])
}

type SendTextRequest struct {
	Device    string   `json:"device"`
	OwnerWxID string   `json:"owner_wxid,omitempty"`
	WxIDs     []string `json:"wx_ids"`
	Text      string   `json:"text"`
}

func (req SendTextRequest) Validate(defaultDevice string) (SendTextRequest, error) {
	if strings.TrimSpace(req.Device) == "" {
		req.Device = defaultDevice
	}
	if strings.TrimSpace(req.Device) == "" {
		return req, errors.New("device is required")
	}
	if len(req.WxIDs) == 0 {
		return req, errors.New("wx_ids is required")
	}
	for i := range req.WxIDs {
		req.WxIDs[i] = strings.TrimSpace(req.WxIDs[i])
		if req.WxIDs[i] == "" {
			return req, errors.New("wx_ids cannot contain empty values")
		}
	}
	req.OwnerWxID = strings.TrimSpace(req.OwnerWxID)
	if strings.TrimSpace(req.Text) == "" {
		return req, errors.New("text is required")
	}
	return req, nil
}

type ModuleRegistrationRequest struct {
	APIKey   string `json:"api_key"`
	Device   string `json:"device"`
	WxID     string `json:"wxid"`
	Nickname string `json:"nickname"`
}

func (req ModuleRegistrationRequest) Validate(defaultDevice string) (ModuleRegistrationRequest, error) {
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.Device = strings.TrimSpace(req.Device)
	req.WxID = strings.TrimSpace(req.WxID)
	req.Nickname = strings.TrimSpace(req.Nickname)
	if req.APIKey == "" {
		return req, errors.New("api_key is required")
	}
	if req.WxID == "" {
		return req, errors.New("wxid is required")
	}
	return req, nil
}

type ModulePollRequest struct {
	APIKey string `json:"api_key"`
	Device string `json:"device"`
	WxID   string `json:"wxid"`
	Limit  int    `json:"limit"`
}

func (req ModulePollRequest) Validate(defaultDevice string) (ModulePollRequest, error) {
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.Device = strings.TrimSpace(req.Device)
	req.WxID = strings.TrimSpace(req.WxID)
	if req.APIKey == "" {
		return req, errors.New("api_key is required")
	}
	if req.Device == "" {
		req.Device = defaultDevice
	}
	if req.Device == "" {
		return req, errors.New("device is required")
	}
	if req.Limit <= 0 {
		req.Limit = 20
	}
	if req.Limit > 100 {
		req.Limit = 100
	}
	return req, nil
}

type ModuleAckRequest struct {
	APIKey string          `json:"api_key"`
	Device string          `json:"device"`
	WxID   string          `json:"wxid,omitempty"`
	Items  []ModuleAckItem `json:"items"`
	IDs    []int64         `json:"ids,omitempty"`
	Error  string          `json:"error,omitempty"`
}

type ModuleAckItem struct {
	ID           int64  `json:"id"`
	Status       string `json:"status"`
	Error        string `json:"error,omitempty"`
	ChatRecordID int64  `json:"chat_record_id,omitempty"`
}

func (req ModuleAckRequest) Validate(defaultDevice string) (ModuleAckRequest, error) {
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.Device = strings.TrimSpace(req.Device)
	req.WxID = strings.TrimSpace(req.WxID)
	if req.APIKey == "" {
		return req, errors.New("api_key is required")
	}
	if req.Device == "" {
		req.Device = defaultDevice
	}
	if req.Device == "" {
		return req, errors.New("device is required")
	}
	for _, id := range req.IDs {
		if id > 0 {
			req.Items = append(req.Items, ModuleAckItem{ID: id, Status: "sent", Error: req.Error})
		}
	}
	for i := range req.Items {
		req.Items[i].Status = strings.ToLower(strings.TrimSpace(req.Items[i].Status))
		if req.Items[i].Status == "" {
			req.Items[i].Status = "sent"
		}
		if req.Items[i].Status != "sent" && req.Items[i].Status != "failed" {
			return req, errors.New("ack item status must be sent or failed")
		}
		req.Items[i].Error = strings.TrimSpace(req.Items[i].Error)
	}
	if len(req.Items) == 0 {
		return req, errors.New("items or ids is required")
	}
	return req, nil
}

type ModuleOutboxItem struct {
	ID           int64  `json:"id"`
	Device       string `json:"device"`
	OwnerWxID    string `json:"owner_wxid,omitempty"`
	WxID         string `json:"wxid"`
	Text         string `json:"text"`
	ChatRecordID int64  `json:"chat_record_id,omitempty"`
	Status       string `json:"status"`
	AttemptCount int    `json:"attempt_count"`
	LastError    string `json:"last_error,omitempty"`
	CreatedAt    string `json:"created_at,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

type ModuleContactSnapshotRequest struct {
	APIKey   string          `json:"api_key"`
	Device   string          `json:"device"`
	WxID     string          `json:"wxid"`
	Complete bool            `json:"complete"`
	Contacts []ModuleContact `json:"contacts"`
}

type ModuleContact struct {
	WxID       string `json:"wxid"`
	Nickname   string `json:"nickname,omitempty"`
	Remark     string `json:"remark,omitempty"`
	Alias      string `json:"alias,omitempty"`
	Type       int    `json:"type,omitempty"`
	VerifyFlag int    `json:"verify_flag,omitempty"`
	Chatroom   bool   `json:"chatroom,omitempty"`
	Deleted    bool   `json:"deleted,omitempty"`
}

func (req ModuleContactSnapshotRequest) Validate(defaultDevice string) (ModuleContactSnapshotRequest, error) {
	req.APIKey = strings.TrimSpace(req.APIKey)
	req.Device = strings.TrimSpace(req.Device)
	req.WxID = strings.TrimSpace(req.WxID)
	if req.APIKey == "" {
		return req, errors.New("api_key is required")
	}
	if req.Device == "" {
		req.Device = defaultDevice
	}
	if req.Device == "" {
		return req, errors.New("device is required")
	}
	if len(req.Contacts) > 10000 {
		return req, errors.New("contacts cannot exceed 10000 items")
	}
	out := make([]ModuleContact, 0, len(req.Contacts))
	for _, contact := range req.Contacts {
		contact.WxID = strings.TrimSpace(contact.WxID)
		contact.Nickname = strings.TrimSpace(contact.Nickname)
		contact.Remark = strings.TrimSpace(contact.Remark)
		contact.Alias = strings.TrimSpace(contact.Alias)
		if contact.WxID == "" {
			continue
		}
		out = append(out, contact)
	}
	req.Contacts = out
	return req, nil
}
