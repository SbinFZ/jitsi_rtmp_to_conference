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
	"bytes"
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
	started  bool

	startTime      time.Time
	audioSeq       uint16
	videoSeq       uint16
	audioBaseInit  bool
	audioBaseRTPTS uint32
	audioStartOff  uint32
	videoBaseInit  bool
	videoBaseRTPTS uint32
	videoStartOff  uint32

	curKeyframeTS   uint32
	curKeyframePkts [][]byte
	lastKeyframe    [][]byte

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

// RegisterMedia pre-declares whether a participant endpoint has audio and/or video
// tracks announced in signalling, so the first RTP packet starts FFmpeg with both
// SDP m= sections without requiring a process restart.
func (b *Broadcaster) RegisterMedia(id ParticipantIdentity, hasAudio, hasVideo bool) {
	if !b.cfg.Enabled {
		return
	}
	ep := strings.TrimSpace(id.Endpoint)
	if ep == "" || ep == "jvb" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	key := streamKey(id, 0)
	ps, ok := b.streams[key]
	if !ok {
		var err error
		ps, err = b.createStreamLocked(key, id)
		if err != nil {
			return
		}
		b.streams[key] = ps
	}
	if id.DisplayName != "" {
		ps.id.DisplayName = id.DisplayName
		ps.sourceName = SourceName(b.cfg.Prefix, b.cfg.Room, ps.id)
	}
	if hasAudio {
		ps.hasAudio = true
	}
	if hasVideo {
		ps.hasVideo = true
	}
}

// stripVP8DescriptorToRFC7741Minimal rewrites a VP8 RTP payload descriptor to a minimal
// 1-byte RFC 7741 descriptor (preserving the S bit and PartID), removing extended X/I/L/T/K
// headers that some FFmpeg RTP VP8 depacketizer builds fail to parse when PictureID or TL0PICIDX is present.
func stripVP8DescriptorToRFC7741Minimal(payload []byte) []byte {
	if len(payload) < 2 {
		return payload
	}
	b0 := payload[0]
	x := (b0 & 0x80) != 0
	if !x {
		return payload
	}
	idx := 1
	ext := payload[idx]
	idx++
	if (ext & 0x80) != 0 { // I bit (PictureID)
		if len(payload) <= idx {
			return payload
		}
		if (payload[idx] & 0x80) != 0 {
			idx += 2
		} else {
			idx++
		}
	}
	if (ext & 0x40) != 0 { // L bit (TL0PICIDX)
		idx++
	}
	if (ext&0x20) != 0 || (ext&0x10) != 0 { // T or K bit
		idx++
	}
	if idx > len(payload) {
		return payload
	}
	out := make([]byte, 1+(len(payload)-idx))
	out[0] = b0 & 0x7f // Clear X bit, keep N/S/PartID
	copy(out[1:], payload[idx:])
	return out
}

// isVP8KeyframeStart checks whether a raw VP8 RTP payload is the first packet of a VP8 keyframe.
func isVP8KeyframeStart(payload []byte) bool {
	if len(payload) < 4 {
		return false
	}
	idx := 0
	x := (payload[0] & 0x80) != 0
	s := (payload[0] & 0x10) != 0
	partID := payload[0] & 0x0f
	if !s || partID != 0 {
		return false
	}
	idx++
	if x {
		if len(payload) <= idx {
			return false
		}
		ext := payload[idx]
		idx++
		if (ext & 0x80) != 0 { // I bit (PictureID)
			if len(payload) <= idx {
				return false
			}
			if (payload[idx] & 0x80) != 0 {
				idx += 2
			} else {
				idx++
			}
		}
		if (ext & 0x40) != 0 { // L bit (TL0PICIDX)
			idx++
		}
		if (ext&0x20) != 0 || (ext&0x10) != 0 { // T or K bit
			idx++
		}
	}
	if len(payload) < idx+10 {
		return false
	}
	return (payload[idx]&0x01) == 0 && payload[idx+3] == 0x9d && payload[idx+4] == 0x01 && payload[idx+5] == 0x2a
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
	if id.DisplayName != "" && ps.id.DisplayName != id.DisplayName {
		ps.id.DisplayName = id.DisplayName
		ps.sourceName = SourceName(b.cfg.Prefix, b.cfg.Room, ps.id)
	}
	needRestart := ps.done == nil
	if isVideo && !ps.hasVideo {
		ps.hasVideo = true
		needRestart = true
	} else if !isVideo && !ps.hasAudio {
		ps.hasAudio = true
		needRestart = true
	}

	if isVideo {
		if isVP8KeyframeStart(pkt.Payload) {
			ps.curKeyframeTS = pkt.Timestamp
			ps.curKeyframePkts = nil
		}
		if ps.curKeyframeTS != 0 && pkt.Timestamp == ps.curKeyframeTS {
			cp := make([]byte, len(pkt.Payload))
			copy(cp, pkt.Payload)
			ps.curKeyframePkts = append(ps.curKeyframePkts, cp)
			if pkt.Marker {
				ps.lastKeyframe = ps.curKeyframePkts
				ps.curKeyframePkts = nil
				ps.curKeyframeTS = 0
			}
		}
	}

	// If this participant is known to have video, wait until the first video packet
	// (which is guaranteed to be a keyframe by jitsi-receive) before starting FFmpeg,
	// so FFmpeg immediately probes VP8 dimensions without discarding audio packets.
	if ps.hasVideo && !ps.started {
		if !isVideo {
			b.mu.Unlock()
			return nil
		}
		ps.started = true
		needRestart = true
	} else if !ps.hasVideo && !ps.started {
		ps.started = true
		needRestart = true
	}

	if ps.startTime.IsZero() {
		ps.startTime = time.Now()
	}

	var pt uint8
	var seq uint16
	var normTS uint32
	if isVideo {
		pt = 96
		ps.videoSeq++
		seq = ps.videoSeq
		if !ps.videoBaseInit {
			ps.videoBaseInit = true
			ps.videoBaseRTPTS = pkt.Timestamp
			ps.videoStartOff = uint32(time.Since(ps.startTime).Seconds()*90000) + 900
		}
		normTS = ps.videoStartOff + (pkt.Timestamp - ps.videoBaseRTPTS)
	} else {
		pt = 111
		ps.audioSeq++
		seq = ps.audioSeq
		if !ps.audioBaseInit {
			ps.audioBaseInit = true
			ps.audioBaseRTPTS = pkt.Timestamp
			ps.audioStartOff = uint32(time.Since(ps.startTime).Seconds()*48000) + 480
		}
		normTS = ps.audioStartOff + (pkt.Timestamp - ps.audioBaseRTPTS)
	}

	clone := *pkt
	clone.Header.PayloadType = pt
	clone.Header.SequenceNumber = seq
	clone.Header.Timestamp = normTS
	clone.Header.Extension = false
	clone.Header.ExtensionProfile = 0
	clone.Header.Extensions = nil
	if isVideo {
		clone.Payload = stripVP8DescriptorToRFC7741Minimal(pkt.Payload)
	}
	raw, err := clone.Marshal()
	if err != nil {
		b.mu.Unlock()
		return err
	}
	if needRestart {
		b.restartProcessLocked(ctx, ps)
	}
	var conn *net.UDPConn
	var dstPort int
	if isVideo {
		conn = ps.videoConn
		dstPort = ps.videoPort
	} else {
		conn = ps.audioConn
		dstPort = ps.audioPort
	}
	b.mu.Unlock()

	if conn == nil || dstPort <= 0 {
		return nil
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
		case <-time.After(1500 * time.Millisecond):
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

	pCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	ps.cancel = cancel
	ps.done = done

	var cmd *exec.Cmd
	var stdin io.WriteCloser
	var stderrBuf bytes.Buffer
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
	cmd.Stderr = &stderrBuf
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
		if err := cmd.Start(); err != nil {
			if pCtx.Err() == nil {
				slog.Warn("participant broadcast process failed to start", "source_name", ps.sourceName, "error", err)
			}
			return
		}
		if err := cmd.Wait(); err != nil && pCtx.Err() == nil {
			slog.Warn("participant broadcast process exited", "source_name", ps.sourceName, "error", err, "stderr", strings.TrimSpace(stderrBuf.String()))
		} else if s := strings.TrimSpace(stderrBuf.String()); s != "" {
			slog.Info("participant broadcast stderr", "source_name", ps.sourceName, "stderr", s)
		}
	}()
	// Give FFmpeg a moment to open its local SDP RTP UDP sockets before the caller sends the first keyframe packet.
	time.Sleep(100 * time.Millisecond)
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
