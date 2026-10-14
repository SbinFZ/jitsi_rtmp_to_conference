package conference

import (
	"strconv"
	"strings"
	"sync"

	j "github.com/zarazaex69/j"
)

// TrackSourceInfo describes the Jitsi participant and media stream associated with an RTP SSRC.
type TrackSourceInfo struct {
	SSRC        uint32 `json:"ssrc"`
	PrimarySSRC uint32 `json:"primary_ssrc,omitempty"`
	IsRTX       bool   `json:"is_rtx,omitempty"`
	Endpoint    string `json:"endpoint"`
	DisplayName string `json:"display_name,omitempty"`
	SourceName  string `json:"source_name,omitempty"`
	MSID        string `json:"msid,omitempty"`
	VideoType   string `json:"video_type,omitempty"`
	Media       string `json:"media,omitempty"`
}

// ParticipantRegistry tracks SSRC -> participant metadata across Jingle session-initiate,
// source-add, source-remove, and MUC presence updates.
type ParticipantRegistry struct {
	mu       sync.RWMutex
	sources  map[uint32]TrackSourceInfo
	nicks    map[string]string
	onUpdate func(TrackSourceInfo)
}

func NewParticipantRegistry(onUpdate func(TrackSourceInfo)) *ParticipantRegistry {
	return &ParticipantRegistry{
		sources:  make(map[uint32]TrackSourceInfo),
		nicks:    make(map[string]string),
		onUpdate: onUpdate,
	}
}

// SyncNicks merges a map of endpoint -> displayName into the registry and refreshes existing sources.
func (r *ParticipantRegistry) SyncNicks(nicks map[string]string) {
	if len(nicks) == 0 {
		return
	}
	var updated []TrackSourceInfo
	r.mu.Lock()
	for ep, name := range nicks {
		if name == "" {
			continue
		}
		r.nicks[ep] = name
		for ssrc, info := range r.sources {
			if info.Endpoint == ep && info.DisplayName != name {
				info.DisplayName = name
				r.sources[ssrc] = info
				updated = append(updated, info)
			}
		}
	}
	cb := r.onUpdate
	r.mu.Unlock()
	if cb != nil {
		for _, info := range updated {
			cb(info)
		}
	}
}

// IngestStanza inspects an incoming XMPP stanza (presence or Jingle IQ) and updates SSRC/nick mappings.
func (r *ParticipantRegistry) IngestStanza(stanza string, session *j.Session) {
	if session != nil && session.Conn != nil {
		r.SyncNicks(session.Conn.OccupantDisplayNames())
	}
	if !strings.Contains(stanza, "<jingle") {
		return
	}
	parsed := j.ParseJingleStanza(stanza)
	if parsed == nil {
		return
	}
	switch parsed.Action {
	case "session-initiate", "source-add", "description-info":
		r.addParsedSources(parsed)
	case "source-remove":
		r.removeParsedSources(parsed)
	}
}

func (r *ParticipantRegistry) addParsedSources(parsed *j.ParsedJingle) {
	if parsed == nil {
		return
	}
	rtxToPrimary := make(map[uint32]uint32)
	for _, grp := range parsed.SSRCGroups {
		if strings.EqualFold(grp.Semantics, "FID") && len(grp.SSRCs) >= 2 {
			p, err1 := strconv.ParseUint(grp.SSRCs[0], 10, 32)
			rtx, err2 := strconv.ParseUint(grp.SSRCs[1], 10, 32)
			if err1 == nil && err2 == nil {
				rtxToPrimary[uint32(rtx)] = uint32(p)
			}
		}
	}

	all := make([]j.Source, 0, len(parsed.AudioSources)+len(parsed.VideoSources))
	all = append(all, parsed.AudioSources...)
	all = append(all, parsed.VideoSources...)

	var updated []TrackSourceInfo
	r.mu.Lock()
	for _, src := range all {
		v, err := strconv.ParseUint(strings.TrimSpace(src.SSRC), 10, 32)
		if err != nil {
			continue
		}
		ssrc := uint32(v)
		primary, isRTX := rtxToPrimary[ssrc]
		ep := strings.TrimSpace(src.Owner)
		info := TrackSourceInfo{
			SSRC:        ssrc,
			PrimarySSRC: primary,
			IsRTX:       isRTX,
			Endpoint:    ep,
			DisplayName: r.nicks[ep],
			SourceName:  src.SourceName,
			MSID:        src.Name,
			VideoType:   src.VideoType,
			Media:       src.Media,
		}
		r.sources[ssrc] = info
		updated = append(updated, info)
	}
	cb := r.onUpdate
	r.mu.Unlock()
	if cb != nil {
		for _, info := range updated {
			cb(info)
		}
	}
}

func (r *ParticipantRegistry) removeParsedSources(parsed *j.ParsedJingle) {
	if parsed == nil {
		return
	}
	all := make([]j.Source, 0, len(parsed.AudioSources)+len(parsed.VideoSources))
	all = append(all, parsed.AudioSources...)
	all = append(all, parsed.VideoSources...)
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, src := range all {
		v, err := strconv.ParseUint(strings.TrimSpace(src.SSRC), 10, 32)
		if err == nil {
			delete(r.sources, uint32(v))
		}
	}
}

// Lookup returns the TrackSourceInfo for an SSRC if known.
func (r *ParticipantRegistry) Lookup(ssrc uint32) (TrackSourceInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	info, ok := r.sources[ssrc]
	if ok && info.DisplayName == "" && info.Endpoint != "" {
		info.DisplayName = r.nicks[info.Endpoint]
	}
	return info, ok
}

// Snapshot returns a copy of all known SSRC mappings.
func (r *ParticipantRegistry) Snapshot() map[uint32]TrackSourceInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[uint32]TrackSourceInfo, len(r.sources))
	for k, v := range r.sources {
		if v.DisplayName == "" && v.Endpoint != "" {
			v.DisplayName = r.nicks[v.Endpoint]
		}
		out[k] = v
	}
	return out
}
