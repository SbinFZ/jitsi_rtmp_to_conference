package rtmpserver

import (
	"context"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	ep, err := Parse("rtmp://0.0.0.0/live/s3cret")
	if err != nil || ep.Addr != "0.0.0.0:1935" || ep.App != "live" || ep.Key != "s3cret" {
		t.Fatalf("%+v %v", ep, err)
	}
	if ep, err = Parse("rtmp://[::]:1936/a/b/key"); err != nil || ep.App != "a/b" || ep.Key != "key" || ep.Addr != "[::]:1936" {
		t.Fatalf("%+v %v", ep, err)
	}
	for _, bad := range []string{"rtmp://0.0.0.0/live", "rtmps://0.0.0.0/live/k", "rtmp://u:p@0.0.0.0/live/k",
		"rtmp://0.0.0.0/live/k?x=1", "https://0.0.0.0/live/k", "rtmp:///live/k", "rtmp://0.0.0.0/live/"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

// Real encoder (FFmpeg RTMP client) against the server: wrong key and wrong
// app are refused before media; the right key is delivered as FLV; a second
// concurrent publisher is refused.
func TestAuthAndDelivery(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg not installed")
	}
	srv, err := Listen(Endpoint{Addr: "127.0.0.1:0", App: "live", Key: "good-key"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	base := "rtmp://" + srv.Addr().String()
	push := func(path string, seconds string) *exec.Cmd {
		return exec.Command(ff, "-hide_banner", "-v", "error", "-re", "-f", "lavfi", "-i", "testsrc2=size=320x240:rate=25",
			"-f", "lavfi", "-i", "sine", "-t", seconds, "-c:v", "libx264", "-preset", "ultrafast", "-g", "25", "-c:a", "aac",
			"-f", "flv", base+path)
	}
	for _, path := range []string{"/live/WRONG", "/other/good-key"} {
		if err := push(path, "1").Run(); err == nil {
			t.Errorf("%s was accepted", path)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	if _, err := srv.Next(ctx); err == nil {
		t.Fatal("rejected publisher produced a stream")
	}
	cancel()

	good := push("/live/good-key", "4")
	started := time.Now()
	if err := good.Start(); err != nil {
		t.Fatal(err)
	}
	// Consumer attaches late (meeting not ready yet): media in the meantime
	// must be dropped, not buffered, and the encoder must not stall.
	time.Sleep(1500 * time.Millisecond)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, err := srv.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := push("/live/good-key", "1").Run(); err == nil {
		t.Error("second concurrent publisher was accepted")
	}
	out := filepath.Join(t.TempDir(), "ingest.flv")
	f, _ := os.Create(out)
	n, _ := io.Copy(f, st)
	f.Close()
	_ = good.Wait()
	if took := time.Since(started); took > 7*time.Second {
		t.Fatalf("encoder stalled by backpressure: %v for a 4 s stream", took)
	}
	if n < 1000 {
		t.Fatalf("only %d bytes ingested", n)
	}
	ffprobe := filepath.Join(filepath.Dir(ff), "ffprobe")
	probe, err := exec.Command(ffprobe, "-v", "error", "-show_entries", "stream=codec_name", "-of", "csv=p=0", out).Output()
	if err != nil || !strings.Contains(string(probe), "h264") || !strings.Contains(string(probe), "aac") {
		t.Fatalf("ingested FLV missing streams: %v %s", err, probe)
	}
	first, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v", "-show_entries", "frame=pts_time,key_frame", "-read_intervals", "%+#1", "-of", "csv=p=0", out).Output()
	if err != nil || !strings.HasPrefix(strings.TrimSpace(string(first)), "1,0.0") {
		t.Fatalf("stream must start at a keyframe with rebased timestamp 0, got %q %v", first, err)
	}
	if err := exec.Command(ff, "-v", "error", "-xerror", "-i", out, "-f", "null", "-").Run(); err != nil {
		t.Fatalf("ingested FLV does not decode cleanly: %v", err)
	}
}

// An anonymous peer that never completes the RTMP handshake is disconnected.
func TestHandshakeTimeout(t *testing.T) {
	old := handshakeTimeout
	handshakeTimeout = 300 * time.Millisecond
	defer func() { handshakeTimeout = old }()
	srv, err := Listen(Endpoint{Addr: "127.0.0.1:0", App: "live", Key: "k"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	start := time.Now()
	_, err = conn.Read(make([]byte, 1))
	if err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("idle unauthenticated connection not closed: err=%v after %v", err, time.Since(start))
	}
}
