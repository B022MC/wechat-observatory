package bridge

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"wechat-observatory/internal/config"
)

var ErrAccountSession = errors.New("account session conflict; refresh the current login or use a separate device binding for another installation")

// AccountBinding is immutable for a session. Credentials are references, never raw keys.
// Epoch is assigned by the server: it increases on every binding change of a
// device, across installations, and is the public account_generation.
type AccountBinding struct {
	Device, InstanceID, SessionID, OwnerWxID, CredentialID string
	Generation, AuthVersion                                int64
	Epoch                                                  int64
}

// SameSession compares the client-provided identity of a binding, ignoring the
// server-assigned epoch.
func (b AccountBinding) SameSession(other AccountBinding) bool {
	b.Epoch, other.Epoch = 0, 0
	return b == other
}

// AccountSessionStore serializes dependent operations across replicas. Registration
// atomically saves the binding, identity, activity and cancellation of old sends.
type AccountSessionStore interface {
	WithDeviceAccountLock(context.Context, string, func(context.Context) error) error
	CurrentAccountBinding(context.Context, string) (AccountBinding, bool, error)
	FindAccountBinding(context.Context, string, string) (AccountBinding, bool, error)
	// RegisterAccountBinding stores an accepted binding and returns it with its epoch.
	RegisterAccountBinding(context.Context, AccountBinding, config.Device, string) (AccountBinding, error)
	MaxInstallationGeneration(ctx context.Context, device, instanceID string) (int64, bool, error)
	TouchInstallation(context.Context, InstallationSeen) error
	FindInstallation(ctx context.Context, device, instanceID string) (InstallationRecord, bool, error)
	InstallationByID(ctx context.Context, device string, id int64) (InstallationRecord, bool, error)
	ListInstallations(ctx context.Context, since time.Time) ([]InstallationRecord, error)
	CurrentSwitchRequest(ctx context.Context, device string) (AccountSwitchRequest, bool, error)
	ListSwitchRequests(context.Context) ([]AccountSwitchRequest, error)
	SetSwitchRequest(context.Context, AccountSwitchRequest) error
	ClearSwitchRequest(ctx context.Context, device, instanceID string) error
}

// AccountOutboxCanceller cancels work that the previous binding may have
// started: every leased row, and pending rows owned by another WeChat account.
// Pending rows for keepOwner move to the new installation.
type AccountOutboxCanceller interface {
	CancelAccountOutbox(ctx context.Context, device, keepOwner string) error
}

func (s *Service) withAccountLock(ctx context.Context, device string, fn func(context.Context) error) error {
	if store, ok := s.persistence.(AccountSessionStore); ok {
		return store.WithDeviceAccountLock(ctx, device, fn)
	}
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	return fn(ctx)
}

func (s *Service) currentBinding(ctx context.Context, device string) (AccountBinding, bool, error) {
	if store, ok := s.persistence.(AccountSessionStore); ok {
		return store.CurrentAccountBinding(ctx, device)
	}
	b, ok := s.accountCurrent[device]
	return b, ok, nil
}

func (s *Service) findBinding(ctx context.Context, device, session string) (AccountBinding, bool, error) {
	if store, ok := s.persistence.(AccountSessionStore); ok {
		return store.FindAccountBinding(ctx, device, session)
	}
	b, ok := s.accountHistory[device+"\x00"+session]
	return b, ok, nil
}

func validAccountToken(value string) bool {
	if len(value) < 16 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func (s *Service) RegisterModule(ctx context.Context, req ModuleRegistrationRequest) (result *ModuleRegistrationResult, err error) {
	req, err = req.Validate(s.cfg.DefaultDevice)
	if err != nil {
		return nil, err
	}
	key, ok, err := s.lookupAPIKey(ctx, req.APIKey)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("invalid api key")
	}
	if key.Disabled {
		return nil, errors.New("api key disabled")
	}
	deviceName := apiKeyDeviceName(key)
	err = s.withAccountLock(ctx, deviceName, func(ctx context.Context) error {
		key, ok, err := s.lookupAPIKey(ctx, req.APIKey)
		if err != nil {
			return err
		}
		if !ok || key.Disabled || apiKeyDeviceName(key) != deviceName {
			return ErrAccountSession
		}
		current, protected, err := s.currentBinding(ctx, deviceName)
		if err != nil {
			return err
		}
		if req.AccountSession == "" && req.InstanceID == "" && req.AccountGeneration == 0 {
			if protected {
				return ErrAccountSession
			}
			result, err = s.registerModule(ctx, req)
			return err
		}
		if !validAccountToken(req.AccountSession) || !validAccountToken(req.InstanceID) || req.AccountGeneration <= 0 || strings.HasPrefix(req.WxID, "acct_") {
			return ErrAccountSession
		}
		binding := AccountBinding{Device: deviceName, InstanceID: req.InstanceID, SessionID: req.AccountSession,
			Generation: req.AccountGeneration, OwnerWxID: req.WxID, CredentialID: key.CredentialID, AuthVersion: key.AuthVersion}
		previous, exists, err := s.findBinding(ctx, deviceName, req.AccountSession)
		if err != nil {
			return err
		}
		if exists && !previous.SameSession(binding) {
			return ErrAccountSession
		}
		// Within one installation generations only move forward, even when another
		// installation is current: a delayed old session must never come back.
		maxGeneration, seenBefore, err := s.maxInstallationGeneration(ctx, deviceName, req.InstanceID)
		if err != nil {
			return err
		}
		if seenBefore && (binding.Generation < maxGeneration || (binding.Generation == maxGeneration && !exists)) {
			return ErrAccountSession
		}
		sameInstallation := protected && current.InstanceID == binding.InstanceID
		if sameInstallation && (binding.Generation < current.Generation ||
			(binding.Generation == current.Generation && !current.SameSession(binding))) {
			return ErrAccountSession
		}
		seen := InstallationSeen{Device: deviceName, InstanceID: req.InstanceID, OwnerWxID: req.WxID,
			WeChatNickname: req.Nickname, Info: req.installationInfo()}
		takeover := ""
		if protected && !sameInstallation {
			if takeover, err = s.takeoverReason(ctx, deviceName, current, req); err != nil {
				return err
			}
			if takeover == "" {
				seen.State = InstallationStateStandby
				if err := s.touchInstallation(ctx, seen); err != nil {
					return err
				}
				return s.standbyError(ctx, deviceName, current)
			}
		}
		device, _, err := s.lookupDevice(ctx, deviceName)
		if err != nil {
			return err
		}
		device.Name = deviceName
		device.WxID = req.WxID
		device.Nickname = firstNonEmpty(device.Nickname, key.Nickname, deviceName)
		device.WeChatNickname = req.Nickname
		if store, ok := s.persistence.(AccountSessionStore); ok {
			if binding, err = store.RegisterAccountBinding(ctx, binding, device, req.APIKey); err != nil {
				return err
			}
		} else {
			if s.persistence != nil {
				return errors.New("durable account sessions are required by this module")
			}
			if binding, err = s.registerMemoryBinding(ctx, binding, current, protected, exists); err != nil {
				return err
			}
		}
		seen.State = InstallationStateActive
		if err := s.touchInstallation(ctx, seen); err != nil {
			return err
		}
		if err := s.clearSwitchRequest(ctx, deviceName, binding.InstanceID); err != nil {
			return err
		}
		s.setCachedDevice(device)
		result = &ModuleRegistrationResult{Device: moduleDeviceView(device), AccountSession: binding.SessionID,
			AccountGeneration: binding.Generation, Takeover: takeover}
		return nil
	})
	return result, err
}

func (s *Service) registerMemoryBinding(ctx context.Context, binding, current AccountBinding, protected, exists bool) (AccountBinding, error) {
	if protected && current.SameSession(binding) {
		binding.Epoch = current.Epoch
		return binding, nil
	}
	canceller, ok := s.outbox.(AccountOutboxCanceller)
	if !ok {
		return binding, errors.New("outbox does not support account transitions")
	}
	if err := canceller.CancelAccountOutbox(ctx, binding.Device, binding.OwnerWxID); err != nil {
		return binding, err
	}
	epoch := current.Epoch
	for _, b := range s.accountHistory {
		if b.Device == binding.Device && b.Epoch > epoch {
			epoch = b.Epoch
		}
	}
	binding.Epoch = epoch + 1
	s.accountCurrent[binding.Device] = binding
	if !exists {
		s.accountHistory[binding.Device+"\x00"+binding.SessionID] = binding
	}
	return binding, nil
}

// historical is only for immutable observed messages. Snapshots, delivery and ACKs
// must belong to the current login even if the user has switched A -> B -> A.
func (s *Service) withModuleAccount(ctx context.Context, apiKey, owner, session string, historical bool, fn func(context.Context, string) error) error {
	auth, err := s.authorizeModuleAPIKey(ctx, apiKey)
	if err != nil {
		return err
	}
	return s.withAccountLock(ctx, auth.Device, func(ctx context.Context) error {
		fresh, err := s.authorizeModuleAPIKey(ctx, apiKey)
		if err != nil {
			return err
		}
		if fresh.Device != auth.Device {
			return ErrAccountSession
		}
		current, protected, err := s.currentBinding(ctx, fresh.Device)
		if err != nil {
			return err
		}
		owner = strings.TrimSpace(owner)
		if session == "" {
			if protected {
				return ErrAccountSession
			}
			active := s.deviceWxID(ctx, fresh.Device)
			if owner == "" {
				owner = active
			}
			if owner == "" || active != "" && owner != active {
				return fmt.Errorf("%w: module wxid is not current device wxid", ErrAccountSession)
			}
			return fn(ctx, owner)
		}
		binding, found, err := s.findBinding(ctx, fresh.Device, session)
		if err != nil {
			return err
		}
		if !protected || !found || binding.CredentialID != fresh.Key.CredentialID || binding.AuthVersion != fresh.Key.AuthVersion ||
			owner != binding.OwnerWxID || (!historical && !binding.SameSession(current)) {
			return ErrAccountSession
		}
		return fn(ctx, binding.OwnerWxID)
	})
}

func (s *Service) Ingest(ctx context.Context, event MessageEvent) (result *IngestResult, err error) {
	if event.Direction == "" {
		event.Direction = DirectionRecv
	}
	self := strings.TrimSpace(event.To)
	if event.Direction == DirectionSent {
		self = strings.TrimSpace(event.From)
	}
	owner := strings.TrimSpace(event.OwnerWxID)
	if owner != "" && self != "" && owner != self {
		return nil, ErrAccountSession
	}
	if owner == "" {
		owner = self
	}
	err = s.withModuleAccount(ctx, event.APIKey, owner, event.AccountSession, true, func(ctx context.Context, owner string) error {
		if event.AccountSession != "" && (self == "" || self != owner) {
			return ErrAccountSession
		}
		event.OwnerWxID = owner
		result, err = s.ingest(ctx, event)
		return err
	})
	return result, err
}

func (s *Service) RecordModuleContacts(ctx context.Context, req ModuleContactSnapshotRequest) (count int, err error) {
	err = s.withModuleAccount(ctx, req.APIKey, req.WxID, req.AccountSession, false, func(ctx context.Context, owner string) error {
		req.WxID = owner
		count, err = s.recordModuleContacts(ctx, req)
		return err
	})
	return
}

func (s *Service) PollOutbox(ctx context.Context, req ModulePollRequest) (items []ModuleOutboxItem, err error) {
	err = s.withModuleAccount(ctx, req.APIKey, req.WxID, req.AccountSession, false, func(ctx context.Context, owner string) error {
		req.WxID = owner
		items, err = s.pollOutbox(ctx, req)
		return err
	})
	return
}

func (s *Service) AckOutbox(ctx context.Context, req ModuleAckRequest) (items []ModuleOutboxItem, err error) {
	err = s.withModuleAccount(ctx, req.APIKey, req.WxID, req.AccountSession, false, func(ctx context.Context, owner string) error {
		req.WxID = owner
		items, err = s.ackOutbox(ctx, req)
		return err
	})
	return
}

func (s *Service) SendText(ctx context.Context, req SendTextRequest) (id int64, err error) {
	device := firstNonEmpty(strings.TrimSpace(req.Device), s.cfg.DefaultDevice)
	req.Device = device
	err = s.withAccountLock(ctx, device, func(ctx context.Context) error {
		if req.AccountGeneration != nil {
			current, _, readErr := s.currentBinding(ctx, device)
			if readErr != nil {
				return readErr
			}
			if *req.AccountGeneration != current.Epoch {
				return ErrAccountSession
			}
		}
		id, err = s.sendText(ctx, req)
		return err
	})
	return
}

func (s *Service) AcquireOutboxSession(ctx context.Context, apiKey, device, owner string, accountSession ...string) (lease ModuleSessionLease, err error) {
	session := ""
	if len(accountSession) > 0 {
		session = accountSession[0]
	}
	err = s.withModuleAccount(ctx, apiKey, owner, session, false, func(ctx context.Context, owner string) error {
		lease, err = s.acquireOutboxSession(ctx, apiKey, device, owner)
		return err
	})
	return
}
