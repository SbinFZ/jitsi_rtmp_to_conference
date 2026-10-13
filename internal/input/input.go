// Package input resolves external protocols independently of Jitsi signalling.
package input

import (
	"context"
	"crypto/md5" // NodeMedia protocol requires MD5, not used for TLS/security hashes.
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// SignNodeMedia generates a fresh PLAY signature. FLV extension is excluded.
func SignNodeMedia(rawURL, key string, expiry time.Time) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.Path == "" {
		return "", fmt.Errorf("invalid NodeMedia URL")
	}
	path := strings.TrimSuffix(u.Path, ".flv")
	exp := fmt.Sprint(expiry.Unix())
	sum := md5.Sum([]byte(path + "-" + exp + "-" + key))
	q := u.Query()
	q.Set("sign", exp+"-"+hex.EncodeToString(sum[:]))
	u.RawQuery = q.Encode()
	return u.String(), nil
}

var secretQuery = regexp.MustCompile(`(?i)([?&](?:sign|sig|token|key|password|pwd|auth|jwt|secret)=)[^&\s"']+`)
var userInfo = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+:[^/@\s]+@`)

func Redact(s string) string {
	return userInfo.ReplaceAllString(secretQuery.ReplaceAllString(s, `${1}[REDACTED]`), `${1}[REDACTED]@`)
}

// Options describes one external media input.
type Options struct {
	Input string // URL, local file, "-" (stdin) or "test"
	Key   string // optional NodeMedia PLAY signing key (HTTP-FLV / RTMP pull)
	Proxy string // optional HTTP-FLV-only proxy
	// Listen: the input URL is a local RTMP ingest (see internal/rtmpserver);
	// Next blocks until an authorized encoder publishes, returning its FLV.
	Listen bool
	Next   func(context.Context) (io.ReadCloser, error)
}

// IsFile reports whether input names an existing local file (any OS path syntax).
func IsFile(source string) (string, bool) {
	if strings.HasPrefix(strings.ToLower(source), "file://") {
		u, err := url.Parse(source)
		if err != nil {
			return "", false
		}
		source = u.Path
		// file:///C:/video.flv on Windows
		if len(source) > 2 && source[0] == '/' && source[2] == ':' {
			source = source[1:]
		}
		source = filepath.FromSlash(source)
	}
	fi, err := os.Stat(source)
	return source, err == nil && fi.Mode().IsRegular()
}

// Restartable reports whether the input can be reopened after it ends/fails.
func (o Options) Restartable() bool { return o.Input != "-" }

// Live reports a network source whose normal end means "reconnect", unlike a
// file/stdin/test source whose end means "finished".
func (o Options) Live() bool {
	if o.Listen {
		return true
	}
	if o.Input == "-" || o.Input == "test" {
		return false
	}
	_, file := IsFile(o.Input)
	return !file
}

// Source returns FFmpeg input arguments and an optional streaming stdin.
// HTTP FLV is fetched by Go to avoid platform-dependent FFmpeg TLS stacks.
func Source(ctx context.Context, opt Options) ([]string, io.ReadCloser, error) {
	source, key, proxy := opt.Input, opt.Key, opt.Proxy
	if opt.Listen {
		if opt.Next == nil {
			return nil, nil, fmt.Errorf("RTMP ingest server is not running")
		}
		body, err := opt.Next(ctx)
		if err != nil {
			return nil, nil, err
		}
		return []string{"-f", "flv", "-i", "pipe:0"}, body, nil
	}
	if source == "test" {
		return []string{"-re", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25", "-re", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000"}, nil, nil
	}
	if source == "-" {
		return []string{"-i", "pipe:0"}, os.Stdin, nil
	}
	// Check local files before URL parsing: "C:\\x.flv" parses as scheme "c".
	if path, ok := IsFile(source); ok {
		return []string{"-re", "-i", path}, nil, nil
	}
	u, err := url.Parse(source)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid input URL")
	}
	isHTTPFLV := (u.Scheme == "http" || u.Scheme == "https") && strings.HasSuffix(strings.ToLower(u.Path), ".flv")
	if key != "" && (isHTTPFLV || u.Scheme == "rtmp" || u.Scheme == "rtmps") {
		source, err = SignNodeMedia(source, key, time.Now().Add(time.Hour))
		if err != nil {
			return nil, nil, err
		}
	}
	if isHTTPFLV {
		body, err := fetchFLV(ctx, source, proxy)
		if err != nil {
			return nil, nil, err
		}
		return []string{"-f", "flv", "-i", "pipe:0"}, body, nil
	}
	switch u.Scheme {
	case "http", "https", "rtmp", "rtmps", "rtmpt", "rtmpts", "rtsp", "rtsps", "srt", "udp":
		if u.Host == "" {
			return nil, nil, fmt.Errorf("input URL needs a host")
		}
		// rw_timeout (microseconds) makes a stalled live source fail instead of
		// freezing the participant forever; the restart loop then reconnects.
		args := []string{"-rw_timeout", "15000000"}
		if strings.HasPrefix(u.Scheme, "rtsp") {
			args = append(args, "-rtsp_transport", "tcp")
		}
		if strings.HasPrefix(u.Scheme, "rtmp") {
			// Live RTMP: play the live stream (not a recorded/VOD lookup).
			args = append(args, "-rtmp_live", "live")
		}
		return append(args, "-i", source), nil, nil
	case "", "file":
		return nil, nil, fmt.Errorf("input file unavailable")
	default:
		return nil, nil, fmt.Errorf("unsupported input protocol %q", u.Scheme)
	}
}

type idleConn struct{ net.Conn }

func (c idleConn) Read(p []byte) (int, error) {
	_ = c.SetReadDeadline(time.Now().Add(20 * time.Second))
	return c.Conn.Read(p)
}

func fetchFLV(ctx context.Context, source, proxy string) (io.ReadCloser, error) {
	proxyFunc := http.ProxyFromEnvironment
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" && u.Scheme != "socks5h") {
			return nil, fmt.Errorf("invalid input proxy URL")
		}
		proxyFunc = http.ProxyURL(u)
	}
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
		return nil, fmt.Errorf("FLV source returned HTTP %d (401 may mean expired/single-use signature)", resp.StatusCode)
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

func (b *bodyWithTransport) Read(p []byte) (int, error) { return b.reader.Read(p) }

func (b *bodyWithTransport) Close() error {
	err := b.ReadCloser.Close()
	b.tr.CloseIdleConnections()
	return err
}
