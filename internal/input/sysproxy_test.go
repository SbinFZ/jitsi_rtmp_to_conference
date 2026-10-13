package input

import (
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
