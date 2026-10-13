package jingle

import (
	"fmt"
	"strings"
)

// SDPToJingleAccept converts a pion-generated SDP answer into Jingle XML
// suitable to put inside <jingle action="session-accept">.
// Returns the XML string of the inner contents (without the <jingle> wrapper).
func SDPToJingleAccept(sdp string) string {
	mediaSections := splitSDP(sdp)
	var b strings.Builder

	// detect BUNDLE order
	var bundleMids []string
	for _, line := range strings.Split(mediaSections.session, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "a=group:BUNDLE ") {
			bundleMids = strings.Fields(line[len("a=group:BUNDLE "):])
		}
	}

	// extract shared transport (BUNDLE master usually carries fingerprint/ICE/candidates)
	shared := extractSharedTransport(mediaSections)

	if len(bundleMids) > 0 {
		b.WriteString(`<group xmlns="urn:xmpp:jingle:apps:grouping:0" semantics="BUNDLE">`)
		for _, m := range bundleMids {
			fmt.Fprintf(&b, `<content name="%s"/>`, m)
		}
		b.WriteString("</group>")
	}

	for _, sec := range mediaSections.media {
		writeJingleContent(&b, sec, shared)
	}

	return b.String()
}

// sharedTransport holds fingerprint/ICE creds/candidates that can be shared across
// all bundled media sections (since pion only writes them on the BUNDLE master).
type sharedTransport struct {
	ufrag       string
	pwd         string
	fingerprint string // "<hash> <value>"
	setup       string
	candidates  []string // SDP candidate lines (without leading "a=")
}

func extractSharedTransport(s sdpSplit) sharedTransport {
	var st sharedTransport

	// session-level (before m=) — pion often puts fingerprint/ice there
	sessionLines := splitLines(s.session)
	st.ufrag = getAttr(sessionLines, "ice-ufrag")
	st.pwd = getAttr(sessionLines, "ice-pwd")
	st.fingerprint = getAttr(sessionLines, "fingerprint")
	st.setup = getAttr(sessionLines, "setup")

	// media-level overrides / supplements
	for _, sec := range s.media {
		lines := splitLines(sec)
		if st.ufrag == "" {
			st.ufrag = getAttr(lines, "ice-ufrag")
		}
		if st.pwd == "" {
			st.pwd = getAttr(lines, "ice-pwd")
		}
		if st.fingerprint == "" {
			st.fingerprint = getAttr(lines, "fingerprint")
		}
		if st.setup == "" {
			st.setup = getAttr(lines, "setup")
		}
		if len(st.candidates) == 0 {
			for _, l := range lines {
				if strings.HasPrefix(l, "a=candidate:") {
					st.candidates = append(st.candidates, strings.TrimPrefix(l, "a=candidate:"))
				}
			}
		}
	}
	return st
}

type sdpSplit struct {
	session string
	media   []string
}

func splitSDP(sdp string) sdpSplit {
	parts := strings.Split(sdp, "\nm=")
	res := sdpSplit{session: parts[0]}
	for i := 1; i < len(parts); i++ {
		res.media = append(res.media, "m="+parts[i])
	}
	return res
}

func writeJingleContent(b *strings.Builder, mediaSection string, shared sharedTransport) {
	lines := splitLines(mediaSection)
	if len(lines) == 0 {
		return
	}

	// parse m= line: m=<media> <port> <proto> <fmt list>
	first := strings.TrimPrefix(lines[0], "m=")
	parts := strings.Fields(first)
	if len(parts) < 3 {
		return
	}
	mediaType := parts[0]
	proto := parts[2]
	payloadIDs := parts[3:]

	mid := getAttr(lines, "mid")
	if mid == "" {
		mid = mediaType
	}

	// senders mapping (Jingle semantics: initiator=offerer, responder=us)
	// SDP a=sendonly  → only we send → senders="responder"
	// SDP a=recvonly  → only offerer sends → senders="initiator"
	senders := "both"
	switch {
	case hasAttrFlag(lines, "sendrecv"):
		senders = "both"
	case hasAttrFlag(lines, "sendonly"):
		senders = "responder"
	case hasAttrFlag(lines, "recvonly"):
		senders = "initiator"
	case hasAttrFlag(lines, "inactive"):
		senders = "none"
	}

	fmt.Fprintf(b, `<content creator="responder" name="%s" senders="%s">`, mid, senders)

	if strings.Contains(proto, "SCTP") {
		// data channel
		port := getAttr(lines, "sctp-port")
		if port == "" {
			port = "5000"
		}
		fmt.Fprintf(b, `<description xmlns="urn:xmpp:jingle:transports:dtls-sctp:1"/>`)
		writeJingleTransport(b, lines, port, shared)
	} else {
		// audio/video
		fmt.Fprintf(b, `<description xmlns="urn:xmpp:jingle:apps:rtp:1" media="%s">`, mediaType)
		writePayloads(b, lines, payloadIDs)
		writeRTPHdrExts(b, lines)
		if hasAttrFlag(lines, "rtcp-mux") {
			b.WriteString("<rtcp-mux/>")
		}
		writeSources(b, lines)
		writeSSRCGroups(b, lines)
		b.WriteString("</description>")
		writeJingleTransport(b, lines, "", shared)
	}

	b.WriteString("</content>")
}

func writePayloads(b *strings.Builder, lines, ids []string) {
	for _, id := range ids {
		rtpmap := getAttrFor(lines, "rtpmap", id)
		// rtpmap: <id> <name>/<clock>[/channels]
		var name, clock, channels string
		if rtpmap != "" {
			rest := rtpmap
			if sp := strings.Index(rest, " "); sp >= 0 {
				rest = rest[sp+1:]
			}
			fields := strings.Split(rest, "/")
			if len(fields) > 0 {
				name = fields[0]
			}
			if len(fields) > 1 {
				clock = fields[1]
			}
			if len(fields) > 2 {
				channels = fields[2]
			}
		}
		fmt.Fprintf(b, `<payload-type id="%s"`, id)
		if name != "" {
			fmt.Fprintf(b, ` name="%s"`, name)
		}
		if clock != "" {
			fmt.Fprintf(b, ` clockrate="%s"`, clock)
		}
		if channels != "" {
			fmt.Fprintf(b, ` channels="%s"`, channels)
		}
		b.WriteString(">")

		// fmtp parameters
		fmtpVal := getAttrFor(lines, "fmtp", id)
		if fmtpVal != "" {
			// strip leading id+space
			if sp := strings.Index(fmtpVal, " "); sp >= 0 {
				fmtpVal = fmtpVal[sp+1:]
			}
			for _, kv := range strings.Split(fmtpVal, ";") {
				kv = strings.TrimSpace(kv)
				if kv == "" {
					continue
				}
				if eq := strings.Index(kv, "="); eq >= 0 {
					fmt.Fprintf(b, `<parameter name="%s" value="%s"/>`,
						xmlAttr(kv[:eq]), xmlAttr(kv[eq+1:]))
				} else {
					fmt.Fprintf(b, `<parameter value="%s"/>`, xmlAttr(kv))
				}
			}
		}

		// rtcp-fb
		for _, line := range lines {
			if !strings.HasPrefix(line, "a=rtcp-fb:") {
				continue
			}
			rest := strings.TrimPrefix(line, "a=rtcp-fb:")
			fs := strings.Fields(rest)
			if len(fs) >= 2 && fs[0] == id {
				if len(fs) >= 3 {
					fmt.Fprintf(b, `<rtcp-fb xmlns="urn:xmpp:jingle:apps:rtp:rtcp-fb:0" type="%s" subtype="%s"/>`, fs[1], fs[2])
				} else {
					fmt.Fprintf(b, `<rtcp-fb xmlns="urn:xmpp:jingle:apps:rtp:rtcp-fb:0" type="%s"/>`, fs[1])
				}
			}
		}

		b.WriteString("</payload-type>")
	}
}

func writeRTPHdrExts(b *strings.Builder, lines []string) {
	for _, line := range lines {
		if !strings.HasPrefix(line, "a=extmap:") {
			continue
		}
		rest := strings.TrimPrefix(line, "a=extmap:")
		fs := strings.Fields(rest)
		if len(fs) >= 2 {
			fmt.Fprintf(b, `<rtp-hdrext xmlns="urn:xmpp:jingle:apps:rtp:rtp-hdrext:0" id="%s" uri="%s"/>`, fs[0], fs[1])
		}
	}
}

// parseSSRCGroups returns the a=ssrc-group entries as [semantics, ssrc...] slices.
func parseSSRCGroups(lines []string) [][]string {
	var groups [][]string
	for _, line := range lines {
		if !strings.HasPrefix(line, "a=ssrc-group:") {
			continue
		}
		fs := strings.Fields(strings.TrimPrefix(line, "a=ssrc-group:"))
		if len(fs) < 2 {
			continue
		}
		groups = append(groups, fs)
	}
	return groups
}

func writeSources(b *strings.Builder, lines []string) {
	// Group ssrc lines by SSRC value
	type src struct {
		params [][2]string
	}
	sources := map[string]*src{}
	var order []string
	for _, line := range lines {
		if !strings.HasPrefix(line, "a=ssrc:") {
			continue
		}
		rest := strings.TrimPrefix(line, "a=ssrc:")
		sp := strings.Index(rest, " ")
		if sp < 0 {
			continue
		}
		ssrc := rest[:sp]
		val := rest[sp+1:]
		var name, value string
		if c := strings.Index(val, ":"); c >= 0 {
			name = val[:c]
			value = val[c+1:]
		} else {
			name = val
		}
		if _, ok := sources[ssrc]; !ok {
			sources[ssrc] = &src{}
			order = append(order, ssrc)
		}
		sources[ssrc].params = append(sources[ssrc].params, [2]string{name, value})
	}

	// Synthesize sources for ssrcs that appear only in an ssrc-group (e.g. RTX in
	// FID). Jicofo rejects session-accept if a group references an unsignaled
	// source: "An SSRC group contains an SSRC, which hasn't been signaled as a
	// source". Inherit cname/msid/mslabel/label from a declared sibling.
	for _, g := range parseSSRCGroups(lines) {
		var donor string
		for _, ssrc := range g[1:] {
			if _, ok := sources[ssrc]; ok {
				donor = ssrc
				break
			}
		}
		if donor == "" {
			continue
		}
		for _, ssrc := range g[1:] {
			if _, ok := sources[ssrc]; ok {
				continue
			}
			sources[ssrc] = &src{params: sources[donor].params}
			order = append(order, ssrc)
		}
	}

	for _, ssrc := range order {
		// Jitsi clients map mute state and receiver constraints by source name.
		// Primary and its RTX source belong to the same named track.
		name := ""
		for _, p := range sources[ssrc].params {
			if p[0] == "msid" {
				parts := strings.Fields(p[1])
				if len(parts) == 2 {
					name = parts[1]
				}
			}
		}
		fmt.Fprintf(b, `<source xmlns="urn:xmpp:jingle:apps:rtp:ssma:0" ssrc="%s"`, ssrc)
		if name != "" {
			fmt.Fprintf(b, ` name="%s"`, xmlAttr(name))
		}
		b.WriteString(">")
		for _, p := range sources[ssrc].params {
			if p[1] == "" {
				fmt.Fprintf(b, `<parameter name="%s"/>`, xmlAttr(p[0]))
			} else {
				fmt.Fprintf(b, `<parameter name="%s" value="%s"/>`, xmlAttr(p[0]), xmlAttr(p[1]))
			}
		}
		b.WriteString("</source>")
	}
}

func writeSSRCGroups(b *strings.Builder, lines []string) {
	for _, g := range parseSSRCGroups(lines) {
		fmt.Fprintf(b, `<ssrc-group xmlns="urn:xmpp:jingle:apps:rtp:ssma:0" semantics="%s">`, g[0])
		for _, ssrc := range g[1:] {
			fmt.Fprintf(b, `<source ssrc="%s"/>`, ssrc)
		}
		b.WriteString("</ssrc-group>")
	}
}

func writeJingleTransport(b *strings.Builder, lines []string, sctpPort string, shared sharedTransport) {
	rtcpMux := hasAttrFlag(lines, "rtcp-mux")
	ufrag := getAttr(lines, "ice-ufrag")
	if ufrag == "" {
		ufrag = shared.ufrag
	}
	pwd := getAttr(lines, "ice-pwd")
	if pwd == "" {
		pwd = shared.pwd
	}
	fmt.Fprintf(b, `<transport xmlns="urn:xmpp:jingle:transports:ice-udp:1" ufrag="%s" pwd="%s">`,
		xmlAttr(ufrag), xmlAttr(pwd))

	// fingerprint
	fp := getAttr(lines, "fingerprint")
	if fp == "" {
		fp = shared.fingerprint
	}
	if fp != "" {
		sp := strings.Index(fp, " ")
		if sp >= 0 {
			hash := fp[:sp]
			val := fp[sp+1:]
			setup := getAttr(lines, "setup")
			if setup == "" {
				setup = shared.setup
			}
			if setup == "" {
				setup = "active"
			}
			fmt.Fprintf(b, `<fingerprint xmlns="urn:xmpp:jingle:apps:dtls:0" hash="%s" setup="%s">%s</fingerprint>`, hash, setup, val)
		}
	}

	// candidates — prefer per-section, fall back to shared. Skip component=2 if rtcp-mux.
	wroteCandidate := false
	for _, line := range lines {
		if !strings.HasPrefix(line, "a=candidate:") {
			continue
		}
		raw := strings.TrimPrefix(line, "a=candidate:")
		if rtcpMux && candidateComponent(raw) == "2" {
			continue
		}
		writeJingleCandidate(b, raw)
		wroteCandidate = true
	}
	if !wroteCandidate {
		for _, raw := range shared.candidates {
			if rtcpMux && candidateComponent(raw) == "2" {
				continue
			}
			writeJingleCandidate(b, raw)
		}
	}

	// SCTP map
	if sctpPort != "" {
		fmt.Fprintf(b, `<sctpmap xmlns="urn:xmpp:jingle:transports:dtls-sctp:1" number="%s" protocol="webrtc-datachannel" streams="1024"/>`, sctpPort)
	}

	b.WriteString("</transport>")
}

func candidateComponent(raw string) string {
	fs := strings.Fields(raw)
	if len(fs) >= 2 {
		return fs[1]
	}
	return ""
}

func writeJingleCandidate(b *strings.Builder, raw string) {
	fs := strings.Fields(raw)
	if len(fs) < 8 {
		return
	}
	foundation := fs[0]
	component := fs[1]
	protocol := fs[2]
	priority := fs[3]
	ip := fs[4]
	port := fs[5]
	// fs[6] should be "typ"
	candType := fs[7]

	var raddr, rport, generation, tcptype string
	for i := 8; i+1 < len(fs); i += 2 {
		switch fs[i] {
		case "raddr":
			raddr = fs[i+1]
		case "rport":
			rport = fs[i+1]
		case "generation":
			generation = fs[i+1]
		case "tcptype":
			tcptype = fs[i+1]
		}
	}
	if generation == "" {
		generation = "0"
	}

	fmt.Fprintf(b, `<candidate component="%s" foundation="%s" generation="%s" id="%s" ip="%s" network="0" port="%s" priority="%s" protocol="%s" type="%s"`,
		component, foundation, generation, foundation+component, ip, port, priority, strings.ToLower(protocol), candType)
	if raddr != "" {
		fmt.Fprintf(b, ` rel-addr="%s" rel-port="%s"`, raddr, rport)
	}
	if tcptype != "" {
		fmt.Fprintf(b, ` tcptype="%s"`, tcptype)
	}
	b.WriteString("/>")
}

// helpers
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r", "")
	return strings.Split(strings.TrimSpace(s), "\n")
}

func getAttr(lines []string, key string) string {
	prefix := "a=" + key + ":"
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(l, prefix))
		}
	}
	return ""
}

func hasAttrFlag(lines []string, key string) bool {
	target := "a=" + key
	for _, l := range lines {
		if strings.TrimSpace(l) == target {
			return true
		}
	}
	return false
}

// getAttrFor finds e.g. "a=rtpmap:111 opus/48000/2" by id
func getAttrFor(lines []string, key, id string) string {
	prefix := "a=" + key + ":"
	for _, l := range lines {
		if !strings.HasPrefix(l, prefix) {
			continue
		}
		rest := strings.TrimPrefix(l, prefix)
		sp := strings.Index(rest, " ")
		if sp < 0 {
			continue
		}
		if rest[:sp] == id {
			return rest
		}
	}
	return ""
}

func xmlAttr(s string) string {
	r := strings.NewReplacer(`&`, "&amp;", `"`, "&quot;", `'`, "&apos;", `<`, "&lt;", `>`, "&gt;")
	return r.Replace(s)
}
