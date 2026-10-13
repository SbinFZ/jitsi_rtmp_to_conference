package media

import "github.com/pion/rtp"

// Reorder is a bounded diagnostic jitter buffer, including sequence wraparound
// and duplicate suppression. It is not used on the publishing path.
type Reorder struct {
	pending map[uint16]*rtp.Packet
	next    uint16
	started bool
	limit   int16
}

func NewReorder(limit int16) *Reorder {
	return &Reorder{pending: make(map[uint16]*rtp.Packet), limit: limit}
}
func (r *Reorder) Push(p *rtp.Packet) []*rtp.Packet {
	if !r.started {
		r.next = p.SequenceNumber
		r.started = true
	}
	distance := int16(p.SequenceNumber - r.next)
	if distance < 0 {
		return nil
	}
	var out []*rtp.Packet
	// Skip holes only after the bounded reordering window has elapsed.
	for distance >= r.limit {
		if q := r.pending[r.next]; q != nil {
			out = append(out, q)
			delete(r.pending, r.next)
		}
		r.next++
		distance--
	}
	if _, exists := r.pending[p.SequenceNumber]; !exists {
		r.pending[p.SequenceNumber] = p
	}
	for {
		q := r.pending[r.next]
		if q == nil {
			break
		}
		out = append(out, q)
		delete(r.pending, r.next)
		r.next++
	}
	return out
}
