package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strings"
)

// wsMessage reflects the server payload for snapshot/updates.
type wsMessage struct {
	Type     string            `json:"type"`
	StepID   int64             `json:"step_id"`
	StepTs   string            `json:"step_ts"`
	StepUnix int64             `json:"step_unix"`
	Updates  []wsMessageUpdate `json:"updates"`
}

type wsMessageUpdate struct {
	ID       int64   `json:"id"`
	Value    float64 `json:"value"`
	HasValue bool    `json:"has_value"`
	Name     string  `json:"name,omitempty"`
	TextName string  `json:"textname,omitempty"`
}

func main() {
	var (
		raw    bool
		limit  int
		urlStr string
	)
	flag.StringVar(&urlStr, "url", "ws://127.0.0.1:19001/api/v2/ws/state", "WebSocket URL of timemachine server")
	flag.BoolVar(&raw, "raw", false, "print raw JSON messages")
	flag.IntVar(&limit, "limit", 0, "stop after N update messages (0 = infinite)")
	flag.Parse()

	u, err := url.Parse(urlStr)
	if err != nil {
		log.Fatalf("invalid url: %v", err)
	}
	if u.Scheme != "ws" && u.Scheme != "wss" {
		log.Fatalf("url must start with ws:// or wss://")
	}
	addr := u.Host
	if !strings.Contains(addr, ":") {
		if u.Scheme == "wss" {
			addr += ":443"
		} else {
			addr += ":80"
		}
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if err := sendHandshake(conn, u); err != nil {
		log.Fatalf("handshake: %v", err)
	}
	log.Printf("connected to %s", urlStr)

	reader := bufio.NewReader(conn)
	updatesSeen := 0
	for {
		op, payload, err := readFrame(reader)
		if err != nil {
			if err == io.EOF {
				log.Println("connection closed by peer")
				return
			}
			log.Fatalf("read frame: %v", err)
		}
		if op == 0x8 { // close frame
			log.Println("received close frame")
			return
		}
		if op != 0x1 {
			continue
		}

		if raw {
			fmt.Println(string(payload))
			continue
		}

		var msg wsMessage
		if err := json.Unmarshal(payload, &msg); err != nil {
			log.Printf("invalid json: %v", err)
			continue
		}

		switch strings.ToLower(msg.Type) {
		case "snapshot":
			log.Printf("snapshot: step=%d ts=%s updates=%d", msg.StepID, msg.StepTs, len(msg.Updates))
		case "updates":
			updatesSeen++
			log.Printf("updates: step=%d ts=%s count=%d", msg.StepID, msg.StepTs, len(msg.Updates))
			for i, u := range msg.Updates {
				if i >= 5 {
					log.Printf("  ... and %d more", len(msg.Updates)-i)
					break
				}
				log.Printf("  id=%d value=%.4f", u.ID, u.Value)
			}
			if limit > 0 && updatesSeen >= limit {
				log.Printf("limit reached (%d updates), exiting", limit)
				return
			}
		default:
			log.Printf("message type=%s (ignored)", msg.Type)
		}
	}
}

func sendHandshake(conn net.Conn, u *url.URL) error {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	secKey := base64.StdEncoding.EncodeToString(key)
	host := u.Host
	if host == "" {
		host = "localhost"
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n", path, host, secKey)
	if _, err := io.WriteString(conn, req); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		return err
	}
	if !strings.HasPrefix(status, "HTTP/1.1 101") {
		return fmt.Errorf("unexpected status: %s", strings.TrimSpace(status))
	}
	var acceptOk bool
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line == "\r\n" {
			break
		}
		if strings.HasPrefix(strings.ToLower(line), "sec-websocket-accept") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				accept := strings.TrimSpace(parts[1])
				expected := computeAccept(secKey)
				acceptOk = accept == expected
			}
		}
	}
	if !acceptOk {
		return fmt.Errorf("handshake failed: Sec-WebSocket-Accept mismatch")
	}
	return nil
}

func computeAccept(key string) string {
	const guid = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	sum := sha1.Sum([]byte(key + guid))
	return base64.StdEncoding.EncodeToString(sum[:])
}

// readFrame reads a single unmasked text frame (server-to-client).
func readFrame(r *bufio.Reader) (opcode byte, payload []byte, err error) {
	h1, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	h2, err := r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	opcode = h1 & 0x0f
	masked := h2&0x80 != 0
	if masked {
		return 0, nil, fmt.Errorf("server sent masked frame")
	}
	length := int(h2 & 0x7f)
	switch length {
	case 126:
		var buf [2]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint16(buf[:]))
	case 127:
		var buf [8]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, nil, err
		}
		length = int(binary.BigEndian.Uint64(buf[:]))
	}
	if length < 0 {
		return 0, nil, fmt.Errorf("invalid length %d", length)
	}
	payload = make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return opcode, payload, nil
}
