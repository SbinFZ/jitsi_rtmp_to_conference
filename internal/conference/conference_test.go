package conference

import "testing"

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
