// jitsi-receive is a browser-free integration probe, not a meeting UI.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media/ivfwriter"
	"github.com/pion/webrtc/v4/pkg/media/oggwriter"
	"jitsi-stream/internal/conference"
	"jitsi-stream/internal/media"
)

type recorder interface {
	WriteRTP(*rtp.Packet) error
	Close() error
}

func run() error {
	var cc conference.Config
	var out string
	var duration time.Duration
	var videoOnly bool
	flag.StringVar(&cc.Host, "host", "m.yuanjinapp.com", "Jitsi host")
	flag.StringVar(&cc.Room, "room", "", "room name")
	flag.StringVar(&cc.Name, "name", "CLI Receiver", "participant name")
	flag.StringVar(&out, "out", "recordings", "directory for received .ivf and .ogg files (per SSRC)")
	flag.BoolVar(&videoOnly, "video-only", false, "do not require audio for successful validation")
	flag.DurationVar(&duration, "duration", time.Minute, "runtime including join")
	flag.DurationVar(&cc.JoinTimeout, "join-timeout", 5*time.Minute, "wait for Jingle")
	flag.DurationVar(&cc.ConnectTimeout, "connect-timeout", 30*time.Second, "ICE/DTLS timeout")
	flag.BoolVar(&cc.Debug, "debug-xmpp", false, "sensitive raw signalling logs")
	flag.Parse()
	if cc.Room == "" || duration <= 0 || cc.JoinTimeout <= 0 || cc.ConnectTimeout <= 0 {
		return fmt.Errorf("--room and positive duration/timeouts required")
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	var audio, video atomic.Uint64
	var wg sync.WaitGroup
	var mu sync.Mutex
	closing := false
	// Register before negotiation. Shutdown prevents new callback goroutines from
	// adding to the wait group while already-open track readers are being drained.
	cc.OnTrack = func(pc *webrtc.PeerConnection, track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		mu.Lock()
		if closing {
			mu.Unlock()
			return
		}
		wg.Add(1)
		mu.Unlock()
		defer wg.Done()
		codec := track.Codec().MimeType
		if codec != webrtc.MimeTypeVP8 && codec != webrtc.MimeTypeOpus {
			return
		}
		slog.Info("received track", "codec", codec, "ssrc", track.SSRC())
		var writer recorder
		var err error
		if codec == webrtc.MimeTypeOpus {
			writer, err = oggwriter.New(filepath.Join(out, fmt.Sprintf("audio-%d.ogg", track.SSRC())), 48000, 2)
			if err != nil {
				slog.Error("recording", "error", err)
				return
			}
		}
		defer func() {
			if writer != nil {
				_ = writer.Close()
			}
		}()
		pliCtx, stopPLI := context.WithCancel(ctx)
		defer stopPLI()
		if track.Kind() == webrtc.RTPCodecTypeVideo {
			go func() {
				t := time.NewTicker(time.Second)
				defer t.Stop()
				for {
					select {
					case <-pliCtx.Done():
						return
					case <-t.C:
						_ = pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(track.SSRC())}})
					}
				}
			}()
		}
		reorder := media.NewReorder(128)
		for {
			packet, _, e := track.ReadRTP()
			if e != nil {
				return
			}
			for _, packet := range reorder.Push(packet) {
				if writer == nil {
					width, height, ok := vp8Dimensions(packet.Payload)
					if !ok {
						continue
					}
					writer, err = ivfwriter.New(filepath.Join(out, fmt.Sprintf("video-%d.ivf", track.SSRC())), ivfwriter.WithCodec(webrtc.MimeTypeVP8), ivfwriter.WithWidthAndHeight(width, height))
					if err != nil {
						slog.Error("recording", "error", err)
						return
					}
				}
				if e = writer.WriteRTP(packet); e != nil {
					slog.Warn("recording packet", "error", e)
					continue
				}
				if track.Kind() == webrtc.RTPCodecTypeAudio {
					audio.Add(1)
				} else {
					video.Add(1)
				}
			}
		}
	}
	c, err := conference.Join(ctx, cc)
	if err != nil {
		mu.Lock()
		closing = true
		mu.Unlock()
		wg.Wait()
		return err
	}
	defer c.Close()
	cleanup := func() { mu.Lock(); closing = true; mu.Unlock(); c.Close(); wg.Wait() }
	if err := c.Session.RequestVideo(ctx, 1080); err != nil {
		cleanup()
		return err
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case err = <-c.Done:
			break loop
		case <-ticker.C:
			slog.Info("media received", "video_packets", video.Load(), "audio_packets", audio.Load())
		}
	}
	cleanup()
	slog.Info("receive stopped", "video_packets", video.Load(), "audio_packets", audio.Load())
	if err != nil {
		return err
	}
	if video.Load() == 0 || (!videoOnly && audio.Load() == 0) {
		return fmt.Errorf("did not receive required media")
	}
	return nil
}

func vp8Dimensions(payload []byte) (uint16, uint16, bool) {
	vp := new(codecs.VP8Packet)
	data, err := vp.Unmarshal(payload)
	if err != nil || vp.S != 1 || len(data) < 10 || data[0]&1 != 0 || data[3] != 0x9d || data[4] != 0x01 || data[5] != 0x2a {
		return 0, 0, false
	}
	return binary.LittleEndian.Uint16(data[6:8]) & 0x3fff, binary.LittleEndian.Uint16(data[8:10]) & 0x3fff, true
}
func main() {
	if err := run(); err != nil {
		slog.Error("receive failed", "error", err)
		os.Exit(1)
	}
}
