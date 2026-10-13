package jingle

import "encoding/xml"

// XMLJingle is the full Jingle stanza structure for parsing session-initiate.
type XMLJingle struct {
	XMLName   xml.Name     `xml:"jingle"`
	Action    string       `xml:"action,attr"`
	Initiator string       `xml:"initiator,attr"`
	Responder string       `xml:"responder,attr"`
	SID       string       `xml:"sid,attr"`
	Contents  []XMLContent `xml:"content"`
	Group     *XMLGroup    `xml:"group"`
}

type XMLGroup struct {
	Semantics string         `xml:"semantics,attr"`
	Contents  []XMLGroupItem `xml:"content"`
}

type XMLGroupItem struct {
	Name string `xml:"name,attr"`
}

type XMLContent struct {
	Creator     string          `xml:"creator,attr"`
	Name        string          `xml:"name,attr"`
	Senders     string          `xml:"senders,attr"`
	Description *XMLDescription `xml:"description"`
	Transport   *XMLTransport   `xml:"transport"`
}

type XMLDescription struct {
	XMLName    xml.Name         `xml:"description"`
	XMLNS      string           `xml:"xmlns,attr"`
	Media      string           `xml:"media,attr"`
	Maxptime   string           `xml:"maxptime,attr"`
	Payloads   []XMLPayloadType `xml:"payload-type"`
	Sources    []XMLSource      `xml:"source"`
	SSRCGroups []XMLSSRCGroup   `xml:"ssrc-group"`
	RTCPMux    *struct{}        `xml:"rtcp-mux"`
	RTPHdrExts []XMLRTPHdrExt   `xml:"rtp-hdrext"`
	SCTPMap    *XMLSCTPMap      `xml:"sctpmap"`
}

type XMLPayloadType struct {
	ID         string            `xml:"id,attr"`
	Name       string            `xml:"name,attr"`
	Clockrate  string            `xml:"clockrate,attr"`
	Channels   string            `xml:"channels,attr"`
	Parameters []XMLParameter    `xml:"parameter"`
	RTCPFB     []XMLRTCPFeedback `xml:"rtcp-fb"`
}

type XMLRTCPFeedback struct {
	XMLNS   string `xml:"xmlns,attr"`
	Type    string `xml:"type,attr"`
	Subtype string `xml:"subtype,attr"`
}

type XMLParameter struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type XMLSource struct {
	XMLNS      string         `xml:"xmlns,attr"`
	SSRC       string         `xml:"ssrc,attr"`
	Name       string         `xml:"name,attr"`
	VideoType  string         `xml:"videoType,attr"`
	Parameters []XMLParameter `xml:"parameter"`
}

type XMLSSRCGroup struct {
	XMLNS     string    `xml:"xmlns,attr"`
	Semantics string    `xml:"semantics,attr"`
	Sources   []XMLSSRC `xml:"source"`
}

type XMLSSRC struct {
	SSRC string `xml:"ssrc,attr"`
}

type XMLRTPHdrExt struct {
	XMLNS string `xml:"xmlns,attr"`
	ID    string `xml:"id,attr"`
	URI   string `xml:"uri,attr"`
}

type XMLTransport struct {
	XMLNS       string          `xml:"xmlns,attr"`
	Ufrag       string          `xml:"ufrag,attr"`
	Pwd         string          `xml:"pwd,attr"`
	Candidates  []XMLCandidate  `xml:"candidate"`
	Fingerprint *XMLFingerprint `xml:"fingerprint"`
	SCTPMap     *XMLSCTPMap     `xml:"sctpmap"`
	WebSockets  []XMLWebSocket  `xml:"web-socket"`
}

type XMLWebSocket struct {
	XMLNS string `xml:"xmlns,attr"`
	URL   string `xml:"url,attr"`
}

type XMLCandidate struct {
	Component  string `xml:"component,attr"`
	Foundation string `xml:"foundation,attr"`
	Generation string `xml:"generation,attr"`
	ID         string `xml:"id,attr"`
	IP         string `xml:"ip,attr"`
	Network    string `xml:"network,attr"`
	Port       string `xml:"port,attr"`
	Priority   string `xml:"priority,attr"`
	Protocol   string `xml:"protocol,attr"`
	Type       string `xml:"type,attr"`
	RelAddr    string `xml:"rel-addr,attr"`
	RelPort    string `xml:"rel-port,attr"`
	TCPType    string `xml:"tcptype,attr"`
}

type XMLFingerprint struct {
	XMLNS    string `xml:"xmlns,attr"`
	Hash     string `xml:"hash,attr"`
	Setup    string `xml:"setup,attr"`
	Required string `xml:"required,attr"`
	Value    string `xml:",chardata"`
}

type XMLSCTPMap struct {
	XMLNS    string `xml:"xmlns,attr"`
	Number   string `xml:"number,attr"`
	Port     string `xml:"port,attr"`
	Protocol string `xml:"protocol,attr"`
	Streams  string `xml:"streams,attr"`
}

// ParseStanza extracts the Jingle element from a full IQ stanza.
func ParseStanza(raw string) (*XMLJingle, error) {
	type IQ struct {
		XMLName xml.Name  `xml:"iq"`
		Jingle  XMLJingle `xml:"jingle"`
	}
	var iq IQ
	if err := xml.Unmarshal([]byte(raw), &iq); err != nil {
		return nil, err
	}
	return &iq.Jingle, nil
}
