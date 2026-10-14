package conference

import (
	"testing"
)

func TestParticipantRegistrySessionInitiateAndNickUpdate(t *testing.T) {
	var updates []TrackSourceInfo
	reg := NewParticipantRegistry(func(info TrackSourceInfo) {
		updates = append(updates, info)
	})

	reg.SyncNicks(map[string]string{
		"422ab3db": "Alice Stream",
	})

	stanza := `<iq type='set' from='room@conference.example.com/focus' to='user@example.com/res' id='j1'>
  <jingle xmlns='urn:xmpp:jingle:1' action='session-initiate' sid='sid1'>
    <content name='audio'>
      <description xmlns='urn:xmpp:jingle:apps:rtp:1' media='audio'>
        <source xmlns='urn:xmpp:jingle:apps:rtp:ssma:0' ssrc='111111' name='422ab3db-a0'>
          <ssrc-info xmlns='http://jitsi.org/jitmeet' owner='room@conference.example.com/422ab3db'/>
          <parameter name='msid' value='422ab3db-stream 422ab3db-a0'/>
        </source>
      </description>
    </content>
    <content name='video'>
      <description xmlns='urn:xmpp:jingle:apps:rtp:1' media='video'>
        <source xmlns='urn:xmpp:jingle:apps:rtp:ssma:0' ssrc='222222' name='422ab3db-v0' videoType='camera'>
          <ssrc-info xmlns='http://jitsi.org/jitmeet' owner='room@conference.example.com/422ab3db'/>
          <parameter name='msid' value='422ab3db-stream 422ab3db-v0'/>
        </source>
        <source xmlns='urn:xmpp:jingle:apps:rtp:ssma:0' ssrc='222223' name='422ab3db-v0-rtx'>
          <ssrc-info xmlns='http://jitsi.org/jitmeet' owner='room@conference.example.com/422ab3db'/>
        </source>
        <ssrc-group xmlns='urn:xmpp:jingle:apps:rtp:ssma:0' semantics='FID'>
          <source ssrc='222222'/>
          <source ssrc='222223'/>
        </ssrc-group>
      </description>
    </content>
  </jingle>
</iq>`

	reg.IngestStanza(stanza, nil)

	audioInfo, ok := reg.Lookup(111111)
	if !ok {
		t.Fatalf("expected audio SSRC 111111 to be registered")
	}
	if audioInfo.Endpoint != "422ab3db" || audioInfo.DisplayName != "Alice Stream" || audioInfo.Media != "audio" {
		t.Fatalf("unexpected audio info: %+v", audioInfo)
	}

	videoInfo, ok := reg.Lookup(222222)
	if !ok {
		t.Fatalf("expected video SSRC 222222 to be registered")
	}
	if videoInfo.Endpoint != "422ab3db" || videoInfo.VideoType != "camera" || videoInfo.IsRTX {
		t.Fatalf("unexpected video info: %+v", videoInfo)
	}

	rtxInfo, ok := reg.Lookup(222223)
	if !ok {
		t.Fatalf("expected RTX SSRC 222223 to be registered")
	}
	if !rtxInfo.IsRTX || rtxInfo.PrimarySSRC != 222222 {
		t.Fatalf("expected 222223 to be marked as RTX for 222222, got %+v", rtxInfo)
	}

	// Test source-add for a second participant whose nick arrives later.
	sourceAdd := `<iq type='set' from='room@conference.example.com/focus' id='j2'>
  <jingle xmlns='urn:xmpp:jingle:1' action='source-add' sid='sid1'>
    <content name='video'>
      <description xmlns='urn:xmpp:jingle:apps:rtp:1' media='video'>
        <source xmlns='urn:xmpp:jingle:apps:rtp:ssma:0' ssrc='333333' name='99887766-v1' videoType='desktop'>
          <parameter name='msid' value='99887766-screen 99887766-v1'/>
        </source>
      </description>
    </content>
  </jingle>
</iq>`
	reg.IngestStanza(sourceAdd, nil)
	deskInfo, ok := reg.Lookup(333333)
	if !ok || deskInfo.Endpoint != "99887766" || deskInfo.VideoType != "desktop" {
		t.Fatalf("unexpected desktop source-add info: %+v (ok=%v)", deskInfo, ok)
	}

	reg.SyncNicks(map[string]string{"99887766": "Bob Screen"})
	deskInfo, _ = reg.Lookup(333333)
	if deskInfo.DisplayName != "Bob Screen" {
		t.Fatalf("expected updated display name 'Bob Screen', got %q", deskInfo.DisplayName)
	}

	// Test source-remove.
	sourceRemove := `<iq type='set' from='room@conference.example.com/focus' id='j3'>
  <jingle xmlns='urn:xmpp:jingle:1' action='source-remove' sid='sid1'>
    <content name='video'>
      <description xmlns='urn:xmpp:jingle:apps:rtp:1' media='video'>
        <source xmlns='urn:xmpp:jingle:apps:rtp:ssma:0' ssrc='333333'/>
      </description>
    </content>
  </jingle>
</iq>`
	reg.IngestStanza(sourceRemove, nil)
	if _, ok := reg.Lookup(333333); ok {
		t.Fatalf("expected SSRC 333333 to be removed after source-remove")
	}
	if len(updates) == 0 {
		t.Fatalf("expected OnSourceUpdate callbacks to have fired")
	}
}
