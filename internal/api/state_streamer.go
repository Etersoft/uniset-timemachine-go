package api

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/pv/uniset-timemachine-go/internal/replay"
	"github.com/pv/uniset-timemachine-go/internal/sharedmem"
	"github.com/pv/uniset-timemachine-go/pkg/config"
)

// SensorInfo описывает метаданные датчика для стриминга в UI.
type SensorInfo struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	TextName string `json:"textname,omitempty"`
}

type sensorValue struct {
	info        SensorInfo
	value       float64
	hasValue    bool
	stepID      int64
	stepTs      time.Time
	lastChanged time.Time
}

type wsMessage struct {
	Type     string        `json:"type"`
	StepID   int64         `json:"step_id,omitempty"`
	StepTs   string        `json:"step_ts,omitempty"`
	StepUnix int64         `json:"step_unix,omitempty"`
	Updates  []wsSensorRow `json:"updates,omitempty"`
}

type wsSensorRow struct {
	ID       int64   `json:"id"`
	Name     string  `json:"name,omitempty"`     // snapshot only
	TextName string  `json:"textname,omitempty"` // snapshot only
	Value    float64 `json:"value,omitempty"`
	HasValue bool    `json:"has_value,omitempty"`
}

// StateStreamer копит состояние датчиков и отдаёт изменения через WebSocket.
type StateStreamer struct {
	mu      sync.RWMutex
	sensors map[int64]SensorInfo
	state   map[int64]*sensorValue
	clients map[*wsClient]struct{}
	lastID  int64
	lastTs  time.Time
}

// NewStateStreamer создаёт пустой стример.
func NewStateStreamer() *StateStreamer {
	return &StateStreamer{
		sensors: map[int64]SensorInfo{},
		state:   map[int64]*sensorValue{},
		clients: map[*wsClient]struct{}{},
	}
}

// BuildSensorInfo подготавливает карту ID → SensorInfo из конфига.
func BuildSensorInfo(cfg *config.Config, ids []int64) map[int64]SensorInfo {
	infos := make(map[int64]SensorInfo, len(ids))
	for _, id := range ids {
		var name string
		var meta config.SensorMeta
		if cfg != nil {
			name, _ = cfg.NameByID(id)
			meta = cfg.SensorMeta[name]
		}
		if name == "" {
			name = fmt.Sprintf("id%d", id)
		}
		infos[id] = SensorInfo{
			ID:       id,
			Name:     name,
			TextName: meta.TextName,
		}
	}
	return infos
}

// Reset очищает состояние и публикует событие reset клиентам.
func (s *StateStreamer) Reset(infos map[int64]SensorInfo) {
	s.mu.Lock()
	s.sensors = infos
	s.state = map[int64]*sensorValue{}
	s.lastID = 0
	s.lastTs = time.Time{}
	msg := wsMessage{Type: "reset"}
	s.broadcastLocked(msg)
	s.mu.Unlock()
}

// Publish применяет обновления шага и рассылает их по WebSocket.
func (s *StateStreamer) Publish(step replay.StepInfo, updates []sharedmem.SensorUpdate) {
	s.mu.Lock()
	s.lastID = step.StepID
	s.lastTs = step.StepTs

	rows := make([]wsSensorRow, 0, len(updates))
	for _, upd := range updates {
		info, ok := s.sensors[upd.ID]
		if !ok {
			info = SensorInfo{ID: upd.ID, Name: fmt.Sprintf("id%d", upd.ID)}
			s.sensors[upd.ID] = info
		}
		val := s.state[upd.ID]
		if val == nil {
			val = &sensorValue{info: info}
			s.state[upd.ID] = val
		}
		val.info = info
		val.value = upd.Value
		val.hasValue = true
		val.stepID = step.StepID
		val.stepTs = step.StepTs
		val.lastChanged = step.StepTs

		rows = append(rows, wsSensorRow{
			ID:       info.ID,
			Value:    upd.Value,
			HasValue: true,
		})
	}

	msg := wsMessage{
		Type:     "updates",
		StepID:   step.StepID,
		StepTs:   step.StepTs.Format(time.RFC3339),
		StepUnix: unixMs(step.StepTs),
		Updates:  rows,
	}
	// Даже при пустых обновлениях отправляем метаданные шага, чтобы UI видел прогресс.
	if len(rows) == 0 {
		msg.Updates = nil
	}
	s.broadcastLocked(msg)
	s.mu.Unlock()
}

// ServeWS обрабатывает подключение клиента WebSocket.
func (s *StateStreamer) ServeWS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	conn, rw, err := websocketUpgrade(w, r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	client := newWSClient(conn, rw)
	s.addClient(client)

	if err := client.writeJSON(s.snapshotMessage()); err != nil {
		s.removeClient(client)
		return
	}

	go client.writePump(func() {
		s.removeClient(client)
	})
}

func (s *StateStreamer) addClient(c *wsClient) {
	s.mu.Lock()
	s.clients[c] = struct{}{}
	s.mu.Unlock()
}

func (s *StateStreamer) removeClient(c *wsClient) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
	c.close()
}

func (s *StateStreamer) snapshotMessage() wsMessage {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows := make([]wsSensorRow, 0, len(s.sensors))
	for id, info := range s.sensors {
		val := s.state[id]
		row := wsSensorRow{
			ID:       info.ID,
			Name:     info.Name,
			TextName: info.TextName,
			HasValue: val != nil && val.hasValue,
		}
		if val != nil && val.hasValue {
			row.Value = val.value
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name == rows[j].Name {
			return rows[i].ID < rows[j].ID
		}
		return rows[i].Name < rows[j].Name
	})

	return wsMessage{
		Type:     "snapshot",
		StepID:   s.lastID,
		StepTs:   formatTime(s.lastTs),
		StepUnix: unixMs(s.lastTs),
		Updates:  rows,
	}
}

func (s *StateStreamer) broadcastLocked(msg wsMessage) {
	if len(s.clients) == 0 {
		return
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	for c := range s.clients {
		select {
		case c.send <- data:
		default:
			// Клиент не успевает читать — отрубаем.
			go s.removeClient(c)
		}
	}
}

func formatTime(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

func unixMs(ts time.Time) int64 {
	if ts.IsZero() {
		return 0
	}
	return ts.UTC().UnixMilli()
}

// --- WebSocket utils (минимальная реализация только для server-push) ---

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func websocketUpgrade(w http.ResponseWriter, r *http.Request) (net.Conn, *bufio.ReadWriter, error) {
	if !headerContains(r.Header, "Connection", "Upgrade") || !headerContains(r.Header, "Upgrade", "websocket") {
		return nil, nil, errors.New("upgrade request expected")
	}
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if key == "" {
		return nil, nil, errors.New("missing Sec-WebSocket-Key")
	}
	accept := computeAcceptKey(key)

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("http hijacking not supported")
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return nil, nil, err
	}
	if rw == nil {
		rw = bufio.NewReadWriter(bufio.NewReader(conn), bufio.NewWriter(conn))
	}

	response := fmt.Sprintf("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	if _, err := rw.WriteString(response); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return nil, nil, err
	}
	return conn, rw, nil
}

func computeAcceptKey(key string) string {
	h := sha1.Sum([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h[:])
}

func headerContains(h http.Header, name, value string) bool {
	for _, v := range h.Values(name) {
		for _, part := range strings.Split(v, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return true
			}
		}
	}
	return false
}

type wsClient struct {
	conn net.Conn
	rw   *bufio.ReadWriter
	send chan []byte
	once sync.Once
}

func newWSClient(conn net.Conn, rw *bufio.ReadWriter) *wsClient {
	return &wsClient{
		conn: conn,
		rw:   rw,
		send: make(chan []byte, 32),
	}
}

func (c *wsClient) writeJSON(msg wsMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return writeTextFrame(c.rw, data)
}

func (c *wsClient) writePump(onClose func()) {
	defer onClose()
	for msg := range c.send {
		if err := writeTextFrame(c.rw, msg); err != nil {
			return
		}
	}
}

func (c *wsClient) close() {
	c.once.Do(func() {
		_ = c.conn.Close()
		close(c.send)
	})
}

func writeTextFrame(w *bufio.ReadWriter, payload []byte) error {
	var header [10]byte
	header[0] = 0x81 // FIN + text frame
	var headerLen int
	switch {
	case len(payload) < 126:
		header[1] = byte(len(payload))
		headerLen = 2
	case len(payload) <= 0xFFFF:
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:], uint16(len(payload)))
		headerLen = 4
	default:
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:], uint64(len(payload)))
		headerLen = 10
	}
	if _, err := w.Write(header[:headerLen]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return w.Flush()
}
