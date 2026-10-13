package input

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

const scutilSample = `<dictionary> {
  ExceptionsList : <array> {
    0 : 127.0.0.1
    1 : 192.168.0.0/16
    2 : *.local
    3 : <local>
  }
  HTTPEnable : 1
  HTTPPort : 7897
  HTTPProxy : 127.0.0.1
  HTTPSEnable : 1
  HTTPSPort : 7897
  HTTPSProxy : 127.0.0.1
  SOCKSEnable : 1
  SOCKSPort : 7898
  SOCKSProxy : 127.0.0.1
}`

func TestParseScutilAndPick(t *testing.T) {
	cfg := parseScutil(scutilSample)
	if cfg.HTTPS != "127.0.0.1:7897" || cfg.SOCKS != "127.0.0.1:7898" || len(cfg.Bypass) != 4 {
		t.Fatalf("%+v", cfg)
	}
	must := func(s string) *url.URL { u, _ := url.Parse(s); return u }
	if p := cfg.pick(must("https://stream.example.com:8443/a.flv")); p == nil || p.String() != "http://127.0.0.1:7897" {
		t.Fatalf("https proxy %v", p)
	}
	for _, direct := range []string{"https://192.168.1.10/a.flv", "https://nas.local/a.flv", "https://intranet/a.flv", "http://localhost/a.flv"} {
		if p := cfg.pick(must(direct)); p != nil {
			t.Errorf("%s should bypass proxy, got %v", direct, p)
		}
	}
	if p := parseScutil("<dictionary> {\n  HTTPSEnable : 0\n}").pick(must("https://x/a.flv")); p != nil {
		t.Fatal("disabled proxy used")
	}
}

func TestParseWinProxy(t *testing.T) {
	c := parseWinProxy("127.0.0.1:7890", "localhost;10.*;<local>")
	if c.HTTPS != "127.0.0.1:7890" || !bypass("10.1.2.3", c.Bypass) || !bypass("printer", c.Bypass) || bypass("example.com", c.Bypass) {
		t.Fatalf("%+v", c)
	}
	c = parseWinProxy("http=h:1;https=h:2;socks=h:3", "")
	if c.HTTP != "h:1" || c.HTTPS != "h:2" || c.SOCKS != "h:3" {
		t.Fatalf("%+v", c)
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

func TestUnauthorizedIsTyped(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer s.Close()
	_, _, err := Source(context.Background(), Options{Input: s.URL + "/live.flv?sign=used", Proxy: "direct"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestInputCommandFreshURLEachAttempt(t *testing.T) {
	n := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		_, _ = io.WriteString(w, "FLV\x01\x05\x00\x00\x00\x09")
	}))
	defer s.Close()
	opt := Options{Command: "echo ignored-line && echo " + s.URL + "/live.flv?sign=fresh", Proxy: "direct"}
	if !opt.Live() || !opt.Refreshable() || !opt.Restartable() {
		t.Fatal("command input must be live, refreshable and restartable")
	}
	for i := 0; i < 2; i++ {
		_, body, err := Source(context.Background(), opt)
		if err != nil {
			t.Fatal(err)
		}
		body.Close()
	}
	if n != 2 {
		t.Fatalf("server hit %d times", n)
	}
	if _, _, err := Source(context.Background(), Options{Command: "exit 3"}); err == nil {
		t.Fatal("failing command accepted")
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
