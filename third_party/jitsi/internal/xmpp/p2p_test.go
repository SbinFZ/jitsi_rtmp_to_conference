package xmpp

import (
	"strings"
	"testing"
	"time"
)

func TestPeerJingleCannotOverwriteFocusOffer(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()
	peer := `<iq from="testroom@conference.test.example.com/peer" id="p2p1" type="set" xmlns="jabber:client"><jingle xmlns="urn:xmpp:jingle:1" action="session-initiate" sid="peer-sid"/></iq>`
	mt.inject(peer)
	select {
	case reply := <-mt.outgoing:
		if !strings.Contains(string(reply), `type="error"`) {
			t.Fatal(string(reply))
		}
	case <-time.After(time.Second):
		t.Fatal("no peer rejection")
	}
	if c.LastJingleStanza() != "" {
		t.Fatal("peer replaced focus offer")
	}
	mt.inject(`<iq from="testroom@conference.test.example.com/focus" id="focus1" type="set" xmlns="jabber:client"><jingle xmlns="urn:xmpp:jingle:1" action="session-initiate" sid="focus-sid"/></iq>`)
	select {
	case raw := <-c.jingles:
		if !strings.Contains(raw, "focus-sid") {
			t.Fatal(raw)
		}
	case <-time.After(time.Second):
		t.Fatal("focus blocked")
	}
}
