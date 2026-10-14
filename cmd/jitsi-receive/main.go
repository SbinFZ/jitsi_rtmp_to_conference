// jitsi-receive is a browser-free multi-participant receiver, recorder, and NDI broadcaster.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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
	"jitsi-stream/internal/ndi"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

type recorder interface {
	WriteRTP(*rtp.Packet) error
	Close() error
}

// TrackRecord describes a recorded RTP track in participants.json.
type TrackRecord struct {
	SSRC        uint32 `json:"ssrc"`
	Codec       string `json:"codec"`
	Media       string `json:"media"`
	Endpoint    string `json:"endpoint,omitempty"`
	DisplayName string `json:"display_name,omitempty"`
	VideoType   string `json:"video_type,omitempty"`
	NDISource   string `json:"ndi_source,omitempty"`
	RawFile     string `json:"raw_file"`
	UserFile    string `json:"user_file,omitempty"`
	Packets     uint64 `json:"packets"`
}

func run() error {
	var cc conference.Config
	var out string
	var duration time.Duration
	var videoOnly bool
	var ndiCfg ndi.Config

	flag.StringVar(&cc.Host, "host", "m.yuanjinapp.com", "Jitsi host")
	flag.StringVar(&cc.Room, "room", "", "room name")
	flag.StringVar(&cc.Name, "name", "CLI Receiver", "participant name")
	flag.StringVar(&out, "out", "recordings", "directory for received .ivf, .ogg, and per-participant files")
	flag.BoolVar(&videoOnly, "video-only", false, "do not require audio for successful validation")
	flag.DurationVar(&duration, "duration", time.Minute, "runtime including join")
	flag.DurationVar(&cc.JoinTimeout, "join-timeout", 5*time.Minute, "wait for Jingle")
	flag.DurationVar(&cc.ConnectTimeout, "connect-timeout", 30*time.Second, "ICE/DTLS timeout")
	flag.BoolVar(&cc.Debug, "debug-xmpp", false, "sensitive raw signalling logs")
	flag.BoolVar(&ndiCfg.Enabled, "ndi", false, "broadcast each remote participant's stream via NDI (or --ndi-format / --ndi-cmd)")
	flag.StringVar(&ndiCfg.Prefix, "ndi-prefix", "Jitsi", "NDI source name prefix (e.g. 'Jitsi (room) - DisplayName [endpoint]')")
	flag.StringVar(&ndiCfg.Format, "ndi-format", "libndi_newtek", "FFmpeg output format for --ndi (libndi_newtek, mpegts, webm)")
	flag.StringVar(&ndiCfg.SinkCmd, "ndi-cmd", "", "optional custom command per participant stream (receives JITSI_NDI_NAME, JITSI_SDP_PATH, etc.)")
	flag.StringVar(&ndiCfg.FFmpegBin, "ffmpeg", "ffmpeg", "FFmpeg executable path for NDI / stream muxing")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("jitsi-receive", version)
		return nil
	}
	if cc.Room == "" || duration <= 0 || cc.JoinTimeout <= 0 || cc.ConnectTimeout <= 0 {
		return fmt.Errorf("--room and positive duration/timeouts required")
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	_, normRoom, err := conference.Normalize(cc.Host, cc.Room)
	if err != nil {
		return err
	}
	ndiCfg.Room = normRoom
	ndiCfg.OutDir = out
	broadcaster := ndi.NewBroadcaster(ndiCfg)
	defer broadcaster.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()

	var audio, video atomic.Uint64
	var wg sync.WaitGroup
	var mu sync.Mutex
	closing := false

	var connPtr atomic.Pointer[conference.Connection]
	var tracksMu sync.Mutex
	trackRecords := make(map[uint32]*TrackRecord)

	lookupIdentity := func(ssrc uint32) (ndi.ParticipantIdentity, bool) {
		c := connPtr.Load()
		if c == nil || c.Participants == nil {
			return ndi.ParticipantIdentity{}, false
		}
		info, ok := c.Participants.Lookup(ssrc)
		if !ok {
			return ndi.ParticipantIdentity{}, false
		}
		if info.IsRTX && info.PrimarySSRC != 0 {
			if pInfo, pOk := c.Participants.Lookup(info.PrimarySSRC); pOk {
				info = pInfo
			}
		}
		return ndi.ParticipantIdentity{
			Endpoint:    info.Endpoint,
			DisplayName: info.DisplayName,
			VideoType:   info.VideoType,
		}, true
	}

	writeManifest := func() {
		tracksMu.Lock()
		list := make([]TrackRecord, 0, len(trackRecords))
		for ssrc, rec := range trackRecords {
			if id, ok := lookupIdentity(ssrc); ok {
				if id.Endpoint != "" {
					rec.Endpoint = id.Endpoint
				}
				if id.DisplayName != "" {
					rec.DisplayName = id.DisplayName
				}
				if id.VideoType != "" {
					rec.VideoType = id.VideoType
				}
				rec.NDISource = ndi.SourceName(ndiCfg.Prefix, normRoom, id)
			}
			list = append(list, *rec)
		}
		tracksMu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].SSRC < list[j].SSRC })
		data, e := json.MarshalIndent(list, "", "  ")
		if e == nil {
			_ = os.WriteFile(filepath.Join(out, "participants.json"), data, 0644)
		}
	}

	cc.OnSourceUpdate = func(info conference.TrackSourceInfo) {
		if info.IsRTX {
			return
		}
		slog.Info("participant stream mapped",
			"ssrc", info.SSRC,
			"endpoint", info.Endpoint,
			"display_name", info.DisplayName,
			"media", info.Media,
			"video_type", info.VideoType,
		)
		writeManifest()
	}

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
		ssrc := uint32(track.SSRC())
		id, _ := lookupIdentity(ssrc)
		slog.Info("received track", "codec", codec, "ssrc", ssrc, "endpoint", id.Endpoint, "display_name", id.DisplayName)

		isVideo := track.Kind() == webrtc.RTPCodecTypeVideo
		mediaKind := "audio"
		rawName := fmt.Sprintf("audio-%d.ogg", ssrc)
		if isVideo {
			mediaKind = "video"
			rawName = fmt.Sprintf("video-%d.ivf", ssrc)
		}
		rawPath := filepath.Join(out, rawName)

		tracksMu.Lock()
		trackRecords[ssrc] = &TrackRecord{
			SSRC:        ssrc,
			Codec:       codec,
			Media:       mediaKind,
			Endpoint:    id.Endpoint,
			DisplayName: id.DisplayName,
			VideoType:   id.VideoType,
			NDISource:   ndi.SourceName(ndiCfg.Prefix, normRoom, id),
			RawFile:     rawName,
		}
		tracksMu.Unlock()
		writeManifest()

		var writer recorder
		var err error
		if codec == webrtc.MimeTypeOpus {
			writer, err = oggwriter.New(rawPath, 48000, 2)
			if err != nil {
				slog.Error("recording", "error", err)
				return
			}
		}
		defer func() {
			if writer != nil {
				_ = writer.Close()
			}
			// Create a friendly per-participant hardlink/copy if participant identity was resolved.
			finalID, ok := lookupIdentity(ssrc)
			if ok && finalID.Endpoint != "" {
				ext := ".ogg"
				if isVideo {
					ext = ".ivf"
				}
				label := finalID.Endpoint
				if finalID.DisplayName != "" {
					label = finalID.DisplayName + "-" + finalID.Endpoint
				}
				userFile := fmt.Sprintf("user-%s-%s-%d%s", ndi.SanitizeToken(label), mediaKind, ssrc, ext)
				userPath := filepath.Join(out, userFile)
				_ = os.Remove(userPath)
				if linkErr := os.Link(rawPath, userPath); linkErr == nil {
					tracksMu.Lock()
					if rec, exists := trackRecords[ssrc]; exists {
						rec.UserFile = userFile
					}
					tracksMu.Unlock()
				}
			}
			writeManifest()
		}()

		pliCtx, stopPLI := context.WithCancel(ctx)
		defer stopPLI()
		if isVideo {
			go func() {
				t := time.NewTicker(time.Second)
				defer t.Stop()
				for {
					select {
					case <-pliCtx.Done():
						return
					case <-t.C:
						_ = pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
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
					writer, err = ivfwriter.New(rawPath, ivfwriter.WithCodec(webrtc.MimeTypeVP8), ivfwriter.WithWidthAndHeight(width, height))
					if err != nil {
						slog.Error("recording", "error", err)
						return
					}
				}
				if e = writer.WriteRTP(packet); e != nil {
					slog.Warn("recording packet", "error", e)
					continue
				}
				curID, _ := lookupIdentity(ssrc)
				if ndiCfg.Enabled {
					_ = broadcaster.WriteRTP(ctx, curID, ssrc, isVideo, packet)
				}
				tracksMu.Lock()
				if rec, exists := trackRecords[ssrc]; exists {
					rec.Packets++
				}
				tracksMu.Unlock()
				if !isVideo {
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
	connPtr.Store(c)
	writeManifest()
	defer c.Close()
	cleanup := func() {
		mu.Lock()
		closing = true
		mu.Unlock()
		c.Close()
		wg.Wait()
		_ = broadcaster.Close()
		writeManifest()
	}
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
			slog.Info("media received",
				"video_packets", video.Load(),
				"audio_packets", audio.Load(),
				"ndi_sources", len(broadcaster.ActiveSources()),
			)
			writeManifest()
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
