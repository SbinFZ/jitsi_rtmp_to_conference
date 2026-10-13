package j

import (
	"context"
	"fmt"
	"github.com/zarazaex69/j/internal/jingle"
)

// WaitMedia populates the initial Jingle offer after JoinMUC. Empty rooms can
// legitimately wait until a second participant joins; no dummy attendee needed.
func (s *Session) WaitMedia(ctx context.Context) error {
	raw, err := s.Conn.WaitJingle(ctx)
	if err != nil {
		return fmt.Errorf("wait jingle (another participant must join): %w", err)
	}
	parsed := jingle.Parse(raw)
	if parsed == nil || parsed.SDP == "" {
		return fmt.Errorf("empty Jingle offer")
	}
	s.SDP = parsed.SDP
	s.Candidates = parsed.Candidates
	s.DataChannel = parsed.DataChannel
	s.AudioSSRC = parsed.AudioSources
	s.VideoSSRC = parsed.VideoSources
	s.ColibriWS = parsed.ColibriWS
	s.jingleSID = parsed.SID
	s.initiator = parsed.Initiator
	return nil
}
