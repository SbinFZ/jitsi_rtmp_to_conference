# jitsi_rtmp_to_conference

<p align="center">
  <b>Publish RTMP, HTTP-FLV, RTSP, HLS, or local media files into Jitsi Meet as a native participant with video, audio, and display name.</b>
</p>

<p align="center">
  <a href="#english">English</a> | <a href="#中文说明">中文说明</a>
</p>

<p align="center">
  <a href="https://github.com/SbinFZ/jitsi_rtmp_to_conference/actions/workflows/ci.yml"><img src="https://github.com/SbinFZ/jitsi_rtmp_to_conference/actions/workflows/ci.yml/badge.svg" alt="CI Status"></a>
  <a href="https://github.com/SbinFZ/jitsi_rtmp_to_conference/releases"><img src="https://img.shields.io/github/v/release/SbinFZ/jitsi_rtmp_to_conference?include_prereleases" alt="Latest Release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/SbinFZ/jitsi_rtmp_to_conference" alt="License: MIT"></a>
</p>

<p align="center">
  📖 <b>Featured in the official <a href="https://jitsi.github.io/handbook/docs/community/third-party-software/">Jitsi Handbook (Third-Party Software)</a></b>
</p>

---

<a name="english"></a>
## English

**jitsi-stream** is a standalone, lightweight command-line converter written in **Go + Pion WebRTC + FFmpeg**. It connects to any standard Jitsi Meet instance via native XMPP/Jingle signalling, negotiates ICE/DTLS, and streams transcoded VP8 video and Opus audio to the Jitsi Videobridge (JVB).

No browsers, headless Chromium, Electron, X11/Wayland display servers, or virtual camera kernel modules are required. It runs cleanly inside minimal Linux containers (Docker / Kubernetes), bare-metal servers (systemd), macOS, and Windows.

```
RTMP Push (OBS, vMix, encoders) ─┐
RTMP Pull / HTTP-FLV /           ├─→ jitsi-stream ──WebRTC (VP8+Opus)──→ Jitsi JVB ──→ Meeting Participants
RTSP / HLS / Local Files         ─┘        └── Native XMPP/Jingle Signalling
```

### Key Features

| Input Mode | CLI Example | Status |
|---|---|---|
| **RTMP Push Server** (OBS/encoder pushes to tool) | `--listen --input rtmp://0.0.0.0:1935/live/<secret>` | ✅ Verified, constant-time token auth |
| **RTMP Pull** (pull from media server) | `--input rtmp://source.example.com/live/stream` | ✅ Verified, supports RTMPS/RTMPT |
| **HTTP(S)-FLV Live Stream** | `--input https://source.example.com/live/stream.flv` | ✅ Verified, auto system proxy support |
| **Local Video Files** (MP4, MKV, FLV...) | `--input /data/sample.mp4` | ✅ Real-time playback (`-re`) |
| **Standard Input (pipe)** | `ffmpeg ... \| jitsi-stream --room r --input -` | ✅ Direct stdin ingestion |
| **RTSP / HLS / SRT / UDP** | `--input rtsp://camera:554/live` | ⚠️ Implemented via FFmpeg |
| **NodeMedia Signed URLs** | Set `NODEMEDIA_AUTH_KEY`, automatic signature generation | ✅ Automatic token refresh on reconnect |

- **Meeting Presence & Continuity**: Input restarts (network hiccup, encoder reconnect) do NOT drop the participant from the meeting. RTP sequence numbers and timestamps remain contiguous. When input stream drops, the participant smoothly transitions to a muted avatar rather than freezing.
- **Meeting-Driven Exponential Backoff**: By default, checks if a meeting exists before creating one. If absent, retries with binary exponential backoff (`1s, 2s, 4s, 8s, 16s`) up to 5 times before exiting cleanly (configurable via `--meeting-retries`).
- **One-Time URL Recovery**: Supports `--input-cmd` to run a command fetching a fresh signed URL before each (re)connection attempt.
- **Auto Proxy Detection**: On macOS (`scutil`) and Windows (Registry), automatically reads system proxy settings with bypass rules for HTTP-FLV streams.
- **Security & Privacy**: Built-in RTMP server validates App and Stream Key before accepting frames; credentials and signatures in URLs are automatically redacted in logs.

### Quick Start

#### 1. Binary Download

Download pre-compiled binaries from [GitHub Releases](https://github.com/SbinFZ/jitsi_rtmp_to_conference/releases):

| OS | Architecture | Package |
|---|---|---|
| **Linux** | x86_64 / ARM64 / ARMv7 | `linux_amd64.tar.gz`, `linux_arm64.tar.gz`, `linux_armv7.tar.gz` |
| **macOS** | Apple Silicon / Intel | `darwin_arm64.tar.gz`, `darwin_amd64.tar.gz` |
| **Windows** | x86_64 / ARM64 | `windows_amd64.zip`, `windows_arm64.zip` |

> **Requirement**: [FFmpeg](https://ffmpeg.org/) (with `libvpx` and `libopus`) must be installed on your host system.

#### 2. Running Examples

```bash
# Pull an RTMP live stream into meeting "team-sync"
jitsi-stream --host meet.example.com --room team-sync --name "Live Stream" \
  --input "rtmp://live.example.com/app/stream"

# Run as local RTMP server for OBS (listen mode)
jitsi-stream --host meet.example.com --room team-sync --name "OBS Stream" \
  --listen --input "rtmp://0.0.0.0:1935/live/MySecretKey123"

# Pull HTTP-FLV with custom resolution and bitrate
jitsi-stream --host meet.example.com --room team-sync \
  --input "https://cdn.example.com/live/feed.flv" \
  --height 1080 --fps 30 --bitrate 3000

# Record & broadcast all remote participants' streams via NDI (zero GStreamer dependency)
# Supports simultaneous Camera + Screen Share (desktop) streams per participant
jitsi-receive --host meet.example.com --room team-sync \
  --out recordings --ndi --ndi-prefix "Jitsi"
```

#### 3. Docker & Docker Compose

Pre-built multi-arch images (`linux/amd64`, `linux/arm64`, `linux/arm/v7`):

```bash
# Pull stream via Docker
docker run --rm --init --network host \
  ghcr.io/sbinfz/jitsi_rtmp_to_conference:latest \
  --host meet.example.com --room team-sync \
  --input "rtmp://live.example.com/app/stream"

# Using Docker Compose
cd deploy
cp env.example .env     # Edit JITSI_HOST, JITSI_ROOM, STREAM_INPUT
docker compose up -d    # Launches pull service
```

---

<a name="中文说明"></a>
## 中文说明

**jitsi-stream** 是一个基于 **Go + Pion WebRTC + FFmpeg** 的轻量级纯命令行转换工具。它能将外部的音视频流（RTMP、HTTP-FLV、RTSP、HLS 或本地视频文件）作为**真实与会者**推入 Jitsi Meet 会议室中，会议内其他成员无需任何插件即可正常收看与收听。

无需安装无头浏览器、Chromium、Electron、X11/Wayland 桌面环境或虚拟摄像头驱动。纯命令行设计，极低资源开销，可直接运行在 Linux 服务器、Docker 容器、Kubernetes、macOS 以及 Windows。

### 核心特性

- **多协议输入**：
  - **RTMP 推流服务（Listen 模式）**：内置轻量级 RTMP 服务端，支持 OBS、vMix 或手机推流软件直接推流到本工具，握手阶段进行常量时间密钥鉴权。
  - **RTMP / HTTP-FLV 拉流**：从直播流媒体服务拉取，支持单次签名、时间戳防盗链重签。
  - **本地文件 & 管道输入**：按真实时间戳播放本地 MP4/FLV/MKV 视频文件，或接收标准输入管道 (`pipe:0`)。
  - **RTSP / HLS / SRT**：通过底层 FFmpeg 解封装适配。
- **会议连续性保证**：推流端断开重连或网络短暂抖动时，**与会者不会退出会议**，音视频序列号和时间戳平滑递增。当无流输入时，自动切换为静音头像状态。
- **会议不存在重试机制**：默认在入会前检测会议是否已有人开启，若会议室不存在，采用**二进制指数退避（1s, 2s, 4s, 8s, 16s）重试 5 次**，超时后优雅退出（可通过 `--meeting-retries 0` 恢复为主动建会模式）。
- **一次性签名防失效**：针对断流后一次性 Token 失效的问题，支持 `--input-cmd` 在每次（重）连接前动态执行脚本获取最新推流地址；遇到不可刷新的 401 立即退出并输出明确指引。
- **全自动代理路由**：在 macOS (`scutil`) 和 Windows (注册表) 下能自动识别系统代理及白名单规则，解决“浏览器可看但命令行拉不到流”的常见网络问题。
- **安全与合规**：日志自动脱敏敏感参数（Token、Key、Password 等）；镜像默认非 root、只读文件系统运行。

### 常用命令示例

```bash
# 1. 作为接收端接收 OBS 推流（推流地址：rtmp://<IP>:1935/live/MyKey）
jitsi-stream --host meet.example.com --room my-room --name "OBS直播" \
  --listen --input "rtmp://0.0.0.0:1935/live/MyKey"

# 2. 拉取公网 HTTP-FLV 直播流
jitsi-stream --host meet.example.com --room my-room --name "监控流" \
  --input "https://stream.example.com/live/camera1.flv"

# 3. 循环播放本地宣传视频到会议室
jitsi-stream --host meet.example.com --room my-room --name "宣传片" \
  --input "/data/videos/promo.mp4"

# 4. 按参会者录制所有人的音视频流并通过 NDI 广播（零 GStreamer 依赖，支持同一用户摄像头+屏幕共享双流独立录制）
jitsi-receive --host meet.example.com --room my-room \
  --out recordings --ndi --ndi-prefix "Jitsi"
```

---

## 常用参数 / Command-Line Flags

| Flag | Default | Description (EN / 中文) |
|---|---|---|
| `--host` | `m.yuanjinapp.com` | Jitsi Meet domain or HTTPS URL / Jitsi 域名或 HTTPS 地址 |
| `--room` | *(Required)* | Meeting room name / 会议室房间名 |
| `--name` | `Live Stream` | Participant display name / 与会者显示名称 |
| `--input` | `$STREAM_INPUT` | Input URL, file path, `-` (stdin), or `test` / 输入流地址或文件路径 |
| `--listen` | `false` | Enable RTMP server ingest mode for OBS / 启用 RTMP 服务端接收推流 |
| `--input-cmd` | `""` | Shell command executed before each connect to fetch fresh URL / 动态获取新地址的命令 |
| `--input-proxy` | `""` (auto) | HTTP-FLV proxy (`auto`, `direct`, or `http(s)://\|socks5://`) / 代理配置 |
| `--sign-key-file`| `""` | File containing NodeMedia signing key / NodeMedia 签名密钥文件路径 |
| `--meeting-retries` | `5` | Retries with exponential backoff if room doesn't exist (`0` to disable) / 会议不存在时重试次数 |
| `--meeting-backoff` | `1s` | Initial backoff duration (doubles each retry) / 会议不存在时的初始退避时长 |
| `--height` | `720` | Output video height (144-2160, even number) / 输出视频高度 |
| `--fps` | `25` | Output frame rate (1-60) / 输出视频帧率 |
| `--bitrate` | `1500` | Target VP8 video bitrate in kbps / 输出目标码率 (kbps) |
| `--no-audio` | `false` | Publish video only (for muted sources) / 仅推视频轨（无音频源时使用） |
| `--ffmpeg` | `ffmpeg` | Path to FFmpeg executable / FFmpeg 二进制路径 |
| `--debug-xmpp` | `false` | Log raw XMPP stanzas (contains credentials, for debug only) / 原始信令调试日志 |
| `--version` | `false` | Show version information and exit / 打印版本信息 |

---

## Documentation & Manuals / 文档索引

- **[使用说明书 (User Manual)](docs/USAGE.md)**：详细参数说明、OBS 配置指南、NodeMedia 签名机制、Systemd 守护进程与故障排查。
- **[编译与构建指南 (Build & Release)](docs/BUILD.md)**：本地交叉编译多架构产物、GitHub Actions CI/CD 自动化构建与发布流程。
- **[系统架构解析 (Architecture)](docs/ARCHITECTURE.md)**：内部模块划分、数据流动链路、核心状态机与扩展开发指南。
- **[版本变更记录 (Changelog)](CHANGELOG.md)**：各版本发布历史与改进说明。
- **[开发与验证记录 (Dev Records)](docs/dev/)**：
  - [需求设计与技术选型 (PLAN.md)](docs/dev/PLAN.md)
  - [真实测试验收记录 (VALIDATION.md)](docs/dev/VALIDATION.md)

---

## Repository Structure / 代码仓库结构

```
cmd/           jitsi-stream (main tool), jitsi-receive (per-user recorder & NDI broadcaster)
internal/
  ├── input/         Protocol resolution, proxy routing, token signing, URL redaction
  ├── rtmpserver/    Built-in authenticated RTMP ingest server (for OBS push)
  ├── media/         FFmpeg process lifecycle, loopback RTP, continuous sequence/timestamp sink
  ├── ndi/           Zero-GStreamer per-participant NDI broadcaster & SDP/FFmpeg sink manager
  └── conference/    Native Jitsi XMPP/Jingle signalling, ICE/DTLS transport, SSRC-to-participant mapping
third_party/   Vendored & patched signalling and RTMP modules, full third-party notices
deploy/        Docker Compose templates (pull & ingest profiles) and systemd service files
scripts/       Packaging script, E2E verification script, license generator
docs/          User manuals, architecture specs, build guides, and validation reports
```

## License

This project is licensed under the [MIT License](LICENSE).
Third-party dependencies and notices are detailed in [THIRD_PARTY.md](THIRD_PARTY.md) and [NOTICES.txt](third_party/NOTICES.txt).
