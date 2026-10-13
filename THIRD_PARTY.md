# Third-party components

- Fixed-version Jitsi Go signalling client: `github.com/zarazaex69/j`, commit
  `6129578a8a498e6ca59e8c66b63b876892279b12`. Source and original permissive
  ZARAZAEX ANY DO license are retained under `third_party/jitsi/`.
- Pion WebRTC v4.2.12 and its RTP/RTCP/ICE/DTLS/interceptor packages: mostly MIT,
  with dependency notices in their upstream source. Full versions/checksums are
  recorded in `go.mod` / `go.sum` (and the signalling fork's module files).
- `github.com/coder/websocket`: MIT; original source available through the pinned
  Go module dependency.
- FFmpeg is invoked as a separate executable, not linked into the Go binary.
  The Docker image uses Debian's FFmpeg package with its corresponding GPL/LGPL
  codec libraries. Image redistribution must comply with those packages'
  licenses; notices live in `/usr/share/doc/` in the runtime image. Source can be
  obtained from Debian's source package repositories and https://ffmpeg.org/.
- ShowMeMac was reviewed for architecture only. No GPL ShowMeMac source was copied
  into this project. No browser-based Jitsi Meet JavaScript was copied.

The MIT license at the project root applies to the newly authored project code,
not to separately licensed third-party packages.
