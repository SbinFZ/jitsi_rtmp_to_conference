# Default: reproducible source build. --target prebuilt uses local dist/linux/ binaries.
FROM golang:1.25-bookworm AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY CGO_ENABLED=0 GOMAXPROCS=2
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/jitsi ./third_party/jitsi
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -trimpath -ldflags="-s -w" -o /out/jitsi-stream ./cmd/jitsi-stream && \
    go build -trimpath -ldflags="-s -w" -o /out/jitsi-receive ./cmd/jitsi-receive

FROM debian:bookworm-slim AS runtime
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg ca-certificates && \
    rm -rf /var/lib/apt/lists/* && useradd --uid 10001 --create-home streamer
COPY LICENSE THIRD_PARTY.md third_party/NOTICES.txt /usr/local/share/jitsi-stream/
USER 10001:10001
WORKDIR /home/streamer
ENTRYPOINT ["/usr/local/bin/jitsi-stream"]

FROM runtime AS prebuilt
COPY dist/linux/jitsi-stream dist/linux/jitsi-receive /usr/local/bin/

FROM runtime AS release
COPY --from=build /out/jitsi-stream /out/jitsi-receive /usr/local/bin/
