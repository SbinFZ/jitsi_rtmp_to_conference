# Third-party components

Full license texts of every Go module linked into the binaries are in
`third_party/NOTICES.txt` (shipped as `NOTICES.txt` in each release package;
regenerate with `make notices`). Versions and checksums: `go.mod` / `go.sum`.

| Component | License | Use |
|---|---|---|
| `github.com/zarazaex69/j` @ `6129578a8a498e6ca59e8c66b63b876892279b12` | ZARAZAEX ANY DO (permissive) | Jitsi XMPP/Jingle signalling; modified fork in `third_party/jitsi/` (changes: `UPSTREAM.md`) |
| Pion WebRTC v4 and sub-packages (`rtp`, `rtcp`, `ice`, `dtls`, `srtp`, `sctp`, …) | MIT | WebRTC transport |
| `github.com/coder/websocket` | ISC | XMPP / Colibri WebSockets |
| `github.com/yutopp/go-rtmp`, `go-flv`, `go-amf0` | Boost Software License 1.0 | RTMP push ingest (`--listen`) |
| `github.com/sirupsen/logrus`, `github.com/pkg/errors`, `github.com/mitchellh/mapstructure` | MIT / BSD-2-Clause / MIT | go-rtmp dependencies |
| `github.com/hashicorp/go-multierror`, `github.com/hashicorp/errwrap` | MPL-2.0 | go-rtmp dependencies, unmodified. MPL is file-level copyleft: the corresponding source is the unmodified upstream module at the version in `go.sum` |
| `golang.org/x/{crypto,net,sys,time}`, Go standard library | BSD-3-Clause | |
| `github.com/google/uuid`, `github.com/wlynxg/anet` | BSD-3-Clause | Pion dependencies |

FFmpeg is invoked as a separate executable and is not linked into the Go
binaries. Release archives do not contain FFmpeg. The Docker image installs
Debian's FFmpeg package (GPL/LGPL codec libraries); its notices are in
`/usr/share/doc/` inside the image, and source is available from Debian's
source repositories and https://ffmpeg.org/.

ShowMeMac was reviewed for architecture only; no ShowMeMac (GPL) source was
copied. No browser Jitsi Meet JavaScript was copied.

The MIT license at the repository root covers the newly authored project code,
not separately licensed third-party packages.
