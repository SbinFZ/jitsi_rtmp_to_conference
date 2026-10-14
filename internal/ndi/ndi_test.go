package ndi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestSourceNameAndSanitize(t *testing.T) {
	id := ParticipantIdentity{
		Endpoint:    "422ab3db",
		DisplayName: "Alice Smith",
		VideoType:   "camera",
	}
	got := SourceName("Jitsi", "team-sync", id)
	want := "Jitsi (team-sync) - Alice Smith [422ab3db]"
	if got != want {
		t.Fatalf("SourceName = %q, want %q", got, want)
	}

	idScreen := ParticipantIdentity{
		Endpoint:    "422ab3db",
		DisplayName: "Alice Smith",
		VideoType:   "desktop",
	}
	gotScreen := SourceName("", "team-sync", idScreen)
	wantScreen := "Jitsi (team-sync) - Alice Smith [422ab3db] (Screen)"
	if gotScreen != wantScreen {
		t.Fatalf("SourceName screen = %q, want %q", gotScreen, wantScreen)
	}

	if tok := SanitizeToken("Alice Smith / 2026!"); tok != "Alice_Smith_2026" {
		t.Fatalf("SanitizeToken = %q, want Alice_Smith_2026", tok)
	}
}

func TestBuildSDPAndFFmpegArgs(t *testing.T) {
	sdp := BuildSDP("Jitsi (room) - Bob [1234]", 5004, 5006)
	if !strings.Contains(sdp, "m=audio 5004 RTP/AVP 111") || !strings.Contains(sdp, "a=rtpmap:111 opus/48000/2") {
		t.Fatalf("missing audio SDP lines:\n%s", sdp)
	}
	if !strings.Contains(sdp, "m=video 5006 RTP/AVP 96") || !strings.Contains(sdp, "a=rtpmap:96 VP8/90000") {
		t.Fatalf("missing video SDP lines:\n%s", sdp)
	}

	args := BuildFFmpegArgs("/tmp/test.sdp", "libndi_newtek", "Jitsi (room) - Bob [1234]")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-f libndi_newtek Jitsi (room) - Bob [1234]") || !strings.Contains(joined, "-pix_fmt uyvy422") {
		t.Fatalf("unexpected NDI ffmpeg args: %v", args)
	}
}

func TestBroadcasterWithSinkCommand(t *testing.T) {
	if os.Getenv("GO_WANT_NDI_SINK_HELPER") == "1" {
		marker := os.Getenv("TEST_NDI_MARKER")
		if marker != "" {
			payload := fmt.Sprintf("%s|%s", os.Getenv("JITSI_NDI_NAME"), os.Getenv("JITSI_ENDPOINT"))
			_ = os.WriteFile(marker, []byte(payload), 0644)
		}
		return
	}

	tmp := t.TempDir()
	marker := filepath.Join(tmp, "env.txt")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable failed: %v", err)
	}
	t.Setenv("GO_WANT_NDI_SINK_HELPER", "1")
	t.Setenv("TEST_NDI_MARKER", marker)

	sinkCmd := fmt.Sprintf("%q -test.run=^TestBroadcasterWithSinkCommand$", exe)
	if runtime.GOOS == "windows" {
		sinkCmd = fmt.Sprintf("\"%s\" -test.run=^TestBroadcasterWithSinkCommand$", exe)
	}

	b := NewBroadcaster(Config{
		Enabled: true,
		Room:    "testroom",
		Prefix:  "Jitsi",
		OutDir:  tmp,
		SinkCmd: sinkCmd,
	})
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    111,
			SequenceNumber: 1,
			Timestamp:      480,
			SSRC:           998877,
		},
		Payload: []byte{0x01, 0x02, 0x03, 0x04},
	}

	id := ParticipantIdentity{
		Endpoint:    "ep01",
		DisplayName: "Charlie",
		VideoType:   "camera",
	}

	if err := b.WriteRTP(ctx, id, 998877, false, pkt); err != nil {
		t.Fatalf("WriteRTP failed: %v", err)
	}

	sources := b.ActiveSources()
	if len(sources) != 1 || sources[0] != "Jitsi (testroom) - Charlie [ep01]" {
		t.Fatalf("unexpected active sources: %v", sources)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(marker)
		if err == nil && len(data) > 0 {
			if string(data) != "Jitsi (testroom) - Charlie [ep01]|ep01" {
				t.Fatalf("unexpected sink env marker: %q", string(data))
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for sink command to write marker file")
}
