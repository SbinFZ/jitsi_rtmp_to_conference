// Package media converts external audio/video into WebRTC-compatible RTP.
package media

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"jitsi-stream/internal/input"
)

type Config struct {
	Input, Key, FFmpeg, Proxy string
	Height, FPS, VideoKbps    int
	NoAudio                   bool
}

type Counters struct{ Audio, Video atomic.Uint64 }

func outputArgs(cfg Config, videoPort, audioPort int) []string {
	args := []string{"-map", "0:v:0", "-an", "-vf", fmt.Sprintf("scale=-2:%d,fps=%d", cfg.Height, cfg.FPS), "-c:v", "libvpx", "-deadline", "realtime", "-cpu-used", "6", "-threads", "2", "-b:v", fmt.Sprintf("%dk", cfg.VideoKbps), "-g", strconv.Itoa(cfg.FPS), "-pix_fmt", "yuv420p", "-f", "rtp", "-payload_type", "96", fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", videoPort)}
	if !cfg.NoAudio {
		audioMap := "0:a:0"
		if cfg.Input == "test" {
			audioMap = "1:a:0"
		}
		args = append(args, "-map", audioMap, "-vn", "-af", "aresample=async=1:first_pts=0", "-c:a", "libopus", "-ar", "48000", "-ac", "2", "-b:a", "96k", "-frame_duration", "20", "-f", "rtp", "-payload_type", "111", fmt.Sprintf("rtp://127.0.0.1:%d?pkt_size=1200", audioPort))
	}
	return args
}

func listen() (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err == nil {
		_ = conn.SetReadBuffer(2 * 1024 * 1024)
	}
	return conn, err
}

// Run owns exactly one FFmpeg process and both loopback RTP sockets. It returns
// only after process + forwarding goroutines have stopped.
func Run(ctx context.Context, cfg Config, video, audio *webrtc.TrackLocalStaticRTP, stats *Counters) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	v, err := listen()
	if err != nil {
		return err
	}
	defer v.Close()
	a, err := listen()
	if err != nil {
		return err
	}
	defer a.Close()
	args, body, err := input.Source(ctx, cfg.Input, cfg.Key, cfg.Proxy)
	if err != nil {
		return err
	}
	if body != nil {
		defer body.Close()
	}
	args = append([]string{"-hide_banner", "-nostdin", "-loglevel", "warning", "-fflags", "+genpts", "-probesize", "2000000", "-analyzeduration", "2000000"}, args...)
	args = append(args, outputArgs(cfg, v.LocalAddr().(*net.UDPAddr).Port, a.LocalAddr().(*net.UDPAddr).Port)...)
	cmd := exec.CommandContext(ctx, cfg.FFmpeg, args...)
	cmd.WaitDelay = 2 * time.Second
	// Explicit copier avoids cmd.Wait blocking on a live source after child exit.
	var copyDone chan struct{}
	var sourceDone chan error
	var stdin io.WriteCloser
	if body != nil {
		stdin, err = cmd.StdinPipe()
		if err != nil {
			return err
		}
		defer stdin.Close()
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if body != nil {
			body.Close()
		}
		return fmt.Errorf("start FFmpeg: %w", err)
	}
	if body != nil {
		copyDone = make(chan struct{})
		sourceDone = make(chan error, 1)
		go func() { defer close(copyDone); defer stdin.Close(); _, err := io.Copy(stdin, body); sourceDone <- err }()
	}
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	wg.Add(1)
	go func() { defer wg.Done(); forward(ctx, v, video, &stats.Video, errors) }()
	if audio != nil {
		wg.Add(1)
		go func() { defer wg.Done(); forward(ctx, a, audio, &stats.Audio, errors) }()
	}
	logDone := make(chan struct{})
	go func() {
		defer close(logDone)
		s := bufio.NewScanner(stderr)
		for s.Scan() {
			slog.Warn("ffmpeg", "message", input.Redact(s.Text()))
		}
	}()
	wait := make(chan error, 1)
	go func() { wait <- cmd.Wait() }()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var result error
loop:
	for {
		select {
		case <-ctx.Done():
			result = ctx.Err()
			break loop
		case err := <-sourceDone:
			sourceDone = nil
			if err != nil {
				result = fmt.Errorf("input read: %s", input.Redact(err.Error()))
				break loop
			}
		case result = <-errors:
			break loop
		case result = <-wait:
			wait = nil
			break loop
		case <-ticker.C:
			slog.Info("media sent", "video_packets", stats.Video.Load(), "audio_packets", stats.Audio.Load())
		}
	}
	cancel()
	if body != nil {
		body.Close()
	}
	v.Close()
	a.Close()
	wg.Wait()
	if wait != nil {
		<-wait
	}
	if copyDone != nil {
		<-copyDone
	}
	<-logDone
	if result != nil {
		return fmt.Errorf("media pipeline: %s", input.Redact(result.Error()))
	}
	if stats.Video.Load() == 0 || (!cfg.NoAudio && stats.Audio.Load() == 0) {
		return fmt.Errorf("FFmpeg exited without sending required audio/video packets")
	}
	return nil
}

func forward(ctx context.Context, conn *net.UDPConn, track *webrtc.TrackLocalStaticRTP, count *atomic.Uint64, errs chan<- error) {
	if track == nil {
		return
	}
	buf := make([]byte, 65535)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		packet := new(rtp.Packet)
		if err := packet.Unmarshal(buf[:n]); err != nil {
			continue
		}
		if err := track.WriteRTP(packet); err != nil {
			select {
			case errs <- err:
			case <-ctx.Done():
			}
			return
		}
		count.Add(1)
	}
}
