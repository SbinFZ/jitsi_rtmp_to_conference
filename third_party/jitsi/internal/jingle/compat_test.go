package jingle

import (
	"strings"
	"testing"
)

func TestOfferDirection(t *testing.T) {
	for _, tc := range []struct{ senders, want string }{{"initiator", "sendonly"}, {"responder", "recvonly"}, {"both", "sendrecv"}, {"none", "inactive"}} {
		s := JingleToSDP(&XMLJingle{Contents: []XMLContent{{Name: "audio", Senders: tc.senders, Description: &XMLDescription{Media: "audio"}}}})
		if !strings.Contains(s, "a="+tc.want+"\r\n") {
			t.Fatal(s)
		}
	}
}
func TestSourceName(t *testing.T) {
	var b strings.Builder
	writeSources(&b, []string{"a=ssrc:123 cname:s", "a=ssrc:123 msid:s endpoint-v0"})
	if !strings.Contains(b.String(), `name="endpoint-v0"`) {
		t.Fatal(b.String())
	}
}
