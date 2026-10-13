package input

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSignNodeMedia(t *testing.T) {
	got, err := SignNodeMedia("https://example.test/app/live.flv?other=1&sign=old", "test-key", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(got)
	if u.Query().Get("other") != "1" || u.Query().Get("sign") != "1700000000-466f7092c7c65e18418bc3b85d1e5f30" {
		t.Fatalf("signature = %s", got)
	}
}

func TestSignatureDetection(t *testing.T) {
	if !HasSignature("https://h/app/s.flv?sign=1-abc") || HasSignature("https://h/app/s.flv") {
		t.Fatal("signature detection")
	}
	if (Options{Input: "https://h/s.flv?sign=x"}).Refreshable() || !(Options{Key: "k"}).Refreshable() {
		t.Fatal("refreshable")
	}
}

func TestRedact(t *testing.T) {
	raw := "https://host/live.flv?sign=SECRET&token=TOKEN&other=ok"
	got := Redact(raw)
	if strings.Contains(got, "SECRET") || strings.Contains(got, "TOKEN") || !strings.Contains(got, "other=ok") {
		t.Fatal(got)
	}
}
