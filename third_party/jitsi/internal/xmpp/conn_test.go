package xmpp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// mockTransport is a fake transport for testing.
type mockTransport struct {
	incoming chan []byte
	outgoing chan []byte
	closed   atomic.Bool
	mu       sync.Mutex
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		incoming: make(chan []byte, 1024),
		outgoing: make(chan []byte, 8192),
	}
}

func (m *mockTransport) Send(data []byte) error {
	if m.closed.Load() {
		return fmt.Errorf("closed")
	}
	select {
	case m.outgoing <- append([]byte(nil), data...):
		return nil
	default:
		return fmt.Errorf("outgoing full")
	}
}

func (m *mockTransport) Recv(ctx context.Context) ([]byte, error) {
	select {
	case d := <-m.incoming:
		return d, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (m *mockTransport) Close() error {
	m.closed.Store(true)
	return nil
}

func (m *mockTransport) inject(s string) {
	m.incoming <- []byte(s)
}

func (m *mockTransport) drain() []string {
	var out []string
	for {
		select {
		case d := <-m.outgoing:
			out = append(out, string(d))
		default:
			return out
		}
	}
}

func newTestConn(mt *mockTransport) *Conn {
	return &Conn{
		tr:         mt,
		host:       "test.example.com",
		room:       "testroom",
		mucDomain:  "conference.test.example.com",
		xmppDomain: "test.example.com",
		nick:       "testnick",
		occupants:  make(map[string]struct{}),
		stanzas:    make(chan string, 64),
		jingles:    make(chan string, 8),
		closed:     make(chan struct{}),
		iqWaiters:  make(map[string]chan string),
	}
}

// --- Config parsing tests ---

func TestFetchConfigUsesInjectedHTTPClient(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != "https://custom.test/config.js" {
			t.Fatalf("request URL = %q", req.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`config.hosts.domain = 'xmpp.test';
config.hosts.muc = 'conference.test';
config.websocket = 'wss://ws.test/xmpp-websocket';
config.bosh = 'https://bosh.test/http-bind';`)),
		}, nil
	})}

	cfg := fetchConfig(context.Background(), "custom.test", client)
	if cfg.xmppDomain != "xmpp.test" || cfg.mucDomain != "conference.test" ||
		cfg.websocket != "wss://ws.test/xmpp-websocket" || cfg.bosh != "https://bosh.test/http-bind" {
		t.Fatalf("fetchConfig() = %#v", cfg)
	}
}

func TestSelectHTTPClientPrefersInjectedClient(t *testing.T) {
	client := &http.Client{}
	if got := selectHTTPClient(client, true); got != client {
		t.Fatal("selectHTTPClient did not prefer the injected client")
	}
	if got := selectHTTPClient(nil, false); got != http.DefaultClient {
		t.Fatal("selectHTTPClient did not use the default client")
	}
}

func TestExtractStringField(t *testing.T) {
	tests := []struct {
		name, body, key, want string
	}{
		{"simple domain", "config.hosts.domain = 'meet.jitsi';", "domain", "meet.jitsi"},
		{"colon style", "  domain: 'example.com',", "domain", "example.com"},
		{"concat with var", "config.hosts.muc = 'muc.' + subdomain + 'meet.jitsi';", "muc", "muc.meet.jitsi"},
		{"double quotes", `config.hosts.domain = "meet.jitsi";`, "domain", "meet.jitsi"},
		{"websocket url", "config.websocket = 'wss://jitsi.example.com/xmpp-websocket';", "websocket", "wss://jitsi.example.com/xmpp-websocket"},
		{"bosh url", "config.bosh = 'https://meet.example.com/http-bind';", "bosh", "https://meet.example.com/http-bind"},
		{"no match", "config.hosts.domain = 'x';", "muc", ""},
		{"partial key no match", "config.hosts.domainExtra = 'x';", "domain", ""},
		{"concat subdir", "config.websocket = 'wss://host/' + subdir + 'xmpp-websocket';", "websocket", "wss://host/xmpp-websocket"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractStringField(tt.body, tt.key)
			if got != tt.want {
				t.Errorf("extractStringField(%q, %q) = %q, want %q", tt.body, tt.key, got, tt.want)
			}
		})
	}
}

func TestJoinStringLiterals(t *testing.T) {
	tests := []struct {
		expr, want string
	}{
		{"'hello'", "hello"},
		{"'a' + 'b'", "ab"},
		{"'muc.' + subdomain + 'meet.jitsi'", "muc.meet.jitsi"},
		{`"wss://host/ws"`, "wss://host/ws"},
		{"''", ""},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			got := joinStringLiterals(tt.expr)
			if got != tt.want {
				t.Errorf("joinStringLiterals(%q) = %q, want %q", tt.expr, got, tt.want)
			}
		})
	}
}

// --- IQ ack tests ---

func TestAckIQ_SessionInitiate(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()

	// Inject a session-initiate IQ
	mt.inject(`<iq from="testroom@conference.test.example.com/focus" id="si_123" type="set" xmlns="jabber:client"><jingle xmlns="urn:xmpp:jingle:1" action="session-initiate" sid="abc"><content name="audio"/></jingle></iq>`)

	// Wait for jingle to arrive
	select {
	case <-c.jingles:
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for jingle")
	}

	// Check that IQ result was sent
	sent := mt.drain()
	found := false
	for _, s := range sent {
		if strings.Contains(s, `id="si_123"`) && strings.Contains(s, `type="result"`) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected IQ result for session-initiate, got: %v", sent)
	}
}

func TestAckIQ_SourceAdd(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()

	mt.inject(`<iq from="testroom@conference.test.example.com/focus" id="sa_456" type="set" xmlns="jabber:client"><jingle xmlns="urn:xmpp:jingle:1" action="source-add" sid="abc"><content name="video"/></jingle></iq>`)

	time.Sleep(50 * time.Millisecond)
	sent := mt.drain()
	found := false
	for _, s := range sent {
		if strings.Contains(s, `id="sa_456"`) && strings.Contains(s, `type="result"`) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected IQ result for source-add, got: %v", sent)
	}
}

func TestDiscoInfoResponse(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()

	mt.inject(`<iq from="focus@auth.test.example.com" id="disco_99" type="get" xmlns="jabber:client"><query xmlns="http://jabber.org/protocol/disco#info"/></iq>`)

	time.Sleep(50 * time.Millisecond)
	sent := mt.drain()
	found := false
	for _, s := range sent {
		if strings.Contains(s, `id="disco_99"`) && strings.Contains(s, `type="result"`) && strings.Contains(s, "disco#info") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected disco#info result, got: %v", sent)
	}
}

// --- Stress tests ---

func TestStress_ConcurrentSends(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	defer c.Close()

	const goroutines = 50
	const msgsPerGoroutine = 100

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < msgsPerGoroutine; j++ {
				_ = c.send(fmt.Sprintf(`<message id="%d-%d"/>`, id, j))
			}
		}(i)
	}
	wg.Wait()

	sent := mt.drain()
	if len(sent) != goroutines*msgsPerGoroutine {
		t.Errorf("expected %d messages, got %d", goroutines*msgsPerGoroutine, len(sent))
	}
}

func TestStress_ConcurrentSendAfterClose(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)

	c.Close()

	var wg sync.WaitGroup
	wg.Add(100)
	errors := make(chan error, 100)
	for i := 0; i < 100; i++ {
		go func() {
			defer wg.Done()
			errors <- c.send(`<message/>`)
		}()
	}
	wg.Wait()
	close(errors)

	for err := range errors {
		if err == nil {
			t.Error("expected error sending on closed conn")
		}
	}
}

func TestStress_OccupantTracking(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	// Use larger stanzas buffer to avoid blocking readLoop
	c.stanzas = make(chan string, 512)
	go c.readLoop()
	defer c.Close()

	// Simulate 100 participants joining
	for i := 0; i < 100; i++ {
		nick := fmt.Sprintf("user%04d", i)
		mt.inject(fmt.Sprintf(
			`<presence from="testroom@conference.test.example.com/%s" xmlns="jabber:client"><x xmlns="http://jabber.org/protocol/muc#user"><item role="participant"/></x></presence>`,
			nick))
	}
	time.Sleep(200 * time.Millisecond)

	occs := c.Occupants()
	if len(occs) != 100 {
		t.Errorf("expected 100 occupants, got %d", len(occs))
	}

	// Simulate 50 leaving
	for i := 0; i < 50; i++ {
		nick := fmt.Sprintf("user%04d", i)
		mt.inject(fmt.Sprintf(
			`<presence from="testroom@conference.test.example.com/%s" type="unavailable" xmlns="jabber:client"><x xmlns="http://jabber.org/protocol/muc#user"><item role="none"/></x></presence>`,
			nick))
	}
	time.Sleep(200 * time.Millisecond)

	occs = c.Occupants()
	if len(occs) != 50 {
		t.Errorf("expected 50 occupants after leaves, got %d", len(occs))
	}
}

func TestStress_IQWaiters(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()

	const count = 50
	var wg sync.WaitGroup
	wg.Add(count)

	for i := 0; i < count; i++ {
		go func(id int) {
			defer wg.Done()
			iqID := fmt.Sprintf("stress-%d", id)
			iq := fmt.Sprintf(`<iq to="target" type="set" id="%s" xmlns="jabber:client"><body/></iq>`, iqID)
			go func() {
				time.Sleep(10 * time.Millisecond)
				mt.inject(fmt.Sprintf(`<iq from="target" id="%s" type="result" xmlns="jabber:client"/>`, iqID))
			}()
			_, err := c.SendIQWait(iq, iqID, 2*time.Second)
			if err != nil {
				t.Errorf("IQ %s failed: %v", iqID, err)
			}
		}(i)
	}
	wg.Wait()
}

func TestStress_IQWaiterTimeout(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()

	_, err := c.SendIQWait(`<iq to="x" type="set" id="timeout1" xmlns="jabber:client"/>`, "timeout1", 50*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

// --- BOSH helper tests ---

func TestExtractBOSHInner(t *testing.T) {
	tests := []struct {
		name, input, want string
	}{
		{"normal", `<body sid="x" xmlns="http://jabber.org/protocol/httpbind"><iq id="1"/><presence/></body>`, `<iq id="1"/><presence/>`},
		{"self-closing", `<body sid="x" xmlns="http://jabber.org/protocol/httpbind"/>`, ""},
		{"empty body", `<body sid="x"></body>`, ""},
		{"with features", `<body sid="abc"><stream:features><mechanisms xmlns="urn:ietf:params:xml:ns:xmpp-sasl"><mechanism>ANONYMOUS</mechanism></mechanisms></stream:features></body>`, `<stream:features><mechanisms xmlns="urn:ietf:params:xml:ns:xmpp-sasl"><mechanism>ANONYMOUS</mechanism></mechanisms></stream:features>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBOSHInner(tt.input)
			if got != tt.want {
				t.Errorf("extractBOSHInner = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSplitXMLElements(t *testing.T) {
	tests := []struct {
		name  string
		input string
		count int
	}{
		{"two elements", `<iq id="1"/><presence from="x"/>`, 2},
		{"nested", `<stream:features><mechanisms><mechanism>ANONYMOUS</mechanism></mechanisms></stream:features>`, 1},
		{"mixed", `<iq id="1"/><presence><x><item/></x></presence><message><body>hi</body></message>`, 3},
		{"empty", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitXMLElements(tt.input)
			if len(got) != tt.count {
				t.Errorf("splitXMLElements got %d elements, want %d: %v", len(got), tt.count, got)
			}
		})
	}
}

// --- LeaveMUCWait test ---

func TestLeaveMUCWait_Success(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()

	go func() {
		time.Sleep(20 * time.Millisecond)
		// Echo back our own unavailable presence
		mt.inject(fmt.Sprintf(
			`<presence from="testroom@conference.test.example.com/testnick" type="unavailable" xmlns="jabber:client"><x xmlns="http://jabber.org/protocol/muc#user"><item role="none"/><status code="110"/></x></presence>`))
	}()

	err := c.LeaveMUCWait("testroom", 2*time.Second)
	if err != nil {
		t.Errorf("LeaveMUCWait failed: %v", err)
	}
}

func TestLeaveMUCWait_Timeout(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()

	err := c.LeaveMUCWait("testroom", 50*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("expected timeout, got: %v", err)
	}
}

// --- Stream management in readLoop ---

func TestReadLoop_StreamManagement(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()

	// Server sends <r/> — we should respond with <a/>
	mt.inject(`<r xmlns="urn:xmpp:sm:3"/>`)
	time.Sleep(50 * time.Millisecond)

	sent := mt.drain()
	found := false
	for _, s := range sent {
		if strings.Contains(s, `<a h="`) && strings.Contains(s, `xmlns="urn:xmpp:sm:3"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("expected SM ack, got: %v", sent)
	}
}

// --- Rapid connect/disconnect stress ---

func TestStress_RapidCloseReopen(t *testing.T) {
	for i := 0; i < 100; i++ {
		mt := newMockTransport()
		c := newTestConn(mt)
		go c.readLoop()
		go c.keepaliveLoop()
		_ = c.send(`<presence/>`)
		c.Close()
	}
}

// --- Concurrent readLoop + close ---

func TestStress_ReadLoopCloseRace(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mt := newMockTransport()
			c := newTestConn(mt)
			go c.readLoop()
			// Inject some messages while closing
			go func() {
				for j := 0; j < 10; j++ {
					mt.inject(fmt.Sprintf(`<presence from="testroom@conference.test.example.com/u%d" xmlns="jabber:client"/>`, j))
				}
			}()
			time.Sleep(5 * time.Millisecond)
			c.Close()
		}()
	}
	wg.Wait()
}

// --- extractXMLAttr ---

func TestExtractXMLAttr(t *testing.T) {
	tests := []struct {
		s, attr, want string
	}{
		{`<iq from='focus@auth.host' id='123' type='set'/>`, "from", "focus@auth.host"},
		{`<iq from="focus@auth.host" id="123" type="set"/>`, "id", "123"},
		{`<iq from='a' type='result'/>`, "type", "result"},
		{`<presence/>`, "from", ""},
	}
	for _, tt := range tests {
		got := extractXMLAttr(tt.s, tt.attr)
		if got != tt.want {
			t.Errorf("extractXMLAttr(%q, %q) = %q, want %q", tt.s, tt.attr, got, tt.want)
		}
	}
}
