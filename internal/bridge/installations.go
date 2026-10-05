package bridge

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

// One device binding (API Key -> device) may be used by several phones over
// time. Exactly one installation is current ("active"); other installations
// that keep registering are "standby". A standby installation takes over when
// the user opens WeChat on it (foreground claim), when an operator requests a
// switch to it, or when the active installation stops heart-beating.
const (
	DefaultModuleTakeoverAfter = 2 * time.Minute
	standbyFreshness           = 90 * time.Second
	switchRequestTTL           = 10 * time.Minute
	installationListWindow     = 30 * 24 * time.Hour
	maxInstallationsPerDevice  = 10

	TakeoverForeground = "foreground"
	takeoverAdmin      = "admin"
	takeoverStale      = "stale"

	InstallationStateActive  = "active"
	InstallationStateStandby = "standby"
	InstallationStateOffline = "offline"
)

var (
	ErrDeviceStandby         = errors.New("device binding is in use by another phone; this installation is on standby")
	ErrInstallationNotFound  = errors.New("installation not found for this device")
	ErrInstallationActive    = errors.New("installation is already the active phone")
	ErrInstallationSwitchArg = errors.New("device and installation_id are required")
)

// StandbyError rejects a registration while another installation is active.
// It carries the active phone summary so the module can tell its user.
type StandbyError struct {
	Active ModuleInstallationView
}

func (e *StandbyError) Error() string { return ErrDeviceStandby.Error() }
func (e *StandbyError) Unwrap() error { return ErrDeviceStandby }

type InstallationInfo struct {
	DeviceModel    string
	AndroidVersion string
	WeChatVersion  string
	ModuleVersion  string
}

// InstallationSeen records one registration attempt by an installation.
type InstallationSeen struct {
	Device         string
	InstanceID     string
	OwnerWxID      string
	WeChatNickname string
	Info           InstallationInfo
	State          string
	Seen           time.Time
	// OnlyIfAbsent creates a row without refreshing an existing one. It gives a
	// binding created before installation tracking one full takeover window.
	OnlyIfAbsent bool
}

type InstallationRecord struct {
	ID             int64
	Device         string
	InstanceID     string
	OwnerWxID      string
	WeChatNickname string
	Info           InstallationInfo
	LastState      string
	LastSeenAt     time.Time
	LastActiveAt   time.Time
	Current        bool
}

type AccountSwitchRequest struct {
	Device     string
	InstanceID string
	ExpiresAt  time.Time
}

type ModuleInstallationView struct {
	ID             int64  `json:"id"`
	ShortID        string `json:"short_id"`
	Active         bool   `json:"active"`
	State          string `json:"state"`
	OwnerWxID      string `json:"owner_wxid,omitempty"`
	WeChatNickname string `json:"wechat_nickname,omitempty"`
	DeviceModel    string `json:"device_model,omitempty"`
	AndroidVersion string `json:"android_version,omitempty"`
	WeChatVersion  string `json:"wechat_version,omitempty"`
	ModuleVersion  string `json:"module_version,omitempty"`
	LastSeenAt     string `json:"last_seen_at,omitempty"`
	LastActiveAt   string `json:"last_active_at,omitempty"`
	SwitchPending  bool   `json:"switch_pending"`
}

type ModuleSwitchRequestView struct {
	InstallationID int64  `json:"installation_id"`
	ExpiresAt      string `json:"expires_at"`
}

func (req ModuleRegistrationRequest) installationInfo() InstallationInfo {
	return InstallationInfo{
		DeviceModel:    req.DeviceModel,
		AndroidVersion: req.AndroidVersion,
		WeChatVersion:  req.WeChatVersion,
		ModuleVersion:  req.ModuleVersion,
	}
}

func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

func (s *Service) accountStore() (AccountSessionStore, bool) {
	store, ok := s.persistence.(AccountSessionStore)
	return store, ok
}

func installationKey(device, instance string) string {
	return device + "\x00" + instance
}

func shortInstanceID(instance string) string {
	instance = strings.ReplaceAll(instance, "-", "")
	if len(instance) > 8 {
		return instance[:8]
	}
	return instance
}

// --- Helpers below must run while the device account lock is held. ---

func (s *Service) maxInstallationGeneration(ctx context.Context, device, instance string) (int64, bool, error) {
	if store, ok := s.accountStore(); ok {
		return store.MaxInstallationGeneration(ctx, device, instance)
	}
	var max int64
	found := false
	for _, b := range s.accountHistory {
		if b.Device == device && b.InstanceID == instance && (!found || b.Generation > max) {
			max, found = b.Generation, true
		}
	}
	return max, found, nil
}

func (s *Service) touchInstallation(ctx context.Context, seen InstallationSeen) error {
	if seen.Seen.IsZero() {
		seen.Seen = s.now()
	}
	if store, ok := s.accountStore(); ok {
		return store.TouchInstallation(ctx, seen)
	}
	key := installationKey(seen.Device, seen.InstanceID)
	rec, ok := s.accountInstallations[key]
	if ok && seen.OnlyIfAbsent {
		return nil
	}
	if !ok {
		s.accountInstallSeq++
		rec = &InstallationRecord{ID: s.accountInstallSeq, Device: seen.Device, InstanceID: seen.InstanceID}
		s.accountInstallations[key] = rec
	}
	rec.OwnerWxID = firstNonEmpty(seen.OwnerWxID, rec.OwnerWxID)
	rec.WeChatNickname = firstNonEmpty(seen.WeChatNickname, rec.WeChatNickname)
	rec.Info.DeviceModel = firstNonEmpty(seen.Info.DeviceModel, rec.Info.DeviceModel)
	rec.Info.AndroidVersion = firstNonEmpty(seen.Info.AndroidVersion, rec.Info.AndroidVersion)
	rec.Info.WeChatVersion = firstNonEmpty(seen.Info.WeChatVersion, rec.Info.WeChatVersion)
	rec.Info.ModuleVersion = firstNonEmpty(seen.Info.ModuleVersion, rec.Info.ModuleVersion)
	rec.LastState = seen.State
	rec.LastSeenAt = seen.Seen
	if seen.State == InstallationStateActive {
		rec.LastActiveAt = seen.Seen
	}
	return nil
}

func (s *Service) findInstallation(ctx context.Context, device, instance string) (InstallationRecord, bool, error) {
	if store, ok := s.accountStore(); ok {
		return store.FindInstallation(ctx, device, instance)
	}
	rec, ok := s.accountInstallations[installationKey(device, instance)]
	if !ok {
		return InstallationRecord{}, false, nil
	}
	return *rec, true, nil
}

func (s *Service) installationByID(ctx context.Context, device string, id int64) (InstallationRecord, bool, error) {
	if store, ok := s.accountStore(); ok {
		return store.InstallationByID(ctx, device, id)
	}
	for _, rec := range s.accountInstallations {
		if rec.Device == device && rec.ID == id {
			return *rec, true, nil
		}
	}
	return InstallationRecord{}, false, nil
}

func (s *Service) currentSwitchRequest(ctx context.Context, device string) (AccountSwitchRequest, bool, error) {
	var req AccountSwitchRequest
	var ok bool
	if store, isStore := s.accountStore(); isStore {
		var err error
		if req, ok, err = store.CurrentSwitchRequest(ctx, device); err != nil {
			return AccountSwitchRequest{}, false, err
		}
	} else {
		req, ok = s.accountSwitch[device]
	}
	if !ok || !s.now().Before(req.ExpiresAt) {
		return AccountSwitchRequest{}, false, nil
	}
	return req, true, nil
}

func (s *Service) setSwitchRequest(ctx context.Context, req AccountSwitchRequest) error {
	if store, ok := s.accountStore(); ok {
		return store.SetSwitchRequest(ctx, req)
	}
	s.accountSwitch[req.Device] = req
	return nil
}

func (s *Service) clearSwitchRequest(ctx context.Context, device, instance string) error {
	if store, ok := s.accountStore(); ok {
		return store.ClearSwitchRequest(ctx, device, instance)
	}
	if current, ok := s.accountSwitch[device]; ok && (instance == "" || current.InstanceID == instance) {
		delete(s.accountSwitch, device)
	}
	return nil
}

// takeoverReason decides whether another installation may replace the current
// one. An empty reason means the caller stays on standby.
func (s *Service) takeoverReason(ctx context.Context, device string, current AccountBinding, req ModuleRegistrationRequest) (string, error) {
	if req.Takeover == TakeoverForeground && !s.cfg.DisableForegroundTakeover {
		return TakeoverForeground, nil
	}
	switchReq, ok, err := s.currentSwitchRequest(ctx, device)
	if err != nil {
		return "", err
	}
	if ok && switchReq.InstanceID == req.InstanceID {
		return takeoverAdmin, nil
	}
	active, found, err := s.findInstallation(ctx, device, current.InstanceID)
	if err != nil {
		return "", err
	}
	if !found {
		return "", s.touchInstallation(ctx, InstallationSeen{Device: device, InstanceID: current.InstanceID,
			OwnerWxID: current.OwnerWxID, State: InstallationStateActive, OnlyIfAbsent: true})
	}
	if s.now().Sub(active.LastSeenAt) > s.takeoverAfter {
		return takeoverStale, nil
	}
	return "", nil
}

func (s *Service) standbyError(ctx context.Context, device string, current AccountBinding) error {
	active, found, err := s.findInstallation(ctx, device, current.InstanceID)
	if err != nil || !found {
		active = InstallationRecord{Device: device, InstanceID: current.InstanceID, OwnerWxID: current.OwnerWxID}
	}
	active.Current = true
	return &StandbyError{Active: s.installationView(active, s.now(), "")}
}

// --- Admin views and operations (take their own locks). ---

func (s *Service) installationView(rec InstallationRecord, now time.Time, switchTarget string) ModuleInstallationView {
	state := InstallationStateOffline
	age := now.Sub(rec.LastSeenAt)
	if !rec.LastSeenAt.IsZero() {
		if rec.Current && age <= s.takeoverAfter {
			state = InstallationStateActive
		} else if !rec.Current && age <= standbyFreshness {
			state = InstallationStateStandby
		}
	}
	return ModuleInstallationView{
		ID:             rec.ID,
		ShortID:        shortInstanceID(rec.InstanceID),
		Active:         rec.Current,
		State:          state,
		OwnerWxID:      rec.OwnerWxID,
		WeChatNickname: rec.WeChatNickname,
		DeviceModel:    rec.Info.DeviceModel,
		AndroidVersion: rec.Info.AndroidVersion,
		WeChatVersion:  rec.Info.WeChatVersion,
		ModuleVersion:  rec.Info.ModuleVersion,
		LastSeenAt:     formatTimeRFC3339Nano(rec.LastSeenAt),
		LastActiveAt:   formatTimeRFC3339Nano(rec.LastActiveAt),
		SwitchPending:  switchTarget != "" && switchTarget == rec.InstanceID,
	}
}

func formatTimeRFC3339Nano(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *Service) loadInstallations(ctx context.Context) ([]InstallationRecord, []AccountSwitchRequest, error) {
	since := s.now().Add(-installationListWindow)
	if store, ok := s.accountStore(); ok {
		records, err := store.ListInstallations(ctx, since)
		if err != nil {
			return nil, nil, err
		}
		requests, err := store.ListSwitchRequests(ctx)
		if err != nil {
			return nil, nil, err
		}
		return records, requests, nil
	}
	s.accountMu.Lock()
	defer s.accountMu.Unlock()
	records := make([]InstallationRecord, 0, len(s.accountInstallations))
	for _, rec := range s.accountInstallations {
		copyRec := *rec
		copyRec.Current = s.accountCurrent[rec.Device].InstanceID == rec.InstanceID
		if copyRec.Current || copyRec.LastSeenAt.After(since) {
			records = append(records, copyRec)
		}
	}
	requests := make([]AccountSwitchRequest, 0, len(s.accountSwitch))
	for _, req := range s.accountSwitch {
		requests = append(requests, req)
	}
	return records, requests, nil
}

// AttachInstallations adds the per-device phone list and pending switch to
// module status rows.
func (s *Service) AttachInstallations(ctx context.Context, statuses []ModuleStatusView) error {
	if len(statuses) == 0 {
		return nil
	}
	records, requests, err := s.loadInstallations(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	targets := map[string]AccountSwitchRequest{}
	for _, req := range requests {
		if now.Before(req.ExpiresAt) {
			targets[req.Device] = req
		}
	}
	byDevice := map[string][]InstallationRecord{}
	for _, rec := range records {
		byDevice[rec.Device] = append(byDevice[rec.Device], rec)
	}
	for index := range statuses {
		device := statuses[index].Device
		list := byDevice[device]
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].Current != list[j].Current {
				return list[i].Current
			}
			return list[i].LastSeenAt.After(list[j].LastSeenAt)
		})
		if len(list) > maxInstallationsPerDevice {
			list = list[:maxInstallationsPerDevice]
		}
		target := targets[device]
		views := make([]ModuleInstallationView, 0, len(list))
		for _, rec := range list {
			view := s.installationView(rec, now, target.InstanceID)
			if view.SwitchPending {
				statuses[index].SwitchRequest = &ModuleSwitchRequestView{InstallationID: view.ID, ExpiresAt: formatTimeRFC3339Nano(target.ExpiresAt)}
			}
			views = append(views, view)
		}
		statuses[index].Installations = views
	}
	return nil
}

// RequestInstallationSwitch asks a standby phone to take over at its next
// registration heartbeat. The request expires if that phone does not appear.
func (s *Service) RequestInstallationSwitch(ctx context.Context, device string, installationID int64) (view ModuleSwitchRequestView, err error) {
	device = strings.TrimSpace(device)
	if device == "" || installationID <= 0 {
		return view, ErrInstallationSwitchArg
	}
	err = s.withAccountLock(ctx, device, func(ctx context.Context) error {
		rec, ok, err := s.installationByID(ctx, device, installationID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrInstallationNotFound
		}
		current, protected, err := s.currentBinding(ctx, device)
		if err != nil {
			return err
		}
		if protected && current.InstanceID == rec.InstanceID {
			if err := s.clearSwitchRequest(ctx, device, ""); err != nil {
				return err
			}
			return ErrInstallationActive
		}
		req := AccountSwitchRequest{Device: device, InstanceID: rec.InstanceID, ExpiresAt: s.now().Add(switchRequestTTL)}
		if err := s.setSwitchRequest(ctx, req); err != nil {
			return err
		}
		view = ModuleSwitchRequestView{InstallationID: rec.ID, ExpiresAt: formatTimeRFC3339Nano(req.ExpiresAt)}
		return nil
	})
	return view, err
}

func truncateRunes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}
