package j

import (
	"reflect"
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/zarazaex69/j/internal/xmpp"
)

// TestConvertICEURLFormation locks down the URL rendering of single XEP-0215
// services, in particular the edge cases that used to produce URLs pion
// rejects inside NewPeerConnection ("invalid port"), such as "stun:host:"
// when the disco reply carries no port attribute.
func TestConvertICEURLFormation(t *testing.T) {
	tests := []struct {
		name string
		svc  xmpp.Service
		want string // expected single URL; empty means the entry must be skipped
	}{
		{
			name: "stun explicit port",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com", Port: "3478"},
			want: "stun:stun.example.com:3478",
		},
		{
			name: "stun empty port defaults to 3478",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com", Port: ""},
			want: "stun:stun.example.com:3478",
		},
		{
			name: "stuns empty port defaults to 5349",
			svc:  xmpp.Service{Type: "stuns", Host: "stun.example.com"},
			want: "stuns:stun.example.com:5349",
		},
		{
			name: "turn empty port defaults to 3478",
			svc:  xmpp.Service{Type: "turn", Host: "turn.example.com", Transport: "udp", Username: "user", Password: "pass"},
			want: "turn:turn.example.com:3478?transport=udp",
		},
		{
			name: "turns empty port defaults to 5349",
			svc:  xmpp.Service{Type: "turns", Host: "turn.example.com", Transport: "tcp", Username: "user", Password: "pass"},
			want: "turns:turn.example.com:5349?transport=tcp",
		},
		{
			name: "turn empty transport omits query",
			svc:  xmpp.Service{Type: "turn", Host: "turn.example.com", Port: "3478", Transport: "", Username: "user", Password: "pass"},
			want: "turn:turn.example.com:3478",
		},
		{
			name: "turn unknown transport omits query",
			svc:  xmpp.Service{Type: "turn", Host: "turn.example.com", Port: "443", Transport: "ssltcp", Username: "user", Password: "pass"},
			want: "turn:turn.example.com:443",
		},
		{
			name: "turn uppercase transport normalised",
			svc:  xmpp.Service{Type: "turn", Host: "turn.example.com", Port: "3478", Transport: "UDP", Username: "user", Password: "pass"},
			want: "turn:turn.example.com:3478?transport=udp",
		},
		{
			name: "stun ignores advertised transport",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com", Port: "3478", Transport: "udp"},
			want: "stun:stun.example.com:3478",
		},
		{
			name: "uppercase type accepted",
			svc:  xmpp.Service{Type: "STUN", Host: "stun.example.com", Port: "3478"},
			want: "stun:stun.example.com:3478",
		},
		{
			name: "surrounding whitespace trimmed",
			svc:  xmpp.Service{Type: " stun ", Host: " stun.example.com ", Port: " 3478 "},
			want: "stun:stun.example.com:3478",
		},
		{
			name: "ipv6 host bracketed with default port",
			svc:  xmpp.Service{Type: "stun", Host: "2001:db8::1"},
			want: "stun:[2001:db8::1]:3478",
		},
		{
			name: "ipv6 host bracketed with explicit port",
			svc:  xmpp.Service{Type: "turn", Host: "2001:db8::1", Port: "3478", Transport: "tcp", Username: "user", Password: "pass"},
			want: "turn:[2001:db8::1]:3478?transport=tcp",
		},
		{
			name: "ipv6 host already bracketed",
			svc:  xmpp.Service{Type: "stun", Host: "[2001:db8::1]", Port: "3478"},
			want: "stun:[2001:db8::1]:3478",
		},
		{
			name: "missing host skipped",
			svc:  xmpp.Service{Type: "stun", Host: "", Port: "3478"},
			want: "",
		},
		{
			name: "missing type skipped",
			svc:  xmpp.Service{Type: "", Host: "stun.example.com", Port: "3478"},
			want: "",
		},
		{
			name: "non ice type skipped",
			svc:  xmpp.Service{Type: "speech-to-text", Host: "stt.example.com", Port: "443"},
			want: "",
		},
		{
			name: "non numeric port skipped",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com", Port: "notaport"},
			want: "",
		},
		{
			name: "out of range port skipped",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com", Port: "70000"},
			want: "",
		},
		{
			name: "zero port skipped",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com", Port: "0"},
			want: "",
		},
		{
			name: "negative port skipped",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com", Port: "-1"},
			want: "",
		},
		{
			name: "host with url metacharacters skipped",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com?x", Port: "3478"},
			want: "",
		},
		{
			name: "colon-containing non-ipv6 host skipped",
			svc:  xmpp.Service{Type: "stun", Host: "evil:host", Port: "3478"},
			want: "",
		},
		{
			name: "host with embedded port skipped",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com:3478", Port: "3478"},
			want: "",
		},
		{
			name: "bracketed non-ipv6 host skipped",
			svc:  xmpp.Service{Type: "stun", Host: "[evil:host]", Port: "3478"},
			want: "",
		},
		{
			name: "malformed ipv6 literal skipped",
			svc:  xmpp.Service{Type: "stun", Host: "2001:db8::zz", Port: "3478"},
			want: "",
		},

		// TURN/TURNS credential gating: pion rejects any TURN URL whose
		// server has an empty username or nil credential inside
		// NewPeerConnection, so such services must be skipped entirely.
		{
			name: "turn without credentials skipped",
			svc:  xmpp.Service{Type: "turn", Host: "turn.example.com", Port: "3478", Transport: "udp"},
			want: "",
		},
		{
			name: "turns without credentials skipped",
			svc:  xmpp.Service{Type: "turns", Host: "turn.example.com", Port: "5349", Transport: "tcp"},
			want: "",
		},
		{
			name: "turn username only skipped",
			svc:  xmpp.Service{Type: "turn", Host: "turn.example.com", Port: "3478", Transport: "udp", Username: "user"},
			want: "",
		},
		{
			name: "turn password only skipped",
			svc:  xmpp.Service{Type: "turn", Host: "turn.example.com", Port: "3478", Transport: "udp", Password: "pass"},
			want: "",
		},
		{
			name: "turn with credentials kept",
			svc:  xmpp.Service{Type: "turn", Host: "turn.example.com", Port: "3478", Transport: "udp", Username: "user", Password: "pass"},
			want: "turn:turn.example.com:3478?transport=udp",
		},
		{
			name: "stun without credentials kept",
			svc:  xmpp.Service{Type: "stun", Host: "stun.example.com", Port: "3478"},
			want: "stun:stun.example.com:3478",
		},
		{
			name: "stuns without credentials kept",
			svc:  xmpp.Service{Type: "stuns", Host: "stun.example.com", Port: "5349"},
			want: "stuns:stun.example.com:5349",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := convertICE([]xmpp.Service{tc.svc})
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("convertICE(%+v) = %+v, want entry skipped", tc.svc, got)
				}
				return
			}
			if len(got) != 1 || len(got[0].URLs) != 1 || got[0].URLs[0] != tc.want {
				t.Fatalf("convertICE(%+v) = %+v, want single URL %q", tc.svc, got, tc.want)
			}
		})
	}
}

// TestConvertICEMixedServices verifies that one malformed or unauthenticated
// advertisement does not poison the rest of the list, that ordering is
// stable, and that TURN credentials survive the conversion.
func TestConvertICEMixedServices(t *testing.T) {
	services := []xmpp.Service{
		{Type: "stun", Host: "stun.example.com"},                                 // valid: default port
		{Type: "turn", Host: "", Port: "3478", Transport: "udp"},                 // skipped: no host
		{Type: "turn", Host: "anon.example.com", Port: "3478", Transport: "udp"}, // skipped: no credentials
		{Type: "turn", Host: "turn.example.com", Transport: "udp", Username: "user1", Password: "pass1"},
		{Type: "speech-to-text", Host: "stt.example.com", Port: "443"}, // skipped: not an ICE service
		{Type: "turns", Host: "turn.example.com", Port: "443", Transport: "tcp", Username: "user2", Password: "pass2"},
		{Type: "turns", Host: "partial.example.com", Port: "443", Transport: "tcp", Username: "user3"}, // skipped: no password
		{Type: "stun", Host: "broken.example.com", Port: "notaport"},                                   // skipped: unusable port
	}
	want := []ICEServer{
		{URLs: []string{"stun:stun.example.com:3478"}},
		{URLs: []string{"turn:turn.example.com:3478?transport=udp"}, Username: "user1", Credential: "pass1"},
		{URLs: []string{"turns:turn.example.com:443?transport=tcp"}, Username: "user2", Credential: "pass2"},
	}

	got := convertICE(services)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("convertICE mismatch:\n got: %+v\nwant: %+v", got, want)
	}
}

// TestConvertICEEmptyInput keeps the existing "no services" behaviour: no
// servers, so ICE falls back to host candidates.
func TestConvertICEEmptyInput(t *testing.T) {
	if got := convertICE(nil); got != nil {
		t.Fatalf("convertICE(nil) = %+v, want nil", got)
	}
}

// TestConvertICEOutputAcceptedByPion feeds converted servers through the same
// path production code uses (Session.IceConfig -> webrtc.NewPeerConnection).
// pion validates every ICE server URL inside NewPeerConnection, so this is
// the regression check for the "InvalidAccessError: invalid port" failure
// seen against deployments that advertise services without a port, and for
// the "no turn server credentials" failure caused by unauthenticated TURN
// advertisements (which must be skipped, not passed through).
func TestConvertICEOutputAcceptedByPion(t *testing.T) {
	services := []xmpp.Service{
		{Type: "stun", Host: "stun.example.com"},           // no port at all
		{Type: "stun", Host: "stun.example.com", Port: ""}, // explicit empty port
		{Type: "turn", Host: "turn.example.com", Transport: "udp", Username: "user", Password: "pass"},
		{Type: "turn", Host: "turn.example.com", Port: "443", Transport: "", Username: "user", Password: "pass"},
		{Type: "turns", Host: "turn.example.com", Transport: "wat", Username: "user", Password: "pass"},
		{Type: "stun", Host: "2001:db8::1"},
	}
	// Unauthenticated or partially authenticated TURN services: pion would
	// reject the whole configuration if these leaked through conversion.
	skipped := []xmpp.Service{
		{Type: "turn", Host: "anon.example.com", Port: "3478", Transport: "udp"},
		{Type: "turns", Host: "anon.example.com", Port: "5349", Transport: "tcp", Username: "user"},
		{Type: "turn", Host: "anon.example.com", Port: "3478", Transport: "udp", Password: "pass"},
	}

	sess := &Session{ICEServers: convertICE(append(services, skipped...))}
	cfg := sess.IceConfig()
	if len(cfg.ICEServers) != len(services) {
		t.Fatalf("expected the %d valid services to convert and %d credential-less TURN services to be skipped, got %d: %+v",
			len(services), len(skipped), len(cfg.ICEServers), cfg.ICEServers)
	}

	pc, err := webrtc.NewPeerConnection(cfg)
	if err != nil {
		t.Fatalf("pion rejected converted ICE servers: %v\nconfig: %+v", err, cfg.ICEServers)
	}
	_ = pc.Close()
}
