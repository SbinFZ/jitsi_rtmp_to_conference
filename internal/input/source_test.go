package input

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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

// Windows paths such as C:\video.flv must not be parsed as URL scheme "c".
func TestLocalFilePathsAllPlatforms(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "my video.flv")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, body, err := Source(context.Background(), Options{Input: path})
	if err != nil || body != nil || args[len(args)-1] != path {
		t.Fatalf("args=%v err=%v", args, err)
	}
	uri := "file://" + filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		uri = "file:///" + filepath.ToSlash(path)
	}
	if got, ok := IsFile(uri); !ok || got != path {
		t.Fatalf("file URI %s -> %q %v", uri, got, ok)
	}
	if (Options{Input: path}).Live() {
		t.Fatal("file must not be treated as live")
	}
}

func TestRTMPPull(t *testing.T) {
	args, body, err := Source(context.Background(), Options{Input: "rtmp://example.test:1935/live/stream?token=abc"})
	if err != nil || body != nil {
		t.Fatal(err)
	}
	s := strings.Join(args, " ")
	if !strings.Contains(s, "-rtmp_live live") || !strings.Contains(s, "-rw_timeout") || !strings.HasSuffix(s, "-i rtmp://example.test:1935/live/stream?token=abc") {
		t.Fatal(s)
	}
	if !(Options{Input: "rtmp://example.test/live/stream"}).Live() {
		t.Fatal("RTMP pull must be live")
	}
	// NodeMedia signing applies to RTMP playback too.
	args, _, err = Source(context.Background(), Options{Input: "rtmp://example.test/app/live", Key: "test-key"})
	if err != nil || !strings.Contains(args[len(args)-1], "sign=") {
		t.Fatal(args, err)
	}
}

func TestListenUsesIngestStream(t *testing.T) {
	args, body, err := Source(context.Background(), Options{Listen: true, Next: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("FLV")), nil
	}})
	if err != nil || body == nil || strings.Join(args, " ") != "-f flv -i pipe:0" {
		t.Fatalf("args=%v err=%v", args, err)
	}
	if _, _, err := Source(context.Background(), Options{Listen: true}); err == nil {
		t.Fatal("listen without server must fail")
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
