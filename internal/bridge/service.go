package bridge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"wechat-observatory/internal/config"
)

type Service struct {
	cfg          Config
	hub          *Hub
	persistence  Persistence
	outbox       Outbox
	adminReader  AdminReader
	instanceID   string
	sessionTTL   time.Duration
	pollEvery    time.Duration
	offlineAfter time.Duration

	mu               sync.RWMutex
	nextChatRecordID int64

	outboxNotifyMu sync.Mutex
	outboxNotify   map[string]map[chan struct{}]struct{}
}

const maxOutboxPollBatch = 1

// IngestValidationError identifies a module payload that cannot safely cross
// the ingest boundary. It lets HTTP callers distinguish a non-retryable 400
// response from a durable-storage failure without inspecting error text.
type IngestValidationError struct {
	Field   string
	Problem string
}

func (e *IngestValidationError) Error() string {
	return fmt.Sprintf("%s %s", e.Field, e.Problem)
}

// IngestPersistenceError identifies a retryable failure to durably record a
// v2 event. V2 delivery must not continue to SSE until this operation succeeds.
type IngestPersistenceError struct {
	Err error
}

func (e *IngestPersistenceError) Error() string {
	return fmt.Sprintf("persist inbound event: %v", e.Err)
}

func (e *IngestPersistenceError) Unwrap() error {
	return e.Err
}

type Config struct {
	DefaultDevice          string
	Devices                map[string]config.Device
	APIKeys                map[string]config.APIKey
	InstanceID             string
	SessionTTL             time.Duration
	PollInterval           time.Duration
	OfflineAfter           time.Duration
	EventIdentityV2Devices map[string]struct{}
}

func NewService(cfg Config, opts ...Option) *Service {
	service := &Service{
		cfg:              cfg,
		hub:              NewHub(500),
		outbox:           NewMemoryOutbox(),
		outboxNotify:     map[string]map[chan struct{}]struct{}{},
		nextChatRecordID: time.Now().Unix() * 1000,
		instanceID:       firstNonEmpty(cfg.InstanceID, "local"),
		sessionTTL:       cfg.SessionTTL,
		pollEvery:        cfg.PollInterval,
		offlineAfter:     cfg.OfflineAfter,
	}
	if service.sessionTTL <= 0 {
		service.sessionTTL = 15 * time.Second
	}
	if service.pollEvery <= 0 {
		service.pollEvery = 3 * time.Second
	}
	if service.offlineAfter <= 0 {
		service.offlineAfter = 5 * time.Minute
	}
	for _, opt := range opts {
		opt(service)
	}
	for code, key := range service.cfg.APIKeys {
		key.Code = firstNonEmpty(key.Code, code)
		ensureAPIKeyCredential(&key)
		service.cfg.APIKeys[code] = key
	}
	return service
}

func (s *Service) Hub() *Hub {
	return s.hub
}

func (s *Service) DefaultDevice() string {
	return s.cfg.DefaultDevice
}

func (s *Service) OutboxPollInterval() time.Duration {
	return s.pollEvery
}

func (s *Service) ModuleOfflineAfter() time.Duration {
	return s.offlineAfter
}

func (s *Service) NormalizeModuleStatuses(statuses []ModuleStatusView) []ModuleStatusView {
	now := time.Now()
	for index := range statuses {
		statuses[index].NormalizeRuntimeStatusAt(now, s.offlineAfter)
	}
	return statuses
}

func (s *Service) Device(name string) (config.Device, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	device, ok := s.cfg.Devices[name]
	return device, ok
}

func (s *Service) Devices() []config.Device {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]config.Device, 0, len(s.cfg.Devices))
	for _, device := range s.cfg.Devices {
		out = append(out, device)
	}
	return out
}

func (s *Service) APIKeys() []config.APIKey {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]config.APIKey, 0, len(s.cfg.APIKeys))
	for _, key := range s.cfg.APIKeys {
		out = append(out, key)
	}
	return out
}

func (s *Service) AdminReader() AdminReader {
	return s.adminReader
}

func (s *Service) AdminWriter() AdminWriter {
	if writer, ok := s.persistence.(AdminWriter); ok {
		return writer
	}
	return nil
}

func (s *Service) UpsertAPIKey(ctx context.Context, req APIKeyUpsertRequest) (APIKeyView, error) {
	key := config.APIKey{
		Code:     firstNonEmpty(req.APIKey, req.Code),
		Device:   strings.TrimSpace(req.Device),
		Nickname: strings.TrimSpace(req.Nickname),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.APIKeys == nil {
		s.cfg.APIKeys = map[string]config.APIKey{}
	}
	if key.Code == "" {
		for i := 0; i < 5; i++ {
			key.Code = generateAPIKey()
			if _, exists := s.cfg.APIKeys[key.Code]; !exists {
				break
			}
		}
		if _, exists := s.cfg.APIKeys[key.Code]; exists {
			return APIKeyView{}, fmt.Errorf("failed to generate unique api key")
		}
	}
	if key.Device == "" {
		key.Device = apiKeyDeviceName(key)
	}
	if existing, ok := s.cfg.APIKeys[key.Code]; ok {
		key.CredentialID = existing.CredentialID
		key.AuthVersion = existing.AuthVersion
		if existing.Device != key.Device || existing.Disabled {
			key.AuthVersion++
		}
	}
	ensureAPIKeyCredential(&key)
	if writer := s.AdminWriter(); writer != nil {
		if err := writer.UpsertAPIKey(ctx, key); err != nil {
			return APIKeyView{}, err
		}
	}
	s.cfg.APIKeys[key.Code] = key
	return apiKeyView(key), nil
}

func (s *Service) DeleteAPIKey(ctx context.Context, apiKey string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return fmt.Errorf("api key is required")
	}

	if _, ok, err := s.lookupAPIKey(ctx, apiKey); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("api key %q not found", apiKey)
	}
	if writer := s.AdminWriter(); writer != nil {
		if err := writer.DeleteAPIKey(ctx, apiKey); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cfg.APIKeys, apiKey)
	return nil
}

func (s *Service) SetAPIKeyEnabled(ctx context.Context, apiKey string, enabled bool) (APIKeyView, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return APIKeyView{}, fmt.Errorf("api key is required")
	}

	key, ok, err := s.lookupAPIKey(ctx, apiKey)
	if err != nil {
		return APIKeyView{}, err
	}
	if !ok {
		return APIKeyView{}, fmt.Errorf("api key %q not found", apiKey)
	}
	if key.Disabled == enabled {
		key.AuthVersion++
	}
	key.Disabled = !enabled
	ensureAPIKeyCredential(&key)
	if writer := s.AdminWriter(); writer != nil {
		if err := writer.SetAPIKeyEnabled(ctx, apiKey, enabled); err != nil {
			return APIKeyView{}, err
		}
	}
	s.setCachedAPIKey(key)
	return apiKeyView(key), nil
}

func (s *Service) IntrospectAPIKey(ctx context.Context, req APIKeyIntrospectionRequest) (APIKeyIntrospection, error) {
	apiKey := strings.TrimSpace(req.APIKey)
	credentialRef := strings.TrimSpace(req.CredentialRef)
	if (apiKey == "") == (credentialRef == "") {
		return APIKeyIntrospection{}, fmt.Errorf("exactly one of api_key or credential_ref is required")
	}

	var (
		key config.APIKey
		ok  bool
		err error
	)
	if apiKey != "" {
		key, ok, err = s.lookupAPIKey(ctx, apiKey)
	} else {
		key, ok, err = s.lookupAPIKeyByCredentialRef(ctx, credentialRef)
	}
	if err != nil {
		return APIKeyIntrospection{}, err
	}
	deviceName := strings.TrimSpace(key.Device)
	if !ok || key.Disabled || deviceName == "" || strings.TrimSpace(key.CredentialID) == "" || key.AuthVersion <= 0 {
		return APIKeyIntrospection{Active: false}, nil
	}
	if _, found, lookupErr := s.lookupDevice(ctx, deviceName); lookupErr != nil {
		return APIKeyIntrospection{}, lookupErr
	} else if !found {
		return APIKeyIntrospection{Active: false}, nil
	}
	return APIKeyIntrospection{
		Active: true, CredentialRef: key.CredentialID, AuthVersion: key.AuthVersion, Device: deviceName,
	}, nil
}

func (s *Service) UpsertDevice(ctx context.Context, req DeviceUpsertRequest) (ModuleStatusView, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return ModuleStatusView{}, fmt.Errorf("device name is required")
	}
	nickname := strings.TrimSpace(req.Nickname)

	device, ok, err := s.lookupDevice(ctx, name)
	if err != nil {
		return ModuleStatusView{}, err
	}
	if !ok {
		return ModuleStatusView{}, fmt.Errorf("unknown device %q", name)
	}
	device.Nickname = firstNonEmpty(nickname, device.Nickname, device.Name)
	if writer := s.AdminWriter(); writer != nil {
		if err := writer.UpsertDevice(ctx, device); err != nil {
			return ModuleStatusView{}, err
		}
	}
	s.setCachedDevice(device)
	status := ModuleStatusView{
		Device:         device.Name,
		DeviceWxID:     device.WxID,
		DeviceNickname: device.Nickname,
		WeChatNickname: device.WeChatNickname,
		Enabled:        true,
	}
	if strings.TrimSpace(device.WxID) != "" {
		status.Registered = true
	}
	status.NormalizeRuntimeStatus()
	return status, nil
}

func (s *Service) RegisterModule(ctx context.Context, req ModuleRegistrationRequest) (*ModuleRegistrationResult, error) {
	req, err := req.Validate(s.cfg.DefaultDevice)
	if err != nil {
		return nil, err
	}

	key, ok, err := s.lookupAPIKey(ctx, req.APIKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("invalid api key")
	}
	if key.Disabled {
		return nil, fmt.Errorf("api key disabled")
	}
	deviceName := apiKeyDeviceName(key)
	req.Device = deviceName
	if strings.TrimSpace(req.Device) == "" {
		return nil, fmt.Errorf("device is required")
	}
	if key.Device != req.Device {
		key.Device = req.Device
		key.AuthVersion++
		ensureAPIKeyCredential(&key)
		if writer := s.AdminWriter(); writer != nil {
			if err := writer.UpsertAPIKey(ctx, key); err != nil {
				return nil, err
			}
		}
		s.setCachedAPIKey(key)
	}
	device, found, err := s.lookupDevice(ctx, req.Device)
	if err != nil {
		return nil, err
	}
	if !found {
		device = config.Device{}
	}
	if strings.TrimSpace(device.Name) == "" {
		device.Name = req.Device
		device.Timeout = 5 * time.Second
	}
	device.WxID = req.WxID
	device.Nickname = firstNonEmpty(device.Nickname, key.Nickname, device.Name)
	device.WeChatNickname = strings.TrimSpace(req.Nickname)
	if s.persistence != nil {
		if err := s.persistence.UpdateDeviceIdentity(ctx, req.Device, device.WxID, device.Nickname); err != nil {
			return nil, err
		}
		if identityPersistence, ok := s.persistence.(DeviceWeChatIdentityPersistence); ok {
			if err := identityPersistence.UpdateDeviceWeChatIdentity(ctx, req.Device, device.WxID, device.WeChatNickname); err != nil {
				return nil, err
			}
		}
	}
	s.recordModuleActivity(ctx, ModuleActivity{
		Device: req.Device,
		WxID:   req.WxID,
		APIKey: req.APIKey,
		Kind:   "register",
	})
	s.setCachedDevice(device)
	s.setCachedAPIKey(key)
	return &ModuleRegistrationResult{
		Device: moduleDeviceView(device),
	}, nil
}

func (s *Service) Ingest(ctx context.Context, event MessageEvent) (*IngestResult, error) {
	auth, err := s.authorizeModuleAPIKey(ctx, event.APIKey)
	if err != nil {
		return nil, err
	}
	if event.Device == "" {
		event.Device = auth.Device
	}
	if event.Direction == "" {
		event.Direction = DirectionRecv
	}
	if event.MessageType == 0 {
		event.MessageType = 1
	}
	sourceCreateTime := event.CreateTime
	event = event.Normalize()
	mediaError := ""
	if stored, err := s.StoreMediaAttachment(event); err != nil {
		event.MediaBase64 = ""
		mediaError = err.Error()
	} else {
		event = stored
	}
	event.APIKey = ""
	if err := event.Validate(); err != nil {
		return nil, err
	}
	event.Device = auth.Device
	if ownerWxID := s.deviceWxID(ctx, event.Device); ownerWxID != "" {
		event.OwnerWxID = ownerWxID
	}
	// event_key is a server-owned transport identity. Never allow the module to
	// choose the business idempotency boundary. A v2 device must never fall
	// back to the legacy key family because that would make a retry eligible
	// under two durable identities.
	event.EventKey = ""
	if _, enabled := s.cfg.EventIdentityV2Devices[event.Device]; enabled {
		if sourceCreateTime <= 0 {
			return nil, &IngestValidationError{
				Field:   "create_time",
				Problem: "must be positive for v2 event identity",
			}
		}
		event.EventKey = event.CanonicalEventKeyV2()
	} else {
		event.EventKey = event.CanonicalEventKey()
	}
	if event.CreateTime == 0 {
		event.CreateTime = time.Now().Unix()
	}

	result := &IngestResult{}
	if mediaError != "" {
		result.PersistenceError = "media: " + mediaError
	}
	if s.persistence != nil {
		stored, err := s.persistence.RecordInboundEvent(ctx, event)
		if err != nil {
			if IsCanonicalEventKeyV2(event.EventKey) {
				return nil, &IngestPersistenceError{Err: err}
			}
			// Preserve the established legacy contract outside the canary: expose
			// the persistence error for audit and keep publishing the observation.
			result.PersistenceError = err.Error()
		} else {
			event = stored
		}
	}
	s.hub.Publish(event)
	result.Published = true
	return result, nil
}

func (s *Service) SendText(ctx context.Context, req SendTextRequest) (int64, error) {
	req, err := req.Validate(s.cfg.DefaultDevice)
	if err != nil {
		return 0, err
	}
	if _, ok, err := s.lookupDevice(ctx, req.Device); err != nil {
		return 0, err
	} else if !ok {
		return 0, fmt.Errorf("unknown device %q", req.Device)
	}
	ownerWxID := s.deviceWxID(ctx, req.Device)
	if req.OwnerWxID != "" && req.OwnerWxID != ownerWxID {
		return 0, fmt.Errorf("send owner wxid %q is not current device wxid", req.OwnerWxID)
	}
	if checker, ok := s.persistence.(ModuleLivenessChecker); ok {
		online, err := checker.ModuleOnline(ctx, req.Device, ownerWxID, s.offlineAfter)
		if err != nil {
			return 0, err
		}
		if !online {
			return 0, ErrModuleOffline
		}
	}
	firstID := int64(0)
	for _, wxid := range req.WxIDs {
		item, err := s.outbox.EnqueueReply(ctx, ReplyAction{
			Device:    req.Device,
			OwnerWxID: ownerWxID,
			WxID:      wxid,
			Text:      req.Text,
		})
		if err != nil {
			return 0, err
		}
		if firstID == 0 {
			firstID = item.ID
		}
	}
	s.notifyOutbox(req.Device)
	return firstID, nil
}

func (s *Service) PollOutbox(ctx context.Context, req ModulePollRequest) ([]ModuleOutboxItem, error) {
	req, err := req.Validate(s.cfg.DefaultDevice)
	if err != nil {
		return nil, err
	}
	auth, err := s.authorizeModuleAPIKey(ctx, req.APIKey)
	if err != nil {
		return nil, err
	}
	req.Device = auth.Device
	currentWxID := s.deviceWxID(ctx, req.Device)
	if req.WxID == "" {
		req.WxID = currentWxID
	}
	if currentWxID != "" && req.WxID != "" && currentWxID != req.WxID {
		return nil, fmt.Errorf("module wxid %q is not current device wxid", req.WxID)
	}
	if req.Limit > maxOutboxPollBatch {
		req.Limit = maxOutboxPollBatch
	}
	items, err := s.outbox.PollReplyActions(ctx, req)
	if err != nil {
		return nil, err
	}
	s.recordModuleActivity(ctx, ModuleActivity{
		Device:        req.Device,
		WxID:          req.WxID,
		APIKey:        req.APIKey,
		Kind:          "poll",
		PollLimit:     req.Limit,
		PollItemCount: len(items),
	})
	return items, nil
}

func (s *Service) AcquireOutboxSession(ctx context.Context, apiKey, requestedDevice, wxid string) (ModuleSessionLease, error) {
	auth, err := s.authorizeModuleAPIKey(ctx, apiKey)
	if err != nil {
		return ModuleSessionLease{}, err
	}
	device := auth.Device
	if requestedDevice = strings.TrimSpace(requestedDevice); requestedDevice != "" && requestedDevice != device {
		return ModuleSessionLease{}, fmt.Errorf("device %q does not match api key device", requestedDevice)
	}
	currentWxID := s.deviceWxID(ctx, device)
	wxid = strings.TrimSpace(wxid)
	if wxid == "" {
		wxid = currentWxID
	}
	if currentWxID != "" && wxid != "" && currentWxID != wxid {
		return ModuleSessionLease{}, fmt.Errorf("module wxid %q is not current device wxid", wxid)
	}
	lease := ModuleSessionLease{
		Device:    device,
		OwnerWxID: wxid,
		HolderID:  s.instanceID,
		Token:     generateSessionToken(),
		TTL:       s.sessionTTL,
	}
	if leaser, ok := s.persistence.(ModuleSessionLeaser); ok {
		claimed, err := leaser.ClaimModuleSession(ctx, lease)
		if err != nil {
			return ModuleSessionLease{}, err
		}
		if !claimed {
			return ModuleSessionLease{}, ErrModuleSessionActive
		}
	}
	return lease, nil
}

func (s *Service) RenewOutboxSession(ctx context.Context, lease ModuleSessionLease) (bool, error) {
	if leaser, ok := s.persistence.(ModuleSessionLeaser); ok {
		return leaser.RenewModuleSession(ctx, lease)
	}
	return true, nil
}

func (s *Service) ReleaseOutboxSession(ctx context.Context, lease ModuleSessionLease) {
	if leaser, ok := s.persistence.(ModuleSessionLeaser); ok {
		_ = leaser.ReleaseModuleSession(ctx, lease)
	}
}

func (s *Service) AckOutbox(ctx context.Context, req ModuleAckRequest) ([]ModuleOutboxItem, error) {
	req, err := req.Validate(s.cfg.DefaultDevice)
	if err != nil {
		return nil, err
	}
	auth, err := s.authorizeModuleAPIKey(ctx, req.APIKey)
	if err != nil {
		return nil, err
	}
	req.Device = auth.Device
	currentWxID := s.deviceWxID(ctx, req.Device)
	if req.WxID == "" {
		req.WxID = currentWxID
	}
	if currentWxID != "" && req.WxID != "" && currentWxID != req.WxID {
		return nil, fmt.Errorf("module wxid %q is not current device wxid", req.WxID)
	}
	items, err := s.outbox.AckReplyActions(ctx, req)
	if err != nil {
		return nil, err
	}
	s.recordModuleActivity(ctx, ackActivity(req))
	acks := map[int64]ModuleAckItem{}
	for _, ack := range req.Items {
		acks[ack.ID] = ack
	}
	for _, item := range items {
		ack := acks[item.ID]
		if ack.Status != "sent" {
			continue
		}
		recordID := ack.ChatRecordID
		if recordID <= 0 {
			recordID = s.nextRecordID()
		}
		event := MessageEvent{
			ID:           strconv.FormatInt(recordID, 10),
			EventID:      recordID,
			ChatRecordID: recordID,
			Device:       item.Device,
			OwnerWxID:    firstNonEmpty(item.OwnerWxID, s.deviceWxID(ctx, item.Device)),
			From:         s.deviceWxID(ctx, item.Device),
			To:           item.WxID,
			Text:         item.Text,
			MessageType:  1,
			Direction:    DirectionSent,
			CreateTime:   time.Now().Unix(),
			RawProvider:  RawProviderModuleAck,
		}.Normalize()
		if s.persistence != nil {
			if stored, err := s.persistence.RecordOutboundEvent(ctx, event); err == nil {
				event = stored
			}
		}
		s.hub.Publish(event)
	}
	return items, nil
}

func (s *Service) RecordModuleContacts(ctx context.Context, req ModuleContactSnapshotRequest) (int, error) {
	req, err := req.Validate(s.cfg.DefaultDevice)
	if err != nil {
		return 0, err
	}
	auth, err := s.authorizeModuleAPIKey(ctx, req.APIKey)
	if err != nil {
		return 0, err
	}
	req.Device = auth.Device
	if req.WxID == "" {
		req.WxID = s.deviceWxID(ctx, req.Device)
	}
	if s.persistence == nil {
		return len(req.Contacts), nil
	}
	store, ok := s.persistence.(ModuleContactStore)
	if !ok {
		return len(req.Contacts), nil
	}
	if err := store.RecordModuleContacts(ctx, req); err != nil {
		return 0, err
	}
	return len(req.Contacts), nil
}

func (s *Service) recordModuleActivity(ctx context.Context, activity ModuleActivity) {
	if s.persistence == nil {
		return
	}
	recorder, ok := s.persistence.(ModuleActivityRecorder)
	if !ok {
		return
	}
	_ = recorder.RecordModuleActivity(ctx, activity)
}

func (s *Service) subscribeOutbox(device string) (<-chan struct{}, func()) {
	device = strings.TrimSpace(device)
	ch := make(chan struct{}, 1)
	s.outboxNotifyMu.Lock()
	if s.outboxNotify[device] == nil {
		s.outboxNotify[device] = map[chan struct{}]struct{}{}
	}
	s.outboxNotify[device][ch] = struct{}{}
	s.outboxNotifyMu.Unlock()
	unsubscribe := func() {
		s.outboxNotifyMu.Lock()
		if subscribers := s.outboxNotify[device]; subscribers != nil {
			delete(subscribers, ch)
			if len(subscribers) == 0 {
				delete(s.outboxNotify, device)
			}
		}
		s.outboxNotifyMu.Unlock()
	}
	return ch, unsubscribe
}

func (s *Service) notifyOutbox(device string) {
	device = strings.TrimSpace(device)
	s.outboxNotifyMu.Lock()
	defer s.outboxNotifyMu.Unlock()
	for ch := range s.outboxNotify[device] {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func ackActivity(req ModuleAckRequest) ModuleActivity {
	activity := ModuleActivity{
		Device: req.Device,
		Kind:   "ack",
	}
	for _, item := range req.Items {
		switch item.Status {
		case "failed":
			activity.AckFailedCount++
			if activity.LastError == "" {
				activity.LastError = item.Error
			}
		case "sent":
			activity.AckSentCount++
		}
	}
	return activity
}

func (s *Service) deviceWxID(ctx context.Context, deviceName string) string {
	if device, ok, err := s.lookupDevice(ctx, deviceName); err == nil && ok {
		return device.WxID
	}
	return ""
}

type moduleAPIKeyAuth struct {
	Key    config.APIKey
	Device string
}

func (s *Service) authorizeModuleAPIKey(ctx context.Context, apiKey string) (moduleAPIKeyAuth, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return moduleAPIKeyAuth{}, fmt.Errorf("api_key is required")
	}
	key, ok, err := s.lookupAPIKey(ctx, apiKey)
	if err != nil {
		return moduleAPIKeyAuth{}, err
	}
	if !ok {
		return moduleAPIKeyAuth{}, fmt.Errorf("invalid api key")
	}
	if key.Disabled {
		return moduleAPIKeyAuth{}, fmt.Errorf("api key disabled")
	}
	device := apiKeyDeviceName(key)
	if strings.TrimSpace(device) == "" {
		return moduleAPIKeyAuth{}, fmt.Errorf("device is required")
	}
	if _, ok, err := s.lookupDevice(ctx, device); err != nil {
		return moduleAPIKeyAuth{}, err
	} else if !ok {
		return moduleAPIKeyAuth{}, fmt.Errorf("unknown device %q", device)
	}
	return moduleAPIKeyAuth{Key: key, Device: device}, nil
}

func (s *Service) lookupAPIKey(ctx context.Context, code string) (config.APIKey, bool, error) {
	if reader, ok := s.persistence.(ModuleConfigReader); ok {
		return reader.LookupAPIKey(ctx, code)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.cfg.APIKeys[strings.TrimSpace(code)]
	return key, ok, nil
}

func (s *Service) lookupAPIKeyByCredentialRef(ctx context.Context, credentialRef string) (config.APIKey, bool, error) {
	credentialRef = strings.TrimSpace(credentialRef)
	if reader, ok := s.persistence.(APIKeyCredentialReader); ok {
		return reader.LookupAPIKeyByCredentialRef(ctx, credentialRef)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, key := range s.cfg.APIKeys {
		if key.CredentialID == credentialRef {
			return key, true, nil
		}
	}
	return config.APIKey{}, false, nil
}

func (s *Service) lookupDevice(ctx context.Context, name string) (config.Device, bool, error) {
	if reader, ok := s.persistence.(ModuleConfigReader); ok {
		return reader.LookupDevice(ctx, name)
	}
	device, ok := s.Device(name)
	return device, ok, nil
}

func (s *Service) setCachedDevice(device config.Device) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.Devices == nil {
		s.cfg.Devices = map[string]config.Device{}
	}
	s.cfg.Devices[device.Name] = device
}

func (s *Service) setCachedAPIKey(key config.APIKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg.APIKeys == nil {
		s.cfg.APIKeys = map[string]config.APIKey{}
	}
	s.cfg.APIKeys[key.Code] = key
}

func (s *Service) nextRecordID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextChatRecordID++
	return s.nextChatRecordID
}

type IngestResult struct {
	Published        bool   `json:"published"`
	PersistenceError string `json:"persistence_error,omitempty"`
}

type ReplyAction struct {
	Device       string `json:"device"`
	OwnerWxID    string `json:"owner_wxid,omitempty"`
	WxID         string `json:"wxid"`
	Text         string `json:"text"`
	ChatRecordID int64  `json:"chat_record_id,omitempty"`
	Error        string `json:"error,omitempty"`
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func apiKeyView(key config.APIKey) APIKeyView {
	return APIKeyView{
		Code:     key.Code,
		APIKey:   key.Code,
		Device:   key.Device,
		Nickname: key.Nickname,
		Enabled:  !key.Disabled,
	}
}

func generateAPIKey() string {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("wg_%d", time.Now().UnixNano())
	}
	return "wg_" + hex.EncodeToString(random)
}

func ensureAPIKeyCredential(key *config.APIKey) {
	if key.AuthVersion <= 0 {
		key.AuthVersion = 1
	}
	if strings.TrimSpace(key.CredentialID) != "" {
		return
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		key.CredentialID = fmt.Sprintf("ak_%d", time.Now().UnixNano())
		return
	}
	key.CredentialID = "ak_" + hex.EncodeToString(random)
}

func generateSessionToken() string {
	bytes := make([]byte, 24)
	if _, err := rand.Read(bytes); err != nil {
		return fmt.Sprintf("session-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

func safeCodePart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '-' || r == '_':
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "code"
	}
	return builder.String()
}

type ModuleRegistrationResult struct {
	Device ModuleDeviceView `json:"device"`
}

type ModuleDeviceView struct {
	Name           string `json:"name"`
	WxID           string `json:"wxid,omitempty"`
	Nickname       string `json:"nickname,omitempty"`
	WeChatNickname string `json:"wechat_nickname,omitempty"`
}

func apiKeyDeviceName(key config.APIKey) string {
	if value := strings.TrimSpace(key.Device); value != "" {
		return value
	}
	return "device-" + safeCodePart(key.Code)
}

func moduleDeviceView(device config.Device) ModuleDeviceView {
	return ModuleDeviceView{
		Name:           device.Name,
		WxID:           device.WxID,
		Nickname:       device.Nickname,
		WeChatNickname: device.WeChatNickname,
	}
}
