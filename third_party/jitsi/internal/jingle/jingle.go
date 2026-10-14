package jingle

import (
	"strings"
)

type Candidate struct {
	Component  string
	Foundation string
	Generation string
	ID         string
	IP         string
	Port       string
	Priority   string
	Protocol   string
	Type       string
	RelAddr    string
	RelPort    string
}

type Source struct {
	SSRC       string
	Name       string // msid value, e.g. "422ab3db 422ab3db-v0"
	Label      string
	Owner      string // endpoint ID from <ssrc-info owner="..."/> or inferred from msid / source name
	SourceName string // <source name="422ab3db-v0"/>
	VideoType  string // <source videoType="camera"/> or "desktop"
	Media      string // "audio" or "video"
}

type SSRCGroup struct {
	Semantics string
	SSRCs     []string
	Media     string
}

type DataChannel struct {
	Port     string
	Protocol string
}

type Parsed struct {
	Action       string
	SID          string
	Initiator    string
	SDP          string
	Jingle       *XMLJingle
	Candidates   []Candidate
	AudioSources []Source
	VideoSources []Source
	SSRCGroups   []SSRCGroup
	DataChannel  *DataChannel
	ColibriWS    string // bridge WebSocket URL (modern Jitsi data channel)
}

func Parse(raw string) *Parsed {
	p := &Parsed{}

	p.SID = extractAttr(raw, "sid")
	p.Initiator = extractAttr(raw, "initiator")

	jng, err := ParseStanza(raw)
	if err != nil || jng == nil {
		return p
	}
	p.Jingle = jng
	p.Action = jng.Action
	p.SDP = JingleToSDP(jng)

	for _, content := range jng.Contents {
		if content.Transport != nil {
			if p.ColibriWS == "" {
				for _, ws := range content.Transport.WebSockets {
					if ws.URL != "" {
						p.ColibriWS = ws.URL
						break
					}
				}
			}
			for _, c := range content.Transport.Candidates {
				p.Candidates = append(p.Candidates, Candidate{
					Component:  c.Component,
					Foundation: c.Foundation,
					Generation: c.Generation,
					ID:         c.ID,
					IP:         c.IP,
					Port:       c.Port,
					Priority:   c.Priority,
					Protocol:   c.Protocol,
					Type:       c.Type,
					RelAddr:    c.RelAddr,
					RelPort:    c.RelPort,
				})
			}
		}

		if content.Description != nil {
			media := content.Description.Media
			if media == "" {
				media = content.Name
			}
			for _, src := range content.Description.Sources {
				var name, label string
				for _, p := range src.Parameters {
					switch p.Name {
					case "msid":
						name = p.Value
					case "label":
						label = p.Value
					}
				}
				owner := ""
				if src.SSRCInfo != nil {
					owner = strings.TrimSpace(src.SSRCInfo.Owner)
					if idx := strings.LastIndexByte(owner, '/'); idx >= 0 {
						owner = owner[idx+1:]
					}
				}
				if owner == "" && name != "" {
					if fields := strings.Fields(name); len(fields) > 0 {
						owner = fields[0]
						if idx := strings.IndexByte(owner, '-'); idx > 0 {
							owner = owner[:idx]
						}
					}
				}
				if owner == "" && src.Name != "" {
					if idx := strings.IndexByte(src.Name, '-'); idx > 0 {
						owner = src.Name[:idx]
					}
				}
				s := Source{
					SSRC:       src.SSRC,
					Name:       name,
					Label:      label,
					Owner:      owner,
					SourceName: src.Name,
					VideoType:  src.VideoType,
					Media:      media,
				}
				switch media {
				case "audio":
					p.AudioSources = append(p.AudioSources, s)
				case "video":
					p.VideoSources = append(p.VideoSources, s)
				}
			}
			for _, grp := range content.Description.SSRCGroups {
				var ssrcs []string
				for _, gs := range grp.Sources {
					if gs.SSRC != "" {
						ssrcs = append(ssrcs, gs.SSRC)
					}
				}
				if len(ssrcs) > 0 {
					p.SSRCGroups = append(p.SSRCGroups, SSRCGroup{
						Semantics: grp.Semantics,
						SSRCs:     ssrcs,
						Media:     media,
					})
				}
			}
		}

		if content.Name == "data" || (content.Description != nil && content.Description.SCTPMap != nil) ||
			(content.Transport != nil && content.Transport.SCTPMap != nil) {
			dc := &DataChannel{}
			if content.Description != nil && content.Description.SCTPMap != nil {
				dc.Port = content.Description.SCTPMap.Number
				if dc.Port == "" {
					dc.Port = content.Description.SCTPMap.Port
				}
				dc.Protocol = content.Description.SCTPMap.Protocol
			}
			if content.Transport != nil && content.Transport.SCTPMap != nil {
				if dc.Port == "" {
					dc.Port = content.Transport.SCTPMap.Number
					if dc.Port == "" {
						dc.Port = content.Transport.SCTPMap.Port
					}
				}
				if dc.Protocol == "" {
					dc.Protocol = content.Transport.SCTPMap.Protocol
				}
			}
			p.DataChannel = dc
		}
	}

	return p
}

func extractAttr(s, attr string) string {
	key := attr + `="`
	i := strings.Index(s, key)
	if i == -1 {
		key = attr + `='`
		i = strings.Index(s, key)
		if i == -1 {
			return ""
		}
	}
	i += len(key)
	end := strings.IndexByte(s[i:], s[i-1])
	if end == -1 {
		return ""
	}
	return s[i : i+end]
}
