package colibri

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/pion/webrtc/v4"
)

// SCTPBridge wraps a pion DataChannel to implement the Bridge interface.
type SCTPBridge struct {
	dc        *webrtc.DataChannel
	incoming  chan Message
	rawIn     chan []byte
	rawEnable atomic.Bool
	closed    chan struct{}
	closeOnce sync.Once
}

// WrapDataChannel creates a Bridge from an already-open pion DataChannel.
func WrapDataChannel(dc *webrtc.DataChannel) *SCTPBridge {
	b := &SCTPBridge{
		dc:       dc,
		incoming: make(chan Message, 64),
		rawIn:    make(chan []byte, 256),
		closed:   make(chan struct{}),
	}
	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		data := msg.Data
		if b.rawEnable.Load() {
			cp := append([]byte(nil), data...)
			select {
			case b.rawIn <- cp:
			case <-b.closed:
			default:
			}
			return
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			return
		}
		class, _ := fields["colibriClass"].(string)
		from, _ := fields["from"].(string)
		to, _ := fields["to"].(string)
		m := Message{
			Class:   class,
			From:    from,
			To:      to,
			Fields:  fields,
			RawJSON: append([]byte(nil), data...),
		}
		select {
		case b.incoming <- m:
		case <-b.closed:
		}
	})
	dc.OnClose(func() {
		b.closeOnce.Do(func() {
			close(b.closed)
			close(b.incoming)
			close(b.rawIn)
		})
	})
	return b
}

func (b *SCTPBridge) send(data []byte) error {
	select {
	case <-b.closed:
		return fmt.Errorf("datachannel closed")
	default:
	}
	return b.dc.SendText(string(data))
}

func (b *SCTPBridge) SendJSON(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return b.send(data)
}

func (b *SCTPBridge) SendRaw(to string, payload []byte) error {
	msg := map[string]any{
		"colibriClass": "EndpointMessage",
		"to":           to,
		"raw":          base64.StdEncoding.EncodeToString(payload),
	}
	return b.SendJSON(msg)
}

func (b *SCTPBridge) SendEndpointMessage(to string, extras map[string]any) error {
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
	return b.SendJSON(msg)
}

func (b *SCTPBridge) SendLastN(n int) error {
	return b.SendJSON(map[string]any{"colibriClass": "LastNChangedEvent", "lastN": n})
}

func (b *SCTPBridge) SendReceiverVideoConstraints(constraints map[string]any) error {
	msg := map[string]any{"colibriClass": "ReceiverVideoConstraints"}
	for k, v := range constraints {
		if k != "colibriClass" {
			msg[k] = v
		}
	}
	return b.SendJSON(msg)
}

func (b *SCTPBridge) SendVideoType(videoType string) error {
	return b.SendJSON(map[string]any{"colibriClass": "VideoTypeMessage", "videoType": videoType})
}

func (b *SCTPBridge) TrySendJSON(payload any) error {
	return b.SendJSON(payload)
}

func (b *SCTPBridge) TrySendRaw(to string, payload []byte) error {
	return b.SendRaw(to, payload)
}

func (b *SCTPBridge) Messages() <-chan Message { return b.incoming }
func (b *SCTPBridge) EnableRawMode()           { b.rawEnable.Store(true) }
func (b *SCTPBridge) RawFrames() <-chan []byte { return b.rawIn }
func (b *SCTPBridge) SendQueueDepth() int      { return 0 }
func (b *SCTPBridge) CanSend() bool            { return true }

// PrependMessages re-enqueues messages at the front of the incoming channel.
// Used to replay messages that were peeked during handshake.
func (b *SCTPBridge) PrependMessages(msgs []Message) {
	for _, m := range msgs {
		select {
		case b.incoming <- m:
		case <-b.closed:
			return
		default:
			// drop on overflow
		}
	}
}

func (b *SCTPBridge) Close() error {
	b.closeOnce.Do(func() {
		close(b.closed)
		close(b.incoming)
		close(b.rawIn)
	})
	return b.dc.Close()
}
