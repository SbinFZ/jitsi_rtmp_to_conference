package media

import (
	"context"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestReorderWrapAndDuplicates(t *testing.T) {
	r := NewReorder(4)
	for _, tc := range []struct {
		seq  uint16
		want int
	}{{65534, 1}, {0, 0}, {65535, 2}, {65535, 0}, {1, 1}} {
		out := r.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: tc.seq}})
		if len(out) != tc.want {
			t.Fatalf("seq=%d got %d want %d", tc.seq, len(out), tc.want)
		}
	}
}
func TestReorderSkipsLost(t *testing.T) {
	r := NewReorder(3)
	r.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 1}})
	r.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 3}})
	out := r.Push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 5}})
	if len(out) == 0 || out[0].SequenceNumber != 3 {
		t.Fatal(out)
	}
}
func TestOutputArgs(t *testing.T) {
	cfg := Config{Input: "test", Height: 720, FPS: 25, VideoKbps: 1500}
	s := strings.Join(outputArgs(cfg, 1234, 2345), " ")
	if !strings.Contains(s, "1:a:0") || !strings.Contains(s, "libopus") || !strings.Contains(s, "pkt_size=1200") {
		t.Fatal(s)
	}
	cfg.NoAudio = true
	if strings.Contains(strings.Join(outputArgs(cfg, 1234, 2345), " "), "libopus") {
		t.Fatal("audio still enabled")
	}
}
func TestPipelineCancellation(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("FFmpeg not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	video, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}, "v", "s")
	audio, _ := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "a", "s")
	stats := new(Counters)
	start := time.Now()
	err = Run(ctx, Config{Input: "test", FFmpeg: ff, Height: 360, FPS: 25, VideoKbps: 300}, video, audio, stats)
	if err == nil || time.Since(start) > 8*time.Second || stats.Video.Load() == 0 || stats.Audio.Load() == 0 {
		t.Fatalf("err=%v stats=%d/%d runtime=%v", err, stats.Video.Load(), stats.Audio.Load(), time.Since(start))
	}
}
