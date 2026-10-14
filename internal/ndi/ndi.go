// Package ndi implements zero-GStreamer, zero-CGO per-participant stream
// broadcasting and FFmpeg-driven output sinks for Jitsi conferences.
//
// Each remote participant's RTP audio (Opus) and video (VP8) tracks are
// forwarded over loopback UDP ports to a lightweight per-participant FFmpeg
// subprocess (using dynamically generated SDP), which can output to:
//   - NDI (`-f libndi_newtek <NDI_SOURCE_NAME>`)
//   - Custom sink commands (`--ndi-cmd`, receiving environment variables
//     JITSI_NDI_NAME, JITSI_ENDPOINT, JITSI_DISPLAY_NAME, JITSI_SDP_PATH, etc.)
//   - Live MPEG-TS / NUT / Matroska stream files per participant.
package ndi

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/pion/rtp"
)

// Mode defines how per-participant streams are broadcast.
type Mode string

const (
	ModeDisabled Mode = ""
	ModeNDI      Mode = "libndi_newtek" // FFmpeg -f libndi_newtek <SourceName>
	ModeMPEGTS   Mode = "mpegts"        // Live per-participant .ts stream file / UDP output
	ModeCommand  Mode = "command"       // Custom command template (--ndi-cmd)
)

// Config configures the NDI / per-participant stream broadcaster.
type Config struct {
	Enabled   bool
	Room      string
	Prefix    string // Default: "Jitsi"
	FFmpegBin string // Default: "ffmpeg"
	OutDir    string // Directory for SDP files and optional muxed outputs
	SinkCmd   string // Optional custom command (runs via sh -c / cmd /C with JITSI_* env vars)
	Format    string // Optional FFmpeg output format override (default: "libndi_newtek")
}

// ParticipantIdentity represents a resolved Jitsi participant for a media stream.
type ParticipantIdentity struct {
	Endpoint    string
	DisplayName string
	VideoType   string
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// SanitizeToken converts an arbitrary string into a safe filename token.
func SanitizeToken(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	s = unsafeChars.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		return "user"
	}
	return s
}

// SourceName formats a human-readable NDI source name for a participant.
// Example: "Jitsi (team-sync) - Alice [422ab3db]"
func SourceName(prefix, room string, id ParticipantIdentity) string {
	if prefix == "" {
		prefix = "Jitsi"
	}
	ep := strings.TrimSpace(id.Endpoint)
	if ep == "" {
		ep = "unknown"
	}
	name := strings.TrimSpace(id.DisplayName)
	suffix := ""
	if id.VideoType == "desktop" {
		suffix = " (Screen)"
	}
	if name != "" && name != ep {
		return fmt.Sprintf("%s (%s) - %s [%s]%s", prefix, room, name, ep, suffix)
	}
	return fmt.Sprintf("%s (%s) - %s%s", prefix, room, ep, suffix)
}

// BuildSDP generates a valid SDP description for FFmpeg to receive local RTP
// Opus (PT 111) and/or VP8 (PT 96) over loopback UDP.
func BuildSDP(sourceName string, audioPort, videoPort int) string {
	var b strings.Builder
	b.WriteString("v=0\r\n")
	b.WriteString("o=- 0 0 IN IP4 127.0.0.1\r\n")
	b.WriteString("s=" + strings.ReplaceAll(sourceName, "\r", "") + "\r\n")
	b.WriteString("c=IN IP4 127.0.0.1\r\n")
	b.WriteString("t=0 0\r\n")
	if audioPort > 0 {
		fmt.Fprintf(&b, "m=audio %d RTP/AVP 111\r\n", audioPort)
		b.WriteString("a=rtpmap:111 opus/48000/2\r\n")
	}
	if videoPort > 0 {
		fmt.Fprintf(&b, "m=video %d RTP/AVP 96\r\n", videoPort)
		b.WriteString("a=rtpmap:96 VP8/90000\r\n")
	}
	return b.String()
}

// BuildFFmpegArgs returns the FFmpeg CLI arguments for broadcasting a participant SDP stream.
func BuildFFmpegArgs(sdpPath, format, target string) []string {
	if format == "" {
		format = string(ModeNDI)
	}
	args := []string{
		"-y",
		"-hide_banner", "-loglevel", "warning",
		"-protocol_whitelist", "file,udp,rtp",
		"-i", sdpPath,
	}
	switch format {
	case "libndi_newtek", "ndi":
		args = append(args,
			"-c:v", "rawvideo", "-pix_fmt", "uyvy422",
			"-c:a", "pcm_s16le", "-ar", "48000", "-ac", "2",
			"-f", "libndi_newtek", target,
		)
	case "mpegts":
		args = append(args,
			"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
			"-c:a", "aac", "-ar", "48000", "-ac", "2",
			"-flush_packets", "1",
			"-f", "mpegts", target,
		)
	case "webm":
		args = append(args,
			"-c:v", "copy",
			"-c:a", "copy",
			"-live", "1",
			"-flush_packets", "1",
			"-f", "webm", target,
		)
	default:
		args = append(args, "-f", format, target)
	}
	return args
}

// Broadcaster manages per-participant RTP forwarders and NDI/FFmpeg output processes.
type Broadcaster struct {
	cfg     Config
	mu      sync.Mutex
	streams map[string]*participantStream
	closed  bool
}

type participantStream struct {
	key        string
	id         ParticipantIdentity
	sourceName string
	sdpPath    string

	audioConn *net.UDPConn
	videoConn *net.UDPConn
	audioPort int
	videoPort int

	hasAudio bool
	hasVideo bool

	stdin  io.WriteCloser
	cancel context.CancelFunc
	done   chan struct{}
}

// NewBroadcaster creates a new per-participant NDI broadcaster.
func NewBroadcaster(cfg Config) *Broadcaster {
	if cfg.Prefix == "" {
		cfg.Prefix = "Jitsi"
	}
	if cfg.FFmpegBin == "" {
		cfg.FFmpegBin = "ffmpeg"
	}
	if cfg.Format == "" {
		cfg.Format = string(ModeNDI)
	}
	return &Broadcaster{
		cfg:     cfg,
		streams: make(map[string]*participantStream),
	}
}

func streamKey(id ParticipantIdentity, fallbackSSRC uint32) string {
	ep := strings.TrimSpace(id.Endpoint)
	if ep == "" {
		return fmt.Sprintf("ssrc-%d", fallbackSSRC)
	}
	if id.VideoType == "desktop" {
		return ep + "-desktop"
	}
	return ep
}

// WriteRTP forwards an RTP packet (audio or video) to the participant's NDI/broadcast pipeline.
// Payload types are normalized to 111 (Opus) and 96 (VP8) to match the generated SDP.
func (b *Broadcaster) WriteRTP(ctx context.Context, id ParticipantIdentity, fallbackSSRC uint32, isVideo bool, pkt *rtp.Packet) error {
	if !b.cfg.Enabled || pkt == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	key := streamKey(id, fallbackSSRC)
	ps, ok := b.streams[key]
	if !ok {
		var err error
		ps, err = b.createStreamLocked(key, id)
		if err != nil {
			b.mu.Unlock()
			return err
		}
		b.streams[key] = ps
	}
	needRestart := false
	if id.DisplayName != "" && ps.id.DisplayName != id.DisplayName {
		ps.id.DisplayName = id.DisplayName
		ps.sourceName = SourceName(b.cfg.Prefix, b.cfg.Room, ps.id)
	}
	if isVideo && !ps.hasVideo {
		ps.hasVideo = true
		needRestart = true
	} else if !isVideo && !ps.hasAudio {
		ps.hasAudio = true
		needRestart = true
	}
	if needRestart {
		b.restartProcessLocked(ctx, ps)
	}
	var conn *net.UDPConn
	var dstPort int
	var pt uint8
	if isVideo {
		conn = ps.videoConn
		dstPort = ps.videoPort
		pt = 96
	} else {
		conn = ps.audioConn
		dstPort = ps.audioPort
		pt = 111
	}
	b.mu.Unlock()

	if conn == nil || dstPort <= 0 {
		return nil
	}
	clone := *pkt
	clone.Header.PayloadType = pt
	raw, err := clone.Marshal()
	if err != nil {
		return err
	}
	_, err = conn.WriteToUDP(raw, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: dstPort})
	return err
}

func allocRTPPortPair(excludePort int) (int, error) {
	for i := 0; i < 64; i++ {
		c1, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
		if err != nil {
			return 0, err
		}
		port := c1.LocalAddr().(*net.UDPAddr).Port
		if port%2 != 0 {
			_ = c1.Close()
			port++
			c1, err = net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
			if err != nil {
				continue
			}
		}
		if excludePort > 0 && (port == excludePort || port == excludePort+1 || port+1 == excludePort) {
			_ = c1.Close()
			continue
		}
		c2, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port + 1})
		if err != nil {
			_ = c1.Close()
			continue
		}
		_ = c2.Close()
		_ = c1.Close()
		return port, nil
	}
	return 0, fmt.Errorf("unable to allocate even RTP/RTCP loopback port pair")
}

func (b *Broadcaster) createStreamLocked(key string, id ParticipantIdentity) (*participantStream, error) {
	aConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return nil, err
	}
	vConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		_ = aConn.Close()
		return nil, err
	}
	outDir := b.cfg.OutDir
	if outDir == "" {
		outDir = os.TempDir()
	}
	sdpPath := filepath.Join(outDir, fmt.Sprintf("ndi-%s.sdp", SanitizeToken(key)))
	return &participantStream{
		key:        key,
		id:         id,
		sourceName: SourceName(b.cfg.Prefix, b.cfg.Room, id),
		sdpPath:    sdpPath,
		audioConn:  aConn,
		videoConn:  vConn,
	}, nil
}

func stopProcess(ps *participantStream) {
	if ps.stdin != nil {
		_, _ = ps.stdin.Write([]byte("q\n"))
		_ = ps.stdin.Close()
		ps.stdin = nil
	}
	if ps.done != nil {
		select {
		case <-ps.done:
		case <-time.After(150 * time.Millisecond):
			if ps.cancel != nil {
				ps.cancel()
			}
			<-ps.done
		}
		ps.done = nil
	}
	if ps.cancel != nil {
		ps.cancel()
		ps.cancel = nil
	}
}

func (b *Broadcaster) restartProcessLocked(ctx context.Context, ps *participantStream) {
	stopProcess(ps)
	aPort := 0
	if ps.hasAudio {
		if p, err := allocRTPPortPair(0); err == nil {
			ps.audioPort = p
			aPort = p
		}
	}
	vPort := 0
	if ps.hasVideo {
		if p, err := allocRTPPortPair(aPort); err == nil {
			ps.videoPort = p
			vPort = p
		}
	}
	if aPort == 0 && vPort == 0 {
		return
	}
	sdp := BuildSDP(ps.sourceName, aPort, vPort)
	if err := os.WriteFile(ps.sdpPath, []byte(sdp), 0644); err != nil {
		slog.Warn("failed to write participant SDP", "path", ps.sdpPath, "error", err)
		return
	}

	pCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	ps.cancel = cancel
	ps.done = done

	var cmd *exec.Cmd
	var stdin io.WriteCloser
	if b.cfg.SinkCmd != "" {
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(pCtx, "cmd", "/C", b.cfg.SinkCmd)
		} else {
			cmd = exec.CommandContext(pCtx, "sh", "-c", b.cfg.SinkCmd)
		}
		if b.cfg.OutDir != "" {
			cmd.Dir = b.cfg.OutDir
		}
		cmd.Env = append(os.Environ(),
			"JITSI_NDI_NAME="+ps.sourceName,
			"JITSI_ENDPOINT="+ps.id.Endpoint,
			"JITSI_DISPLAY_NAME="+ps.id.DisplayName,
			"JITSI_SDP_PATH="+ps.sdpPath,
			fmt.Sprintf("JITSI_AUDIO_PORT=%d", aPort),
			fmt.Sprintf("JITSI_VIDEO_PORT=%d", vPort),
		)
	} else {
		target := ps.sourceName
		if b.cfg.Format == "mpegts" {
			target = filepath.Join(b.cfg.OutDir, fmt.Sprintf("participant-%s.ts", SanitizeToken(ps.key)))
		} else if b.cfg.Format == "webm" {
			target = filepath.Join(b.cfg.OutDir, fmt.Sprintf("participant-%s.webm", SanitizeToken(ps.key)))
		}
		args := BuildFFmpegArgs(ps.sdpPath, b.cfg.Format, target)
		cmd = exec.CommandContext(pCtx, b.cfg.FFmpegBin, args...)
		stdin, _ = cmd.StdinPipe()
	}
	ps.stdin = stdin

	slog.Info("starting participant NDI/broadcast stream",
		"source_name", ps.sourceName,
		"endpoint", ps.id.Endpoint,
		"format", b.cfg.Format,
		"audio_port", aPort,
		"video_port", vPort,
	)

	go func() {
		defer close(done)
		if err := cmd.Run(); err != nil && pCtx.Err() == nil {
			slog.Warn("participant broadcast process exited", "source_name", ps.sourceName, "error", err)
		}
	}()
}

// ActiveSources returns the list of currently active NDI source names.
func (b *Broadcaster) ActiveSources() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.streams))
	for _, ps := range b.streams {
		out = append(out, ps.sourceName)
	}
	return out
}

// Close stops all participant broadcast processes and closes UDP sockets.
func (b *Broadcaster) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	streams := make([]*participantStream, 0, len(b.streams))
	for _, ps := range b.streams {
		streams = append(streams, ps)
	}
	b.mu.Unlock()

	for _, ps := range streams {
		stopProcess(ps)
		if ps.audioConn != nil {
			_ = ps.audioConn.Close()
		}
		if ps.videoConn != nil {
			_ = ps.videoConn.Close()
		}
		if ps.sdpPath != "" {
			_ = os.Remove(ps.sdpPath)
		}
	}
	return nil
}
