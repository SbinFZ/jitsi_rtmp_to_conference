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
	"os/exec"
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
	MuxedFile   string `json:"muxed_file,omitempty"`
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
	var sourcesMu sync.RWMutex
	sources := make(map[uint32]conference.TrackSourceInfo)
	var tracksMu sync.Mutex
	trackRecords := make(map[uint32]*TrackRecord)

	lookupIdentity := func(ssrc uint32) (ndi.ParticipantIdentity, bool) {
		sourcesMu.RLock()
		info, ok := sources[ssrc]
		if ok && info.IsRTX && info.PrimarySSRC != 0 {
			if pInfo, pOk := sources[info.PrimarySSRC]; pOk {
				info = pInfo
			}
		}
		sourcesMu.RUnlock()
		if !ok {
			if c := connPtr.Load(); c != nil && c.Participants != nil {
				info, ok = c.Participants.Lookup(ssrc)
				if ok && info.IsRTX && info.PrimarySSRC != 0 {
					if pInfo, pOk := c.Participants.Lookup(info.PrimarySSRC); pOk {
						info = pInfo
					}
				}
			}
		}
		if !ok {
			return ndi.ParticipantIdentity{}, false
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
		sourcesMu.Lock()
		sources[info.SSRC] = info
		sourcesMu.Unlock()
		if info.IsRTX {
			return
		}
		if ndiCfg.Enabled && info.Endpoint != "" && info.Endpoint != "jvb" {
			broadcaster.RegisterMedia(ndi.ParticipantIdentity{
				Endpoint:    info.Endpoint,
				DisplayName: info.DisplayName,
				VideoType:   info.VideoType,
			}, info.Media == "audio", info.Media == "video")
		}
		slog.Info("participant stream mapped",
			"ssrc", info.SSRC,
			"endpoint", info.Endpoint,
			"display_name", info.DisplayName,
			"media", info.Media,
			"video_type", info.VideoType,
		)
		if info.Media == "video" && info.Endpoint != "" && info.Endpoint != "jvb" {
			if c := connPtr.Load(); c != nil {
				var srcNames []string
				if c.Participants != nil {
					srcNames = c.Participants.SourceNames()
				}
				_ = c.Session.RequestVideo(ctx, 1080, srcNames...)
				if c.PC != nil {
					_ = c.PC.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: info.SSRC}})
				}
			}
		}
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
				kindLabel := mediaKind
				if isVideo && finalID.VideoType == "desktop" {
					kindLabel = "desktop"
				}
				userFile := fmt.Sprintf("user-%s-%s-%d%s", ndi.SanitizeToken(label), kindLabel, ssrc, ext)
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
			_ = pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: ssrc}})
			go func() {
				t := time.NewTicker(400 * time.Millisecond)
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
		finalizeParticipantMux(out, ndiCfg, trackRecords, &tracksMu)
		writeManifest()
	}
	var initSrcs []string
	if c.Participants != nil {
		initSrcs = c.Participants.SourceNames()
	}
	if err := c.Session.RequestVideo(ctx, 1080, initSrcs...); err != nil {
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
			var curSrcs []string
			if c.Participants != nil {
				curSrcs = c.Participants.SourceNames()
			}
			_ = c.Session.RequestVideo(ctx, 1080, curSrcs...)
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

func finalizeParticipantMux(out string, ndiCfg ndi.Config, trackRecords map[uint32]*TrackRecord, tracksMu *sync.Mutex) {
	ffmpegBin := ndi.ResolveFFmpegBin(ndiCfg.FFmpegBin)
	if _, err := exec.LookPath(ffmpegBin); err != nil {
		if info, statErr := os.Stat(ffmpegBin); statErr != nil || info.IsDir() {
			return
		}
	}
	type videoInput struct {
		ssrc      uint32
		path      string
		videoType string
	}
	type epTracks struct {
		endpoint    string
		displayName string
		audioSSRC   uint32
		audioPath   string
		videos      []videoInput
	}
	byEp := make(map[string]*epTracks)
	tracksMu.Lock()
	for ssrc, rec := range trackRecords {
		if rec.Endpoint == "" || rec.Endpoint == "jvb" || rec.Packets == 0 {
			continue
		}
		rawPath := filepath.Join(out, rec.RawFile)
		info, err := os.Stat(rawPath)
		if err != nil || info.Size() == 0 {
			continue
		}
		g, ok := byEp[rec.Endpoint]
		if !ok {
			g = &epTracks{endpoint: rec.Endpoint, displayName: rec.DisplayName}
			byEp[rec.Endpoint] = g
		}
		if g.displayName == "" && rec.DisplayName != "" {
			g.displayName = rec.DisplayName
		}
		if rec.Media == "video" && info.Size() > 32 {
			g.videos = append(g.videos, videoInput{
				ssrc:      ssrc,
				path:      rawPath,
				videoType: rec.VideoType,
			})
		} else if rec.Media == "audio" && info.Size() > 64 {
			g.audioPath = rawPath
			g.audioSSRC = ssrc
		}
	}
	tracksMu.Unlock()

	type muxTarget struct {
		endpoint    string
		displayName string
		suffix      string
		audioPath   string
		videoPath   string
		ssrcs       []uint32
	}
	var targets []muxTarget
	for ep, g := range byEp {
		if len(g.videos) == 0 && g.audioPath == "" {
			continue
		}
		if len(g.videos) == 0 {
			targets = append(targets, muxTarget{
				endpoint:    ep,
				displayName: g.displayName,
				audioPath:   g.audioPath,
				ssrcs:       []uint32{g.audioSSRC},
			})
			continue
		}
		sort.Slice(g.videos, func(i, j int) bool { return g.videos[i].ssrc < g.videos[j].ssrc })
		cameraCount := 0
		desktopCount := 0
		for _, v := range g.videos {
			suffix := ""
			if v.videoType == "desktop" {
				desktopCount++
				if desktopCount == 1 {
					suffix = "-desktop"
				} else {
					suffix = fmt.Sprintf("-desktop-%d", v.ssrc)
				}
			} else {
				cameraCount++
				if cameraCount > 1 {
					suffix = fmt.Sprintf("-%d", v.ssrc)
				}
			}
			ssrcs := []uint32{v.ssrc}
			if g.audioSSRC != 0 && suffix == "" {
				ssrcs = append(ssrcs, g.audioSSRC)
			}
			targets = append(targets, muxTarget{
				endpoint:    ep,
				displayName: g.displayName,
				suffix:      suffix,
				audioPath:   g.audioPath,
				videoPath:   v.path,
				ssrcs:       ssrcs,
			})
		}
	}

	for _, t := range targets {
		epToken := ndi.SanitizeToken(t.endpoint + t.suffix)
		// 1. Always produce a standard MP4 (or M4A if audio-only) playable in QuickTime / Finder.
		mp4Ext := ".mp4"
		if t.videoPath == "" {
			mp4Ext = ".m4a"
		}
		mp4Name := fmt.Sprintf("participant-%s%s", epToken, mp4Ext)
		mp4Path := filepath.Join(out, mp4Name)
		var mp4Args []string
		mp4Args = append(mp4Args, "-y", "-hide_banner", "-loglevel", "error")
		if t.videoPath != "" {
			mp4Args = append(mp4Args, "-i", t.videoPath)
		}
		if t.audioPath != "" {
			mp4Args = append(mp4Args, "-i", t.audioPath)
		}
		if t.videoPath != "" {
			mp4Args = append(mp4Args, "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
		}
		if t.audioPath != "" {
			mp4Args = append(mp4Args, "-c:a", "aac", "-ar", "48000", "-ac", "2")
		}
		mp4Args = append(mp4Args, "-movflags", "+faststart", mp4Path)
		primaryMuxName := ""
		if err := exec.Command(ffmpegBin, mp4Args...).Run(); err == nil {
			primaryMuxName = mp4Name
			if t.displayName != "" {
				namedMP4 := fmt.Sprintf("participant-%s-%s%s", ndi.SanitizeToken(t.displayName), epToken, mp4Ext)
				namedPath := filepath.Join(out, namedMP4)
				_ = os.Remove(namedPath)
				_ = os.Link(mp4Path, namedPath)
				primaryMuxName = namedMP4
			}
			slog.Info("muxed participant MP4 saved", "endpoint", t.endpoint, "display_name", t.displayName, "file", primaryMuxName)
		} else {
			slog.Warn("participant MP4 mux failed", "endpoint", t.endpoint, "error", err)
		}

		// 2. Also produce lossless WebM (VP8 + Opus) or MPEG-TS when requested.
		ext := ".webm"
		if ndiCfg.Format == "mpegts" {
			ext = ".ts"
		}
		muxName := fmt.Sprintf("participant-%s%s", epToken, ext)
		muxPath := filepath.Join(out, muxName)
		var args []string
		args = append(args, "-y", "-hide_banner", "-loglevel", "error")
		if t.videoPath != "" {
			args = append(args, "-i", t.videoPath)
		}
		if t.audioPath != "" {
			args = append(args, "-i", t.audioPath)
		}
		if ext == ".webm" {
			args = append(args, "-c", "copy", "-f", "webm", muxPath)
		} else {
			if t.videoPath != "" {
				args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
			}
			if t.audioPath != "" {
				args = append(args, "-c:a", "aac", "-ar", "48000", "-ac", "2")
			}
			args = append(args, "-f", "mpegts", muxPath)
		}
		if err := exec.Command(ffmpegBin, args...).Run(); err == nil {
			if t.displayName != "" {
				namedMux := fmt.Sprintf("participant-%s-%s%s", ndi.SanitizeToken(t.displayName), epToken, ext)
				namedPath := filepath.Join(out, namedMux)
				_ = os.Remove(namedPath)
				_ = os.Link(muxPath, namedPath)
			}
			if primaryMuxName == "" || ndiCfg.Format == "webm" || ndiCfg.Format == "mpegts" {
				primaryMuxName = muxName
			}
		}
		if primaryMuxName != "" {
			tracksMu.Lock()
			for _, ssrc := range t.ssrcs {
				if rec, ok := trackRecords[ssrc]; ok {
					rec.MuxedFile = primaryMuxName
				}
			}
			tracksMu.Unlock()
		}
	}
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
