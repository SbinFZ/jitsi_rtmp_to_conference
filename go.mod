module jitsi-stream

go 1.24.0

require (
	github.com/pion/rtcp v1.2.16
	github.com/pion/rtp v1.10.1
	github.com/pion/webrtc/v4 v4.2.12
	github.com/sirupsen/logrus v1.7.0
	github.com/yutopp/go-flv v0.3.1
	github.com/yutopp/go-rtmp v0.0.7
	github.com/zarazaex69/j v0.0.0
	golang.org/x/net v0.50.0
	golang.org/x/sys v0.41.0
)

require (
	github.com/coder/websocket v1.8.14 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/hashicorp/errwrap v1.1.0 // indirect
	github.com/hashicorp/go-multierror v1.1.0 // indirect
	github.com/mitchellh/mapstructure v1.4.1 // indirect
	github.com/pion/datachannel v1.6.0 // indirect
	github.com/pion/dtls/v3 v3.1.2 // indirect
	github.com/pion/ice/v4 v4.2.5 // indirect
	github.com/pion/interceptor v0.1.44 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/sctp v1.9.5 // indirect
	github.com/pion/sdp/v3 v3.0.18 // indirect
	github.com/pion/srtp/v3 v3.0.10 // indirect
	github.com/pion/stun/v3 v3.1.2 // indirect
	github.com/pion/transport/v4 v4.0.1 // indirect
	github.com/pion/turn/v5 v5.0.3 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	github.com/yutopp/go-amf0 v0.1.0 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/text v0.34.0 // indirect
	golang.org/x/time v0.14.0 // indirect
)

replace github.com/zarazaex69/j => ./third_party/jitsi

replace github.com/yutopp/go-rtmp => ./third_party/go-rtmp
