# Multi-arch image (linux/amd64, linux/arm64, linux/arm/v7).
# Go cross-compiles natively on the build platform (no QEMU for the Go build);
# only the Debian runtime layer (FFmpeg) is per-architecture.
#   docker build -t jitsi-stream .
#   docker buildx build --platform linux/amd64,linux/arm64 -t jitsi-stream .
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS build
ARG TARGETOS=linux TARGETARCH=amd64 TARGETVARIANT=""
ARG VERSION=dev
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party/jitsi ./third_party/jitsi
COPY third_party/go-rtmp ./third_party/go-rtmp
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
      go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/jitsi-stream ./cmd/jitsi-stream && \
    GOOS=$TARGETOS GOARCH=$TARGETARCH GOARM=${TARGETVARIANT#v} \
      go build -trimpath -ldflags="-s -w -X main.version=$VERSION" -o /out/jitsi-receive ./cmd/jitsi-receive

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg ca-certificates && \
    rm -rf /var/lib/apt/lists/* && useradd --uid 10001 --create-home streamer
COPY LICENSE THIRD_PARTY.md third_party/NOTICES.txt /usr/local/share/jitsi-stream/
COPY --from=build /out/jitsi-stream /out/jitsi-receive /usr/local/bin/
USER 10001:10001
WORKDIR /home/streamer
# Only used with --listen (RTMP push ingest); not required for pull inputs.
EXPOSE 1935/tcp
ENTRYPOINT ["/usr/local/bin/jitsi-stream"]
