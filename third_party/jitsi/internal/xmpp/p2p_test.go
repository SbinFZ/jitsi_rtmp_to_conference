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

func TestRoomOccupants(t *testing.T) {
	for _, tc := range []struct {
		reply string
		n     int
		err   error
	}{
		{`<iq id="ID" type="error" xmlns="jabber:client"><error type="cancel"><item-not-found xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/></error></iq>`, 0, ErrRoomNotFound},
		{`<iq id="ID" type="error" xmlns="jabber:client"><error type="auth"><forbidden xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/></error></iq>`, -1, nil},
		{`<iq id="ID" type="result" xmlns="jabber:client"><query><x><field var='muc#roominfo_occupants' label='n'><value>3</value></field></x></query></iq>`, 3, nil},
	} {
		mt := newMockTransport()
		c := newTestConn(mt)
		go c.readLoop()
		go func() {
			sent := string(<-mt.outgoing)
			id := sent[strings.Index(sent, `id="`)+4:]
			id = id[:strings.Index(id, `"`)]
			mt.inject(strings.Replace(tc.reply, `"ID"`, `"`+id+`"`, 1))
		}()
		n, err := c.RoomOccupants("testroom", time.Second)
		if n != tc.n || err != tc.err {
			t.Errorf("reply %.40s: n=%d err=%v want %d %v", tc.reply, n, err, tc.n, tc.err)
		}
		c.Close()
	}
}
