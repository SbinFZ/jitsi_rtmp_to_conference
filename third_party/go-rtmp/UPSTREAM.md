# Source provenance

Upstream: https://github.com/yutopp/go-rtmp — v0.0.7 (Go module zip, h1:sKKm1MVV3ANbJHZlf3Kq8ecq99y5U7XnDUDxSjuK7KU=)
License: Boost Software License 1.0 (`LICENCE.txt`, unchanged).

Copied without examples and tests. Local changes:

- `conn_state.go`: default `MaxChunkStreams` / `MaxMessageStreams` use `math.MaxInt32`
  instead of `math.MaxUint32`, which does not fit in `int` on 32-bit targets
  (linux/arm) and broke compilation. jitsi-stream sets explicit, much smaller
  limits anyway (`internal/rtmpserver`).
