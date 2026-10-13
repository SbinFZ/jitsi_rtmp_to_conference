# Source provenance

Upstream: https://github.com/zarazaex69/j
Pinned commit: 6129578a8a498e6ca59e8c66b63b876892279b12
License: LICENSE in this directory (ZARAZAEX ANY DO; modification/distribution permitted).

Local fork for jitsi-stream. Only library and unit tests copied (not upstream CLI/e2e tests).
Local changes:
- `Session.WaitMedia`: join and wait phases separated; empty-room waiting is explicit.
- Publish actual mute/source state and VP8 codec list; safely escaped display names and UTF-8-independent stats ID.
- Name Jingle sources using the track ID/MSID (including RTX siblings) so Jitsi source state and receiver constraints match.
- Correct initiator/responder direction when translating Jingle offers to SDP.
- Bound WS writes to 10s; acknowledge session-accept by IQ and reject IQ errors without logging raw credentials.
- Expose transport Done channel so application stanza drainers exit rather than leak.
- Mark presence `botType=poltergeist` and reject non-focus (P2P) Jingle so a peer offer cannot replace or terminate the JVB session.
- `Config.RequireExistingRoom` / `Conn.RoomOccupants`: check the room exists via disco#info BEFORE focus allocation (which creates rooms); handles Prosody `muc_hide_all` (`forbidden` = exists, `item-not-found` = absent).
- Remove unsupported JSON-source and TCC capability advertisements (XML source signalling, Pion NACK, periodic keyframes used instead).

Compatibility and security regression tests added under internal/xmpp and internal/jingle. The protocol is tested against stable-10314; upstream updates must be revalidated.
