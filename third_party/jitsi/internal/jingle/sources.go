package jingle

import (
	"fmt"
	"strings"
)

// SDPSourcesXML extracts `<content name="audio|video">…<description><source/></description></content>`
// blocks from a local SDP, suitable for embedding into a Jingle source-add IQ.
//
// It is a minimal version of SDPToJingleAccept that only emits the per-content source list
// without ICE/DTLS/transport (Jicofo doesn't need transport for source-add).
func SDPSourcesXML(sdp string) string {
	sections := splitSDP(sdp)
	var b strings.Builder
	for _, sec := range sections.media {
		writeSourceContent(&b, sec)
	}
	return b.String()
}

func writeSourceContent(b *strings.Builder, mediaSection string) {
	lines := splitLines(mediaSection)
	if len(lines) == 0 {
		return
	}
	first := strings.TrimPrefix(lines[0], "m=")
	parts := strings.Fields(first)
	if len(parts) < 1 {
		return
	}
	mediaType := parts[0]
	if mediaType != "audio" && mediaType != "video" {
		return
	}
	mid := getAttr(lines, "mid")
	if mid == "" {
		mid = mediaType
	}
	// only emit if there are sources
	hasSrc := false
	for _, l := range lines {
		if strings.HasPrefix(l, "a=ssrc:") {
			hasSrc = true
			break
		}
	}
	if !hasSrc {
		return
	}

	fmt.Fprintf(b, `<content creator="responder" name="%s">`, mid)
	fmt.Fprintf(b, `<description xmlns="urn:xmpp:jingle:apps:rtp:1" media="%s">`, mediaType)
	writeSources(b, lines)
	writeSSRCGroups(b, lines)
	b.WriteString("</description></content>")
}
