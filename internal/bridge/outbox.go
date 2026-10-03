package bridge

import (
	"context"
	"strings"
	"sync"
	"time"
)

const defaultOutboxLease = 60 * time.Second

type MemoryOutbox struct {
	mu     sync.Mutex
	nextID int64
	items  []memoryOutboxItem
	now    func() time.Time
}

type memoryOutboxItem struct {
	ModuleOutboxItem
	leaseUntil time.Time
}

func NewMemoryOutbox() *MemoryOutbox {
	return &MemoryOutbox{now: time.Now}
}

func (o *MemoryOutbox) EnqueueReply(_ context.Context, action ReplyAction) (ModuleOutboxItem, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.nextID++
	now := o.now()
	item := memoryOutboxItem{
		ModuleOutboxItem: ModuleOutboxItem{
			ID:        o.nextID,
			Device:    action.Device,
			OwnerWxID: action.OwnerWxID,
			WxID:      action.WxID,
			Text:      action.Text,
			Status:    "pending",
			CreatedAt: formatRFC3339(now),
			UpdatedAt: formatRFC3339(now),
		},
	}
	o.items = append(o.items, item)
	return item.ModuleOutboxItem, nil
}

func (o *MemoryOutbox) PollReplyActions(_ context.Context, req ModulePollRequest) ([]ModuleOutboxItem, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	now := o.now()
	out := []ModuleOutboxItem{}
	for i := range o.items {
		if len(out) >= limit {
			break
		}
		if o.items[i].Device != req.Device {
			continue
		}
		if strings.TrimSpace(req.WxID) != "" && strings.TrimSpace(o.items[i].OwnerWxID) != strings.TrimSpace(req.WxID) {
			continue
		}
		if o.items[i].Status != "pending" && !(o.items[i].Status == "leased" && o.items[i].leaseUntil.Before(now)) {
			continue
		}
		o.items[i].Status = "leased"
		o.items[i].AttemptCount++
		o.items[i].leaseUntil = now.Add(defaultOutboxLease)
		o.items[i].UpdatedAt = formatRFC3339(now)
		out = append(out, o.items[i].ModuleOutboxItem)
	}
	return out, nil
}

func (o *MemoryOutbox) AckReplyActions(_ context.Context, req ModuleAckRequest) ([]ModuleOutboxItem, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	acks := map[int64]ModuleAckItem{}
	for _, item := range req.Items {
		acks[item.ID] = item
	}
	now := o.now()
	out := []ModuleOutboxItem{}
	for i := range o.items {
		ack, ok := acks[o.items[i].ID]
		if !ok || o.items[i].Device != req.Device || o.items[i].Status != "leased" {
			continue
		}
		if strings.TrimSpace(req.WxID) != "" && strings.TrimSpace(o.items[i].OwnerWxID) != strings.TrimSpace(req.WxID) {
			continue
		}
		o.items[i].Status = ack.Status
		o.items[i].LastError = ack.Error
		if ack.ChatRecordID > 0 {
			o.items[i].ChatRecordID = ack.ChatRecordID
		}
		o.items[i].leaseUntil = time.Time{}
		o.items[i].UpdatedAt = formatRFC3339(now)
		out = append(out, o.items[i].ModuleOutboxItem)
	}
	return out, nil
}

func (o *MemoryOutbox) CancelAccountOutbox(_ context.Context, device, keepOwner string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i := range o.items {
		item := &o.items[i]
		if item.Device == device && (item.Status == "leased" || (item.Status == "pending" && item.OwnerWxID != keepOwner)) {
			item.Status = "cancelled"
			item.LastError = "account session changed"
			item.leaseUntil = time.Time{}
			item.UpdatedAt = formatRFC3339(o.now())
		}
	}
	return nil
}

func formatRFC3339(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
