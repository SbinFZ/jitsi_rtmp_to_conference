package conference

import "testing"

func TestIsTerminationOnlyFocusIQ(t *testing.T) {
	focus := "room@muc.meet.jitsi/focus"
	cases := map[string]bool{
		`<iq from='room@muc.meet.jitsi/focus' type='set'><jingle action='session-terminate' sid='x'/></iq>`: true,
		`<message from='room@muc.meet.jitsi/bob' type='groupchat'><body>session-terminate</body></message>`: false,
		`<iq from='room@muc.meet.jitsi/bob' type='set'><jingle action='session-terminate' sid='p2p'/></iq>`: false,
		`<iq from='room@muc.meet.jitsi/focus' type='set'><jingle action='source-add'/></iq>`:                false,
	}
	for stanza, want := range cases {
		if got := isTermination(stanza, focus); got != want {
			t.Errorf("%s => %v want %v", stanza, got, want)
		}
	}
}

func TestNormalize(t *testing.T) {
	h, r, e := Normalize("https://m.yuanjinapp.com/", " MyRoom ")
	if e != nil || h != "m.yuanjinapp.com" || r != "myroom" {
		t.Fatal(h, r, e)
	}
	for _, tc := range [][2]string{{"host", "room/foo"}, {"host", "a\"/><presence"}, {"http://host", "room"}, {"https://host/path", "room"}, {"", "room"}} {
		if _, _, e := Normalize(tc[0], tc[1]); e == nil {
			t.Fatal(tc)
		}
	}
}
