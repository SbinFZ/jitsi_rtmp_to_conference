package xmpp

import (
	"encoding/xml"
	"strings"
	"testing"
	"time"
)

func TestIQRejectionIsErrorAndDoesNotLeakStanza(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	go c.readLoop()
	defer c.Close()
	go func() {
		<-mt.outgoing
		mt.inject(`<iq id="reject" type="error" xmlns="jabber:client"><secret>ICE-SECRET</secret><error><bad-request xmlns="urn:ietf:params:xml:ns:xmpp-stanzas"/></error></iq>`)
	}()
	_, err := c.SendIQWait(`<iq id="reject" type="set" xmlns="jabber:client"/>`, "reject", time.Second)
	if err == nil || strings.Contains(err.Error(), "ICE-SECRET") {
		t.Fatal(err)
	}
}

func TestMediaPresenceEscapesName(t *testing.T) {
	mt := newMockTransport()
	c := newTestConn(mt)
	if err := c.SetMediaPresence(`😀 <name & "quoted">`, true, true); err != nil {
		t.Fatal(err)
	}
	raw := <-mt.outgoing
	var presence struct {
		BotType    string `xml:"botType"`
		Nick       string `xml:"nick"`
		AudioMuted string `xml:"audiomuted"`
		SourceInfo string
	}
	if err := xml.Unmarshal(raw, &presence); err != nil {
		t.Fatal(err)
	}
	if presence.Nick != `😀 <name & "quoted">` || presence.AudioMuted != "false" || presence.BotType != "poltergeist" || !strings.Contains(presence.SourceInfo, `"muted":false`) {
		t.Fatalf("%+v", presence)
	}
}
