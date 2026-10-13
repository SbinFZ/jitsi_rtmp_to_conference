// Package conference owns native Jitsi signalling and the WebRTC transport.
package conference

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	j "github.com/zarazaex69/j"
)

type Config struct {
	Host, Room, Name            string
	JoinTimeout, ConnectTimeout time.Duration
	Debug                       bool
	PublishAudio, PublishVideo  bool
	OnTrack                     func(*webrtc.PeerConnection, *webrtc.TrackRemote, *webrtc.RTPReceiver)
}

type Connection struct {
	Session      *j.Session
	PC           *webrtc.PeerConnection
	Audio, Video *webrtc.TrackLocalStaticRTP
	Done         chan error
	closeOnce    sync.Once
}

func Normalize(host, room string) (string, string, error) {
	if strings.Contains(host, "://") {
		u, err := url.Parse(host)
		if err != nil || u.Scheme != "https" || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
			return "", "", fmt.Errorf("host must be a hostname or HTTPS origin")
		}
		host = u.Host
	}
	if host == "" || strings.ContainsAny(host, "/?#@ \t\r\n") {
		return "", "", fmt.Errorf("invalid host")
	}
	room = strings.ToLower(strings.TrimSpace(room))
	if room == "" || strings.ContainsAny(room, "/@?#<>&\"' \t\r\n") {
		return "", "", fmt.Errorf("room must be a single room name without URL or spaces")
	}
	return host, room, nil
}

func Join(ctx context.Context, cfg Config) (_ *Connection, err error) {
	host, room, err := Normalize(cfg.Host, cfg.Room)
	if err != nil {
		return nil, err
	}
	joinCtx, cancel := context.WithTimeout(ctx, cfg.JoinTimeout)
	defer cancel()
	sess, err := j.JoinMUC(joinCtx, j.Config{Host: host, Room: room, Nick: cfg.Name, Debug: cfg.Debug})
	if err != nil {
		return nil, err
	}
	c := &Connection{Session: sess, Done: make(chan error, 1)}
	defer func() {
		if err != nil {
			c.Close()
		}
	}()
	slog.Info("joined meeting; waiting for Jingle (empty room needs a second participant)", "host", host, "room", room, "name", cfg.Name)
	// Pion binds unknown incoming SSRCs dynamically. Drain stanzas for long-lived
	// sessions so Prosody SM acknowledgements cannot stall behind a full queue.
	go func() {
		for {
			select {
			case <-sess.Conn.Done():
				c.fail(fmt.Errorf("XMPP connection closed"))
				return
			case <-ctx.Done():
				return
			case stanza := <-sess.Conn.Stanzas():
				if strings.Contains(stanza, "session-terminate") || strings.Contains(stanza, "<stream:error") {
					c.fail(fmt.Errorf("Jitsi session terminated; reconnect required"))
				}
			}
		}
	}()
	if err = sess.WaitMedia(joinCtx); err != nil {
		return nil, err
	}
	pc, err := webrtc.NewPeerConnection(sess.IceConfig())
	if err != nil {
		return nil, err
	}
	c.PC = pc
	if cfg.OnTrack != nil {
		pc.OnTrack(func(t *webrtc.TrackRemote, r *webrtc.RTPReceiver) { cfg.OnTrack(pc, t, r) })
	}
	ready := make(chan struct{})
	var once sync.Once
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		slog.Info("WebRTC state", "state", state.String())
		if state == webrtc.PeerConnectionStateConnected {
			once.Do(func() { close(ready) })
		}
		if state == webrtc.PeerConnectionStateFailed {
			c.fail(fmt.Errorf("ICE/DTLS failed; check JVB UDP candidates / firewall"))
		}
	})
	nick := sess.Conn.Nick()
	if cfg.PublishAudio {
		c.Audio, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, nick+"-a0", nick)
		if err != nil {
			return nil, err
		}
		sender, e := pc.AddTrack(c.Audio)
		if e != nil {
			return nil, e
		}
		go drainRTCP(sender)
	} else {
		if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			return nil, err
		}
	}
	if cfg.PublishVideo {
		c.Video, err = webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, nick+"-v0", nick)
		if err != nil {
			return nil, err
		}
		sender, e := pc.AddTrack(c.Video)
		if e != nil {
			return nil, e
		}
		go drainRTCP(sender)
	} else {
		if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			return nil, err
		}
	}
	// Some Jicofo versions advertise only Colibri WS even when JVB supports
	// SCTP. Never create/wait for a data channel missing from the offer.
	sctp := strings.Contains(sess.SDP, "m=application")
	if sctp {
		if err = sess.PrepareBridgeSCTP(pc); err != nil {
			return nil, err
		}
	}
	neg := sess.Negotiator()
	neg.PC = pc
	if err = neg.Accept(joinCtx); err != nil {
		return nil, fmt.Errorf("Jingle answer: %w", err)
	}
	connectCtx, stop := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer stop()
	select {
	case <-ready:
	case err = <-c.Done:
		return nil, err
	case <-connectCtx.Done():
		return nil, fmt.Errorf("ICE/DTLS connection: %w", connectCtx.Err())
	}
	if sctp {
		err = sess.WaitBridgeSCTP(connectCtx)
	} else {
		err = sess.OpenBridge(connectCtx)
	}
	if err != nil {
		return nil, fmt.Errorf("bridge channel: %w", err)
	}
	if err = sess.Conn.SetMediaPresence(cfg.Name, cfg.PublishAudio, cfg.PublishVideo); err != nil {
		return nil, err
	}
	slog.Info("Jitsi transport ready", "endpoint", nick)
	return c, nil
}

func drainRTCP(s *webrtc.RTPSender) {
	b := make([]byte, 1500)
	for {
		if _, _, err := s.Read(b); err != nil {
			return
		}
	}
}
func (c *Connection) fail(err error) {
	select {
	case c.Done <- err:
	default:
	}
}
func (c *Connection) Close() {
	c.closeOnce.Do(func() {
		if c.PC != nil {
			_ = c.PC.Close()
		}
		if c.Session != nil {
			_ = c.Session.Close()
		}
	})
}
