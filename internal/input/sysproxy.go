package input

import (
	"net"
	"net/url"
	"strings"
)

// systemProxyConfig is the OS-level proxy configuration that browsers use but
// Go's HTTP client ignores (it only reads HTTP(S)_PROXY environment variables).
type systemProxyConfig struct {
	HTTP, HTTPS, SOCKS string // host:port, empty when disabled
	Bypass             []string
}

// pick returns the proxy for target, or nil when none applies.
func (c systemProxyConfig) pick(target *url.URL) *url.URL {
	if bypass(target.Hostname(), c.Bypass) {
		return nil
	}
	switch {
	case target.Scheme == "https" && c.HTTPS != "":
		return &url.URL{Scheme: "http", Host: c.HTTPS}
	case target.Scheme == "http" && c.HTTP != "":
		return &url.URL{Scheme: "http", Host: c.HTTP}
	case c.SOCKS != "":
		return &url.URL{Scheme: "socks5", Host: c.SOCKS}
	}
	return nil
}

// bypass implements the common exception syntaxes of macOS/Windows proxy
// settings: exact host, *.suffix / .suffix, CIDR, and <local> (dotless host).
func bypass(host string, rules []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() || host == "localhost" {
		return true
	}
	for _, r := range rules {
		r = strings.ToLower(strings.TrimSpace(r))
		switch {
		case r == "":
		case r == "<local>":
			if ip == nil && !strings.Contains(host, ".") {
				return true
			}
		case strings.Contains(r, "/"):
			if _, n, err := net.ParseCIDR(r); err == nil && ip != nil && n.Contains(ip) {
				return true
			}
		case strings.HasPrefix(r, "*."), strings.HasPrefix(r, "."):
			suffix := strings.TrimPrefix(r, "*")
			if strings.HasSuffix(host, suffix) || host == strings.TrimPrefix(suffix, ".") {
				return true
			}
		case strings.Contains(r, "*"):
			if wildcard(r, host) {
				return true
			}
		case r == host:
			return true
		}
	}
	return false
}

// wildcard matches '*' globs such as 192.168.* (Windows ProxyOverride).
func wildcard(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for i, p := range parts[1:] {
		if i == len(parts)-2 {
			return strings.HasSuffix(s, p)
		}
		k := strings.Index(s, p)
		if k < 0 {
			return false
		}
		s = s[k+len(p):]
	}
	return true
}

func hostPort(host, port string) string {
	host, port = strings.TrimSpace(host), strings.TrimSpace(port)
	if host == "" || port == "" || port == "0" {
		return ""
	}
	return net.JoinHostPort(host, port)
}

// parseScutil parses macOS `scutil --proxy` output.
func parseScutil(out string) systemProxyConfig {
	kv := map[string]string{}
	var cfg systemProxyConfig
	inExceptions := false
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "ExceptionsList") {
			inExceptions = true
			continue
		}
		if inExceptions {
			if line == "}" {
				inExceptions = false
				continue
			}
			if _, v, ok := strings.Cut(line, " : "); ok {
				cfg.Bypass = append(cfg.Bypass, strings.TrimSpace(v))
			}
			continue
		}
		if k, v, ok := strings.Cut(line, " : "); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if kv["HTTPEnable"] == "1" {
		cfg.HTTP = hostPort(kv["HTTPProxy"], kv["HTTPPort"])
	}
	if kv["HTTPSEnable"] == "1" {
		cfg.HTTPS = hostPort(kv["HTTPSProxy"], kv["HTTPSPort"])
	}
	if kv["SOCKSEnable"] == "1" {
		cfg.SOCKS = hostPort(kv["SOCKSProxy"], kv["SOCKSPort"])
	}
	return cfg
}

// parseWinProxy handles "host:port" (all protocols) and
// "http=h:p;https=h:p;socks=h:p".
func parseWinProxy(server, override string) systemProxyConfig {
	var cfg systemProxyConfig
	for _, part := range strings.Split(server, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		proto, addr, ok := strings.Cut(part, "=")
		if !ok {
			cfg.HTTP, cfg.HTTPS = part, part
			continue
		}
		switch strings.ToLower(proto) {
		case "http":
			cfg.HTTP = addr
		case "https":
			cfg.HTTPS = addr
		case "socks":
			cfg.SOCKS = addr
		}
	}
	cfg.Bypass = strings.Split(override, ";")
	return cfg
}
