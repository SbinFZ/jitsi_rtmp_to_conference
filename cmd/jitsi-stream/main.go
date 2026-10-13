// jitsi-stream publishes an external stream (HTTP-FLV, RTMP pull/push, RTSP,
// HLS, files, ...) as one native Jitsi participant. No browser is used.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"jitsi-stream/internal/conference"
	"jitsi-stream/internal/input"
	"jitsi-stream/internal/media"
	"jitsi-stream/internal/rtmpserver"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

type options struct {
	conf     conference.Config
	media    media.Config
	duration time.Duration
	retries  int
	ingest   rtmpserver.Endpoint
	// meeting existence: retries with binary exponential backoff
	meetingRetries int
	meetingBackoff time.Duration
}

func parse() (options, error) {
	var o options
	cc, mc := &o.conf, &o.media
	var showVersion bool
	var keyFile string
	flag.StringVar(&cc.Host, "host", "m.yuanjinapp.com", "Jitsi hostname / HTTPS origin")
	flag.StringVar(&cc.Room, "room", "", "meeting room name (required)")
	flag.StringVar(&cc.Name, "name", "Live Stream", "participant display name")
	flag.StringVar(&mc.Input.Input, "input", "", "input: HTTP(S)-FLV / rtmp(s):// / rtsp:// / HLS URL, local file, - (stdin) or test; env STREAM_INPUT")
	flag.StringVar(&mc.Input.Command, "input-cmd", "", "shell command printing a FRESH input URL; run before every (re)connect (for one-time URLs)")
	flag.StringVar(&keyFile, "sign-key-file", "", "file containing the NodeMedia signing key (alternative to env NODEMEDIA_AUTH_KEY)")
	flag.IntVar(&o.meetingRetries, "meeting-retries", 5, "if the meeting does not exist, retry this many times with exponential backoff, then exit (0 = do not check; create/wait for the meeting)")
	flag.DurationVar(&o.meetingBackoff, "meeting-backoff", time.Second, "first backoff delay when the meeting does not exist; doubles each retry (1s,2s,4s,8s,16s)")
	flag.BoolVar(&mc.Input.Listen, "listen", false, "act as RTMP server for --input rtmp://bind:port/app/streamkey; encoders (OBS) push to it")
	flag.StringVar(&mc.Input.Proxy, "input-proxy", "", "HTTP-FLV proxy: empty = auto (HTTPS_PROXY env, else macOS/Windows system proxy), direct, or http(s)://|socks5://host:port")
	flag.StringVar(&mc.FFmpeg, "ffmpeg", "ffmpeg", "FFmpeg executable")
	flag.IntVar(&mc.Height, "height", 720, "output video height (even)")
	flag.IntVar(&mc.FPS, "fps", 25, "output frame rate")
	flag.IntVar(&mc.VideoKbps, "bitrate", 1500, "VP8 video kbps")
	flag.BoolVar(&mc.NoAudio, "no-audio", false, "publish video only (input has no audio track)")
	flag.DurationVar(&cc.JoinTimeout, "join-timeout", 5*time.Minute, "timeout waiting for second participant / Jingle")
	flag.DurationVar(&cc.ConnectTimeout, "connect-timeout", 30*time.Second, "ICE/DTLS + bridge timeout")
	flag.DurationVar(&o.duration, "duration", 0, "optional total runtime including join (0 = until stopped)")
	flag.IntVar(&o.retries, "retries", 3, "consecutive failures (no media delivered) tolerated before exiting")
	flag.BoolVar(&cc.Debug, "debug-xmpp", false, "raw signalling logs; may contain ICE credentials (do not share)")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()
	if showVersion {
		fmt.Println("jitsi-stream", version)
		os.Exit(0)
	}
	if mc.Input.Input == "" {
		mc.Input.Input = os.Getenv("STREAM_INPUT")
	}
	if mc.Input.Input != "" && mc.Input.Command != "" {
		return o, fmt.Errorf("use either --input or --input-cmd, not both")
	}
	if cc.Room == "" || (mc.Input.Input == "" && mc.Input.Command == "") {
		return o, fmt.Errorf("--room and --input (or --input-cmd) are required; see --help")
	}
	if o.meetingRetries < 0 || o.meetingRetries > 30 || o.meetingBackoff <= 0 {
		return o, fmt.Errorf("--meeting-retries must be 0..30 and --meeting-backoff positive")
	}
	if mc.Height < 144 || mc.Height > 2160 || mc.Height%2 != 0 || mc.FPS < 1 || mc.FPS > 60 ||
		mc.VideoKbps < 100 || mc.VideoKbps > 20000 || o.retries < 0 || cc.JoinTimeout <= 0 ||
		cc.ConnectTimeout <= 0 || o.duration < 0 {
		return o, fmt.Errorf("invalid media settings / timeout / retries")
	}
	if _, _, err := conference.Normalize(cc.Host, cc.Room); err != nil {
		return o, err
	}
	// Validate the ingest URL early (no network), so typos fail before joining.
	if mc.Input.Listen {
		ep, err := rtmpserver.Parse(mc.Input.Input)
		if err != nil {
			return o, err
		}
		o.ingest = ep
	}
	if _, err := exec.LookPath(mc.FFmpeg); err != nil {
		return o, fmt.Errorf("FFmpeg executable not found: install ffmpeg or use --ffmpeg")
	}
	if !mc.Input.Restartable() {
		o.retries = 0 // consumed stdin cannot be reopened
	}
	mc.Input.Key = os.Getenv("NODEMEDIA_AUTH_KEY")
	if keyFile != "" {
		b, err := os.ReadFile(keyFile)
		if err != nil {
			return o, fmt.Errorf("--sign-key-file: %w", err)
		}
		mc.Input.Key = strings.TrimSpace(string(b))
	}
	if mc.Input.Input != "" && input.HasSignature(mc.Input.Input) && !mc.Input.Refreshable() {
		slog.Warn("input URL carries a signature that may be one-time/expiring: a reconnect will be refused (HTTP 401). " +
			"For automatic re-signing set NODEMEDIA_AUTH_KEY or --sign-key-file, or use --input-cmd to fetch a fresh URL")
	}
	cc.RequireMeeting = o.meetingRetries > 0
	cc.PublishAudio, cc.PublishVideo = !mc.NoAudio, true
	return o, nil
}

func run() error {
	o, err := parse()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if o.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.duration)
		defer cancel()
	}
	if o.media.Input.Listen {
		// Bind before joining: a port conflict fails fast. Encoders may connect
		// at any time; media flows once the meeting transport is ready.
		srv, err := rtmpserver.Listen(o.ingest)
		if err != nil {
			return err
		}
		defer srv.Close()
		o.media.Input.Next = srv.Next
		slog.Info("RTMP push mode: publish to rtmp://<this-host>:<port>/<app>/<stream key>", "app", o.ingest.App)
	}
	s := &supervisor{o: o}
	err = s.loop(ctx)
	if ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && !s.delivered {
			return fmt.Errorf("runtime elapsed before required media was sent")
		}
		return nil
	}
	return err
}

// supervisor keeps one participant in the meeting and restarts the media
// input underneath it. Only consecutive attempts that deliver no media count
// against --retries, so a long-running stream survives occasional outages.
type supervisor struct {
	o         options
	failures  int
	delivered bool
}

var errInputFinished = errors.New("input finished")

func (s *supervisor) fail(ctx context.Context, err error) error {
	s.failures++
	if s.failures > s.o.retries {
		return err
	}
	delay := time.Duration(1<<min(s.failures-1, 4)) * time.Second
	slog.Warn("attempt failed; retrying", "error", input.Redact(err.Error()), "failure", s.failures, "max", s.o.retries, "delay", delay)
	select {
	case <-ctx.Done():
	case <-time.After(delay):
	}
	return nil
}

func (s *supervisor) loop(ctx context.Context) error {
	missing := 0 // consecutive "meeting does not exist" answers
	for ctx.Err() == nil {
		c, err := conference.Join(ctx, s.o.conf)
		if errors.Is(err, conference.ErrMeetingNotFound) {
			if missing >= s.o.meetingRetries {
				return fmt.Errorf("meeting %q does not exist (nobody has joined it); gave up after %d retries — start the meeting first or use --meeting-retries 0 to create it", s.o.conf.Room, s.o.meetingRetries)
			}
			delay := s.o.meetingBackoff << missing // binary exponential: 1s,2s,4s,8s,16s
			missing++
			slog.Warn("meeting does not exist yet; retrying", "room", s.o.conf.Room, "retry", missing, "max", s.o.meetingRetries, "delay", delay)
			if sleep(ctx, delay) != nil {
				return nil
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if e := s.fail(ctx, err); e != nil {
				return e
			}
			continue
		}
		missing = 0
		err = s.session(ctx, c)
		c.Close()
		if errors.Is(err, errInputFinished) {
			slog.Info("input finished")
			return nil
		}
		if err != nil {
			return err
		}
		// Conference dropped: rejoin (not a media failure by itself).
	}
	return nil
}

// session runs media until the conference breaks (nil) or a terminal error.
func (s *supervisor) session(ctx context.Context, c *conference.Connection) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case err := <-c.Done:
			slog.Warn("conference interrupted; rejoining", "error", input.Redact(err.Error()))
			cancel()
		case <-sctx.Done():
		}
	}()
	video := media.NewSink(c.Video, 90000)
	audio := media.NewSink(c.Audio, 48000)
	go presence(sctx, c, video, audio)
	for sctx.Err() == nil {
		before := video.Count()
		err := media.Run(sctx, s.o.media, video, audio)
		sent := video.Count() > before
		if sent {
			s.failures, s.delivered = 0, true
		}
		switch {
		case sctx.Err() != nil:
			return nil // shutdown or conference loss; loop() decides
		case s.o.media.Input.Listen:
			// One bad/short publisher must not take the ingest service down.
			if err != nil {
				slog.Warn("RTMP publish session ended with error; waiting for the next publisher", "error", input.Redact(err.Error()))
			} else {
				slog.Info("RTMP publisher disconnected; waiting for the next publisher")
			}
		case err == nil && sent && s.o.media.Input.Live():
			slog.Info("live source ended; reconnecting (participant stays in the meeting)")
			if e := sleep(sctx, time.Second); e != nil {
				return nil
			}
		case err == nil && sent:
			slog.Info("publish stopped", "video_packets", video.Count(), "audio_packets", audio.Count())
			return errInputFinished
		default:
			if err == nil {
				err = fmt.Errorf("input ended without media")
			}
			if !s.o.media.Input.Restartable() {
				return err
			}
			// A one-time URL cannot be reused: retrying the same credential is
			// pointless, so stop immediately with an actionable message.
			if errors.Is(err, input.ErrUnauthorized) && !s.o.media.Input.Refreshable() {
				return fmt.Errorf("%w; the one-time URL cannot be reused after a disconnect: set NODEMEDIA_AUTH_KEY / --sign-key-file to re-sign automatically, or use --input-cmd to fetch a fresh URL", err)
			}
			if e := s.fail(sctx, err); e != nil {
				return e
			}
		}
	}
	return nil
}

// presence mirrors live media into Jitsi mute state (video tile vs avatar).
func presence(ctx context.Context, c *conference.Connection, video, audio *media.Sink) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var lastV, lastA uint64
	var idleV, idleA int
	known, liveV, liveA := false, false, false
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		v, a := video.Count(), audio.Count()
		idleV, idleA = idle(v, lastV, idleV), idle(a, lastA, idleA)
		lastV, lastA = v, a
		// Live after any packet this second; muted after 5 quiet seconds (avoids
		// flapping on jittery networks).
		nv, na := idleV == 0 || (liveV && idleV < 5), audio != nil && (idleA == 0 || (liveA && idleA < 5))
		if !known || nv != liveV || na != liveA {
			if err := c.SetMedia(na, nv); err != nil {
				slog.Warn("presence update failed", "error", err)
				continue
			}
			known, liveV, liveA = true, nv, na
			slog.Info("participant media state", "video", nv, "audio", na)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func idle(now, last uint64, n int) int {
	if now != last {
		return 0
	}
	return n + 1
}

func main() {
	if err := run(); err != nil {
		slog.Error("jitsi-stream failed", "error", input.Redact(err.Error()))
		os.Exit(1)
	}
}
