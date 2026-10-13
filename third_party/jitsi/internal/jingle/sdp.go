package jingle

import (
	"fmt"
	"strings"
)

// JingleToSDP converts a parsed Jingle session-initiate to a WebRTC-compatible SDP offer.
func JingleToSDP(j *XMLJingle) string {
	var b strings.Builder

	// session-level
	b.WriteString("v=0\r\n")
	b.WriteString("o=- 0 2 IN IP4 0.0.0.0\r\n")
	b.WriteString("s=-\r\n")
	b.WriteString("t=0 0\r\n")

	// BUNDLE group
	if j.Group != nil && strings.EqualFold(j.Group.Semantics, "BUNDLE") {
		var mids []string
		for _, gi := range j.Group.Contents {
			mids = append(mids, gi.Name)
		}
		fmt.Fprintf(&b, "a=group:BUNDLE %s\r\n", strings.Join(mids, " "))
	} else {
		var mids []string
		for _, c := range j.Contents {
			mids = append(mids, c.Name)
		}
		fmt.Fprintf(&b, "a=group:BUNDLE %s\r\n", strings.Join(mids, " "))
	}

	b.WriteString("a=msid-semantic: WMS *\r\n")

	// each m-line
	for _, c := range j.Contents {
		writeContent(&b, c)
	}
	return b.String()
}

func writeContent(b *strings.Builder, c XMLContent) {
	desc := c.Description
	transport := c.Transport

	mediaType := ""
	if desc != nil {
		mediaType = desc.Media
	}
	if mediaType == "" {
		// data channel content has no media in description; use content name
		if c.Name == "data" || (desc != nil && desc.SCTPMap != nil) || (transport != nil && transport.SCTPMap != nil) {
			mediaType = "application"
		}
	}

	// build m= line
	if mediaType == "application" {
		// data channel
		port := "5000"
		if desc != nil && desc.SCTPMap != nil {
			port = desc.SCTPMap.Number
			if port == "" {
				port = desc.SCTPMap.Port
			}
		}
		if transport != nil && transport.SCTPMap != nil {
			if transport.SCTPMap.Number != "" {
				port = transport.SCTPMap.Number
			} else if transport.SCTPMap.Port != "" {
				port = transport.SCTPMap.Port
			}
		}
		fmt.Fprintf(b, "m=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\n")
		_ = port
	} else {
		// audio or video — collect payload IDs
		var ids []string
		if desc != nil {
			for _, p := range desc.Payloads {
				ids = append(ids, p.ID)
			}
		}
		if len(ids) == 0 {
			ids = []string{"0"}
		}
		fmt.Fprintf(b, "m=%s 9 UDP/TLS/RTP/SAVPF %s\r\n", mediaType, strings.Join(ids, " "))
	}

	b.WriteString("c=IN IP4 0.0.0.0\r\n")
	b.WriteString("a=rtcp:9 IN IP4 0.0.0.0\r\n")
	fmt.Fprintf(b, "a=mid:%s\r\n", c.Name)

	// senders
	switch strings.ToLower(c.Senders) {
	case "both", "":
		b.WriteString("a=sendrecv\r\n")
	case "initiator":
		b.WriteString("a=sendonly\r\n")
	case "responder":
		b.WriteString("a=recvonly\r\n")
	case "none":
		b.WriteString("a=inactive\r\n")
	}

	// ICE
	if transport != nil {
		if transport.Ufrag != "" {
			fmt.Fprintf(b, "a=ice-ufrag:%s\r\n", transport.Ufrag)
		}
		if transport.Pwd != "" {
			fmt.Fprintf(b, "a=ice-pwd:%s\r\n", transport.Pwd)
		}
		b.WriteString("a=ice-options:trickle\r\n")

		// fingerprint
		if transport.Fingerprint != nil {
			fp := strings.TrimSpace(transport.Fingerprint.Value)
			fmt.Fprintf(b, "a=fingerprint:%s %s\r\n", transport.Fingerprint.Hash, fp)
			setup := transport.Fingerprint.Setup
			if setup == "" {
				setup = "actpass"
			}
			fmt.Fprintf(b, "a=setup:%s\r\n", setup)
		}

		// candidates
		for _, ca := range transport.Candidates {
			line := fmt.Sprintf("a=candidate:%s %s %s %s %s %s typ %s",
				ca.Foundation, ca.Component, strings.ToLower(ca.Protocol),
				ca.Priority, ca.IP, ca.Port, ca.Type)
			if ca.RelAddr != "" {
				line += fmt.Sprintf(" raddr %s rport %s", ca.RelAddr, ca.RelPort)
			}
			if ca.Generation != "" {
				line += fmt.Sprintf(" generation %s", ca.Generation)
			}
			if ca.TCPType != "" {
				line += fmt.Sprintf(" tcptype %s", ca.TCPType)
			}
			b.WriteString(line)
			b.WriteString("\r\n")
		}
	}

	if mediaType == "application" {
		// SCTP
		port := "5000"
		if desc != nil && desc.SCTPMap != nil && desc.SCTPMap.Number != "" {
			port = desc.SCTPMap.Number
		}
		if transport != nil && transport.SCTPMap != nil && transport.SCTPMap.Number != "" {
			port = transport.SCTPMap.Number
		}
		fmt.Fprintf(b, "a=sctp-port:%s\r\n", port)
		b.WriteString("a=max-message-size:262144\r\n")
		return
	}

	// rtcp-mux
	if desc != nil && desc.RTCPMux != nil {
		b.WriteString("a=rtcp-mux\r\n")
	}

	// RTP header extensions
	if desc != nil {
		for _, ext := range desc.RTPHdrExts {
			fmt.Fprintf(b, "a=extmap:%s %s\r\n", ext.ID, ext.URI)
		}
	}

	// payload types
	if desc != nil {
		for _, pt := range desc.Payloads {
			channels := pt.Channels
			if channels == "" {
				channels = ""
			} else {
				channels = "/" + channels
			}
			clock := pt.Clockrate
			if clock == "" {
				clock = "90000"
			}
			fmt.Fprintf(b, "a=rtpmap:%s %s/%s%s\r\n", pt.ID, pt.Name, clock, channels)

			// rtcp-fb
			for _, fb := range pt.RTCPFB {
				if fb.Subtype != "" {
					fmt.Fprintf(b, "a=rtcp-fb:%s %s %s\r\n", pt.ID, fb.Type, fb.Subtype)
				} else {
					fmt.Fprintf(b, "a=rtcp-fb:%s %s\r\n", pt.ID, fb.Type)
				}
			}

			// fmtp parameters
			if len(pt.Parameters) > 0 {
				var parts []string
				for _, p := range pt.Parameters {
					if p.Name != "" {
						parts = append(parts, fmt.Sprintf("%s=%s", p.Name, p.Value))
					} else {
						parts = append(parts, p.Value)
					}
				}
				fmt.Fprintf(b, "a=fmtp:%s %s\r\n", pt.ID, strings.Join(parts, ";"))
			}
		}
	}

	// SSRC groups
	if desc != nil {
		for _, sg := range desc.SSRCGroups {
			var ssrcs []string
			for _, s := range sg.Sources {
				ssrcs = append(ssrcs, s.SSRC)
			}
			fmt.Fprintf(b, "a=ssrc-group:%s %s\r\n", sg.Semantics, strings.Join(ssrcs, " "))
		}

		// SSRCs
		for _, s := range desc.Sources {
			for _, p := range s.Parameters {
				if p.Name == "" {
					fmt.Fprintf(b, "a=ssrc:%s %s\r\n", s.SSRC, p.Value)
				} else if p.Value == "" {
					fmt.Fprintf(b, "a=ssrc:%s %s\r\n", s.SSRC, p.Name)
				} else {
					fmt.Fprintf(b, "a=ssrc:%s %s:%s\r\n", s.SSRC, p.Name, p.Value)
				}
			}
		}
	}
}
