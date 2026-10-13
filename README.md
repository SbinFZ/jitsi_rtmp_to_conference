# jitsi-stream：外部流 → Jitsi 与会者

纯命令行转换器：**Go + Pion 原生 WebRTC + FFmpeg**。把外部流的音视频作为一个有显示名称的参会者发布到 Jitsi；不打开视频网页，不使用 Chromium、无头浏览器、WebView 或显示服务器。

已在 `m.yuanjinapp.com` 验证 HTTP(S)-FLV。其他输入协议通过 FFmpeg 适配，见下方验证范围。

## 快速开始（本机）

需要 Go ≥ 1.24 和 FFmpeg（含 `libvpx`、`libopus`）。

```bash
make build
# 国内 Go 依赖下载失败时：
# GOPROXY=https://goproxy.cn,direct make build

bin/jitsi-stream \
  --host m.yuanjinapp.com --room my-live-room \
  --name '直播视频' --input 'https://example.com/app/live.flv?sign=...'
```

打开 `https://m.yuanjinapp.com/my-live-room`，加入会议即可接收。转换器本身不需要浏览器。**空会议中 Jicofo 通常要等第二个与会者加入才发起媒体协商**；日志会提示等待，默认等 5 分钟。房间名统一小写。

签名 URL 不想放进命令参数/进程列表时，可以通过环境变量输入：

```bash
export STREAM_INPUT='https://example.com/app/live.flv?sign=...'
bin/jitsi-stream --room my-live-room --name '直播视频'
```

### 你的 NodeMedia 流

使用不带签名的基础 URL，并通过环境变量提供 Auth Key；每次重新拉流生成新的签名，不必手动访问面板。密钥不是面板登录密码。

```bash
# Bash：交互输入密钥，不写进仓库或命令历史
read -r -s -p 'NodeMedia Auth Key: ' NODEMEDIA_AUTH_KEY; echo
export NODEMEDIA_AUTH_KEY

bin/jitsi-stream --room my-live-room --name '远近直播' \
  --input 'https://stream.yuanjinapp.com:8443/yuanjin/YJIntroCN.flv' \
  --input-proxy 'http://127.0.0.1:7897'
```

`--input-proxy` 是本次探测到的本机代理地址，仅用于 HTTP-FLV 拉流。浏览器/Python 自动使用 macOS 系统代理，Go/FFmpeg 不会自动读取该设置；**本机浏览器能播放，不等于命令行直连可用**。在能直连源站的 Linux 上省略该参数。也支持 HTTP(S)/SOCKS5 代理；未指定时 Go 使用 `HTTP_PROXY` / `HTTPS_PROXY` / `NO_PROXY` 环境变量。

签名采用 NodeMedia PLAY 协议：`sign=expiry-MD5('/app/name-expiry-key')`，FLV 扩展名不计入路径，默认有效期一小时。服务器还可能限制签名复用、连接数或来源 IP；只有 URL 没有密钥时无法保证过期后重连。

## Docker / Linux

```bash
docker build -t jitsi-stream:local .

docker run --rm --init --network host \
  --read-only --tmpfs /tmp --cap-drop ALL \
  --security-opt no-new-privileges \
  -e NODEMEDIA_AUTH_KEY \
  jitsi-stream:local \
  --host m.yuanjinapp.com --room my-live-room \
  --name '远近直播' --input 'https://stream.yuanjinapp.com:8443/yuanjin/YJIntroCN.flv'
```

- 镜像含 Go 工具和 FFmpeg，非 root 运行；没有浏览器或显示服务。
- `--network host` 示例针对 Linux。网络需放行到 Jitsi 的 HTTPS/WSS 和 JVB 的 UDP（当前部署为 10000/UDP）；会读取服务端 ICE 候选，不硬编码媒体地址。
- 默认是源码多阶段构建；Go 代理可用 `--build-arg GOPROXY=https://goproxy.cn,direct` 覆盖。基础镜像与 apt 包随上游安全更新，不是逐字节可重复构建。
- `make linux` 生成静态链接 Linux amd64 CLI（`dist/linux/`）；裸机运行还需 FFmpeg 和 CA 证书。ARM64 可用 `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build ./cmd/jitsi-stream`。
- 可选 `docker build --target prebuilt ...` 使用 `dist/linux/`。建议 BuildKit，否则旧版 builder 仍可能执行之前的阶段。

使用 Compose：

```bash
export JITSI_ROOM=my-live-room
export STREAM_INPUT='https://example.com/app/live.flv?sign=...'
docker compose up --build
```

### cmin2 拉流网络问题

当前 cmin2 直连源站不可靠。转换器不修改 Jitsi 服务、DNS、证书验证或防火墙来掩盖它。可选择：

1. 转换器部署到能访问源站的 Linux 机器，远程加入现有 Jitsi。
2. 使用独立可用的拉流代理，通过 `--input-proxy` 指定。
3. 开发联调临时从本机转发现有代理，只监听远端回环地址：

```bash
# 保持这个 SSH 会话；Ctrl+C 关闭。不要绑定公网地址。
ssh -NT -o ExitOnForwardFailure=yes \
  -R 127.0.0.1:17897:127.0.0.1:7897 root@cmin2.baige.cloud

# 在 cmin2 的 host-network 容器加：
# --input-proxy http://127.0.0.1:17897
```

第三种只是临时验证通路，不是脱离本机的正式部署。没有能同时访问源站和 Jitsi 的网络路径，任何转换器都不能凭空完成转发。

## 无浏览器验收

附带 `jitsi-receive` CLI 用于验证：加入同一会议，请求 JVB 转发视频，保存收到的 VP8/Opus。它不是 UI 或完整录制器。

```bash
bin/jitsi-receive --room converter-test --duration 60s --out recordings &
bin/jitsi-stream --room converter-test --input test --duration 45s --retries 0
wait

# 分别查看 / 解码收到的音视频
ffprobe recordings/video-*.ivf
ffprobe recordings/audio-*.ogg
ffmpeg -v error -i recordings/video-*.ivf -f null -
ffmpeg -v error -i recordings/audio-*.ogg -f null -
```

Shell 通配符只适用于单个录制文件；多路 SSRC 时请逐个文件验证。接收器有有限乱序缓冲、重复包抑制和 VP8 尺寸检测；文件分别保存，不代表已验证唇音同步或具备正式录制的同步封装。

源码与测试：

```bash
make test     # 主项目 + 信令 fork 的 race tests，go vet
```

实际运行证据与限制见 [`docs/VALIDATION.md`](docs/VALIDATION.md)。需求和方案复核见 [`docs/PLAN.md`](docs/PLAN.md)。

## 输入与选项

| 输入 | 使用方式 | 验证状态 |
|---|---|---|
| HTTP(S)-FLV | `--input https://.../live.flv`，Go 拉流后送 FFmpeg stdin | 真实源站与 Jitsi 已端到端验证 |
| FLV 文件 | `--input /data/sample.flv`，按实时速率读取 | 可用于 Linux 离线源联调 |
| 测试源 | `--input test`，测试图 + 440 Hz 音频 | 已端到端验证 |
| RTMP/RTMPS | `--input rtmp://host/app/name` | 适配已实现；真实 RTMP 源未联调 |
| RTSP | `--input rtsp://host/stream`，默认 TCP | 适配已实现；未端到端验证 |
| HLS/其他 FFmpeg 输入 | `--input https://host/live.m3u8`；SRT/UDP URL | 适配已实现；未端到端验证 |
| stdin | `--input -`，由上游命令喂流 | 消费后不能重开，自动禁用重试 |

HTTP-FLV 检测依赖 URL 路径 `.flv` 后缀。其他 HTTP 输入走 FFmpeg；`--input-proxy` **不用于** RTMP/RTSP/HLS。

常用参数（完整参数 `--help`）：

- `--height 720 --fps 25 --bitrate 1500`：固定输出尺寸/帧率/VP8 kbps；AAC 等音频转 Opus 48 kHz 双声道。
- `--no-audio`：视频源无音轨时使用，否则缺少音轨会报错；接收验收加 `--video-only`。
- `--join-timeout 5m --connect-timeout 30s`：分别限制入会等待与媒体/桥接连接。
- `--duration 0`：持续运行；正值包括入会等待时间。超时前完全未发送所需媒体会失败。
- `--retries 3`：信令/媒体失败后退会重入、重新拉流，指数退避，重连时显示为新的 endpoint。
- `--ffmpeg /path/to/ffmpeg`：指定媒体工具。
- `--debug-xmpp`：**仅主动启用时打印敏感信令**，可能包含 ICE 密码、内网地址、桥接 URL。不要公开分享。普通日志会脱敏常见 URL 凭据，但不能保证第三方工具所有自由格式消息都无敏感内容。

SIGINT/SIGTERM 会终止 FFmpeg、关闭 RTP/PeerConnection、发送退会 presence；不留下长期占用的“幽灵参会者”。

## 架构与边界

```
外部协议 → internal/input → FFmpeg（VP8/Opus）→ 回环 UDP RTP
                                      → Pion ICE/DTLS/SRTP → JVB → 参会者
internal/conference → XMPP/WSS → MUC + Jicofo/Jingle + Colibri 桥接
```

- `internal/input`：URL/代理、NodeMedia 签名、HTTP-FLV 流读取与超时。
- `internal/media`：FFmpeg 生命周期、RTP 转发、诊断接收乱序缓冲。
- `internal/conference`：原生入会、协商、presence、连接状态和清理。
- `third_party/jitsi`：现成 Go Jitsi 客户端固定版本 fork；来源、许可证、修改记录均保留。

首版面向当前匿名入会部署；不支持 JWT/账号密码/等候室/带房间密码会议、E2EE、多路混流。默认转码而非 H.264 直通，有 CPU 成本；码率固定，不具备完整浏览器客户端的自适应/多层 simulcast。NACK 由 Pion 处理；目前 PLI/FIR 不直接控制 FFmpeg，靠一秒 GOP 恢复。长时稳定性、主观听感、唇音同步与人类浏览器观看仍需现场验收。

协议和编解码扩展分别在输入层/媒体层进行，无需更换 Jitsi 信令层。许可证与第三方说明见 `LICENSE`、`THIRD_PARTY.md`。
