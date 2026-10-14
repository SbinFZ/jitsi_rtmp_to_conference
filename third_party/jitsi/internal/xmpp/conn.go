package xmpp

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// transport abstracts the underlying XMPP framing connection (WebSocket or BOSH).
type transport interface {
	Send([]byte) error
	Recv(ctx context.Context) ([]byte, error)
	Close() error
}

// wsTransport wraps a WebSocket connection.
type wsTransport struct {
	ws *websocket.Conn
}

func (t *wsTransport) Send(data []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return t.ws.Write(ctx, websocket.MessageText, data)
}

func (t *wsTransport) Recv(ctx context.Context) ([]byte, error) {
	_, data, err := t.ws.Read(ctx)
	return data, err
}

func (t *wsTransport) Close() error {
	return t.ws.Close(websocket.StatusNormalClosure, "")
}

type Conn struct {
	tr           transport
	host         string
	room         string
	mucDomain    string // e.g. "muc.meet1.arbitr.ru" or "conference.meet1.arbitr.ru"
	xmppDomain   string // XMPP virtualhost — usually equals host, but docker-jitsi-meet uses "meet.jitsi"
	jid          string
	nick         string
	debug        bool
	anonymous    bool
	bosh         bool // true when using BOSH transport (no stream management)
	focusInfo    FocusInfo
	mu           sync.Mutex
	ackH         atomic.Int64
	idSeq        atomic.Int64
	lastJngMu    sync.Mutex
	lastJng      string
	occMu        sync.Mutex
	occupants    map[string]struct{} // MUC nick → present (excluding self and "focus")
	occupantNick map[string]string   // MUC resource (endpoint ID) → display name (<nick>)
	stanzas      chan string
	jingles      chan string
	closed       chan struct{}
	closeOnce    sync.Once

	// waitMu protects the per-stanza waiter maps below.
	waitMu sync.Mutex
	// iqWaiters resolves <iq type="result"/> or <iq type="error"/> back to
	// the caller of SendIQWait via stanza id. The chan delivers the full
	// stanza so the caller can inspect type/error payload.
	iqWaiters map[string]chan string
	// leaveWaiter fires when we observe our own presence-unavailable
	// echoed back by Prosody — the XMPP equivalent of MUC_LEFT used in
	// lib-jitsi-meet. Nil when no LeaveMUCWait is in flight.
	leaveWaiter chan struct{}
	// smAckWaiter fires when we receive a stream-management <a h=N/>
	// stanza. Used by the keepalive goroutine to detect a wedged or
	// silently-disconnected XMPP websocket: if our periodic <r/> doesn't
	// elicit a response, the connection is dead and we shut it down so
	// Prosody can drop us from the MUC promptly.
	smAckWaiter chan struct{}
}

type Service struct {
	Type      string
	Host      string
	Port      string
	Transport string
	Username  string
	Password  string
}

type FocusInfo struct {
	Ready                  bool
	AuthenticationRequired bool
	ExternalAuth           bool
	VisitorsSupported      bool
	AnonymousXMPP          bool
	Properties             map[string]string
}

const jitsiCapsNode = "https://jitsi.org/jitsi-meet"

var jitsiMeetFeatures = []string{
	"http://jabber.org/protocol/caps",
	"http://jitsi.org/receive-multiple-video-streams",
	"http://jitsi.org/remb",
	"http://jitsi.org/source-name",
	"http://jitsi.org/start-muted-room-metadata",
	"http://jitsi.org/visitors-1",
	"urn:ietf:rfc:4588",
	"urn:xmpp:jingle:1",
	"urn:xmpp:jingle:apps:dtls:0",
	"urn:xmpp:jingle:apps:rtp:1",
	"urn:xmpp:jingle:apps:rtp:audio",
	"urn:xmpp:jingle:apps:rtp:video",
	"urn:xmpp:jingle:transports:dtls-sctp:1",
	"urn:xmpp:jingle:transports:ice-udp:1",
}

var jitsiCapsVersion = calculateJitsiCapsVersion()

func Dial(
	ctx context.Context, host, room string, debug, insecure bool, httpClient *http.Client,
) (*Conn, *http.Client, error) {
	httpClient = selectHTTPClient(httpClient, insecure)
	cfg := fetchConfig(ctx, host, httpClient)

	wsURL := cfg.websocket
	if wsURL == "" {
		wsURL = fmt.Sprintf("wss://%s/xmpp-websocket", host)
	}
	// Append room query param if not already present.
	if !strings.Contains(wsURL, "?") {
		wsURL += "?room=" + room
	} else {
		wsURL += "&room=" + room
	}

	ws, _, wsErr := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		Subprotocols:    []string{"xmpp"},
		CompressionMode: websocket.CompressionContextTakeover,
		HTTPClient:      httpClient,
		HTTPHeader: http.Header{
			"Accept":          {"*/*"},
			"Accept-Language": {"en-US,en;q=0.9"},
			"Cache-Control":   {"no-cache"},
			"Origin":          {"https://" + host},
			"Pragma":          {"no-cache"},
			"Sec-Fetch-Dest":  {"empty"},
			"Sec-Fetch-Mode":  {"websocket"},
			"Sec-Fetch-Site":  {"same-origin"},
			"User-Agent":      {"Mozilla/5.0 (X11; Linux x86_64; rv:150.0) Gecko/20100101 Firefox/150.0"},
		},
	})
	if wsErr != nil {
		// BOSH fallback: if WebSocket dial failed and we have a BOSH URL, try BOSH.
		if cfg.bosh != "" {
			conn, err := dialBOSH(ctx, host, room, debug, cfg, httpClient)
			return conn, httpClient, err
		}
		return nil, httpClient, fmt.Errorf("failed to WebSocket dial: %w", wsErr)
	}
	ws.SetReadLimit(1 << 20)

	c := &Conn{
		tr:           &wsTransport{ws: ws},
		host:         host,
		room:         room,
		mucDomain:    cfg.mucDomain,
		xmppDomain:   cfg.xmppDomain,
		debug:        debug,
		occupants:    make(map[string]struct{}),
		occupantNick: make(map[string]string),
		stanzas:      make(chan string, 64),
		jingles:      make(chan string, 8),
		closed:       make(chan struct{}),
		iqWaiters:    make(map[string]chan string),
	}

	if err := c.auth(ctx); err != nil {
		_ = ws.Close(websocket.StatusInternalError, "")
		return nil, httpClient, err
	}

	go c.readLoop()
	go c.keepaliveLoop()
	return c, httpClient, nil
}

func selectHTTPClient(client *http.Client, insecure bool) *http.Client {
	if client != nil {
		return client
	}
	if insecure {
		return &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	}
	return http.DefaultClient
}

type jitsiConfig struct {
	mucDomain  string
	xmppDomain string
	websocket  string // full wss:// URL from config.websocket (without room param)
	bosh       string // full URL from config.bosh
}

// fetchConfig downloads /config.js from the host and extracts MUC domain,
// XMPP domain, WebSocket URL, and BOSH URL.
func fetchConfig(ctx context.Context, host string, client *http.Client) jitsiConfig {
	cfg := jitsiConfig{
		mucDomain:  "conference." + host,
		xmppDomain: host,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+"/config.js", nil)
	if err != nil {
		return cfg
	}
	resp, err := client.Do(req)
	if err != nil {
		return cfg
	}
	defer func() { _ = resp.Body.Close() }()
	buf := make([]byte, 64*1024)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	if v := extractStringField(body, "domain"); v != "" {
		cfg.xmppDomain = v
	}
	if v := extractStringField(body, "muc"); v != "" {
		cfg.mucDomain = v
	}
	if v := extractStringField(body, "websocket"); v != "" {
		// Handle protocol-relative URLs like "//host/xmpp-websocket"
		if strings.HasPrefix(v, "//") {
			v = "wss:" + v
		}
		cfg.websocket = v
	}
	if v := extractStringField(body, "bosh"); v != "" {
		// Handle protocol-relative URLs like "//host/http-bind"
		if strings.HasPrefix(v, "//") {
			v = "https:" + v
		}
		cfg.bosh = v
	}
	return cfg
}

// extractStringField finds `<key>: <expr>` or `<key> = <expr>` in JS source
// and returns the concatenation of all string literals in <expr>. Identifiers
// (variables) are treated as empty strings — this matches the typical Jitsi
// config pattern where `subdomain` is empty by default.
//
// Examples:
//
//	muc: 'muc.meet.jitsi'                   → "muc.meet.jitsi"
//	muc: 'conference.' + subdomain + 'host' → "conference.host"
//	domain = 'meet.example.com'             → "meet.example.com"
func extractStringField(body, key string) string {
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimSpace(line)
		// Strip leading "config.hosts." / "config." prefixes for assignment style.
		t = strings.TrimPrefix(t, "config.hosts.")
		t = strings.TrimPrefix(t, "config.")
		if !strings.HasPrefix(t, key) {
			continue
		}
		rest := strings.TrimPrefix(t, key)
		// require ":", "=", or whitespace after key (avoid matching e.g. "domain2")
		if len(rest) == 0 || (rest[0] != ':' && rest[0] != '=' && rest[0] != ' ' && rest[0] != '\t') {
			continue
		}
		rest = strings.TrimLeft(rest, " \t:=")
		// strip trailing semicolon / comma
		if i := strings.IndexAny(rest, ";,"); i >= 0 {
			rest = rest[:i]
		}
		// strip JS line comment — but only outside string literals
		if i := indexCommentOutsideStrings(rest); i >= 0 {
			rest = rest[:i]
		}
		v := joinStringLiterals(rest)
		if v != "" {
			return v
		}
	}
	return ""
}

// indexCommentOutsideStrings finds "//" that is NOT inside a string literal.
func indexCommentOutsideStrings(s string) int {
	inStr := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			inStr = c
			continue
		}
		if c == '/' && i+1 < len(s) && s[i+1] == '/' {
			return i
		}
	}
	return -1
}

// joinStringLiterals walks a JS expression and concatenates all single- or
// double-quoted string literals. Other tokens (identifiers, "+", whitespace)
// are ignored.
func joinStringLiterals(expr string) string {
	var out strings.Builder
	for i := 0; i < len(expr); i++ {
		c := expr[i]
		if c != '\'' && c != '"' {
			continue
		}
		end := strings.IndexByte(expr[i+1:], c)
		if end < 0 {
			break
		}
		out.WriteString(expr[i+1 : i+1+end])
		i += 1 + end
	}
	return out.String()
}

func (c *Conn) JID() string       { return c.jid }
func (c *Conn) Nick() string      { return c.nick }
func (c *Conn) Host() string      { return c.host }
func (c *Conn) Room() string      { return c.room }
func (c *Conn) MUCDomain() string { return c.mucDomain }

func (c *Conn) FocusInfo() FocusInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	info := c.focusInfo
	info.AnonymousXMPP = c.anonymous
	info.Properties = cloneStringMap(info.Properties)
	return info
}

// Send transmits an arbitrary XMPP stanza string. Caller is responsible for valid XML
// (and for adding xmlns="jabber:client" on iq/presence/message).
func (c *Conn) Send(s string) error { return c.send(s) }

// NextID returns a unique stanza id for outgoing IQs.
func (c *Conn) NextID() string {
	return fmt.Sprintf("j-%d", c.idSeq.Add(1))
}

// Stanzas returns the channel of incoming non-management XMPP stanzas.
func (c *Conn) Stanzas() <-chan string { return c.stanzas }

// keepaliveLoop periodically pokes the XMPP websocket with a stream
// management <r/> request and verifies that Prosody answers with an
// <a h=N/> ack. If three consecutive cycles fail to elicit an ack we
// declare the connection dead and shut it down.
//
// Why we need this: the e2e test fixture observed 90s windows where
// nothing flowed over XMPP because the application-level data carrier
// (seichannel) was wedged on RTP. In that quiet stretch, Prosody can
// drop us from the bind and our subsequent <presence type="unavailable"/>
// goes into a black hole, leaving ghost participants in the MUC for
// minutes — which is exactly the symptom we kept hitting on back-to-back
// runs of the same room. Keeping the channel pingable so that either the
// server keeps us alive or we detect death promptly mirrors what
// Strophe.js does for lib-jitsi-meet.
//
// Tunables: 30s between pings is well below typical server bind-idle
// timeouts (Prosody mod_smacks defaults to ~5 minutes) but high enough
// to be invisible in the protocol log. 10s ack window covers the
// worst-case meet.cryptopro.ru round trip we've measured.
func (c *Conn) keepaliveLoop() {
	if c.bosh {
		// BOSH uses HTTP polling; stream management is not applicable.
		return
	}
	const (
		interval = 30 * time.Second
		ackWait  = 10 * time.Second
	)
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-c.closed:
			return
		case <-t.C:
		}

		// Arm an ack waiter before sending so a fast server reply can't
		// race us. If a previous cycle's waiter is still pending (which
		// shouldn't normally happen) we just keep using it.
		w := make(chan struct{})
		c.waitMu.Lock()
		if c.smAckWaiter == nil {
			c.smAckWaiter = w
		} else {
			w = c.smAckWaiter
		}
		c.waitMu.Unlock()

		if err := c.send(`<r xmlns="urn:xmpp:sm:3"/>`); err != nil {
			// send already marked the connection closed; just exit.
			return
		}

		select {
		case <-w:
			// Ack received; loop and wait for the next tick.
		case <-c.closed:
			return
		case <-time.After(ackWait):
			// No ack within the window — the websocket is wedged.
			// Shut it down so writers fail fast and Prosody sees the
			// underlying TCP go away (which prompts MUC cleanup on
			// the server side instead of waiting for its own idle
			// timeout, minutes from now).
			c.waitMu.Lock()
			if c.smAckWaiter == w {
				c.smAckWaiter = nil
			}
			c.waitMu.Unlock()
			c.markClosed()
			_ = c.tr.Close()
			return
		}
	}
}

func (c *Conn) Close() error {
	c.markClosed()
	return c.tr.Close()
}

// markClosed signals all waiters (LeaveMUCWait, SendIQWait, keepalive,
// etc.) that the underlying websocket is no longer usable. Idempotent.
func (c *Conn) markClosed() {
	c.closeOnce.Do(func() { close(c.closed) })
}

func (c *Conn) send(s string) error {
	select {
	case <-c.closed:
		return fmt.Errorf("xmpp connection closed")
	default:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.debug {
		fmt.Fprintf(os.Stderr, "[xmpp] -> %s\n", s)
	}
	if err := c.tr.Send([]byte(s)); err != nil {
		c.markClosed()
		return err
	}
	return nil
}

func (c *Conn) readOne(ctx context.Context) (string, error) {
	data, err := c.tr.Recv(ctx)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *Conn) readLoop() {
	// On exit (read error, server FIN, etc.) signal every waiter and
	// the keepalive goroutine that the connection is dead. Without
	// this, callers blocked in LeaveMUCWait / SendIQWait keep waiting
	// for stanzas that will never arrive.
	defer c.markClosed()
	for {
		select {
		case <-c.closed:
			return
		default:
		}
		msg, err := c.readOne(context.Background())
		if err != nil {
			return
		}
		if c.debug {
			fmt.Fprintf(os.Stderr, "[xmpp:loop] <- %s\n", msg)
		}
		// handle stream management
		if strings.Contains(msg, "<r ") || strings.Contains(msg, "<r/>") || strings.Contains(msg, "<r xmlns") {
			_ = c.send(fmt.Sprintf(`<a h="%d" xmlns="urn:xmpp:sm:3"/>`, c.ackH.Load()))
			continue
		}
		if strings.HasPrefix(msg, "<a ") || strings.Contains(msg, "<a xmlns=\"urn:xmpp:sm:3\"") || strings.Contains(msg, "<a xmlns='urn:xmpp:sm:3'") {
			// Wake any pending keepalive ack waiter. The check inside
			// the lock keeps us from racing with keepalive setting up
			// a new waiter for the next cycle.
			c.waitMu.Lock()
			if w := c.smAckWaiter; w != nil {
				c.smAckWaiter = nil
				c.waitMu.Unlock()
				close(w)
			} else {
				c.waitMu.Unlock()
			}
			continue
		}
		c.ackH.Add(1)

		// Dispatch waiters before generic stanza fan-out. IQ result/error
		// is the XMPP-level ack for SendIQWait callers; own presence
		// unavailable echo is the MUC-level ack used by LeaveMUCWait. We
		// resolve them here so callers don't need to scan the stanzas
		// channel themselves.
		if isIQResultOrError(msg) {
			if id := extractXMLAttr(msg, "id"); id != "" {
				c.waitMu.Lock()
				if ch, ok := c.iqWaiters[id]; ok {
					delete(c.iqWaiters, id)
					c.waitMu.Unlock()
					select {
					case ch <- msg:
					default:
					}
					continue
				}
				c.waitMu.Unlock()
			}
		}

		// track MUC occupants from <presence> stanzas
		if strings.HasPrefix(msg, "<presence") || strings.HasPrefix(msg, "<presence ") {
			c.trackPresence(msg)
			if c.isOwnPresenceUnavailable(msg) {
				c.waitMu.Lock()
				if w := c.leaveWaiter; w != nil {
					c.leaveWaiter = nil
					c.waitMu.Unlock()
					close(w)
				} else {
					c.waitMu.Unlock()
				}
			}
		}

		// auto-reply to disco#info queries from Jicofo
		if isDiscoInfoGet(msg) {
			c.handleDiscoQuery(msg)
			continue
		}

		// This client implements JVB sessions only. Reject peer/P2P Jingle
		// before it can overwrite the focus offer or terminate our JVB session.
		if isIQSet(msg) && strings.Contains(msg, "<jingle") && extractXMLAttr(msg, "from") != c.room+"@"+c.mucDomain+"/focus" {
			_ = c.send(fmt.Sprintf(`<iq to="%s" id="%s" type="error" xmlns="jabber:client"><error type="cancel"><feature-not-implemented xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/></error></iq>`, xmlEscape(extractXMLAttr(msg, "from")), xmlEscape(extractXMLAttr(msg, "id"))))
			continue
		}

		// Ack any incoming IQ type="set" that is NOT session-initiate (handled below)
		// and NOT disco#info (handled above). This covers source-add, source-remove, etc.
		if isIQSet(msg) && !isSessionInitiate(msg) {
			c.ackIQ(msg)
		}

		if isSessionInitiate(msg) {
			c.ackIQ(msg) // XMPP requires IQ result for incoming IQ set
			c.lastJngMu.Lock()
			c.lastJng = msg
			c.lastJngMu.Unlock()
			select {
			case c.jingles <- msg:
			default:
			}
		}

		select {
		case c.stanzas <- msg:
		case <-c.closed:
			return
		}
	}
}

// trackPresence updates the occupants map from a <presence> stanza.
// Available → add, type="unavailable" → remove. Skips self and "focus".
func (c *Conn) trackPresence(msg string) {
	from := extractXMLAttr(msg, "from")
	if from == "" {
		return
	}
	// from = "room@conference.host/<nick>"
	slash := strings.LastIndex(from, "/")
	if slash < 0 {
		return
	}
	nick := from[slash+1:]
	if nick == "" || nick == "focus" || nick == c.nick {
		return
	}
	// also skip if not from our MUC room
	if !strings.HasPrefix(from, c.room+"@") {
		return
	}

	c.occMu.Lock()
	defer c.occMu.Unlock()
	if strings.Contains(msg, `type='unavailable'`) || strings.Contains(msg, `type="unavailable"`) {
		delete(c.occupants, nick)
		delete(c.occupantNick, nick)
	} else {
		c.occupants[nick] = struct{}{}
		if display := extractPresenceNick(msg); display != "" {
			c.occupantNick[nick] = display
		}
	}
}

func extractPresenceNick(msg string) string {
	type xmlPresence struct {
		Nick string `xml:"nick"`
	}
	var p xmlPresence
	if err := xml.Unmarshal([]byte(msg), &p); err == nil && strings.TrimSpace(p.Nick) != "" {
		return strings.TrimSpace(p.Nick)
	}
	start := strings.Index(msg, "<nick")
	if start < 0 {
		return ""
	}
	gt := strings.IndexByte(msg[start:], '>')
	if gt < 0 {
		return ""
	}
	contentStart := start + gt + 1
	end := strings.Index(msg[contentStart:], "</nick>")
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(msg[contentStart : contentStart+end])
}

// Occupants returns the list of MUC nicks (other participants) currently in the room.
// "focus" and self are excluded. Order is unspecified.
func (c *Conn) Occupants() []string {
	c.occMu.Lock()
	defer c.occMu.Unlock()
	out := make([]string, 0, len(c.occupants))
	for n := range c.occupants {
		out = append(out, n)
	}
	return out
}

// OccupantDisplayName returns the display name (<nick>) advertised by the given
// MUC endpoint ID, or "" if unknown.
func (c *Conn) OccupantDisplayName(endpoint string) string {
	c.occMu.Lock()
	defer c.occMu.Unlock()
	return c.occupantNick[endpoint]
}

// OccupantDisplayNames returns a snapshot map of endpoint ID -> display name (<nick>).
func (c *Conn) OccupantDisplayNames() map[string]string {
	c.occMu.Lock()
	defer c.occMu.Unlock()
	return cloneStringMap(c.occupantNick)
}

func (c *Conn) handleDiscoQuery(msg string) {
	from := extractXMLAttr(msg, "from")
	id := extractXMLAttr(msg, "id")
	if from == "" || id == "" {
		return
	}
	resp := fmt.Sprintf(`<iq to="%s" id="%s" type="result" xmlns="jabber:client"><query xmlns="http://jabber.org/protocol/disco#info">%s</query></iq>`,
		from, id, discoFeatureXML())
	_ = c.send(resp)
}

func isDiscoInfoGet(msg string) bool {
	return strings.Contains(msg, "disco#info") && extractXMLAttr(msg, "type") == "get"
}

func isSessionInitiate(msg string) bool {
	return strings.Contains(msg, "jingle") && strings.Contains(msg, "session-initiate")
}

func discoFeatureXML() string {
	var b strings.Builder
	for _, feature := range sortedJitsiMeetFeatures() {
		fmt.Fprintf(&b, `<feature var="%s"/>`, feature)
	}
	return b.String()
}

func calculateJitsiCapsVersion() string {
	var s strings.Builder
	for _, feature := range sortedJitsiMeetFeatures() {
		s.WriteString(feature)
		s.WriteByte('<')
	}
	sum := sha1.Sum([]byte(s.String()))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func sortedJitsiMeetFeatures() []string {
	features := append([]string(nil), jitsiMeetFeatures...)
	sort.Strings(features)
	return features
}

func extractXMLAttr(s, attr string) string {
	// try single quotes first (prosody style)
	key := attr + "='"
	i := strings.Index(s, key)
	if i != -1 {
		i += len(key)
		end := strings.IndexByte(s[i:], '\'')
		if end != -1 {
			return s[i : i+end]
		}
	}
	// try double quotes
	key = attr + `="`
	i = strings.Index(s, key)
	if i != -1 {
		i += len(key)
		end := strings.IndexByte(s[i:], '"')
		if end != -1 {
			return s[i : i+end]
		}
	}
	return ""
}

func (c *Conn) auth(ctx context.Context) error {
	open := fmt.Sprintf(`<open to="%s" version="1.0" xmlns="urn:ietf:params:xml:ns:xmpp-framing"/>`, c.xmppDomain)

	// phase 1: open stream
	if err := c.send(open); err != nil {
		return err
	}
	// read until we get stream features (server may send open + features separately or together)
	initialFeatures, err := c.readUntilReturn(ctx, "features")
	if err != nil {
		return fmt.Errorf("initial features: %w", err)
	}
	if !strings.Contains(initialFeatures, "<mechanism>ANONYMOUS</mechanism>") {
		return fmt.Errorf("server does not advertise anonymous XMPP login")
	}

	// ANONYMOUS SASL
	if err := c.send(`<auth mechanism="ANONYMOUS" xmlns="urn:ietf:params:xml:ns:xmpp-sasl"/>`); err != nil {
		return err
	}
	if err := c.readUntil(ctx, "success"); err != nil {
		return fmt.Errorf("sasl: %w", err)
	}
	c.anonymous = true

	// phase 2: reopen stream after SASL
	if err := c.send(open); err != nil {
		return err
	}
	if err := c.readUntil(ctx, "features"); err != nil {
		return fmt.Errorf("post-auth features: %w", err)
	}

	// bind
	if err := c.send(`<iq type="set" id="bind_1" xmlns="jabber:client"><bind xmlns="urn:ietf:params:xml:ns:xmpp-bind"/></iq>`); err != nil {
		return err
	}
	bindResp, err := c.readUntilReturn(ctx, "<jid>")
	if err != nil {
		return fmt.Errorf("bind: %w", err)
	}
	c.jid = extractJID(bindResp)
	if c.jid == "" {
		return fmt.Errorf("bind failed: %s", bindResp)
	}
	parts := strings.Split(c.jid, "@")
	if len(parts) > 0 && len(parts[0]) >= 8 {
		c.nick = parts[0][:8]
	}

	// session
	if err := c.send(`<iq type="set" id="sess_1" xmlns="jabber:client"><session xmlns="urn:ietf:params:xml:ns:xmpp-session"/></iq>`); err != nil {
		return err
	}
	if err := c.readUntil(ctx, "sess_1"); err != nil {
		return fmt.Errorf("session: %w", err)
	}

	// enable stream management
	if err := c.send(`<enable resume="true" xmlns="urn:xmpp:sm:3"/>`); err != nil {
		return err
	}
	if err := c.readUntil(ctx, "enabled"); err != nil {
		return fmt.Errorf("sm enable: %w", err)
	}

	return nil
}

func (c *Conn) readUntil(ctx context.Context, substr string) error {
	for {
		msg, err := c.readOne(ctx)
		if err != nil {
			return err
		}
		if c.debug {
			fmt.Fprintf(os.Stderr, "[xmpp] <- %s\n", msg)
		}
		if strings.Contains(msg, substr) {
			return nil
		}
		if strings.Contains(msg, "stream:error") || strings.Contains(msg, "<failure") {
			return fmt.Errorf("server error: %s", msg)
		}
	}
}

func (c *Conn) readUntilReturn(ctx context.Context, substr string) (string, error) {
	for {
		msg, err := c.readOne(ctx)
		if err != nil {
			return "", err
		}
		if c.debug {
			fmt.Fprintf(os.Stderr, "[xmpp] <- %s\n", msg)
		}
		if strings.Contains(msg, substr) {
			return msg, nil
		}
		if strings.Contains(msg, "stream:error") || strings.Contains(msg, "<failure") {
			return "", fmt.Errorf("server error: %s", msg)
		}
	}
}

func (c *Conn) DiscoverServices(ctx context.Context) ([]Service, error) {
	iq := fmt.Sprintf(`<iq type="get" to="%s" id="disco_1" xmlns="jabber:client"><services xmlns="urn:xmpp:extdisco:2"/></iq>`, c.xmppDomain)
	if err := c.send(iq); err != nil {
		return nil, err
	}
	return c.waitServices(ctx)
}

func (c *Conn) waitServices(ctx context.Context) ([]Service, error) {
	for {
		select {
		case msg := <-c.stanzas:
			if strings.Contains(msg, "urn:xmpp:extdisco:2") {
				return parseServices(msg), nil
			}
			// Server doesn't support extdisco:2 — return empty list, ICE will
			// rely on host candidates only (or whatever Jicofo provides in jingle).
			if strings.Contains(msg, `id='disco_1'`) && strings.Contains(msg, `type='error'`) {
				return nil, nil
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-c.closed:
			return nil, fmt.Errorf("connection closed")
		}
	}
}

func (c *Conn) AllocateFocus(ctx context.Context, room string) error {
	roomJID := fmt.Sprintf("%s@%s", room, c.mucDomain)
	iq := fmt.Sprintf(`<iq to="focus.%s" type="set" id="focus_1" xmlns="jabber:client"><conference room="%s" machine-uid="%s" xmlns="http://jitsi.org/protocol/focus"><property name="rtcstatsEnabled" value="false"/><property name="visitors-version" value="1"/></conference></iq>`,
		c.xmppDomain, roomJID, c.nick)
	if err := c.send(iq); err != nil {
		return err
	}
	// wait for focus response
	for {
		select {
		case msg := <-c.stanzas:
			if strings.Contains(msg, "conference") && strings.Contains(msg, "ready") {
				info := parseFocusInfo(msg)
				info.AnonymousXMPP = c.anonymous
				c.mu.Lock()
				c.focusInfo = info
				c.mu.Unlock()
				return nil
			}
			if isIQError(msg) && strings.Contains(msg, "focus_1") {
				return fmt.Errorf("focus allocation failed: %s", msg)
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closed:
			return fmt.Errorf("connection closed")
		}
	}
}

func (c *Conn) JoinMUC(ctx context.Context, room, displayName string) error {
	roomJID := fmt.Sprintf("%s@%s/%s", room, c.mucDomain, c.nick)
	presence := fmt.Sprintf(`<presence to="%s" xmlns="jabber:client"><x xmlns="http://jabber.org/protocol/muc"/><stats-id>%s</stats-id><c hash="sha-1" node="%s" ver="%s" xmlns="http://jabber.org/protocol/caps"/><SourceInfo>{"%s-a0":{"muted":true},"%s-v0":{"muted":true}}</SourceInfo><jitsi_participant_codecList>vp8</jitsi_participant_codecList><nick xmlns="http://jabber.org/protocol/nick">%s</nick></presence>`,
		xmlEscape(roomJID), c.nick+"-stream", jitsiCapsNode, jitsiCapsVersion, c.nick, c.nick, xmlEscape(displayName))
	if err := c.send(presence); err != nil {
		return err
	}
	// wait for self-presence (status 110)
	for {
		select {
		case msg := <-c.stanzas:
			if strings.Contains(msg, "status code=\"110\"") || strings.Contains(msg, `code='110'`) {
				_ = c.SendRoomInfo(room)
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		case <-c.closed:
			return fmt.Errorf("connection closed")
		}
	}
}

func (c *Conn) SendRoomInfo(room string) error {
	roomBareJID := fmt.Sprintf("%s@%s", room, c.mucDomain)
	id := c.NextID()
	iq := fmt.Sprintf(`<iq to="%s" type="get" id="%s" xmlns="jabber:client"><query xmlns="http://jabber.org/protocol/disco#info"/></iq>`, roomBareJID, id)
	return c.send(iq)
}

func (c *Conn) WaitJingle(ctx context.Context) (string, error) {
	for {
		select {
		case msg := <-c.jingles:
			return msg, nil
		case <-ctx.Done():
			return "", ctx.Err()
		case <-c.closed:
			return "", fmt.Errorf("connection closed")
		}
	}
}

// LastJingleStanza returns the most recently received Jingle session-initiate raw stanza.
func (c *Conn) LastJingleStanza() string {
	c.lastJngMu.Lock()
	defer c.lastJngMu.Unlock()
	return c.lastJng
}

func (c *Conn) SendSessionAccept(sid, initiator, roomJID, sdp string) error {
	_, err := c.SendJingleWait(roomJID+"/focus", "session-accept", sid, initiator, sdp, 10*time.Second)
	return err
}

// SendJingle sends an arbitrary Jingle action (transport-info, source-add, source-remove,
// session-terminate, …). innerXML is the body inside <jingle …>.
func (c *Conn) SendJingle(to, action, sid, initiator string, innerXML string) error {
	id := c.NextID()
	iq := fmt.Sprintf(
		`<iq to="%s" type="set" id="%s" xmlns="jabber:client"><jingle xmlns="urn:xmpp:jingle:1" action="%s" sid="%s" initiator="%s" responder="%s">%s</jingle></iq>`,
		to, id, action, sid, initiator, c.jid, innerXML)
	return c.send(iq)
}

// SendJingleWait sends a Jingle IQ and waits until the recipient acknowledges
// it with a matching <iq type="result"/> or <iq type="error"/>.
func (c *Conn) SendJingleWait(to, action, sid, initiator string, innerXML string, timeout time.Duration) (string, error) {
	id := c.NextID()
	iq := fmt.Sprintf(
		`<iq to="%s" type="set" id="%s" xmlns="jabber:client"><jingle xmlns="urn:xmpp:jingle:1" action="%s" sid="%s" initiator="%s" responder="%s">%s</jingle></iq>`,
		to, id, action, sid, initiator, c.jid, innerXML)
	return c.SendIQWait(iq, id, timeout)
}

func (c *Conn) SendGroupchat(roomJID, body string) error {
	msg := fmt.Sprintf(`<message to="%s" type="groupchat" xmlns="jabber:client"><body>%s</body></message>`, roomJID, xmlEscape(body))
	return c.send(msg)
}

func (c *Conn) RaiseHand(room string) error {
	roomJID := fmt.Sprintf("%s@%s/%s", room, c.mucDomain, c.nick)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	return c.send(fmt.Sprintf(`<presence to="%s" xmlns="jabber:client"><jitsi_participant_raisedHand>%s</jitsi_participant_raisedHand></presence>`, roomJID, ts))
}

func (c *Conn) LowerHand(room string) error {
	roomJID := fmt.Sprintf("%s@%s/%s", room, c.mucDomain, c.nick)
	return c.send(fmt.Sprintf(`<presence to="%s" xmlns="jabber:client"><jitsi_participant_raisedHand/></presence>`, roomJID))
}

func (c *Conn) LeaveMUC(room string) error {
	roomJID := fmt.Sprintf("%s@%s/%s", room, c.mucDomain, c.nick)
	return c.send(fmt.Sprintf(`<presence to="%s" type="unavailable" xmlns="jabber:client"/>`, roomJID))
}

// LeaveMUCWait sends MUC presence unavailable and waits for Prosody to echo
// it back (the same handshake lib-jitsi-meet uses via XMPPEvents.MUC_LEFT).
// Returning nil means the server has acknowledged our exit and routed it on
// to Jicofo; the bridge slot can be reclaimed before this function returns.
// Times out at the supplied deadline so a wedged server never hangs callers.
func (c *Conn) LeaveMUCWait(room string, timeout time.Duration) error {
	w := make(chan struct{})
	c.waitMu.Lock()
	// A second concurrent leave (shouldn't normally happen) just inherits
	// the existing waiter; we don't try to chain them.
	if c.leaveWaiter == nil {
		c.leaveWaiter = w
	} else {
		w = c.leaveWaiter
	}
	c.waitMu.Unlock()

	roomJID := fmt.Sprintf("%s@%s/%s", room, c.mucDomain, c.nick)
	if err := c.send(fmt.Sprintf(`<presence to="%s" type="unavailable" xmlns="jabber:client"/>`, roomJID)); err != nil {
		c.waitMu.Lock()
		if c.leaveWaiter == w {
			c.leaveWaiter = nil
		}
		c.waitMu.Unlock()
		return err
	}

	select {
	case <-w:
		return nil
	case <-c.closed:
		return fmt.Errorf("connection closed before MUC leave confirmed")
	case <-time.After(timeout):
		c.waitMu.Lock()
		if c.leaveWaiter == w {
			c.leaveWaiter = nil
		}
		c.waitMu.Unlock()
		return fmt.Errorf("timeout waiting for MUC leave confirmation")
	}
}

// SendIQWait sends an IQ and waits for a matching <iq type="result"/> or
// <iq type="error"/> keyed by stanza id. Used for fire-and-confirm flows
// like session-terminate where the caller needs to know the server has
// accepted the request before continuing tear-down.
func (c *Conn) SendIQWait(iqXML, id string, timeout time.Duration) (string, error) {
	if id == "" {
		return "", fmt.Errorf("SendIQWait requires non-empty id")
	}
	ch := make(chan string, 1)
	c.waitMu.Lock()
	c.iqWaiters[id] = ch
	c.waitMu.Unlock()

	if err := c.send(iqXML); err != nil {
		c.waitMu.Lock()
		delete(c.iqWaiters, id)
		c.waitMu.Unlock()
		return "", err
	}

	select {
	case reply := <-ch:
		if isIQError(reply) {
			return reply, fmt.Errorf("server rejected IQ %s (use explicit debug logs for details)", id)
		}
		return reply, nil
	case <-c.closed:
		c.waitMu.Lock()
		delete(c.iqWaiters, id)
		c.waitMu.Unlock()
		return "", fmt.Errorf("connection closed before IQ %s reply", id)
	case <-time.After(timeout):
		c.waitMu.Lock()
		delete(c.iqWaiters, id)
		c.waitMu.Unlock()
		return "", fmt.Errorf("timeout waiting for IQ %s reply", id)
	}
}

// isIQResultOrError tells whether a stanza is an IQ acknowledging an earlier
// IQ we sent. Used in the read loop to dispatch SendIQWait callers.
func isIQResultOrError(msg string) bool {
	if !strings.HasPrefix(msg, "<iq") {
		return false
	}
	// type attribute is small and appears near the front of the iq element
	t := extractXMLAttr(msg, "type")
	return t == "result" || t == "error"
}

func isIQError(msg string) bool {
	return strings.HasPrefix(msg, "<iq") && extractXMLAttr(msg, "type") == "error"
}

func isIQSet(msg string) bool {
	return strings.HasPrefix(msg, "<iq") && extractXMLAttr(msg, "type") == "set"
}

// ackIQ sends an <iq type="result"/> for an incoming IQ type="set".
func (c *Conn) ackIQ(msg string) {
	from := extractXMLAttr(msg, "from")
	id := extractXMLAttr(msg, "id")
	if from == "" || id == "" {
		return
	}
	_ = c.send(fmt.Sprintf(`<iq to="%s" id="%s" type="result" xmlns="jabber:client"/>`, from, id))
}

// isOwnPresenceUnavailable matches the broadcast Prosody sends back to us
// when our MUC presence unavailable has been processed: from is our own
// MUC JID with type="unavailable". This is what fires LeaveMUCWait.
func (c *Conn) isOwnPresenceUnavailable(msg string) bool {
	if !strings.Contains(msg, `type='unavailable'`) && !strings.Contains(msg, `type="unavailable"`) {
		return false
	}
	from := extractXMLAttr(msg, "from")
	if from == "" || c.nick == "" || c.room == "" {
		return false
	}
	want := fmt.Sprintf("%s@%s/%s", c.room, c.mucDomain, c.nick)
	return from == want
}

func extractJID(s string) string {
	start := strings.Index(s, "<jid>")
	if start == -1 {
		return ""
	}
	start += 5
	end := strings.Index(s[start:], "</jid>")
	if end == -1 {
		return ""
	}
	return s[start : start+end]
}

func parseServices(s string) []Service {
	type xmlService struct {
		Type      string `xml:"type,attr"`
		Host      string `xml:"host,attr"`
		Port      string `xml:"port,attr"`
		Transport string `xml:"transport,attr"`
		Username  string `xml:"username,attr"`
		Password  string `xml:"password,attr"`
	}
	type xmlServices struct {
		Services []xmlService `xml:"service"`
	}
	type xmlIQ struct {
		Services xmlServices `xml:"services"`
	}

	var iq xmlIQ
	_ = xml.Unmarshal([]byte(s), &iq)

	var result []Service
	for _, svc := range iq.Services.Services {
		result = append(result, Service(svc))
	}
	return result
}

func parseFocusInfo(s string) FocusInfo {
	type xmlProperty struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	}
	type xmlConference struct {
		Ready      string        `xml:"ready,attr"`
		Properties []xmlProperty `xml:"property"`
	}
	type xmlIQ struct {
		Conference xmlConference `xml:"conference"`
	}

	var iq xmlIQ
	_ = xml.Unmarshal([]byte(s), &iq)

	props := make(map[string]string)
	for _, prop := range iq.Conference.Properties {
		if prop.Name != "" {
			props[prop.Name] = prop.Value
		}
	}

	return FocusInfo{
		Ready:                  strings.EqualFold(iq.Conference.Ready, "true"),
		AuthenticationRequired: strings.EqualFold(props["authentication"], "true"),
		ExternalAuth:           strings.EqualFold(props["externalAuth"], "true"),
		VisitorsSupported:      strings.EqualFold(props["visitors-supported"], "true"),
		Properties:             props,
	}
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// --- BOSH transport (XEP-0124 / XEP-0206) ---

// boshTransport implements the transport interface over BOSH (HTTP long-polling).
type boshTransport struct {
	url    string
	client *http.Client
	sid    string
	rid    atomic.Int64
	mu     sync.Mutex
	inbox  chan []byte // stanzas received from server
	closed chan struct{}
	once   sync.Once
}

func newBOSHTransport(boshURL string, client *http.Client) *boshTransport {
	bt := &boshTransport{
		url:    boshURL,
		client: client,
		inbox:  make(chan []byte, 64),
		closed: make(chan struct{}),
	}
	bt.rid.Store(time.Now().UnixNano() % 1000000)
	return bt
}

func (bt *boshTransport) nextRID() int64 {
	return bt.rid.Add(1)
}

// init sends the BOSH session-creation request and starts the long-poll loop.
func (bt *boshTransport) init(xmppDomain string) error {
	rid := bt.nextRID()
	body := fmt.Sprintf(
		`<body rid="%d" to="%s" xml:lang="en" wait="60" hold="1" content="text/xml; charset=utf-8" ver="1.6" xmpp:version="1.0" xmlns="http://jabber.org/protocol/httpbind" xmlns:xmpp="urn:xmpp:xbosh"/>`,
		rid, xmppDomain)

	respBody, err := bt.post(body)
	if err != nil {
		return fmt.Errorf("bosh init: %w", err)
	}

	// Extract sid from response <body sid="..." ...>
	sid := extractXMLAttr(string(respBody), "sid")
	if sid == "" {
		return fmt.Errorf("bosh init: no sid in response: %s", string(respBody))
	}
	bt.sid = sid

	// Extract stanzas from the initial response body
	bt.extractAndQueue(respBody)

	// Start long-poll loop
	go bt.pollLoop()
	return nil
}

func (bt *boshTransport) post(body string) ([]byte, error) {
	req, err := http.NewRequest("POST", bt.url, bytes.NewBufferString(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	resp, err := bt.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bosh: HTTP %d: %s", resp.StatusCode, string(data))
	}
	return data, nil
}

// extractAndQueue pulls child stanzas from a <body>...</body> response and queues them.
func (bt *boshTransport) extractAndQueue(data []byte) {
	s := string(data)
	// Strip outer <body ...>...</body> wrapper to get inner stanzas.
	// Find end of opening <body ...> tag
	inner := extractBOSHInner(s)
	if inner == "" {
		return
	}
	// Split into individual top-level elements (stanzas).
	// Simple approach: each stanza starts with '<' at depth 0.
	stanzas := splitXMLElements(inner)
	for _, st := range stanzas {
		st = strings.TrimSpace(st)
		if st == "" {
			continue
		}
		select {
		case bt.inbox <- []byte(st):
		case <-bt.closed:
			return
		}
	}
}

func (bt *boshTransport) pollLoop() {
	for {
		select {
		case <-bt.closed:
			return
		default:
		}
		rid := bt.nextRID()
		body := fmt.Sprintf(
			`<body rid="%d" sid="%s" xmlns="http://jabber.org/protocol/httpbind"/>`,
			rid, bt.sid)
		respData, err := bt.post(body)
		if err != nil {
			bt.once.Do(func() { close(bt.closed) })
			return
		}
		// Check for terminate
		if strings.Contains(string(respData), `type="terminate"`) || strings.Contains(string(respData), `type='terminate'`) {
			bt.once.Do(func() { close(bt.closed) })
			return
		}
		bt.extractAndQueue(respData)
	}
}

func (bt *boshTransport) Send(data []byte) error {
	select {
	case <-bt.closed:
		return fmt.Errorf("bosh connection closed")
	default:
	}
	bt.mu.Lock()
	defer bt.mu.Unlock()
	rid := bt.nextRID()
	body := fmt.Sprintf(
		`<body rid="%d" sid="%s" xmlns="http://jabber.org/protocol/httpbind">%s</body>`,
		rid, bt.sid, string(data))
	respData, err := bt.post(body)
	if err != nil {
		return err
	}
	bt.extractAndQueue(respData)
	return nil
}

func (bt *boshTransport) Recv(ctx context.Context) ([]byte, error) {
	select {
	case data := <-bt.inbox:
		return data, nil
	case <-bt.closed:
		return nil, fmt.Errorf("bosh connection closed")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (bt *boshTransport) Close() error {
	bt.once.Do(func() { close(bt.closed) })
	// Send terminate
	bt.mu.Lock()
	defer bt.mu.Unlock()
	rid := bt.nextRID()
	body := fmt.Sprintf(
		`<body rid="%d" sid="%s" type="terminate" xmlns="http://jabber.org/protocol/httpbind"/>`,
		rid, bt.sid)
	_, _ = bt.post(body)
	return nil
}

// dialBOSH creates a Conn using BOSH transport.
func dialBOSH(
	ctx context.Context, host, room string, debug bool, cfg jitsiConfig, client *http.Client,
) (*Conn, error) {
	bt := newBOSHTransport(cfg.bosh, client)
	if err := bt.init(cfg.xmppDomain); err != nil {
		return nil, err
	}

	c := &Conn{
		tr:           bt,
		host:         host,
		room:         room,
		mucDomain:    cfg.mucDomain,
		xmppDomain:   cfg.xmppDomain,
		debug:        debug,
		bosh:         true,
		occupants:    make(map[string]struct{}),
		occupantNick: make(map[string]string),
		stanzas:      make(chan string, 64),
		jingles:      make(chan string, 8),
		closed:       make(chan struct{}),
		iqWaiters:    make(map[string]chan string),
	}

	if err := c.authBOSH(ctx, bt); err != nil {
		_ = bt.Close()
		return nil, err
	}

	go c.readLoop()
	go c.keepaliveLoop()
	return c, nil
}

// authBOSH performs SASL ANONYMOUS + bind + session over BOSH.
// The initial <body> already opened the stream; we just need to do SASL and bind.
func (c *Conn) authBOSH(ctx context.Context, bt *boshTransport) error {
	// Read initial features from the inbox (queued by init)
	initialFeatures, err := c.readUntilReturn(ctx, "features")
	if err != nil {
		return fmt.Errorf("initial features: %w", err)
	}
	if !strings.Contains(initialFeatures, "<mechanism>ANONYMOUS</mechanism>") {
		return fmt.Errorf("server does not advertise anonymous XMPP login")
	}

	// ANONYMOUS SASL
	if err := c.send(`<auth mechanism="ANONYMOUS" xmlns="urn:ietf:params:xml:ns:xmpp-sasl"/>`); err != nil {
		return err
	}
	if err := c.readUntil(ctx, "success"); err != nil {
		return fmt.Errorf("sasl: %w", err)
	}
	c.anonymous = true

	// Restart stream via BOSH
	bt.mu.Lock()
	rid := bt.nextRID()
	body := fmt.Sprintf(
		`<body rid="%d" sid="%s" to="%s" xml:lang="en" xmpp:restart="true" xmlns="http://jabber.org/protocol/httpbind" xmlns:xmpp="urn:xmpp:xbosh"/>`,
		rid, bt.sid, c.xmppDomain)
	respData, err := bt.post(body)
	bt.mu.Unlock()
	if err != nil {
		return fmt.Errorf("bosh restart: %w", err)
	}
	bt.extractAndQueue(respData)

	if err := c.readUntil(ctx, "features"); err != nil {
		return fmt.Errorf("post-auth features: %w", err)
	}

	// bind
	if err := c.send(`<iq type="set" id="bind_1" xmlns="jabber:client"><bind xmlns="urn:ietf:params:xml:ns:xmpp-bind"/></iq>`); err != nil {
		return err
	}
	bindResp, err := c.readUntilReturn(ctx, "<jid>")
	if err != nil {
		return fmt.Errorf("bind: %w", err)
	}
	c.jid = extractJID(bindResp)
	if c.jid == "" {
		return fmt.Errorf("bind failed: %s", bindResp)
	}
	parts := strings.Split(c.jid, "@")
	if len(parts) > 0 && len(parts[0]) >= 8 {
		c.nick = parts[0][:8]
	}

	// session
	if err := c.send(`<iq type="set" id="sess_1" xmlns="jabber:client"><session xmlns="urn:ietf:params:xml:ns:xmpp-session"/></iq>`); err != nil {
		return err
	}
	if err := c.readUntil(ctx, "sess_1"); err != nil {
		return fmt.Errorf("session: %w", err)
	}

	return nil
}

// extractBOSHInner returns the content between <body ...> and </body>.
func extractBOSHInner(s string) string {
	// Find end of opening tag
	start := strings.Index(s, ">")
	if start == -1 {
		return ""
	}
	// Check for self-closing <body ... />
	if s[start-1] == '/' {
		return ""
	}
	start++
	end := strings.LastIndex(s, "</body>")
	if end == -1 || end <= start {
		return ""
	}
	return s[start:end]
}

// splitXMLElements splits a string of concatenated XML elements into individual elements.
func splitXMLElements(s string) []string {
	var result []string
	depth := 0
	start := -1
	i := 0
	for i < len(s) {
		if s[i] == '<' {
			if start == -1 {
				start = i
			}
			// Check for self-closing or closing tag
			end := strings.IndexByte(s[i:], '>')
			if end == -1 {
				break
			}
			tag := s[i : i+end+1]
			if strings.HasPrefix(tag, "</") {
				depth--
				if depth == 0 && start != -1 {
					result = append(result, s[start:i+end+1])
					start = -1
				}
			} else if strings.HasSuffix(tag, "/>") {
				if depth == 0 {
					result = append(result, s[start:i+end+1])
					start = -1
				}
			} else {
				depth++
			}
			i += end + 1
		} else {
			i++
		}
	}
	return result
}
