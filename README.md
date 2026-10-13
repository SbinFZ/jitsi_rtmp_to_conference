# jitsi_rtmp_to_conference

**jitsi-stream**：把 RTMP / HTTP-FLV / RTSP / HLS / 本地文件等外部视频流，转换为 Jitsi 会议中的一个**真实与会者**（有名字、有画面、有声音）。

纯命令行：**Go + Pion 原生 WebRTC + FFmpeg**。不需要浏览器、无头浏览器、虚拟摄像头或显示服务器，可直接运行在 Linux 服务器 / Docker 中，也提供 Windows、macOS 版本。

```
RTMP 推流 (OBS…) ─┐
RTMP / HTTP-FLV / ├─→ jitsi-stream ──WebRTC (VP8+Opus)──→ Jitsi JVB ──→ 会议中的所有人
RTSP / HLS / 文件 ─┘        └── XMPP/Jingle 原生入会（匿名）
```

## 功能

| 输入 | 用法 | 状态 |
|---|---|---|
| **RTMP 拉流** | `--input rtmp://host/app/stream` | ✅ 已端到端验证 |
| **RTMP 推流接收**（OBS / 推流软件 / 手机 App 推到本工具） | `--listen --input rtmp://0.0.0.0:1935/live/<密钥>` | ✅ 已端到端验证，含密钥鉴权 |
| HTTP(S)-FLV 直播 | `--input https://host/app/stream.flv` | ✅ 已端到端验证 |
| 本地文件（FLV/MP4/MKV…） | `--input /path/video.mp4` | ✅ 已端到端验证 |
| RTSP / HLS / SRT / UDP | `--input rtsp://…` 等，由 FFmpeg 处理 | ⚠️ 已实现，未用真实源验证 |
| NodeMedia 签名 | 设置 `NODEMEDIA_AUTH_KEY`，自动生成 `sign=` | ✅ |

- 输入中断后自动重连；**与会者保持在会议中**（RTP 序列号/时间戳连续），无媒体时自动显示为静音头像。
- 一次性签名 URL：用签名密钥自动重签，或 `--input-cmd` 每次重连前获取新地址；无法刷新时遇 401 立即给出提示退出。
- 会议不存在时不会自己开空会议：二进制指数退避重试 5 次（1s→16s）后退出（`--meeting-retries 0` 可关闭）。
- HTTP-FLV 自动使用系统代理（macOS/Windows，与浏览器一致）或 `HTTPS_PROXY`。
- RTMP 推流模式：app + 推流密钥在 RTMP 握手阶段校验（常量时间比较），同一时间只接受一个推流端；会议未就绪时丢弃而不是积压，推流软件不会卡住。
- 优雅退出（Ctrl+C / SIGTERM）：停止 FFmpeg、退出会议，不留“幽灵与会者”。
- 日志自动隐藏 URL 中的 `sign/token/key/password` 等敏感参数。

## 下载

在 GitHub **Releases** 页面下载对应平台的压缩包（打 `v*` 标签时由 GitHub Actions 自动编译发布）：

| 系统 | x86_64 | ARM |
|---|---|---|
| Linux | `linux_amd64.tar.gz` | `linux_arm64.tar.gz`、`linux_armv7.tar.gz`（树莓派等） |
| macOS | `darwin_amd64.tar.gz`（Intel） | `darwin_arm64.tar.gz`（Apple Silicon） |
| Windows | `windows_amd64.zip` | `windows_arm64.zip` |

Docker 镜像（linux/amd64、arm64、arm/v7）：`ghcr.io/sbinfz/jitsi_rtmp_to_conference:<版本>`。
每次提交的编译产物也可在 Actions 的 CI 运行记录中下载（`jitsi-stream-packages`）。

压缩包内含 `jitsi-stream`（转换器）、`jitsi-receive`（无浏览器验收工具）、说明书与许可证。**需另外安装 FFmpeg**（含 libvpx、libopus，各平台安装方法见[说明书](docs/USAGE.md#1-安装)）。

## 快速开始

```bash
# 1) 拉取 RTMP 直播流，作为与会者“直播画面”加入 my-room
jitsi-stream --host meet.example.com --room my-room --name '直播画面' \
  --input 'rtmp://live.example.com/app/stream'

# 2) 做 RTMP 推流服务器：OBS 推到 rtmp://<本机IP>:1935/live/<密钥>
jitsi-stream --host meet.example.com --room my-room --name 'OBS' \
  --listen --input 'rtmp://0.0.0.0:1935/live/换成一个长随机密钥'

# 3) HTTP-FLV 直播
jitsi-stream --host meet.example.com --room my-room --input 'https://cdn.example.com/live/stream.flv'
```

**先在浏览器打开 `https://meet.example.com/my-room` 开始会议**，再启动转换器（会议不存在时会以 1s、2s、4s、8s、16s 退避重试 5 次后退出）。

Windows（PowerShell）示例：

```powershell
.\jitsi-stream.exe --host meet.example.com --room my-room --input "rtmp://live.example.com/app/stream"
```

Docker：

```bash
docker run --rm --init --network host ghcr.io/sbinfz/jitsi_rtmp_to_conference:latest \
  --host meet.example.com --room my-room --input 'rtmp://live.example.com/app/stream'

# 或用 Compose（拉流 / RTMP 推流两种模式）
cd deploy && cp env.example .env && docker compose up -d
```

完整参数、OBS 设置、NodeMedia 签名、代理、Docker Compose、常见问题见 **[说明书 docs/USAGE.md](docs/USAGE.md)**。

## 文档

- [说明书 / 使用手册](docs/USAGE.md)
- [编译、CI 与发布](docs/BUILD.md)
- [架构与代码结构](docs/ARCHITECTURE.md)
- [部署模板 deploy/](deploy/)：Docker Compose、systemd
- [变更记录 CHANGELOG](CHANGELOG.md)
- 开发记录：[需求与方案](docs/dev/PLAN.md)、[实际验证记录](docs/dev/VALIDATION.md)
- 第三方组件与许可证：[THIRD_PARTY.md](THIRD_PARTY.md)

## 范围与限制

- 面向允许匿名入会的 Jitsi（docker-jitsi-meet 默认配置）；暂不支持 JWT/账号密码/等候室/房间密码、E2EE。
- 输出固定为 VP8 + Opus 单层（不做 simulcast/自适应码率）。转码需要 CPU：实测 1080p60 输入 → 720p25 输出约占 0.5 个 x86 核、190 MB 内存。
- 同一进程发布一路流；多路请运行多个进程（不同 `--name`，推流模式用不同端口）。
- 尚无人工浏览器长时观感/唇音同步验收与 24 小时稳定性测试，详见验证记录。

## 项目结构

```
cmd/           jitsi-stream（转换器）、jitsi-receive（无浏览器验收工具）
internal/      input（输入协议）· rtmpserver（推流接收）· media（FFmpeg→RTP）· conference（Jitsi 入会）
third_party/   固定版本的 Jitsi 信令与 RTMP 库 fork、第三方许可证
deploy/        Docker Compose、systemd 模板
scripts/       打包、端到端验证、许可证生成
docs/          说明书、编译、架构；docs/dev/ 开发记录
```

详见 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)。

## 开发

```bash
make build      # bin/jitsi-stream, bin/jitsi-receive
make test       # 单元测试（含 race）；有 FFmpeg 时运行媒体/RTMP 集成测试
make package    # 交叉编译全部平台 → dist/*.tar.gz|zip + SHA256SUMS
make verify     # 真实 Jitsi 端到端验证（测试源 → 会议 → 接收并解码）
```

许可证：MIT（新编写代码）。第三方组件许可证见 `THIRD_PARTY.md`、`NOTICES.txt`。
