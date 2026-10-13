// Package rtmpserver is a small authenticated RTMP ingest (push) endpoint.
//
// Encoders (OBS, vMix, ffmpeg, mobile apps) publish to
// rtmp://host:port/app/streamkey. The app and stream key are checked during the
// RTMP connect/publish handshake (constant-time key comparison) BEFORE any
// media is accepted, and only one publisher is allowed at a time. Accepted
// streams are re-muxed to FLV and handed out through Next as an io.ReadCloser.
//
// FFmpeg's own "-listen 1" RTMP server is deliberately NOT used: it only warns
// about a wrong stream name and still ingests the media, and it stops
// listening between runs.
package rtmpserver

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirupsen/logrus"
	flv "github.com/yutopp/go-flv"
	flvtag "github.com/yutopp/go-flv/tag"
	rtmp "github.com/yutopp/go-rtmp"
	rtmpmsg "github.com/yutopp/go-rtmp/message"
)

var (
	// handshakeTimeout bounds unauthenticated connections (connect/publish).
	handshakeTimeout = 15 * time.Second
	// idleTimeout ends a publisher that stops sending, so the meeting falls
	// back to the avatar and the slot frees for a reconnecting encoder.
	idleTimeout = 30 * time.Second
)

// idleConn applies a rolling read deadline; the timeout is raised after the
// publisher authenticates.
type idleConn struct {
	net.Conn
	timeout time.Duration
	mu      sync.Mutex
}

func (c *idleConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	t := c.timeout
	c.mu.Unlock()
	_ = c.SetReadDeadline(time.Now().Add(t))
	return c.Conn.Read(p)
}

func (c *idleConn) setTimeout(t time.Duration) {
	c.mu.Lock()
	c.timeout = t
	c.mu.Unlock()
}

// Endpoint is a parsed rtmp://bind:port/app/streamkey listen URL.
type Endpoint struct {
	Addr, App, Key string
}

// Parse validates a listen URL. Plain RTMP only (no TLS server); the stream
// key is the last path element and the app is everything before it.
func Parse(raw string) (Endpoint, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "rtmp" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return Endpoint{}, fmt.Errorf("--listen needs rtmp://bind-address[:port]/app/streamkey (plain rtmp, no query)")
	}
	path := strings.Trim(u.Path, "/")
	i := strings.LastIndex(path, "/")
	if i <= 0 || i == len(path)-1 {
		return Endpoint{}, fmt.Errorf("--listen URL must include /app/streamkey")
	}
	port := u.Port()
	if port == "" {
		port = "1935"
	}
	return Endpoint{Addr: net.JoinHostPort(u.Hostname(), port), App: path[:i], Key: path[i+1:]}, nil
}

// Server accepts at most one authorized publisher at a time.
type Server struct {
	ep       Endpoint
	ln       net.Listener
	srv      *rtmp.Server
	streams  chan *stream
	busy     atomic.Bool
	closed   chan struct{}
	closeErr sync.Once
	// copied at Listen time (package vars are test hooks)
	handshake, idle time.Duration
}

// Listen binds the port immediately so configuration errors (port in use,
// bad address) fail before joining the meeting.
func Listen(ep Endpoint) (*Server, error) {
	ln, err := net.Listen("tcp", ep.Addr)
	if err != nil {
		return nil, fmt.Errorf("RTMP listen %s: %w", ep.Addr, err)
	}
	s := &Server{ep: ep, ln: ln, streams: make(chan *stream, 1), closed: make(chan struct{}),
		handshake: handshakeTimeout, idle: idleTimeout}
	quiet := logrus.New()
	quiet.SetOutput(io.Discard)
	if os.Getenv("RTMP_DEBUG") != "" {
		quiet.SetOutput(os.Stderr)
		quiet.SetLevel(logrus.DebugLevel)
	}
	s.srv = rtmp.NewServer(&rtmp.ServerConfig{
		OnConnect: func(conn net.Conn) (io.ReadWriteCloser, *rtmp.ConnConfig) {
			ic := &idleConn{Conn: conn, timeout: s.handshake}
			h := &handler{s: s, remote: conn.RemoteAddr().String(), idle: ic}
			return ic, &rtmp.ConnConfig{
				Handler: h,
				// Explicit limits: the port is reachable before authentication,
				// so library defaults (unbounded streams, 16 MB messages) would
				// let an anonymous peer exhaust memory.
				ControlState: rtmp.StreamControlStateConfig{
					DefaultBandwidthWindowSize: 6 * 1024 * 1024 / 8,
					MaxChunkSize:               64 * 1024,
					MaxChunkStreams:            64,
					MaxMessageSize:             8 * 1024 * 1024, // > a 4K keyframe
					MaxMessageStreams:          8,
				},
				Logger: quiet,
			}
		},
	})
	go func() { _ = s.srv.Serve(ln) }()
	slog.Info("RTMP ingest listening", "addr", ln.Addr().String(), "app", ep.App)
	return s, nil
}

// Addr is the bound address (useful when listening on port 0 in tests).
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Next waits for the next authorized publisher and returns its FLV stream.
// The stream ends (EOF) when the publisher disconnects.
func (s *Server) Next(ctx context.Context) (io.ReadCloser, error) {
	select {
	case st := <-s.streams:
		st.attached.Store(true)
		return st, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.closed:
		return nil, errors.New("RTMP server closed")
	}
}

func (s *Server) Close() error {
	s.closeErr.Do(func() { close(s.closed) })
	return s.srv.Close()
}

// stream is the consumer side of one publisher's FLV byte stream.
type stream struct {
	*io.PipeReader
	conn     io.Closer
	attached atomic.Bool // set by Next: FFmpeg is about to read
}

// Close drops the publisher connection too, so a stalled consumer never leaves
// a half-open ingest blocking the "one publisher" slot.
func (st *stream) Close() error {
	if st.conn != nil {
		_ = st.conn.Close()
	}
	return st.PipeReader.Close()
}

type handler struct {
	rtmp.DefaultHandler
	s      *Server
	remote string
	idle   *idleConn
	conn   io.Closer
	appOK  bool
	owns   bool
	pw     *io.PipeWriter
	enc    *flv.Encoder
	st     *stream
	// Live gating: until the consumer attaches (the meeting may still be empty)
	// media is dropped instead of buffered, so encoders never stall and the
	// meeting never receives a stale burst. Codec headers are kept and replayed.
	metadata, videoHeader, audioHeader []byte
	forwarding                         bool
	base                               uint32
}

func (h *handler) OnServe(conn *rtmp.Conn) { h.conn = conn }

func (h *handler) OnConnect(_ uint32, cmd *rtmpmsg.NetConnectionConnect) error {
	h.appOK = strings.Trim(cmd.Command.App, "/") == h.s.ep.App
	if !h.appOK {
		slog.Warn("RTMP rejected: wrong app", "remote", h.remote)
		return errors.New("unknown application")
	}
	return nil
}

func (h *handler) OnPublish(_ *rtmp.StreamContext, _ uint32, cmd *rtmpmsg.NetStreamPublish) error {
	// Some encoders append "?query" to the stream key; it is not part of the key.
	key, _, _ := strings.Cut(cmd.PublishingName, "?")
	if !h.appOK || subtle.ConstantTimeCompare([]byte(key), []byte(h.s.ep.Key)) != 1 {
		slog.Warn("RTMP rejected: wrong stream key", "remote", h.remote)
		return errors.New("unauthorized")
	}
	if !h.s.busy.CompareAndSwap(false, true) {
		slog.Warn("RTMP rejected: another publisher is live", "remote", h.remote)
		return errors.New("stream busy")
	}
	h.owns = true
	h.idle.setTimeout(h.s.idle)
	// The FLV encoder is created lazily on the first tag: creating it here would
	// write the FLV header into the pipe and block before anyone reads it.
	pr, pw := io.Pipe()
	h.pw = pw
	st := &stream{PipeReader: pr, conn: h.conn}
	h.st = st
	// Drop any stale, never-consumed stream from a publisher that already left.
	select {
	case old := <-h.s.streams:
		_ = old.Close()
	default:
	}
	h.s.streams <- st
	slog.Info("RTMP publisher accepted", "remote", h.remote)
	return nil
}

func (h *handler) OnSetDataFrame(ts uint32, data *rtmpmsg.NetStreamSetDataFrame) error {
	if h.pw == nil {
		return nil
	}
	var script flvtag.ScriptData
	if err := flvtag.DecodeScriptData(bytes.NewReader(data.Payload), &script); err != nil {
		return nil // metadata is optional
	}
	h.metadata = append([]byte(nil), data.Payload...)
	if !h.forwarding {
		return nil // replayed when forwarding starts
	}
	return h.write(&flvtag.FlvTag{TagType: flvtag.TagTypeScriptData, Timestamp: h.rel(ts), Data: &script})
}

// rel rebases timestamps so the consumer always starts at 0.
func (h *handler) rel(ts uint32) uint32 {
	if ts < h.base {
		return 0
	}
	return ts - h.base
}

// start begins forwarding at a video keyframe (or immediately for audio-only
// streams), replaying metadata and sequence headers first.
func (h *handler) start(ts uint32) error {
	h.forwarding, h.base = true, ts
	if h.metadata != nil {
		var script flvtag.ScriptData
		if flvtag.DecodeScriptData(bytes.NewReader(h.metadata), &script) == nil {
			if err := h.write(&flvtag.FlvTag{TagType: flvtag.TagTypeScriptData, Data: &script}); err != nil {
				return err
			}
		}
	}
	for _, raw := range [][]byte{h.videoHeader, h.audioHeader} {
		if raw == nil {
			continue
		}
		if err := h.replay(raw); err != nil {
			return err
		}
	}
	return nil
}

func (h *handler) replay(raw []byte) error {
	if raw[0] == 'V' {
		var v flvtag.VideoData
		if err := flvtag.DecodeVideoData(bytes.NewReader(raw[1:]), &v); err != nil {
			return err
		}
		return h.write(&flvtag.FlvTag{TagType: flvtag.TagTypeVideo, Data: &v})
	}
	var a flvtag.AudioData
	if err := flvtag.DecodeAudioData(bytes.NewReader(raw[1:]), &a); err != nil {
		return err
	}
	return h.write(&flvtag.FlvTag{TagType: flvtag.TagTypeAudio, Data: &a})
}

func (h *handler) OnAudio(ts uint32, payload io.Reader) error {
	if h.pw == nil {
		return errors.New("audio before publish")
	}
	raw, err := io.ReadAll(payload)
	if err != nil {
		return err
	}
	var audio flvtag.AudioData
	if err := flvtag.DecodeAudioData(bytes.NewReader(raw), &audio); err != nil {
		return err
	}
	header := audio.SoundFormat == flvtag.SoundFormatAAC && audio.AACPacketType == flvtag.AACPacketTypeSequenceHeader
	if header {
		h.audioHeader = append([]byte{'A'}, raw...)
		if h.forwarding {
			return h.replay(h.audioHeader) // mid-stream codec change
		}
		return nil // replayed by start
	}
	if !h.forwarding {
		// With video, forwarding starts at a keyframe; audio-only streams start
		// on the first audio frame once the consumer attached.
		if h.videoHeader != nil || !h.st.attached.Load() {
			return nil
		}
		if err := h.start(ts); err != nil {
			return err
		}
	}
	body := new(bytes.Buffer)
	if _, err := io.Copy(body, audio.Data); err != nil {
		return err
	}
	audio.Data = body
	return h.write(&flvtag.FlvTag{TagType: flvtag.TagTypeAudio, Timestamp: h.rel(ts), Data: &audio})
}

func (h *handler) OnVideo(ts uint32, payload io.Reader) error {
	if h.pw == nil {
		return errors.New("video before publish")
	}
	raw, err := io.ReadAll(payload)
	if err != nil {
		return err
	}
	var video flvtag.VideoData
	if err := flvtag.DecodeVideoData(bytes.NewReader(raw), &video); err != nil {
		return err
	}
	header := video.CodecID == flvtag.CodecIDAVC && video.AVCPacketType == flvtag.AVCPacketTypeSequenceHeader
	if header {
		h.videoHeader = append([]byte{'V'}, raw...)
		if h.forwarding {
			// Mid-stream codec change (resolution switch): forward as-is.
			return h.replay(h.videoHeader)
		}
		return nil
	}
	if !h.forwarding {
		if !h.st.attached.Load() || video.FrameType != flvtag.FrameTypeKeyFrame {
			return nil // drop until the consumer is ready and a keyframe arrives
		}
		if err := h.start(ts); err != nil {
			return err
		}
	}
	body := new(bytes.Buffer)
	if _, err := io.Copy(body, video.Data); err != nil {
		return err
	}
	video.Data = body
	return h.write(&flvtag.FlvTag{TagType: flvtag.TagTypeVideo, Timestamp: h.rel(ts), Data: &video})
}

// write blocks while the consumer (FFmpeg) is slow: TCP backpressure to the
// encoder rather than unbounded buffering. A dead consumer ends the session.
func (h *handler) write(tag *flvtag.FlvTag) error {
	if h.enc == nil {
		enc, err := flv.NewEncoder(h.pw, flv.FlagsAudio|flv.FlagsVideo)
		if err != nil {
			return fmt.Errorf("ingest consumer closed: %w", err)
		}
		h.enc = enc
	}
	if err := h.enc.Encode(tag); err != nil {
		return fmt.Errorf("ingest consumer closed: %w", err)
	}
	return nil
}

func (h *handler) OnClose() {
	if h.pw != nil {
		_ = h.pw.Close()
	}
	if h.owns {
		slog.Info("RTMP publisher disconnected", "remote", h.remote)
		h.release()
	}
}

func (h *handler) release() {
	if h.owns {
		h.owns = false
		// Allow a quick reconnect; the slot is free once the session closes.
		h.s.busy.Store(false)
	}
}
