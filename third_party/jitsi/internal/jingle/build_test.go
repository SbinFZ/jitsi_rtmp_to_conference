package jingle

import (
	"strings"
	"testing"
)

// pion emits a=ssrc only for the primary ssrc; the RTX ssrc appears solely in
// a=ssrc-group:FID. Jicofo rejects session-accept if a group references an
// unsignaled source, so writeSources must synthesize the missing one.
func TestWriteSourcesSynthesizesRTX(t *testing.T) {
	lines := []string{
		"a=ssrc-group:FID 3354541959 3299577015",
		"a=ssrc:3354541959 cname:olcrtc-ka-WMutBdYz",
		"a=ssrc:3354541959 msid:olcrtc-ka-WMutBdYz jitsi-ka-4x861OUH",
		"a=ssrc:3354541959 mslabel:olcrtc-ka-WMutBdYz",
		"a=ssrc:3354541959 label:jitsi-ka-4x861OUH",
	}

	var b strings.Builder
	writeSources(&b, lines)
	out := b.String()

	if !strings.Contains(out, `ssrc="3354541959"`) {
		t.Fatalf("missing primary source: %s", out)
	}
	if !strings.Contains(out, `ssrc="3299577015"`) {
		t.Fatalf("rtx source not synthesized: %s", out)
	}
	// synthesized source must inherit cname so jicofo accepts it
	if strings.Count(out, "olcrtc-ka-WMutBdYz") < 2 {
		t.Fatalf("rtx source did not inherit cname/msid: %s", out)
	}
}

// every ssrc referenced in a group must have a matching <source>.
func TestGroupSSRCsAllSignaled(t *testing.T) {
	lines := []string{
		"a=ssrc-group:FID 111 222",
		"a=ssrc-group:SIM 111 333 444",
		"a=ssrc:111 cname:foo",
	}

	var sb strings.Builder
	writeSources(&sb, lines)
	sources := sb.String()

	var gb strings.Builder
	writeSSRCGroups(&gb, lines)

	for _, ssrc := range []string{"111", "222", "333", "444"} {
		if !strings.Contains(sources, `ssrc="`+ssrc+`"`) {
			t.Errorf("ssrc %s referenced in group but no <source> declared: %s", ssrc, sources)
		}
	}
}

// sources with their own a=ssrc lines must not be duplicated by the synth pass.
func TestNoDuplicateSourceWhenSignaled(t *testing.T) {
	lines := []string{
		"a=ssrc-group:FID 111 222",
		"a=ssrc:111 cname:foo",
		"a=ssrc:222 cname:bar",
	}
	var b strings.Builder
	writeSources(&b, lines)
	out := b.String()
	if n := strings.Count(out, `ssrc="222"`); n != 1 {
		t.Fatalf("source 222 emitted %d times, want 1: %s", n, out)
	}
	// inherited path must not clobber the real cname
	if !strings.Contains(out, "bar") {
		t.Fatalf("real cname for 222 lost: %s", out)
	}
}
