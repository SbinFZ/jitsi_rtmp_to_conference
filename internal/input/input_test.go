package input

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
func TestFLVBodyPreserved(t *testing.T) {
	want := "FLV\x01\x05\x00\x00\x00\x09payload"
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("sign") != "one-use" {
			t.Error("signature not preserved")
		}
		_, _ = io.WriteString(w, want)
	}))
	defer s.Close()
	args, body, err := Source(context.Background(), Options{Input: s.URL + "/app/live.flv?sign=one-use", Key: "", Proxy: ""})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	got, err := io.ReadAll(body)
	if err != nil || string(got) != want || strings.Join(args, " ") != "-f flv -i pipe:0" {
		t.Fatalf("body=%q err=%v args=%v", got, err, args)
	}
}
func TestExplicitProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host != "unreachable.invalid" {
			t.Error("not a proxy request")
		}
		_, _ = io.WriteString(w, "FLV\x01\x05\x00\x00\x00\x09")
	}))
	defer proxy.Close()
	_, body, err := Source(context.Background(), Options{Input: "http://unreachable.invalid/live.flv", Key: "", Proxy: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
}

func TestTLSVerificationNotDisabled(t *testing.T) {
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	_, _, err := Source(context.Background(), Options{Input: s.URL + "/live.flv", Key: "", Proxy: ""})
	if err == nil {
		t.Fatal("accepted untrusted TLS certificate")
	}
}

func TestHTTPErrors(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{401, "denied"}, {200, "not FLV"}} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		_, body, err := Source(context.Background(), Options{Input: s.URL + "/live.flv", Key: "", Proxy: ""})
		s.Close()
		if err == nil {
			body.Close()
			t.Fatal("expected error")
		}
	}
}
func TestInputAdapters(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "input-*.flv")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	for _, source := range []string{"test", f.Name(), "rtmp://example.test/app/live", "rtsp://example.test/stream", "https://example.test/live.m3u8"} {
		args, body, err := Source(context.Background(), Options{Input: source, Key: "", Proxy: ""})
		if err != nil || len(args) == 0 || body != nil {
			t.Fatalf("%s %v %v", source, args, err)
		}
	}
	_, _, err = Source(context.Background(), Options{Input: f.Name(), Key: "test-key", Proxy: ""})
	if err != nil {
		t.Fatal("signing key must not break local files", err)
	}
	if _, _, err = Source(context.Background(), Options{Input: "gopher://example.test/live", Key: "", Proxy: ""}); err == nil {
		t.Fatal("unsupported protocol")
	}
}
func TestRedact(t *testing.T) {
	raw := "https://host/live.flv?sign=SECRET&token=TOKEN&other=ok"
	got := Redact(raw)
	if strings.Contains(got, "SECRET") || strings.Contains(got, "TOKEN") || !strings.Contains(got, "other=ok") {
		t.Fatal(got)
	}
}
