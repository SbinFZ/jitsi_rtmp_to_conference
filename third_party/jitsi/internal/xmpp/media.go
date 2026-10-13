package xmpp

import (
	"encoding/json"
	"fmt"
)

// SetMediaPresence publishes the actual local source state; audio/video tracks
// must use Nick()+"-a0" / Nick()+"-v0" as their Track IDs.
func (c *Conn) SetMediaPresence(displayName string, audio, video bool) error {
	sources := map[string]any{
		c.nick + "-a0": map[string]any{"muted": !audio},
		c.nick + "-v0": map[string]any{"muted": !video, "videoType": "camera"},
	}
	data, _ := json.Marshal(sources)
	return c.Send(fmt.Sprintf(`<presence to="%s@%s/%s" xmlns="jabber:client"><botType xmlns="http://jitsi.org/jitmeet">poltergeist</botType><nick xmlns="http://jabber.org/protocol/nick">%s</nick><audiomuted xmlns="http://jitsi.org/jitmeet/audio">%t</audiomuted><videomuted xmlns="http://jitsi.org/jitmeet/video">%t</videomuted><SourceInfo>%s</SourceInfo><jitsi_participant_codecList>vp8</jitsi_participant_codecList></presence>`, xmlEscape(c.room), xmlEscape(c.mucDomain), xmlEscape(c.nick), xmlEscape(displayName), !audio, !video, xmlEscape(string(data))))
}
