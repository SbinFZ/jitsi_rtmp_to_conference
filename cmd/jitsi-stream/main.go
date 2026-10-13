package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"jitsi-stream/internal/conference"
	"jitsi-stream/internal/input"
	"jitsi-stream/internal/media"
)

func run() error {
	var cc conference.Config
	var mc media.Config
	var duration time.Duration
	var retries int
	flag.StringVar(&cc.Host, "host", "m.yuanjinapp.com", "Jitsi hostname / HTTPS origin")
	flag.StringVar(&cc.Room, "room", "", "meeting room name")
	flag.StringVar(&cc.Name, "name", "Live Stream", "participant display name")
	flag.StringVar(&mc.Input, "input", "", "FLV/RTMP/RTSP/HLS URL, file, - (stdin), or test")
	flag.StringVar(&mc.FFmpeg, "ffmpeg", "ffmpeg", "FFmpeg executable")
	flag.StringVar(&mc.Proxy, "input-proxy", "", "proxy for HTTP-FLV input only (http/https/socks5); e.g. http://127.0.0.1:7897")
	flag.IntVar(&mc.Height, "height", 720, "output video height (even)")
	flag.IntVar(&mc.FPS, "fps", 25, "output frame rate")
	flag.IntVar(&mc.VideoKbps, "bitrate", 1500, "VP8 video kbps")
	flag.BoolVar(&mc.NoAudio, "no-audio", false, "publish video only")
	flag.DurationVar(&cc.JoinTimeout, "join-timeout", 5*time.Minute, "timeout waiting for second participant / Jingle")
	flag.DurationVar(&cc.ConnectTimeout, "connect-timeout", 30*time.Second, "ICE/DTLS + bridge timeout")
	flag.DurationVar(&duration, "duration", 0, "optional total runtime including join (0 = until stopped)")
	flag.IntVar(&retries, "retries", 3, "rejoin / reopen input attempts after failures (0 disables)")
	flag.BoolVar(&cc.Debug, "debug-xmpp", false, "raw signalling logs; may contain ICE credentials (do not share)")
	flag.Parse()
	if mc.Input == "" {
		mc.Input = os.Getenv("STREAM_INPUT")
	}
	if cc.Room == "" || mc.Input == "" {
		return fmt.Errorf("--room and --input required; see --help")
	}
	if mc.Height < 144 || mc.Height > 2160 || mc.Height%2 != 0 || mc.FPS < 1 || mc.FPS > 60 || mc.VideoKbps < 100 || mc.VideoKbps > 20000 || retries < 0 || cc.JoinTimeout <= 0 || cc.ConnectTimeout <= 0 || duration < 0 {
		return fmt.Errorf("invalid media settings / timeout / retries")
	}
	if _, _, err := conference.Normalize(cc.Host, cc.Room); err != nil {
		return err
	}
	if _, err := exec.LookPath(mc.FFmpeg); err != nil {
		return fmt.Errorf("FFmpeg executable not found: install ffmpeg or use --ffmpeg")
	}
	if mc.Input == "-" {
		retries = 0
	} // consumed stdin cannot be reopened
	mc.Key = os.Getenv("NODEMEDIA_AUTH_KEY")
	cc.PublishAudio = !mc.NoAudio
	cc.PublishVideo = true
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}
	sentMedia := false
	for attempt := 0; ; attempt++ {
		sent, err := publish(ctx, cc, mc)
		sentMedia = sentMedia || sent
		if ctx.Err() != nil {
			if ctx.Err() == context.DeadlineExceeded && !sentMedia {
				return fmt.Errorf("runtime elapsed before required media was sent")
			}
			return nil
		}
		if err == nil {
			slog.Info("input finished")
			return nil
		}
		if attempt >= retries {
			return err
		}
		slog.Warn("attempt failed; reconnecting", "error", input.Redact(err.Error()), "retry", attempt+1)
		delay := time.Duration(1<<min(attempt, 4)) * time.Second
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded && !sentMedia {
				return fmt.Errorf("runtime elapsed before required media was sent")
			}
			return nil
		case <-time.After(delay):
		}
	}
}

func publish(ctx context.Context, cc conference.Config, mc media.Config) (bool, error) {
	c, err := conference.Join(ctx, cc)
	if err != nil {
		return false, err
	}
	defer c.Close()
	pipelineCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case err := <-c.Done:
			slog.Warn("conference interrupted", "error", input.Redact(err.Error()))
			cancel()
		case <-pipelineCtx.Done():
		}
	}()
	stats := new(media.Counters)
	err = media.Run(pipelineCtx, mc, c.Video, c.Audio, stats)
	slog.Info("publish stopped", "video_packets", stats.Video.Load(), "audio_packets", stats.Audio.Load())
	return stats.Video.Load() > 0 && (mc.NoAudio || stats.Audio.Load() > 0), err
}

func main() {
	if err := run(); err != nil {
		slog.Error("jitsi-stream failed", "error", input.Redact(err.Error()))
		os.Exit(1)
	}
}
