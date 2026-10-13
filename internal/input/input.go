// Package input resolves external media protocols into FFmpeg input
// arguments, independently of Jitsi signalling.
package input

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Options describes one external media input.
type Options struct {
	Input string // URL, local file, "-" (stdin) or "test"
	// Command, when set, is run before EVERY connection attempt and its last
	// non-empty stdout line is used as the input URL. For sources that hand
	// out one-time / short-lived URLs (anything other than NodeMedia keys).
	Command string
	Key     string // optional NodeMedia PLAY signing key (HTTP-FLV / RTMP pull)
	// Proxy for HTTP-FLV only: "" = auto (HTTPS_PROXY env, else the OS/browser
	// system proxy on macOS/Windows), "direct" = none, or an explicit URL.
	Proxy string
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
func (o Options) Restartable() bool { return o.Input != "-" || o.Command != "" }

// Refreshable reports whether a reconnect gets a NEW credential (fresh
// NodeMedia signature or a new URL from Command). Without it, a one-time
// signed URL cannot be reused after the first connection.
func (o Options) Refreshable() bool { return o.Key != "" || o.Command != "" }

// Live reports a network source whose normal end means "reconnect", unlike a
// file/stdin/test source whose end means "finished".
func (o Options) Live() bool {
	if o.Listen || o.Command != "" {
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
	if opt.Command != "" {
		var err error
		if source, err = runInputCommand(ctx, opt.Command); err != nil {
			return nil, nil, err
		}
	}
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
