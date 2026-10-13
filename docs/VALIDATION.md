# 实际验证记录

验证日期：2026-10-13。本机为 macOS arm64；Linux 为 cmin2 x86_64。
目标 Jitsi：`m.yuanjinapp.com`，Docker stable-10314。

## 已通过的链路

全部使用原生 CLI，没有浏览器、无头浏览器、显示服务器或虚拟摄像头。

| 场景 | 接收端记录 | 独立解码结果 |
|---|---|---|
| 本机合成测试源 → 实际 Jitsi → 本机 CLI | 4,723 视频包 / 1,353 音频包 | VP8 / Opus；早期诊断文件曾有乱序/重复包时间戳告警，随后修复诊断接收器 |
| 本机真实 NodeMedia HTTPS-FLV → Jitsi → 本机 CLI | 5,929 视频包 / 1,971 音频包 | 1280×720 VP8：981 帧；Opus 48 kHz 双声道：1,967 帧；`ffmpeg -v error ... -f null -` 无报错 |
| cmin2 Docker 拉真实 HTTPS-FLV（临时 SSH 代理）→ Jitsi → 本机 CLI | 6,240 视频包 / 1,666 音频包 | 1280×720 VP8：832 帧；Opus 48 kHz 双声道：1,662 帧；独立解码无报错 |
| cmin2 Docker 读取 12 秒真实 FLV 样本 → Jitsi → 本机 CLI | 2,012 视频包 / 602 音频包 | 1280×720 VP8：300 帧；Opus 48 kHz 双声道：598 帧；独立解码无报错；容器退出码 0 |

计数含义：包数是诊断接收器经过有限乱序缓冲与重复包抑制后交给文件 writer 的 RTP 包数；帧数来自 `ffprobe -count_frames`。不能用包数直接推断画质、时延或唇音同步。Linux 临时代理实流的媒体时长短于墙钟时长，存在输入代理/网络抖动，未承诺实时延迟上限。

最新源码又通过 `scripts/verify.sh` 全流程复验：测试源经实际 Jitsi 后，视频解码 998 帧、音频解码 1,993 帧，无解码报错。P2P 隔离修复后再次复验：本机测试源全流程视频 1,009 帧 / 音频 2,019 帧；cmin2 最终镜像读取真实 FLV 样本，发送并接收 2,013 视频包 / 602 音频包，解码 300 帧 VP8 1280×720 / 598 帧 Opus，收发两端退出码均为 0。最终默认 Dockerfile 源码构建镜像 ID：`sha256:6381d398822bb9530c14f240847692a9ecbe6f36367c07dfc8ce1dad3f4f389c`。

P2P 兼容：站点 `config.p2p.enabled=true`。转换器 presence 标记 `botType=poltergeist`（lib-jitsi-meet 据此不对其发起 P2P），并拒绝非 focus 来源的 Jingle，防止覆盖 JVB 会话；有单元测试。浏览器两人会议场景仍需人工确认。

源输入经探测为 H.264 Constrained Baseline、1920×1080、AAC-LC 44100 Hz 双声道。输出是实际转码得到的 VP8 + Opus，不是把 URL 贴进会议聊天。

每个端点完成：匿名 SASL → focus 分配 → MUC 入会 → Jingle session-accept（服务端 IQ 确认）→ ICE/DTLS connected → Colibri bridge → 音视频 RTP/SRTP。实际部署的 offer 没有 SCTP data m-line，因此走 Colibri WebSocket；代码只有服务端提供 SCTP 时才创建/等待 DataChannel。

## 网络结论（重要）

- 浏览器/Python 读取本机系统代理 `127.0.0.1:7897`；curl、FFmpeg、Go 默认没有自动使用它。这是之前“Python 成功、命令行直连失败”差异的重要原因。
- Go 标准 HTTP/TLS 经显式 `--input-proxy` 已成功拉取真实 FLV，未关闭证书/主机名校验。TLS fingerprint 实验已删除，不保留 uTLS 依赖。
- cmin2 直连源站仍不可靠；**没有验证为直连成功**。
- Linux 实流验证临时使用 SSH `-R 127.0.0.1:17897:127.0.0.1:7897`，只监听远端回环，不开放公网代理。正式运行应使用能访问源站的 Linux 或稳定代理，而非依赖开发机。
- 同时用本地真实 FLV 样本独立验证 Linux 媒体与 Jitsi 协议转换，不将源站网络失败归咎于转换协议。

## 工程与失败路径

已执行并通过：

- `make test`：主项目与信令 fork 的 `go test -race ./...`；主项目 `go vet ./...`。
- `make build`；`make linux`：Linux amd64 CLI 为静态链接 ELF。
- cmin2 上按默认 Dockerfile **完整源码构建**（非仅复制 Mac 二进制），非 root、host network、只读文件系统、drop ALL capabilities 运行。
- 信令 fork 的 IQ rejection、XML 字符/UTF-8 nickname、Jingle direction/source name 回归测试。
- NodeMedia 测试密钥签名向量、保留原始签名 URL、代理传输、401、非 FLV 响应、不信任 TLS 证书拒绝测试。
- RTP 乱序、序号回绕、重复包、丢包窗口测试；FFmpeg 测试源流水线取消时及时结束并确实产生两路 RTP。
- 非法房间名退出码 1。
- 空会议 `--join-timeout 3s --retries 0` 退出码 1，并退会。
- `--duration 3s` 内无所需媒体发送，退出码 1，不把超时冒充成功。
- Docker 文件输入 EOF 后自然退出，日志出现 `leave-muc handshake ok`，退出码 0。
- 最终镜像运行测试源后执行 `docker stop -t 10`：SIGTERM 正常退出，发送 1,597 视频包 / 464 音频包，退会确认成功，退出码 0；验证容器和临时代理在测试结束后清理。

验证录像/日志保存在本地 `.artifacts/`（git/docker ignore）。早期 `--debug-xmpp` 文件有敏感 ICE 信息，未提交。服务器验证目录：`/data/jitsi-stream-validation`；镜像：`jitsi-stream:validation`。这些是开发验证产物，不是已启用的常驻直播服务。

可复验：`scripts/verify.sh`。提供 `STREAM_INPUT`、`NODEMEDIA_AUTH_KEY` 和 `INPUT_PROXY` 环境变量可改为真实 HTTP-FLV；默认是测试图和音频。

## RTMP（第二轮，2026-10-13）

| 场景 | 接收端 | 解码结果 |
|---|---|---|
| 本机 RTMP 拉流（本地 RTMP 服务器循环播放真实 FLV 样本）→ Jitsi | 5,370 视频包 / 1,524 音频包 | VP8 1280×720 754 帧；Opus 1,520 帧；无报错 |
| 本机 RTMP 拉取真实 NodeMedia（`rtmp://…:1935`，自动签名） | 184 视频包 / 52 音频包 | 握手、签名、元数据正确；随后到 1935 端口的吞吐几乎为零，断流后自动重连且与会者未离会。cmin2 同样 30 秒内无媒体：**网络路径问题，非协议问题**；真实源 RTMP 长时稳定性未验证 |
| 本机 RTMP 推流接收：两个推流端先后推流 | 3,828 视频包 / 1,153 音频包，单一音视频轨 | VP8 573 帧 / Opus 1,149 帧；同一与会者全程在会，RTP 连续 |
| 本机推流端早于会议就绪 15 秒开始推流 | 2,711 视频包 / 680 音频包 | 382 帧 ≈ 就绪后的剩余直播时长；推流端 36 秒流用时 35 秒（未被反压卡住），无积压突发 |
| cmin2 Docker（legacy builder 源码构建）推流接收 | 2,005 视频包 / 602 音频包 | 300 帧 / 598 帧（12 秒样本全部送达）；容器退出码 0 |

安全：首版曾用 FFmpeg `-listen 1` 做 RTMP 服务器，测试发现**错误密钥只告警、媒体仍进入会议**。已改为内置 RTMP 服务器（`internal/rtmpserver`）：握手阶段校验 app 与密钥（常量时间比较），错误密钥/错误 app/并发第二推流端均被拒绝（集成测试 + 两平台端到端确认，不进入会议）；未认证连接 15 秒握手超时、推流 30 秒无数据断开；显式限制 chunk/消息大小与数量。

其他修复：Windows 盘符路径被误判为 URL scheme；聊天消息含 `session-terminate` 字样会断开会话；输入重启后 RTP 序列号/时间戳跳变（现保持连续，与会者不离会）；无媒体时显示静音头像；go-rtmp 在 32 位 ARM 上编译失败（本地 fork 修复）。

CI（GitHub Actions）：Ubuntu/macOS/Windows 三平台测试、7 个平台包交叉编译、linux/amd64+arm64 镜像构建均通过。

## 一次性 URL、系统代理、会议不存在（第三轮）

用户报告：`--input` 一次性签名 URL，33 秒 0 包后 `unexpected EOF`，重连 3 次均 401 后退出。

- 根因 1（网络）：本机直连源站实测 13 秒仅 909 字节后 EOF；经系统代理 642 kbit/s 稳定。浏览器读取 macOS 系统代理，Go 程序不读。修复：HTTP-FLV 自动使用 `HTTPS_PROXY` → macOS(`scutil --proxy`)/Windows(注册表) 系统代理 → 直连，日志打印实际路径；单元测试使用本机真实 `scutil` 输出。复验时本机系统代理已被代理软件关闭（转 TUN 模式），程序正确识别为 direct；真实 VVD008 40 秒推送，接收 7,192 视频包 / 2,038 音频包，解码 1,008 帧（1306×720）/ 2,034 帧。未改动用户系统代理设置。
- 根因 2（一次性签名）：本地模拟“签名只能用一次 + 8 秒后断线”的服务器：
  - 固定一次性 URL、无密钥：首次 200，断线后第一次 401 **立即退出**并给出指引（不再浪费 3 次重试）。
  - `--input-cmd` 每次生成新签名：3 次连接全部 200，与会者全程在会，接收端单一音视频轨，独立解码无错。
  - 有 `NODEMEDIA_AUTH_KEY` 时每次连接自动重签（真实 NodeMedia 已验证可用）。
- 会议不存在（新需求）：入会前以 disco#info 检查房间（在 focus 分配之前，避免创建会议）；Prosody `muc_hide_all` 下不存在返回 `item-not-found`、存在返回 `forbidden`，已对真实服务器分别验证。
  - 房间始终不存在：重试 5 次，退避 200ms/400ms/800ms/1.6s/3.2s（测试用 `--meeting-backoff 200ms`），退出码 1。每次检查需新建 XMPP 连接（本网络约 3.9 秒），总耗时 25.7 秒。
  - 会议在转换器启动 5 秒后才开始：第 2 次重试时入会并正常推流。

## 未验证 / 未覆盖

- 没有人工浏览器观看/收听（包括与浏览器单人同房时的 P2P 行为）；主观画质、音量、唇音同步、首帧/端到端延迟未人工验收。
- 没有 24 小时稳定性/负载测试，也没有故障注入验证所有重连场景。
- RTSP/HLS/SRT/UDP 输入适配已实现并测试参数路由，但尚未拿真实这些协议源做端到端验证；OBS 本体未实测（用 FFmpeg RTMP 客户端模拟推流）。macOS 端到端均由本机 arm64 构建完成；Windows 版仅 CI 单元测试与二进制格式检查，未在 Windows 上做真实会议端到端；Intel macOS、Linux ARM 仅交叉编译与格式检查。
- JWT、账号密码、等候室、密码房间、E2EE、混流、simulcast、自适应码率未覆盖。

需求映射：R1/R2/R3/R4 的机器可验证部分已完成；R5 提供独立协议输入边界但其他协议不冒称实测；R6 Docker 与封装已完成；R7 签名刷新和密钥隔离已完成；R8 网络限制清楚区分且 Linux 实流/样本分别验收；R9 有测试/日志/超时/清理，长时稳定性仍是后续工作。
