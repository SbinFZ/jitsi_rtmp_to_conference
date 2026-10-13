# Changelog

## v0.2.0 — 2026-10-13

### 修复
- **一次性签名 FLV 地址断线后无法重连**：新增 `--input-cmd`（每次（重）连接前执行命令获取新地址）和 `--sign-key-file`（NodeMedia 自动重签）；无法刷新凭据时遇 HTTP 401 立即退出并给出指引，不再用同一个失效地址白白重试。
- **浏览器能播放、命令行长时间 0 包后 `unexpected EOF`**：HTTP-FLV 现在与浏览器一致，自动使用 `HTTPS_PROXY` 环境变量或 macOS/Windows 系统代理（含例外列表）；`--input-proxy direct|URL` 可覆盖；日志打印实际连接路径。

### 新增
- 会议不存在时不再自行创建空会议：入会前检查房间，按二进制指数退避重试（默认 5 次：1s、2s、4s、8s、16s，`--meeting-retries`、`--meeting-backoff`），仍不存在则退出；`--meeting-retries 0` 恢复旧行为。
- `deploy/`：Compose（pull / ingest 两种模式，通过 `COMPOSE_PROFILES` 选择）、systemd 服务模板，并随发布包提供。
- `docs/ARCHITECTURE.md`、`make verify`。

### 内部
- `internal/input` 按职责拆分（input/httpflv/nodemedia/redact/sysproxy/command）。
- 开发记录移至 `docs/dev/`。

## v0.1.0 — 2026-10-13

首个版本。
- HTTP(S)-FLV、RTMP 拉流、RTMP 推流接收（握手鉴权、单推流端、未就绪丢弃）、本地文件、stdin、测试源；RTSP/HLS/SRT/UDP 经 FFmpeg。
- 纯 Go 原生 Jitsi 入会（XMPP/Jingle/ICE/DTLS），VP8 + Opus 转码发布，无需浏览器。
- 输入重启时与会者不离会；无媒体显示静音头像；优雅退出。
- GitHub Actions：三平台测试、7 个平台包（Linux amd64/arm64/armv7、macOS amd64/arm64、Windows amd64/arm64）、多架构 Docker 镜像（GHCR）。
