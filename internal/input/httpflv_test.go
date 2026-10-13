package input

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

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

func TestUnauthorizedIsTyped(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer s.Close()
	_, _, err := Source(context.Background(), Options{Input: s.URL + "/live.flv?sign=used", Proxy: "direct"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestProxyForPrecedence(t *testing.T) {
	target, _ := url.Parse("https://stream.example.com/live.flv")
	if u, _, err := ProxyFor(target, "direct"); err != nil || u != nil {
		t.Fatal("direct", u, err)
	}
	if u, _, err := ProxyFor(target, "socks5://127.0.0.1:1080"); err != nil || u.String() != "socks5://127.0.0.1:1080" {
		t.Fatal("explicit", u, err)
	}
	if _, _, err := ProxyFor(target, "ftp://x"); err == nil {
		t.Fatal("invalid proxy accepted")
	}
	t.Setenv("HTTPS_PROXY", "http://10.0.0.1:3128")
	if u, _, err := ProxyFor(target, ""); err != nil || u == nil || u.Host != "10.0.0.1:3128" {
		t.Fatal("env proxy must win over system proxy", u, err)
	}
}
