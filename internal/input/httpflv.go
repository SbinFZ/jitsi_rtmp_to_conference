// HTTP(S)-FLV is fetched by Go (not FFmpeg) so TLS, proxy selection, stall
// timeouts and HTTP status handling behave the same on every platform.

package input

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// ErrUnauthorized: the source refused the credential (HTTP 401/403).
var ErrUnauthorized = errors.New("source refused the URL credential")

// idleConn fails a read that waits more than 20 s, so a stalled live source
// is detected and reconnected instead of freezing the participant.
type idleConn struct{ net.Conn }

func (b *bodyWithTransport) Read(p []byte) (int, error) { return b.reader.Read(p) }

// ProxyFor resolves the HTTP-FLV proxy for target (see Options.Proxy) and a
// human-readable description for logs. nil = direct connection.
func ProxyFor(target *url.URL, proxy string) (*url.URL, string, error) {
	switch strings.ToLower(strings.TrimSpace(proxy)) {
	case "direct", "none", "off":
		return nil, "direct (--input-proxy direct)", nil
	case "", "auto", "system":
	default:
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return nil, "", fmt.Errorf("invalid --input-proxy (use http://, https://, socks5://, direct)")
		}
		return u, "--input-proxy " + Redact(u.Redacted()), nil
	}
	if u, err := httpproxy.FromEnvironment().ProxyFunc()(target); err == nil && u != nil {
		return u, "environment HTTPS_PROXY/HTTP_PROXY " + Redact(u.Redacted()), nil
	}
	cfg, err := readSystemProxy()
	if err == nil {
		if u := cfg.pick(target); u != nil {
			return u, "system proxy " + u.String(), nil
		}
	}
	return nil, "direct (no proxy configured)", nil
}

func fetchFLV(ctx context.Context, source, proxy string) (io.ReadCloser, error) {
	target, err := url.Parse(source)
	if err != nil {
		return nil, fmt.Errorf("invalid FLV URL")
	}
	proxyURL, how, err := ProxyFor(target, proxy)
	if err != nil {
		return nil, err
	}
	slog.Info("HTTP-FLV connecting", "via", how)
	proxyFunc := http.ProxyURL(proxyURL)
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 20 * time.Second}
	tr := &http.Transport{
		Proxy: proxyFunc,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, e := d.DialContext(ctx, network, addr)
			if e != nil {
				return nil, e
			}
			return idleConn{c}, nil
		},
		ForceAttemptHTTP2: false, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
		DisableCompression: true, DisableKeepAlives: true,
	}
	req, err := http.NewRequestWithContext(ctx, "GET", source, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid FLV request")
	}
	req.Close = true
	req.Header.Set("User-Agent", "jitsi-stream/0.1")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		tr.CloseIdleConnections()
		return nil, fmt.Errorf("FLV fetch failed: %s", Redact(err.Error()))
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		tr.CloseIdleConnections()
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("FLV source returned HTTP %d: %w (expired or already-used one-time signature)", resp.StatusCode, ErrUnauthorized)
		}
		return nil, fmt.Errorf("FLV source returned HTTP %d", resp.StatusCode)
	}
	header := make([]byte, 9)
	if _, err := io.ReadFull(resp.Body, header); err != nil || string(header[:3]) != "FLV" || header[3] != 1 {
		resp.Body.Close()
		tr.CloseIdleConnections()
		return nil, fmt.Errorf("HTTP source is not a valid FLV stream")
	}
	return &bodyWithTransport{ReadCloser: resp.Body, reader: io.MultiReader(strings.NewReader(string(header)), resp.Body), tr: tr}, nil
}

type bodyWithTransport struct {
	io.ReadCloser
	reader io.Reader
	tr     *http.Transport
}

func (b *bodyWithTransport) Close() error {
	err := b.ReadCloser.Close()
	b.tr.CloseIdleConnections()
	return err
}
