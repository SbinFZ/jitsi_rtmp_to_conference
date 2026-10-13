package media

import (
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestSinkContinuityAcrossRestarts(t *testing.T) {
	var out []*rtp.Packet
	now := time.Unix(100, 0)
	s := &Sink{clock: 90000, now: func() time.Time { return now }, writeRTP: func(p *rtp.Packet) error {
		c := *p
		out = append(out, &c)
		return nil
	}}
	send := func(ssrc uint32, seq uint16, ts uint32) {
		if err := s.Write(&rtp.Packet{Header: rtp.Header{SSRC: ssrc, SequenceNumber: seq, Timestamp: ts}}); err != nil {
			t.Fatal(err)
		}
	}
	send(1, 65534, 4294967000) // first run, near wraparound
	send(1, 65535, 4294967200)
	now = now.Add(2 * time.Second) // FFmpeg restarted after a 2 s gap
	send(2, 10, 5000)
	send(2, 11, 8000)
	wantSeq := []uint16{65534, 65535, 0, 1}
	for i, p := range out {
		if p.SequenceNumber != wantSeq[i] {
			t.Fatalf("packet %d seq=%d want %d", i, p.SequenceNumber, wantSeq[i])
		}
	}
	if gap := out[2].Timestamp - out[1].Timestamp; gap != 180000 {
		t.Fatalf("restart timestamp gap=%d want 2s*90kHz", gap)
	}
	if d := out[3].Timestamp - out[2].Timestamp; d != 3000 {
		t.Fatalf("intra-run spacing changed: %d", d)
	}
	if s.Count() != 4 {
		t.Fatal(s.Count())
	}
	var nilSink *Sink
	if nilSink.Count() != 0 {
		t.Fatal("nil sink count")
	}
}
