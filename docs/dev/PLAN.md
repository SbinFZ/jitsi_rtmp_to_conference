# 需求、资源、方案与验收

## 1. 需求基线

目标不是打开视频网页，而是将外部流的音视频作为一个真实 Jitsi 与会者发布。

| ID | 需求 | 首版交付 / 验收 |
|---|---|---|
| R1 | HTTP(S)-FLV 直播 URL 输入 | 从真实 NodeMedia 源拉流，会议另一端接收并解码 |
| R2 | 纯命令行 Linux，无浏览器 / 无头浏览器 / 显示服务器 | Go + Pion 原生 WebRTC；FFmpeg 子进程负责解封装和编解码 |
| R3 | 使用现有 m.yuanjinapp.com | config.js 发现 XMPP 域，匿名 SASL、MUC、focus、Jingle、ICE/DTLS/SRTP |
| R4 | 一个可命名的参会者，音视频同步 | 单路 VP8 + Opus，单 FFmpeg 时钟，明确发送 / 接收计数 |
| R5 | 方便接入其他协议 | 输入适配层：HTTP-FLV 用 Go 拉流送 stdin；RTMP / RTSP / HLS / 文件交给 FFmpeg（按协议分别验证，不将未测的标记已验证） |
| R6 | 封装、迭代、Docker | 分离输入、媒体、Jitsi 信令、CLI；固定依赖；Dockerfile、使用文档和测试 |
| R7 | 一次性签名地址 | 原样使用 URL；可选 NodeMedia MD5 签名刷新，密钥从环境变量读，不提交实际密钥 |
| R8 | cmin2 到流源网络不好 | 首次联调本机拉流；Linux 验证可使用本机下载样本。网络与协议分开验收，不声称服务器直拉成功 |
| R9 | 可操作、能诊断失败 | 超时、重试、退出清理、stderr 日志、SIGINT/SIGTERM；不默认输出签名 / 密钥 |

范围外：Web UI、转发会议画面到 RTMP、多路混流、任意受限会议/JWT/密码/等候室认证、E2EE、完整自适应码率。首版不自动改动生产 Jitsi 的配置或重启服务。

## 2. 已探测资源

- 当前项目原为空目录；本机有 Go 1.25.3、Rust、Node、FFmpeg（libvpx/libopus/libx264）。
- SSH cmin2 可用，Docker 可用，无宿主机 Go / FFmpeg。
- Jitsi Docker stable-10314；Prosody、Jicofo、JVB、web 已运行。
- config.js: XMPP `meet.jitsi`，MUC `muc.meet.jitsi`；WSS `/xmpp-websocket` 可访问；匿名 SASL 成功。
- 本机真实 FLV 样本：H.264 Constrained Baseline 1920x1080，AAC-LC 44100Hz 双声道。
- 初次探测本机 Python 可拉流，但 curl/FFmpeg 直连失败。后续查明：macOS 系统代理为 127.0.0.1:7897，Python/浏览器自动使用，Go/curl 默认未使用；不是已证实的 TLS 实现问题。Go 显式输入代理已验证。cmin2 直连失败，不能作为首轮验证前提；临时 SSH 回环代理可用于 Linux 实流验证。
- NodeMedia 提供的签名匹配 `MD5("/app/name-expiry-key")`，刷新后本机返回 FLV；这不等于管理面板密码。无需登录面板或读取浏览器凭据。
- 可复用 `github.com/zarazaex69/j`，源码已在 /tmp 下载并编译，提交 `6129578a8a498e6ca59e8c66b63b876892279b12`。许可证允许修改/分发。真实部署已完成匿名登录、focus 分配、MUC 入会。一个参与者时未收到 Jingle，需要第二个接收者验证。
- ShowMeMac 的 `showme-sender` 为原生 Rust H.264/WebRTC 发送端；参考媒体与信令解耦、关键帧/周期 IDR、日志不泄露凭据的原则。不直接复制 GPL 代码；其 SSE 配对信令不能用于 Jitsi。

## 3. 方案选择及复核

选用 **Go + Pion + FFmpeg + 本地固定版本 Jitsi 客户端 fork**。

链路：

```
HTTP-FLV (Go HTTP / 可选重新签名) ─┐
RTMP / RTSP / HLS / 文件 (FFmpeg) ├→ 单 FFmpeg 转码 VP8 + Opus
                                 → loopback RTP → Go Pion TrackLocalStaticRTP
                                 → ICE + DTLS + SRTP → JVB → 会议接收者
Go 原生客户端 → WSS XMPP / MUC / Jingle → Prosody / Jicofo
```

为什么：仅将 AAC/H.264 包换容器不够，AAC 需要转换为 WebRTC Opus；Jitsi 的参会信令也不是 WHIP。FFmpeg 是媒体子系统而不是浏览器。先转 VP8 增加 CPU，但降低首版 H.264 profile/packetization 协商风险；后续可增加 H.264 直通。

备选：Rust 原生 WebRTC 也可行，但没有已验证的 Jitsi 信令基础；lib-jitsi-meet/Jibri 浏览器路线违反 R2；Jigasi 为 SIP 网关，不直接满足任意 FLV 音视频输入且要增加服务。

复核：R1/R4 有真实音视频验收；R2 无任何浏览器依赖；R3 复用真实部署协议；R5 输入边界独立；R6 fork 可控、固定版本；R7 密钥不入库；R8 本机与 Linux 网络验证分离；R9 添加超时和退出清理。最主要风险是 Jingle/SDP 兼容、媒体源命名/静音状态以及 JVB 的公网 ICE 候选；先用两个 CLI 做最小链路验证再完善封装。

## 4. 实施顺序

1. 已完成：需求/资源/选型复核（本文）。
2. 复制固定版本客户端为 third_party/jitsi，保留许可证与来源记录；只做必要兼容修复。
3. CLI 加入/等待第二个参与者；Pion 媒体协商 + CLI 接收诊断工具；明确空会议等待行为。
4. Go HTTP FLV → FFmpeg VP8/Opus → 本地 RTP → JVB。先合成测试源，再真实 FLV。
5. 接收侧统计两路 RTP，保存 WebM/Opus 并通过 ffprobe/ffmpeg 解码确认，避免用“登录成功”冒充音视频成功。
6. Linux 交叉编译 / Docker 构建、运行、停止清理；cmin2 优先用样本避免源站网络干扰。
7. 单元测试、竞态检测、失败路径、Docker/README；更新 docs/dev/VALIDATION.md 实际证据与限制。

实施调整复核：新增 `--input-proxy` 只影响 HTTP-FLV，与 Jitsi 信令独立；cmin2 在不修改 Jitsi 服务的前提下，通过临时 SSH 回环代理完成实流验证，并用本地 FLV 样本完成独立于源站网络的 Docker 验证。TLS 兼容实验代码/依赖已删除，仍使用标准 Go TLS 证书校验。细节见 VALIDATION.md。

## 5. 验收等级

- L1: 编译、单元测试、无浏览器依赖。
- L2: 实际 Jitsi MUC 两个 CLI 与会者，Jingle 接受、ICE/DTLS connected。
- L3: 合成源和真实 FLV 两路媒体到接收者，包数持续增加，视频音频可解码。
- L4: Linux / Docker 运行、终止无残留；人工浏览器观看/收听单独记录是否进行，未进行则不冒称通过。

首版完成以 L1-L4 的机器可验证部分为准；主观听感、延时、唇音同步需人工验收，明确报告证据边界。
