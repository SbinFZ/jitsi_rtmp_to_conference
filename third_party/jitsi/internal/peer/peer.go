// Package peer bridges a pion *webrtc.PeerConnection with a Jicofo Jingle session.
// It is intentionally low-level: caller owns the PC and does whatever they want with
// tracks/data channels. peer just wires up SDP↔Jingle conversion and signalling.
package peer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/zarazaex69/j/internal/jingle"
	"github.com/zarazaex69/j/internal/xmpp"
)

const sourceAddAckTimeout = 5 * time.Second

// Negotiator handles a single Jingle session against Jicofo using a pion PeerConnection.
type Negotiator struct {
	XMPP                       *xmpp.Conn
	JingleStanza               string // raw <iq…><jingle action="session-initiate"…>…</jingle></iq>
	RoomJID                    string // <room>@conference.<host>
	PC                         *webrtc.PeerConnection
	OnRemote                   func(track *webrtc.TrackRemote, recv *webrtc.RTPReceiver) // optional
	OnIceConnectionStateChange func(webrtc.ICEConnectionState)

	parsed *jingle.XMLJingle
}

func (n *Negotiator) ensureParsed() error {
	if n.parsed != nil {
		return nil
	}
	if n.JingleStanza == "" {
		return fmt.Errorf("peer: JingleStanza is empty")
	}
	jng, err := jingle.ParseStanza(n.JingleStanza)
	if err != nil || jng == nil {
		return fmt.Errorf("peer: parse jingle: %w", err)
	}
	n.parsed = jng
	return nil
}

// SID returns the Jingle session id.
func (n *Negotiator) SID() string {
	if n.ensureParsed() != nil {
		return ""
	}
	return n.parsed.SID
}

// Accept performs SetRemoteDescription(offer) → CreateAnswer → SetLocalDescription(answer)
// → wait for ICE gathering complete → SendSessionAccept to Jicofo.
//
// Caller should have configured PC's transceivers/datachannels BEFORE calling Accept.
// Accept performs SetRemoteDescription(offer) → CreateAnswer → SetLocalDescription(answer)
// → wait for ICE gathering complete → SendSessionAccept to Jicofo.
//
// Caller should have configured PC's transceivers/datachannels BEFORE calling Accept.
func (n *Negotiator) Accept(ctx context.Context) error {
	if n.PC == nil || n.XMPP == nil {
		return fmt.Errorf("peer: PC and XMPP must be set")
	}
	if err := n.ensureParsed(); err != nil {
		return err
	}

	if n.OnRemote != nil {
		n.PC.OnTrack(n.OnRemote)
	}
	if n.OnIceConnectionStateChange != nil {
		n.PC.OnICEConnectionStateChange(n.OnIceConnectionStateChange)
	}

	offerSDP := jingle.JingleToSDP(n.parsed)
	pionOfferSDP := stripRemoteSourcesForPion(offerSDP)

	// Detect Plan B: if a single m=video section contains multiple SSRCs from
	// different sources, pion's UnifiedPlan will reject it. Detect and error
	// so caller can recreate PC with SDPSemanticsPlanB.
	if isPlanB(offerSDP) {
		// Try setting with current PC — if it fails, return a typed error
		err := n.PC.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  pionOfferSDP,
		})
		if err != nil && strings.Contains(err.Error(), "PlanB") {
			return fmt.Errorf("peer: remote SDP is Plan B — recreate PeerConnection with webrtc.SDPSemanticsPlanB: %w", err)
		}
		if err != nil {
			return fmt.Errorf("set remote desc: %w", err)
		}
	} else {
		if err := n.PC.SetRemoteDescription(webrtc.SessionDescription{
			Type: webrtc.SDPTypeOffer,
			SDP:  pionOfferSDP,
		}); err != nil {
			return fmt.Errorf("set remote desc: %w", err)
		}
	}

	answer, err := n.PC.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("create answer: %w", err)
	}
	if err := n.PC.SetLocalDescription(answer); err != nil {
		return fmt.Errorf("set local desc: %w", err)
	}

	// Wait for ICE gathering to complete (or timeout) so the session-accept
	// already contains all collected candidates. Late candidates after this
	// will be sent via trickle (transport-info).
	select {
	case <-webrtc.GatheringCompletePromise(n.PC):
	case <-time.After(3 * time.Second):
	case <-ctx.Done():
		return ctx.Err()
	}

	final := n.PC.LocalDescription().SDP
	jingleAccept := jingle.SDPToJingleAccept(final)

	if err := n.XMPP.SendSessionAccept(n.parsed.SID, n.parsed.Initiator, n.RoomJID, jingleAccept); err != nil {
		return err
	}

	// trickle ICE: any candidates discovered AFTER we sent session-accept
	// (e.g. late TURN allocations) get pushed via transport-info.
	n.PC.OnICECandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		js := c.ToJSON()
		raw := strings.TrimPrefix(js.Candidate, "candidate:")
		mediaName := "audio"
		if js.SDPMid != nil {
			mediaName = mediaNameForMid(*js.SDPMid)
		}
		_ = n.SendTransportInfo(mediaName, raw)
	})

	return nil
}

// mediaNameForMid resolves a mid like "0"/"1" or "audio"/"video" to "audio" or "video".
// Used for transport-info routing.
func mediaNameForMid(mid string) string {
	switch mid {
	case "0", "audio":
		return "audio"
	case "1", "video":
		return "video"
	case "2", "data":
		return "data"
	default:
		return mid
	}
}

// SendTransportInfo announces additional ICE candidates to Jicofo (trickle ICE).
// Pass an SDP-style candidate line (without the leading "a=").
func (n *Negotiator) SendTransportInfo(mediaName, candidateLine string) error {
	if err := n.ensureParsed(); err != nil {
		return err
	}
	cand := buildJingleCandidateXML(candidateLine)
	inner := fmt.Sprintf(
		`<content creator="responder" name="%s"><transport xmlns="urn:xmpp:jingle:transports:ice-udp:1">%s</transport></content>`,
		mediaName, cand)
	return n.XMPP.SendJingle(n.RoomJID+"/focus", "transport-info", n.parsed.SID, n.parsed.Initiator, inner)
}

// SendSourceAdd announces local SSRC sources to Jicofo (after adding tracks).
func (n *Negotiator) SendSourceAdd(sourcesXML string) error {
	if err := n.ensureParsed(); err != nil {
		return err
	}
	_, err := n.XMPP.SendJingleWait(
		n.RoomJID+"/focus",
		"source-add",
		n.parsed.SID,
		n.parsed.Initiator,
		sourcesXML,
		sourceAddAckTimeout,
	)
	return err
}

// SendSourceAddFromSDP convenience: extracts <source>/<ssrc-group> elements per content
// from the given local SDP (typically pc.LocalDescription().SDP after AddTrack) and
// sends them via source-add to Jicofo.
func (n *Negotiator) SendSourceAddFromSDP(sdp string) error {
	if err := n.ensureParsed(); err != nil {
		return err
	}
	xmlBody := jingle.SDPSourcesXML(sdp)
	if xmlBody == "" {
		return fmt.Errorf("peer: no <source> elements found in SDP")
	}
	return n.SendSourceAdd(xmlBody)
}

// SendSourceRemove removes previously announced sources.
func (n *Negotiator) SendSourceRemove(sourcesXML string) error {
	if err := n.ensureParsed(); err != nil {
		return err
	}
	return n.XMPP.SendJingle(n.RoomJID+"/focus", "source-remove", n.parsed.SID, n.parsed.Initiator, sourcesXML)
}

// HandleSourceAdd processes an incoming source-add stanza from Jicofo.
// It extracts the new SSRCs and updates the remote SDP so pion can accept incoming RTP.
func (n *Negotiator) HandleSourceAdd(stanza string) error {
	jng, err := jingle.ParseStanza(stanza)
	if err != nil {
		return fmt.Errorf("peer: parse source-add: %w", err)
	}

	// Get current remote description
	rd := n.PC.RemoteDescription()
	if rd == nil {
		return fmt.Errorf("peer: no remote description set")
	}
	if !sdpHasExplicitSources(rd.SDP) {
		return nil
	}

	// For each content in source-add, append SSRC lines to the matching m= section
	sdp := rd.SDP
	for _, content := range jng.Contents {
		if content.Description == nil {
			continue
		}
		media := content.Description.Media
		if media == "" {
			media = content.Name
		}

		var ssrcLines strings.Builder
		for _, sg := range content.Description.SSRCGroups {
			var ssrcs []string
			for _, s := range sg.Sources {
				ssrcs = append(ssrcs, s.SSRC)
			}
			fmt.Fprintf(&ssrcLines, "a=ssrc-group:%s %s\r\n", sg.Semantics, strings.Join(ssrcs, " "))
		}
		for _, src := range content.Description.Sources {
			for _, p := range src.Parameters {
				if p.Name == "" {
					fmt.Fprintf(&ssrcLines, "a=ssrc:%s %s\r\n", src.SSRC, p.Value)
				} else if p.Value == "" {
					fmt.Fprintf(&ssrcLines, "a=ssrc:%s %s\r\n", src.SSRC, p.Name)
				} else {
					fmt.Fprintf(&ssrcLines, "a=ssrc:%s %s:%s\r\n", src.SSRC, p.Name, p.Value)
				}
			}
			if len(src.Parameters) == 0 {
				fmt.Fprintf(&ssrcLines, "a=ssrc:%s cname:source-add-%s\r\n", src.SSRC, src.SSRC)
			}
		}

		if ssrcLines.Len() == 0 {
			continue
		}

		sdp = insertSSRCIntoSection(sdp, media, ssrcLines.String())
	}

	if sdp == rd.SDP {
		return nil // nothing changed
	}

	// Increment o= version to make pion accept the new offer
	sdp = bumpSDPVersion(sdp)

	if err := n.PC.SetRemoteDescription(webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  sdp,
	}); err != nil {
		return fmt.Errorf("peer: set remote desc after source-add: %w", err)
	}

	answer, err := n.PC.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("peer: create answer after source-add: %w", err)
	}
	return n.PC.SetLocalDescription(answer)
}

// insertSSRCIntoSection appends ssrcLines at the end of the m=<mediaType> section.
func insertSSRCIntoSection(sdp, mediaType, ssrcLines string) string {
	marker := "m=" + mediaType
	idx := strings.Index(sdp, marker)
	if idx == -1 {
		return sdp
	}
	// Find end of this m= section (next m= or end of string)
	rest := sdp[idx+len(marker):]
	nextM := strings.Index(rest, "\r\nm=")
	var insertPos int
	if nextM == -1 {
		// Last section — insert before trailing \r\n if any
		insertPos = len(sdp)
		if strings.HasSuffix(sdp, "\r\n") {
			insertPos -= 2
		}
	} else {
		insertPos = idx + len(marker) + nextM + 2 // after the \r\n before next m=
	}
	return sdp[:insertPos] + ssrcLines + sdp[insertPos:]
}

// bumpSDPVersion increments the session version in the o= line.
func bumpSDPVersion(sdp string) string {
	// o=- 0 2 IN IP4 0.0.0.0 -> increment the version number (second number)
	lines := strings.Split(sdp, "\r\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "o=") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				// parts[2] is the session version
				var ver int
				_, _ = fmt.Sscanf(parts[2], "%d", &ver)
				parts[2] = fmt.Sprintf("%d", ver+1)
				lines[i] = strings.Join(parts, " ")
			}
			break
		}
	}
	return strings.Join(lines, "\r\n")
}

// stripRemoteSourcesForPion removes remote SSRC attributes before handing the
// offer to pion. Jitsi/JVB can forward RTP for a newly announced source before
// the matching source-add stanza has been applied locally; if the m-section
// contains any explicit SSRC, pion drops that RTP and never fires OnTrack.
// Leaving the media section source-less lets pion bind tracks dynamically.
func stripRemoteSourcesForPion(sdp string) string {
	lines := strings.Split(sdp, "\r\n")
	out := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, "a=ssrc:") || strings.HasPrefix(line, "a=ssrc-group:") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\r\n")
}

func sdpHasExplicitSources(sdp string) bool {
	for _, line := range strings.Split(sdp, "\r\n") {
		if strings.HasPrefix(line, "a=ssrc:") || strings.HasPrefix(line, "a=ssrc-group:") {
			return true
		}
	}
	return false
}

// Terminate sends session-terminate to gracefully end the Jingle session.
// Fire-and-forget; for graceful tear-down where you need to know Jicofo has
// actually accepted the stanza before closing the websocket, prefer
// TerminateWait.
func (n *Negotiator) Terminate(reason string) error {
	if err := n.ensureParsed(); err != nil {
		return err
	}
	if reason == "" {
		reason = "success"
	}
	inner := fmt.Sprintf(`<reason><%s/></reason>`, reason)
	return n.XMPP.SendJingle(n.RoomJID+"/focus", "session-terminate", n.parsed.SID, n.parsed.Initiator, inner)
}

// TerminateWait is like Terminate but waits for Jicofo's <iq type="result"/>
// (or error) before returning. This matches lib-jitsi-meet's JingleSessionPC
// terminate path which uses sendIQ with a callback rather than a one-shot
// send. Waiting matters because the websocket teardown that usually follows
// would otherwise race the round-trip and Jicofo would only free the bridge
// slot after its idle timeout — exactly the "ghost participant" symptom we
// hit on back-to-back reconnects to the same MUC.
func (n *Negotiator) TerminateWait(reason string, timeout time.Duration) error {
	if err := n.ensureParsed(); err != nil {
		return err
	}
	if reason == "" {
		reason = "success"
	}
	id := n.XMPP.NextID()
	iq := fmt.Sprintf(
		`<iq to="%s" type="set" id="%s" xmlns="jabber:client"><jingle xmlns="urn:xmpp:jingle:1" action="session-terminate" sid="%s" initiator="%s" responder="%s"><reason><%s/></reason></jingle></iq>`,
		n.RoomJID+"/focus", id, n.parsed.SID, n.parsed.Initiator, n.XMPP.JID(), reason)
	_, err := n.XMPP.SendIQWait(iq, id, timeout)
	return err
}

// buildJingleCandidateXML converts an SDP candidate line to <candidate .../>
func buildJingleCandidateXML(raw string) string {
	raw = strings.TrimPrefix(raw, "a=")
	raw = strings.TrimPrefix(raw, "candidate:")
	fs := strings.Fields(raw)
	if len(fs) < 8 {
		return ""
	}
	foundation := fs[0]
	component := fs[1]
	protocol := fs[2]
	priority := fs[3]
	ip := fs[4]
	port := fs[5]
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
	out := fmt.Sprintf(
		`<candidate component="%s" foundation="%s" generation="%s" id="%s" ip="%s" network="0" port="%s" priority="%s" protocol="%s" type="%s"`,
		component, foundation, generation, foundation+component, ip, port, priority, strings.ToLower(protocol), candType)
	if raddr != "" {
		out += fmt.Sprintf(` rel-addr="%s" rel-port="%s"`, raddr, rport)
	}
	if tcptype != "" {
		out += fmt.Sprintf(` tcptype="%s"`, tcptype)
	}
	return out + "/>"
}

// isPlanB detects Plan B SDP: multiple a=ssrc lines with different cname values
// in a single m=video section (Jicofo sends this when other participants have video).
func isPlanB(sdp string) bool {
	inVideo := false
	cnames := map[string]struct{}{}
	for _, line := range strings.Split(sdp, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "m=video") {
			inVideo = true
			cnames = map[string]struct{}{}
		} else if strings.HasPrefix(line, "m=") {
			if inVideo && len(cnames) > 1 {
				return true
			}
			inVideo = false
		}
		if inVideo && strings.HasPrefix(line, "a=ssrc:") && strings.Contains(line, "cname:") {
			parts := strings.SplitN(line, "cname:", 2)
			if len(parts) == 2 {
				cnames[strings.TrimSpace(parts[1])] = struct{}{}
			}
		}
	}
	return inVideo && len(cnames) > 1
}

// IsPlanBError returns true if the error from Accept indicates a Plan B SDP mismatch.
func IsPlanBError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "Plan B")
}
