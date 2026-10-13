package j

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/zarazaex69/j/internal/colibri"
	"github.com/zarazaex69/j/internal/jingle"
	"github.com/zarazaex69/j/internal/peer"
	"github.com/zarazaex69/j/internal/xmpp"
)

type Config struct {
	Host     string // e.g. "meet.cryptopro.ru"
	Room     string // e.g. "myroom"
	Nick     string // display name
	Debug    bool   // verbose XMPP logging
	Insecure bool   // skip TLS certificate verification
	// HTTPClient is used for config discovery, XMPP/BOSH, and Colibri WebSockets.
	// When set, it takes precedence over Insecure.
	HTTPClient *http.Client
}

type ICEServer struct {
	URLs       []string
	Username   string
	Credential string
}

type ServerAuthInfo struct {
	Ready                  bool              `json:"ready"`
	AuthenticationRequired bool              `json:"authentication_required"`
	ExternalAuth           bool              `json:"external_auth"`
	GuestAccess            bool              `json:"guest_access"`
	AnonymousXMPP          bool              `json:"anonymous_xmpp"`
	VisitorsSupported      bool              `json:"visitors_supported"`
	Properties             map[string]string `json:"properties,omitempty"`
}

// Message is an incoming groupchat message.
type Message struct {
	From string // nickname of sender (resource part of JID)
	Body string
}

// Messages returns a channel that delivers incoming groupchat messages.
// Messages from the local user are filtered out.
func (s *Session) Messages() <-chan Message {
	out := make(chan Message, 32)
	go func() {
		defer close(out)
		for stanza := range s.Conn.Stanzas() {
			if !strings.Contains(stanza, "type='groupchat'") && !strings.Contains(stanza, `type="groupchat"`) &&
				!strings.Contains(stanza, "type='chat'") && !strings.Contains(stanza, `type="chat"`) {
				continue
			}
			body := extractTagText(stanza, "body")
			if body == "" {
				continue
			}
			from := extractAttrAny(stanza, "from")
			// from = room@conference.host/nick
			nick := from
			if i := strings.LastIndex(from, "/"); i != -1 {
				nick = from[i+1:]
			}
			// skip our own echo
			if nick == s.Conn.Nick() {
				continue
			}
			out <- Message{From: nick, Body: body}
		}
	}()
	return out
}

func extractTagText(s, tag string) string {
	open := "<" + tag + ">"
	i := strings.Index(s, open)
	if i == -1 {
		return ""
	}
	i += len(open)
	end := strings.Index(s[i:], "</"+tag+">")
	if end == -1 {
		return ""
	}
	return unescapeXML(s[i : i+end])
}

func extractAttrAny(s, attr string) string {
	for _, q := range []string{`'`, `"`} {
		key := attr + "=" + q
		i := strings.Index(s, key)
		if i == -1 {
			continue
		}
		i += len(key)
		end := strings.Index(s[i:], q)
		if end == -1 {
			continue
		}
		return s[i : i+end]
	}
	return ""
}

func unescapeXML(s string) string {
	r := strings.NewReplacer("&lt;", "<", "&gt;", ">", "&quot;", `"`, "&apos;", "'", "&amp;", "&")
	return r.Replace(s)
}

type Session struct {
	JID         string
	RoomJID     string
	SDP         string // remote SDP offer
	ICEServers  []ICEServer
	Candidates  []jingle.Candidate
	DataChannel *jingle.DataChannel
	AudioSSRC   []jingle.Source
	VideoSSRC   []jingle.Source
	ColibriWS   string // bridge WebSocket URL — use for sending EndpointMessage to other participants
	ServerAuth  ServerAuthInfo
	Conn        *xmpp.Conn

	bridge     colibri.Bridge
	bridgeMu   sync.Mutex
	sctpDC     *webrtc.DataChannel
	sctpReady  chan struct{}
	room       string
	jingleSID  string
	initiator  string
	httpClient *http.Client
}

// BridgeMessage is the type returned by the Bridge() channel — see internal/colibri.Message.
type BridgeMessage = colibri.Message

// RequestVideo sends ReceiverVideoConstraints to the bridge, telling JVB to forward
// video streams to this endpoint. Without this call, JVB will NOT send any video.
// maxHeight is the max resolution (e.g. 720, 360, 180). Use -1 for lastN to receive all.
func (s *Session) RequestVideo(ctx context.Context, maxHeight int) error {
	if err := s.OpenBridge(ctx); err != nil {
		return err
	}
	return s.bridge.SendJSON(map[string]any{
		"colibriClass":       "ReceiverVideoConstraints",
		"lastN":              -1,
		"defaultConstraints": map[string]any{"maxHeight": maxHeight},
	})
}

// OpenBridge connects to the Jitsi bridge channel (colibri-ws) using the URL from the
// Jingle session-initiate. Subsequent calls return the existing connection.
// If colibri-ws URL is available, uses WebSocket. Otherwise returns an error;
// use OpenBridgeSCTP(pc) for SCTP datachannel fallback.
func (s *Session) OpenBridge(ctx context.Context) error {
	s.bridgeMu.Lock()
	defer s.bridgeMu.Unlock()
	if s.bridge != nil {
		return nil
	}
	if s.ColibriWS != "" {
		br, err := colibri.Dial(ctx, s.ColibriWS, s.httpClient)
		if err != nil {
			return err
		}
		s.bridge = br
		return nil
	}
	return fmt.Errorf("no colibri-ws URL; use OpenBridgeSCTP(pc) for SCTP datachannel")
}

// PrepareBridgeSCTP creates the DataChannel on the PeerConnection BEFORE Accept,
// so it gets included in the SDP answer. Call WaitBridgeSCTP after Accept to wait
// for the channel to open.
func (s *Session) PrepareBridgeSCTP(pc *webrtc.PeerConnection) error {
	s.bridgeMu.Lock()
	defer s.bridgeMu.Unlock()
	if s.bridge != nil {
		return nil
	}

	ordered := true
	dc, err := pc.CreateDataChannel("JVB data channel", &webrtc.DataChannelInit{
		Protocol: strPtr("http://jitsi.org/protocols/colibri"),
		Ordered:  &ordered,
	})
	if err != nil {
		return fmt.Errorf("create datachannel: %w", err)
	}

	opened := make(chan struct{}, 1)
	dc.OnOpen(func() {
		select {
		case opened <- struct{}{}:
		default:
		}
	})
	s.sctpDC = dc
	s.sctpReady = opened
	return nil
}

// WaitBridgeSCTP waits for the DataChannel created by PrepareBridgeSCTP to open
// (after ICE/DTLS completes via Accept), then wraps it as the bridge.
func (s *Session) WaitBridgeSCTP(ctx context.Context) error {
	s.bridgeMu.Lock()
	if s.bridge != nil {
		s.bridgeMu.Unlock()
		return nil
	}
	dc := s.sctpDC
	ready := s.sctpReady
	s.bridgeMu.Unlock()

	if dc == nil || ready == nil {
		return fmt.Errorf("call PrepareBridgeSCTP before WaitBridgeSCTP")
	}

	select {
	case <-ready:
	case <-ctx.Done():
		return ctx.Err()
	}

	br := colibri.WrapDataChannel(dc)
	// Send ClientHello and wait for ServerHello — this confirms JVB has
	// registered our endpoint for message routing. Without this wait,
	// messages sent immediately after may be dropped by JVB.
	if err := br.SendJSON(map[string]any{"colibriClass": "ClientHello"}); err != nil {
		return fmt.Errorf("send ClientHello: %w", err)
	}
	if err := waitServerHello(ctx, br); err != nil {
		return fmt.Errorf("wait ServerHello: %w", err)
	}
	s.bridgeMu.Lock()
	s.bridge = br
	s.sctpDC = nil
	s.sctpReady = nil
	s.bridgeMu.Unlock()
	return nil
}

func strPtr(s string) *string { return &s }

// waitServerHello reads from the bridge until ServerHello arrives, then
// re-delivers any messages received before it. JVB sends ServerHello in
// response to ClientHello once the endpoint is registered for routing.
func waitServerHello(ctx context.Context, br colibri.Bridge) error {
	var buffered []colibri.Message
	for {
		select {
		case m, ok := <-br.Messages():
			if !ok {
				return fmt.Errorf("bridge closed before ServerHello")
			}
			if m.Class == "ServerHello" {
				// Replay buffered messages back into the bridge incoming channel
				// so callers don't miss them.
				if c, ok := br.(*colibri.SCTPBridge); ok {
					c.PrependMessages(buffered)
				}
				return nil
			}
			buffered = append(buffered, m)
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// SetBridge sets an externally-provided Bridge (e.g. SCTP DataChannel wrapper).
// Bridge returns the underlying bridge connection (after OpenBridge or WaitBridgeSCTP).
func (s *Session) Bridge() colibri.Bridge {
	s.bridgeMu.Lock()
	defer s.bridgeMu.Unlock()
	return s.bridge
}

// BridgeSendRaw sends arbitrary opaque bytes through the bridge channel as a single
// broadcast EndpointMessage. The bytes are base64-encoded; use BridgeMessages and
// colibri.DecodeRaw on the receiver.
//
// to == "" means broadcast.
func (s *Session) BridgeSendRaw(to string, data []byte) error {
	br := s.Bridge()
	if br == nil {
		return fmt.Errorf("bridge not open; call OpenBridge first")
	}
	return br.SendRaw(to, data)
}

// BridgeSendMessage sends a JSON EndpointMessage (broadcast or unicast).
// extras are merged at the top level (e.g. {"text": "hi"} or {"type":"foo","x":1}).
func (s *Session) BridgeSendMessage(to string, extras map[string]any) error {
	br := s.Bridge()
	if br == nil {
		return fmt.Errorf("bridge not open; call OpenBridge first")
	}
	return br.SendEndpointMessage(to, extras)
}

// BridgeMessages returns the channel of incoming bridge messages.
func (s *Session) BridgeMessages() <-chan BridgeMessage {
	br := s.Bridge()
	if br == nil {
		return nil
	}
	return br.Messages()
}

// BridgeTrySendRaw is the non-blocking variant of BridgeSendRaw.
// Returns colibri.ErrQueueFull when the outgoing queue has no room — caller
// can drop, retry or apply own backpressure policy.
func (s *Session) BridgeTrySendRaw(to string, data []byte) error {
	br := s.Bridge()
	if br == nil {
		return fmt.Errorf("bridge not open; call OpenBridge first")
	}
	return br.TrySendRaw(to, data)
}

// BridgeSendQueueDepth returns how many outgoing bridge messages are waiting to be sent.
func (s *Session) BridgeSendQueueDepth() int {
	br := s.Bridge()
	if br == nil {
		return 0
	}
	return br.SendQueueDepth()
}

// BridgeCanSend reports whether the bridge outgoing queue has free room.
func (s *Session) BridgeCanSend() bool {
	br := s.Bridge()
	if br == nil {
		return false
	}
	return br.CanSend()
}

// Negotiator returns a *peer.Negotiator wired to this session for use with a pion
// PeerConnection. Caller sets pc.PC and pc.OnRemote, then calls pc.Accept(ctx) to
// perform SDP negotiation and send session-accept to Jicofo.
//
//	neg := sess.Negotiator()
//	neg.PC = myPionPC
//	neg.OnRemote = func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { … }
//	if err := neg.Accept(ctx); err != nil { … }
func (s *Session) Negotiator() *peer.Negotiator {
	return &peer.Negotiator{
		XMPP:         s.Conn,
		JingleStanza: s.Conn.LastJingleStanza(),
		RoomJID:      s.RoomJID,
	}
}

// WaitJingleReinitiate blocks until Jicofo sends a NEW session-initiate (e.g. after
// session-terminate with reason "moving" or "general-error"). Returns the raw stanza.
// Use this to drive a reconnect loop:
//
//	for {
//	    neg := sess.Negotiator()
//	    neg.PC = newPC
//	    neg.Accept(ctx)
//	    // … wait until pc reports connection failed / terminate received …
//	    if _, err := sess.WaitJingleReinitiate(ctx); err != nil { return }
//	}
func (s *Session) WaitJingleReinitiate(ctx context.Context) (string, error) {
	return s.Conn.WaitJingle(ctx)
}

// OnReinitiate is the asynchronous version of WaitJingleReinitiate.
// Spawns a goroutine that calls cb(rawStanza) on each new session-initiate.
// Stops when ctx is cancelled or the session is closed.
func (s *Session) OnReinitiate(ctx context.Context, cb func(stanza string)) {
	go func() {
		for {
			stanza, err := s.Conn.WaitJingle(ctx)
			if err != nil {
				return
			}
			cb(stanza)
		}
	}()
}

// Endpoints returns the list of other participants currently in the MUC room
// (their MUC nicks — typically first 8 chars of UUID). "focus" and self excluded.
// Useful for client-id-style unicast routing via BridgeSendRaw.
func (s *Session) Endpoints() []string {
	return s.Conn.Occupants()
}

// Rejoin leaves the MUC and rejoins immediately WITHOUT waiting for
// session-initiate. Use WaitJingleReinitiate after Rejoin to wait for
// Jicofo to send a new session-initiate when another participant arrives.
// This avoids blocking indefinitely when the server is alone in the room.
func (s *Session) Rejoin(ctx context.Context, nick string) error {
	s.bridgeMu.Lock()
	if s.bridge != nil {
		_ = s.bridge.Close()
		s.bridge = nil
	}
	s.bridgeMu.Unlock()

	if err := s.Conn.LeaveMUCWait(s.room, 5*time.Second); err != nil {
		log.Printf("j: rejoin leave-muc failed: %v (fire-and-forget)", err)
		_ = s.Conn.LeaveMUC(s.room)
		time.Sleep(200 * time.Millisecond)
	} else {
		log.Printf("j: rejoin leave-muc ok for room %s", s.room)
	}

	if nick == "" {
		nick = s.Conn.Nick()
	}
	log.Printf("j: rejoin joining room %s as %s", s.room, nick)
	return s.Conn.JoinMUC(ctx, s.room, nick)
}

// LowLevel returns the underlying XMPP connection so callers can issue raw XMPP/Jingle stanzas.
func (s *Session) LowLevel() *xmpp.Conn { return s.Conn }

// IceConfig returns ICE servers as a pion-ready webrtc.Configuration.
func (s *Session) IceConfig() webrtc.Configuration {
	var srvs []webrtc.ICEServer
	for _, ice := range s.ICEServers {
		srvs = append(srvs, webrtc.ICEServer{
			URLs:       ice.URLs,
			Username:   ice.Username,
			Credential: ice.Credential,
		})
	}
	return webrtc.Configuration{
		ICEServers:         srvs,
		ICETransportPolicy: webrtc.ICETransportPolicyAll,
	}
}

func (s *Session) Accept(sdp string) error {
	return s.Conn.SendSessionAccept(s.jingleSID, s.initiator, s.RoomJID, sdp)
}

func (s *Session) Chat(msg string) error {
	return s.Conn.SendGroupchat(s.RoomJID, msg)
}

func (s *Session) RaiseHand() error {
	return s.Conn.RaiseHand(s.room)
}

func (s *Session) LowerHand() error {
	return s.Conn.LowerHand(s.room)
}

func (s *Session) Close() error {
	s.bridgeMu.Lock()
	if s.bridge != nil {
		_ = s.bridge.Close()
		s.bridge = nil
	}
	s.bridgeMu.Unlock()

	if s.room != "" {
		// Wait for Prosody to echo our unavailable presence back: that's
		// the XMPP-level confirmation that we've been removed from the
		// MUC roster (same handshake lib-jitsi-meet's ChatRoom.leave
		// awaits via XMPPEvents.MUC_LEFT). Without it, ripping the
		// websocket immediately leaves Jicofo and JVB to discover our
		// departure via idle timeout — minutes later — which is exactly
		// the ghost-participant pattern that wedges back-to-back joins
		// into the same conference.
		//
		// 5s matches lib-jitsi-meet's hardcoded leave timeout. On a
		// healthy bridge this returns in tens of milliseconds; on a
		// wedged one we still bail before the websocket teardown.
		if err := s.Conn.LeaveMUCWait(s.room, 5*time.Second); err != nil {
			// Log the failure so callers can correlate ghost-participant
			// reports with concrete handshake outcomes; then fall back to
			// fire-and-forget + short grace so we don't regress hard if
			// the server is wedged.
			log.Printf("j: leave-muc handshake failed for room %s: %v (falling back to fire-and-forget)", s.room, err)
			_ = s.Conn.LeaveMUC(s.room)
			time.Sleep(200 * time.Millisecond)
		} else {
			log.Printf("j: leave-muc handshake ok for room %s", s.room)
		}
	}
	return s.Conn.Close()
}

// JoinMUC connects to the room without waiting for Jingle session.
func JoinMUC(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Host == "" || cfg.Room == "" {
		return nil, fmt.Errorf("host and room are required")
	}
	if cfg.Nick == "" {
		cfg.Nick = "j-client"
	}

	conn, httpClient, err := xmpp.Dial(ctx, cfg.Host, cfg.Room, cfg.Debug, cfg.Insecure, cfg.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("xmpp dial: %w", err)
	}

	if err := conn.AllocateFocus(ctx, cfg.Room); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("allocate focus: %w", err)
	}
	serverAuth := convertFocusInfo(conn.FocusInfo())

	services, err := conn.DiscoverServices(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("discover services: %w", err)
	}

	if err := conn.JoinMUC(ctx, cfg.Room, cfg.Nick); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("join muc: %w", err)
	}

	return &Session{
		JID:        conn.JID(),
		RoomJID:    fmt.Sprintf("%s@%s", cfg.Room, conn.MUCDomain()),
		ICEServers: convertICE(services),
		ServerAuth: serverAuth,
		Conn:       conn,
		room:       cfg.Room,
		httpClient: httpClient,
	}, nil
}

func Join(ctx context.Context, cfg Config) (*Session, error) {
	if cfg.Host == "" || cfg.Room == "" {
		return nil, fmt.Errorf("host and room are required")
	}
	if cfg.Nick == "" {
		cfg.Nick = "j-client"
	}

	conn, httpClient, err := xmpp.Dial(ctx, cfg.Host, cfg.Room, cfg.Debug, cfg.Insecure, cfg.HTTPClient)
	if err != nil {
		return nil, fmt.Errorf("xmpp dial: %w", err)
	}

	services, err := conn.DiscoverServices(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("discover services: %w", err)
	}

	if err := conn.AllocateFocus(ctx, cfg.Room); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("allocate focus: %w", err)
	}
	serverAuth := convertFocusInfo(conn.FocusInfo())

	if err := conn.JoinMUC(ctx, cfg.Room, cfg.Nick); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("join muc: %w", err)
	}

	ji, err := conn.WaitJingle(ctx)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("wait jingle: %w", err)
	}

	parsed := jingle.Parse(ji)

	sess := &Session{
		JID:         conn.JID(),
		RoomJID:     fmt.Sprintf("%s@%s", cfg.Room, conn.MUCDomain()),
		SDP:         parsed.SDP,
		ICEServers:  convertICE(services),
		Candidates:  parsed.Candidates,
		DataChannel: parsed.DataChannel,
		AudioSSRC:   parsed.AudioSources,
		VideoSSRC:   parsed.VideoSources,
		ColibriWS:   parsed.ColibriWS,
		ServerAuth:  serverAuth,
		Conn:        conn,
		room:        cfg.Room,
		jingleSID:   parsed.SID,
		initiator:   parsed.Initiator,
		httpClient:  httpClient,
	}

	return sess, nil
}

func convertFocusInfo(info xmpp.FocusInfo) ServerAuthInfo {
	return ServerAuthInfo{
		Ready:                  info.Ready,
		AuthenticationRequired: info.AuthenticationRequired,
		ExternalAuth:           info.ExternalAuth,
		GuestAccess:            info.AnonymousXMPP && info.Ready,
		AnonymousXMPP:          info.AnonymousXMPP,
		VisitorsSupported:      info.VisitorsSupported,
		Properties:             info.Properties,
	}
}

func convertICE(services []xmpp.Service) []ICEServer {
	var servers []ICEServer
	for _, s := range services {
		url, ok := iceServiceURL(s)
		if !ok {
			continue
		}
		// pion validates TURN/TURNS servers inside NewPeerConnection and
		// fails the whole configuration ("no turn server credentials") when
		// the username or password is missing, so a single unauthenticated
		// TURN advertisement would poison every other server. Skip turn/turns
		// services without complete credentials; stun/stuns need none and are
		// unaffected.
		if strings.HasPrefix(url, "turn:") || strings.HasPrefix(url, "turns:") {
			if s.Username == "" || s.Password == "" {
				continue
			}
		}
		servers = append(servers, ICEServer{
			URLs:       []string{url},
			Username:   s.Username,
			Credential: s.Password,
		})
	}
	return servers
}

// iceServiceURL renders one XEP-0215 service as an RFC 7064/7065 STUN/TURN
// URI. Servers may advertise services with a missing port or transport;
// naive formatting then yields URLs like "stun:host:" or
// "turn:host:?transport=udp", which pion rejects inside
// NewPeerConnection ("invalid port") and the whole ICE config is lost.
// Missing ports fall back to the scheme defaults (3478 for stun/turn, 5349
// for stuns/turns), IPv6 hosts are bracketed via net.JoinHostPort, and a
// missing or unknown transport omits the query so pion applies the scheme
// default (udp for turn, tcp for turns). Entries that cannot be turned into
// a valid URI (no host, unusable port, host with URL metacharacters,
// colon-containing host that is not an IPv6 literal) are
// skipped with ok=false so one bad advertisement doesn't poison the rest.
// Credential requirements for turn/turns are enforced by convertICE, not
// here; this function only renders the URL.
func iceServiceURL(s xmpp.Service) (string, bool) {
	scheme := strings.ToLower(strings.TrimSpace(s.Type))
	var defaultPort string
	switch scheme {
	case "stun", "turn":
		defaultPort = "3478"
	case "stuns", "turns":
		defaultPort = "5349"
	default:
		return "", false
	}

	host := strings.TrimSpace(s.Host)
	// Unwrap a pre-bracketed IPv6 literal; net.JoinHostPort re-brackets any
	// host containing a colon and would otherwise double the brackets.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = host[1 : len(host)-1]
	}
	if host == "" || strings.ContainsAny(host, "[]?#/@% \t\r\n") {
		return "", false
	}
	// net.JoinHostPort brackets any host containing a colon, so a non-IPv6
	// host like "evil:host" would render as "stun:[evil:host]:3478", which
	// pion rejects. Only valid IPv6 literals may contain colons.
	if strings.Contains(host, ":") && net.ParseIP(host) == nil {
		return "", false
	}

	port := strings.TrimSpace(s.Port)
	if port == "" {
		port = defaultPort
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "", false
	}

	url := scheme + ":" + net.JoinHostPort(host, port)
	if scheme == "turn" || scheme == "turns" {
		switch transport := strings.ToLower(strings.TrimSpace(s.Transport)); transport {
		case "udp", "tcp":
			url += "?transport=" + transport
		}
	}
	return url, true
}
