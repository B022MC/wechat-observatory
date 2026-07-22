package bridge

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	websocketGUID      = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsOpText      byte = 0x1
	wsOpClose     byte = 0x8
	wsOpPing      byte = 0x9
	wsOpPong      byte = 0xA
	wsMaxPayload       = 1 << 20
)

type outboxWSMessage struct {
	Type  string             `json:"type"`
	OK    bool               `json:"ok,omitempty"`
	Time  int64              `json:"time,omitempty"`
	Error string             `json:"error,omitempty"`
	Items []ModuleOutboxItem `json:"items,omitempty"`
	Ack   *ModuleAckRequest  `json:"ack,omitempty"`
}

func (s *HTTPServer) outboxWebSocket(w http.ResponseWriter, r *http.Request) {
	apiKey := strings.TrimSpace(r.URL.Query().Get("api_key"))
	device := strings.TrimSpace(r.URL.Query().Get("device"))
	wxid := strings.TrimSpace(r.URL.Query().Get("wxid"))
	if device == "" {
		device = s.service.DefaultDevice()
	}
	auth, err := s.service.authorizeModuleAPIKey(r.Context(), apiKey)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	device = auth.Device
	session, err := s.service.AcquireOutboxSession(r.Context(), apiKey, device, wxid)
	if err != nil {
		if errors.Is(err, ErrModuleSessionActive) {
			writeError(w, http.StatusConflict, "device_session_active", "device session is active; retry shortly")
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
		return
	}
	conn, err := upgradeWebSocket(w, r)
	if err != nil {
		s.service.ReleaseOutboxSession(context.Background(), session)
		writeError(w, http.StatusBadRequest, "websocket_upgrade_failed", err.Error())
		return
	}
	defer conn.close()
	defer s.service.ReleaseOutboxSession(context.Background(), session)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	notify, unsubscribe := s.service.subscribeOutbox(device)
	defer unsubscribe()

	outgoing := make(chan outboxWSMessage, 8)
	go s.readOutboxWS(ctx, cancel, conn, apiKey, device, wxid, outgoing)

	outgoing <- outboxWSMessage{Type: "ready", OK: true, Time: time.Now().Unix()}
	outgoing <- outboxWSMessage{Type: "wake"}
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()
	poll := time.NewTicker(s.service.OutboxPollInterval())
	defer poll.Stop()
	renewInterval := session.TTL / 3
	if renewInterval <= 0 {
		renewInterval = 5 * time.Second
	}
	renew := time.NewTicker(renewInterval)
	defer renew.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-outgoing:
			if msg.Type == "wake" {
				items, err := s.service.PollOutbox(ctx, ModulePollRequest{APIKey: apiKey, Device: device, WxID: wxid, Limit: 1})
				if err != nil {
					if !conn.writeJSON(outboxWSMessage{Type: "error", Error: err.Error(), Time: time.Now().Unix()}) {
						return
					}
					if isModuleAuthError(err) {
						return
					}
					continue
				}
				if len(items) == 0 {
					continue
				}
				if !conn.writeJSON(outboxWSMessage{Type: "outbox", OK: true, Items: items, Time: time.Now().Unix()}) {
					return
				}
				continue
			}
			if !conn.writeJSON(msg) {
				return
			}
		case <-notify:
			outgoing <- outboxWSMessage{Type: "wake"}
		case <-poll.C:
			outgoing <- outboxWSMessage{Type: "wake"}
		case <-renew.C:
			active, err := s.service.RenewOutboxSession(ctx, session)
			if err != nil || !active {
				_ = conn.writeJSON(outboxWSMessage{Type: "error", Error: "device session lease lost", Time: time.Now().Unix()})
				return
			}
		case <-ping.C:
			if !conn.writeControl(wsOpPing, []byte("ping")) {
				return
			}
			outgoing <- outboxWSMessage{Type: "wake"}
		}
	}
}

func (s *HTTPServer) readOutboxWS(ctx context.Context, cancel context.CancelFunc, conn *wsConn, apiKey string, device string, wxid string, outgoing chan<- outboxWSMessage) {
	defer cancel()
	for {
		payload, op, err := conn.readFrame()
		if err != nil {
			return
		}
		switch op {
		case wsOpClose:
			return
		case wsOpPing:
			if !conn.writeControl(wsOpPong, payload) {
				return
			}
		case wsOpPong:
			continue
		case wsOpText:
			var msg outboxWSMessage
			if err := json.Unmarshal(payload, &msg); err != nil {
				outgoing <- outboxWSMessage{Type: "error", Error: "invalid json: " + err.Error(), Time: time.Now().Unix()}
				continue
			}
			switch strings.ToLower(strings.TrimSpace(msg.Type)) {
			case "ack":
				if msg.Ack == nil {
					outgoing <- outboxWSMessage{Type: "error", Error: "ack payload is required", Time: time.Now().Unix()}
					continue
				}
				msg.Ack.APIKey = apiKey
				msg.Ack.Device = device
				msg.Ack.WxID = wxid
				items, err := s.service.AckOutbox(ctx, *msg.Ack)
				if err != nil {
					outgoing <- outboxWSMessage{Type: "error", Error: err.Error(), Time: time.Now().Unix()}
					if isModuleAuthError(err) {
						return
					}
					continue
				}
				outgoing <- outboxWSMessage{Type: "ack", OK: true, Items: items, Time: time.Now().Unix()}
				outgoing <- outboxWSMessage{Type: "wake"}
			case "poll", "wake":
				outgoing <- outboxWSMessage{Type: "wake"}
			case "pong":
				continue
			default:
				outgoing <- outboxWSMessage{Type: "error", Error: "unknown message type", Time: time.Now().Unix()}
			}
		}
	}
}

func isModuleAuthError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "invalid api key") ||
		strings.Contains(lower, "api key disabled") ||
		strings.Contains(lower, "api_key is required") ||
		strings.Contains(lower, "unknown device") ||
		strings.Contains(lower, "device is required")
}

type wsConn struct {
	conn    net.Conn
	rw      *bufio.ReadWriter
	writeMu sync.Mutex
}

func upgradeWebSocket(w http.ResponseWriter, r *http.Request) (*wsConn, error) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return nil, errors.New("missing websocket upgrade header")
	}
	if !headerHasToken(r.Header.Get("Connection"), "upgrade") {
		return nil, errors.New("missing connection upgrade header")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, errors.New("missing Sec-WebSocket-Key")
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("server does not support hijacking")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, err
	}
	accept := websocketAccept(key)
	response := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n\r\n"
	if _, err := rw.WriteString(response); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &wsConn{conn: conn, rw: rw}, nil
}

func websocketAccept(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func headerHasToken(raw, token string) bool {
	for _, part := range strings.Split(raw, ",") {
		if strings.EqualFold(strings.TrimSpace(part), token) {
			return true
		}
	}
	return false
}

func (c *wsConn) close() {
	_ = c.conn.Close()
}

func (c *wsConn) readFrame() ([]byte, byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(c.rw, header); err != nil {
		return nil, 0, err
	}
	op := header[0] & 0x0F
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7F)
	switch length {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(c.rw, ext); err != nil {
			return nil, 0, err
		}
		length = uint64(binary.BigEndian.Uint16(ext))
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(c.rw, ext); err != nil {
			return nil, 0, err
		}
		length = binary.BigEndian.Uint64(ext)
	}
	if length > wsMaxPayload {
		return nil, 0, errors.New("websocket frame too large")
	}
	mask := []byte(nil)
	if masked {
		mask = make([]byte, 4)
		if _, err := io.ReadFull(c.rw, mask); err != nil {
			return nil, 0, err
		}
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(c.rw, payload); err != nil {
		return nil, 0, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return payload, op, nil
}

func (c *wsConn) writeJSON(payload any) bool {
	data, err := json.Marshal(payload)
	if err != nil {
		return false
	}
	return c.writeFrame(wsOpText, data)
}

func (c *wsConn) writeControl(op byte, payload []byte) bool {
	if len(payload) > 125 {
		payload = payload[:125]
	}
	return c.writeFrame(op, payload)
}

func (c *wsConn) writeFrame(op byte, payload []byte) bool {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	header := []byte{0x80 | op}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(length))
	case length <= 65535:
		header = append(header, 126, byte(length>>8), byte(length))
	default:
		header = append(header, 127, 0, 0, 0, 0, byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	if _, err := c.rw.Write(header); err != nil {
		return false
	}
	if _, err := c.rw.Write(payload); err != nil {
		return false
	}
	return c.rw.Flush() == nil
}
