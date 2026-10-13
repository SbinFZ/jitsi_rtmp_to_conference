package media

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// Sink feeds one published WebRTC track. Every FFmpeg run starts with a new
// random SSRC, sequence number and timestamp; Sink rewrites them into a single
// continuous RTP stream so the input can restart (reconnect, next RTMP
// publisher) without leaving and rejoining the meeting. Receivers see a
// timestamp gap equal to the wall-clock pause, never a backwards jump.
type Sink struct {
	track   *webrtc.TrackLocalStaticRTP
	clock   float64
	Packets atomic.Uint64

	mu       sync.Mutex
	started  bool
	inSSRC   uint32
	seqOff   uint16
	tsOff    uint32
	lastSeq  uint16
	lastTS   uint32
	lastWall time.Time
	writeRTP func(*rtp.Packet) error
	now      func() time.Time
}

func NewSink(track *webrtc.TrackLocalStaticRTP, clockRate uint32) *Sink {
	if track == nil {
		return nil
	}
	return &Sink{track: track, clock: float64(clockRate), writeRTP: track.WriteRTP, now: time.Now}
}

func (s *Sink) Write(p *rtp.Packet) error {
	s.mu.Lock()
	now := s.now()
	if !s.started || p.SSRC != s.inSSRC {
		if s.started {
			gap := uint32(now.Sub(s.lastWall).Seconds() * s.clock)
			if gap == 0 {
				gap = 1
			}
			s.seqOff = s.lastSeq + 1 - p.SequenceNumber
			s.tsOff = s.lastTS + gap - p.Timestamp
		}
		s.started, s.inSSRC = true, p.SSRC
	}
	p.SequenceNumber += s.seqOff
	p.Timestamp += s.tsOff
	s.lastSeq, s.lastTS, s.lastWall = p.SequenceNumber, p.Timestamp, now
	s.mu.Unlock()
	if err := s.writeRTP(p); err != nil {
		return err
	}
	s.Packets.Add(1)
	return nil
}

func (s *Sink) Count() uint64 {
	if s == nil {
		return 0
	}
	return s.Packets.Load()
}
