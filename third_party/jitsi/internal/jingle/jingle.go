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
	SSRC  string
	Name  string
	Label string
}

type DataChannel struct {
	Port     string
	Protocol string
}

type Parsed struct {
	SID          string
	Initiator    string
	SDP          string
	Jingle       *XMLJingle
	Candidates   []Candidate
	AudioSources []Source
	VideoSources []Source
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
				s := Source{SSRC: src.SSRC, Name: name, Label: label}
				switch media {
				case "audio":
					p.AudioSources = append(p.AudioSources, s)
				case "video":
					p.VideoSources = append(p.VideoSources, s)
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
