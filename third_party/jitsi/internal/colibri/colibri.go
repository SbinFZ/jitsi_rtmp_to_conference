// Package colibri implements the Jitsi Videobridge bridge channel protocol over WebSocket.
//
// This is the modern replacement for the SCTP DataChannel in Jitsi Meet. The WebSocket URL is
// delivered inside the Jingle session-initiate from Jicofo (under <transport><web-socket url=.../>).
// All messages are JSON with a "colibriClass" discriminator.
//
// References:
//   - https://github.com/jitsi/jitsi-videobridge/blob/master/jvb/src/main/kotlin/org/jitsi/videobridge/message/BridgeChannelMessage.kt
package colibri

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

// Conn wraps the colibri-ws connection to a JVB.
//
// Sends are buffered through an outgoing queue with a single writer goroutine.
// Use SendQueueDepth / CanSend / SendQueueCap for backpressure-aware code, and
// TrySendJSON / TrySendRaw for non-blocking sends that drop on overflow.
type Conn struct {
	ws        *websocket.Conn
	url       string
	incoming  chan Message
	rawIn     chan []byte // raw WS frame bytes — fast path for high-throughput consumers
	rawEnable atomic.Bool
	outgoing  chan []byte
	closed    chan struct{}
	closeOnce sync.Once
}

const defaultSendQueue = 1024

// Message is any incoming bridge channel message. RawJSON is the original JSON, Class is the
// colibriClass attribute, Fields holds parsed top-level JSON fields (so callers can read e.g.
// "from", "to", custom payloads, etc.).
type Message struct {
	Class   string
	From    string
	To      string
	Fields  map[string]any
	RawJSON []byte
}

// Dial opens a colibri-ws WebSocket and performs the optional ClientHello handshake.
func Dial(ctx context.Context, url string, client *http.Client) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	ws.SetReadLimit(16 << 20) // 16 MiB
	c := &Conn{
		ws:       ws,
		url:      url,
		incoming: make(chan Message, 64),
		rawIn:    make(chan []byte, 256),
		outgoing: make(chan []byte, defaultSendQueue),
		closed:   make(chan struct{}),
	}
	go c.writeLoop()
	// send ClientHello as required by the protocol
	if err := c.SendJSON(map[string]any{"colibriClass": "ClientHello"}); err != nil {
		_ = ws.Close(websocket.StatusInternalError, "")
		return nil, fmt.Errorf("client hello: %w", err)
	}
	go c.readLoop()
	return c, nil
}

func (c *Conn) writeLoop() {
	for {
		select {
		case <-c.closed:
			return
		case data, ok := <-c.outgoing:
			if !ok {
				return
			}
			if err := c.ws.Write(context.Background(), websocket.MessageText, data); err != nil {
				return
			}
		}
	}
}

// SendQueueDepth returns the number of messages currently queued for sending.
func (c *Conn) SendQueueDepth() int { return len(c.outgoing) }

// SendQueueCap returns the queue capacity.
func (c *Conn) SendQueueCap() int { return cap(c.outgoing) }

// CanSend reports whether the queue has free room (TrySend won't drop).
func (c *Conn) CanSend() bool { return len(c.outgoing) < cap(c.outgoing) }

// URL returns the WebSocket URL used for this connection.
func (c *Conn) URL() string { return c.url }

// Messages returns the incoming message channel. It is closed when the connection is torn down.
func (c *Conn) Messages() <-chan Message { return c.incoming }

// Close closes the WebSocket.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

func (c *Conn) readLoop() {
	defer close(c.incoming)
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		// raw fast-path: skip JSON parsing entirely
		if c.rawEnable.Load() {
			cp := append([]byte(nil), data...)
			select {
			case c.rawIn <- cp:
			case <-c.closed:
				return
			default:
				// drop on overflow — caller is too slow
			}
			continue
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			continue
		}
		class, _ := fields["colibriClass"].(string)
		from, _ := fields["from"].(string)
		to, _ := fields["to"].(string)
		msg := Message{
			Class:   class,
			From:    from,
			To:      to,
			Fields:  fields,
			RawJSON: append([]byte(nil), data...),
		}
		select {
		case c.incoming <- msg:
		case <-c.closed:
			return
		}
	}
}

// EnableRawMode switches the read path into raw-frame mode: incoming WS frames
// are forwarded as []byte to RawFrames() and not parsed. This is significantly
// faster for high-throughput data plane use.
//
// Once enabled, Messages() will receive nothing more.
func (c *Conn) EnableRawMode() {
	c.rawEnable.Store(true)
}

// RawFrames returns the channel of raw WS frames (only populated after EnableRawMode).
// Each frame is the raw JSON bytes — caller can extract custom fields with their own
// fast parser, or just count bytes for benchmarks.
func (c *Conn) RawFrames() <-chan []byte { return c.rawIn }

// SendJSON serialises and sends an arbitrary JSON message. Caller is responsible for setting
// "colibriClass".
func (c *Conn) SendJSON(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.sendBytes(data)
}

func (c *Conn) sendBytes(data []byte) error {
	select {
	case c.outgoing <- data:
		return nil
	case <-c.closed:
		return fmt.Errorf("connection closed")
	}
}

// trySendBytes is non-blocking: returns ErrQueueFull if the outgoing queue has no room.
func (c *Conn) trySendBytes(data []byte) error {
	select {
	case c.outgoing <- data:
		return nil
	case <-c.closed:
		return fmt.Errorf("connection closed")
	default:
		return ErrQueueFull
	}
}

// ErrQueueFull is returned by TrySend* when the outgoing queue is full.
var ErrQueueFull = fmt.Errorf("colibri: send queue full")

// TrySendJSON is the non-blocking variant of SendJSON: drops the message and
// returns ErrQueueFull instead of waiting for room in the outgoing queue.
func (c *Conn) TrySendJSON(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return c.trySendBytes(data)
}

// TrySendRaw is the non-blocking variant of SendRaw.
func (c *Conn) TrySendRaw(to string, payload []byte) error {
	msg := map[string]any{
		"colibriClass": "EndpointMessage",
		"to":           to,
		"raw":          base64.StdEncoding.EncodeToString(payload),
	}
	return c.TrySendJSON(msg)
}

// SendRaw sends arbitrary opaque bytes as a single broadcast EndpointMessage. The bytes are
// base64-encoded and placed under the "raw" field; receivers should pass through DecodeRaw.
//
// to == "" means broadcast to every other endpoint in the conference.
func (c *Conn) SendRaw(to string, payload []byte) error {
	msg := map[string]any{
		"colibriClass": "EndpointMessage",
		"to":           to,
		"raw":          base64.StdEncoding.EncodeToString(payload),
	}
	return c.SendJSON(msg)
}

// DecodeRaw extracts the bytes from an EndpointMessage that was produced by SendRaw.
// Returns nil if the message is not a raw frame.
func DecodeRaw(m Message) []byte {
	if m.Class != "EndpointMessage" {
		return nil
	}
	enc, ok := m.Fields["raw"].(string)
	if !ok {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return nil
	}
	return b
}

// SendEndpointMessage sends a JSON-payload EndpointMessage to a specific endpoint or broadcast
// (to == ""). Any extra fields in extras are merged at the top level (alongside colibriClass/to).
//
// Example:
//
//	conn.SendEndpointMessage("", map[string]any{"text": "hello", "type": "chat"})
//
// The bridge does not interpret these fields — it simply forwards them to the receiver(s).
func (c *Conn) SendEndpointMessage(to string, extras map[string]any) error {
	msg := map[string]any{
		"colibriClass": "EndpointMessage",
		"to":           to,
	}
	for k, v := range extras {
		if k == "colibriClass" || k == "to" {
			continue
		}
		msg[k] = v
	}
	return c.SendJSON(msg)
}

// SendLastN tells the bridge how many remote video streams the client wants to receive.
func (c *Conn) SendLastN(n int) error {
	return c.SendJSON(map[string]any{
		"colibriClass": "LastNChangedEvent",
		"lastN":        n,
	})
}

// SendReceiverVideoConstraints announces detailed per-source video constraints to the bridge.
// Pass the constraints object verbatim (see Jitsi docs / ReceiverVideoConstraintsMessage).
func (c *Conn) SendReceiverVideoConstraints(constraints map[string]any) error {
	msg := map[string]any{
		"colibriClass": "ReceiverVideoConstraints",
	}
	for k, v := range constraints {
		if k == "colibriClass" {
			continue
		}
		msg[k] = v
	}
	return c.SendJSON(msg)
}

// SendVideoType signals the local video source type (camera/desktop/none).
//
// videoType: "camera", "desktop", or "none".
func (c *Conn) SendVideoType(videoType string) error {
	return c.SendJSON(map[string]any{
		"colibriClass": "VideoTypeMessage",
		"videoType":    videoType,
	})
}

// SendSourceVideoType signals the video type for a specific source.
func (c *Conn) SendSourceVideoType(sourceName, videoType string) error {
	return c.SendJSON(map[string]any{
		"colibriClass": "SourceVideoTypeMessage",
		"sourceName":   sourceName,
		"videoType":    videoType,
	})
}

// SendEndpointStats publishes endpoint statistics (bitrate, packet loss, RTT…) to the bridge.
// Pass the stats object verbatim — typical fields are "bitrate", "packetLoss", "jvbRTT", etc.
func (c *Conn) SendEndpointStats(stats map[string]any) error {
	msg := map[string]any{
		"colibriClass": "EndpointStats",
	}
	for k, v := range stats {
		if k == "colibriClass" {
			continue
		}
		msg[k] = v
	}
	return c.SendJSON(msg)
}

// SendReceiverAudioSubscription tells the bridge which audio sources to subscribe to.
//
// mode: "All" | "None" | "Include" | "Exclude"
// list: source names (ignored unless mode is Include/Exclude).
func (c *Conn) SendReceiverAudioSubscription(mode string, list []string) error {
	msg := map[string]any{
		"colibriClass": "ReceiverAudioSubscription",
		"mode":         mode,
	}
	if mode == "Include" || mode == "Exclude" {
		msg["list"] = list
	}
	return c.SendJSON(msg)
}
