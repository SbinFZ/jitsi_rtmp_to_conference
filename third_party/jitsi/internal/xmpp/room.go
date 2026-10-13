package xmpp

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrRoomNotFound means the MUC room does not exist: nobody has started the
// meeting yet, or the last participant left and the conference was destroyed.
var ErrRoomNotFound = errors.New("meeting room does not exist")

var occupantsField = regexp.MustCompile(`var=['"]muc#roominfo_occupants['"][^>]*>\s*<value>(\d+)</value>`)

// RoomOccupants queries disco#info of the room WITHOUT joining or allocating a
// conference (focus allocation would create the room). It returns the number
// of occupants including the focus, or ErrRoomNotFound.
func (c *Conn) RoomOccupants(room string, timeout time.Duration) (int, error) {
	id := c.NextID()
	iq := fmt.Sprintf(`<iq to="%s@%s" type="get" id="%s" xmlns="jabber:client"><query xmlns="http://jabber.org/protocol/disco#info"/></iq>`,
		xmlEscape(room), xmlEscape(c.mucDomain), id)
	reply, err := c.SendIQWait(iq, id, timeout)
	if strings.Contains(reply, "item-not-found") {
		return 0, ErrRoomNotFound
	}
	// Prosody mod_muc_hide_all (docker-jitsi-meet default) answers non-occupants
	// with <forbidden/> for rooms that EXIST (details hidden), while unknown
	// rooms still yield <item-not-found/>. Existence is known; count is not.
	if strings.Contains(reply, "<forbidden") {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	m := occupantsField.FindStringSubmatch(reply)
	if m == nil {
		// Room exists but the server does not publish an occupant count.
		return -1, nil
	}
	n, _ := strconv.Atoi(m[1])
	return n, nil
}
