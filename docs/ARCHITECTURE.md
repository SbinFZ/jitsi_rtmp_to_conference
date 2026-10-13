# 架构与代码结构

面向维护和二次开发：代码在哪、数据怎么流动、为什么这样设计、如何扩展。

## 目录结构

```
.
├── cmd/
│   ├── jitsi-stream/        # 转换器 CLI：参数解析 + supervisor（入会/重连/退避/静音状态）
│   └── jitsi-receive/       # 验收 CLI：以与会者身份接收并保存 VP8/Opus（不依赖浏览器）
├── internal/
│   ├── input/               # 外部输入 → FFmpeg 输入参数（与 Jitsi 无关）
│   │   ├── input.go         #   Options、输入类型路由（文件/stdin/test/URL/推流）
│   │   ├── httpflv.go       #   HTTP(S)-FLV 拉流：代理选择、超时、401 识别
│   │   ├── sysproxy*.go     #   macOS(scutil)/Windows(注册表) 系统代理读取与例外匹配
│   │   ├── nodemedia.go     #   NodeMedia 播放签名、签名 URL 识别
│   │   ├── command.go       #   --input-cmd：每次连接前执行命令获取新 URL
│   │   └── redact.go        #   日志脱敏
│   ├── rtmpserver/          # RTMP 推流接收：握手鉴权、单推流端、未就绪丢弃、超时与资源上限
│   ├── media/               # FFmpeg 进程 → 回环 RTP → WebRTC 轨道
│   │   ├── ffmpeg.go        #   单个 FFmpeg 进程的生命周期（VP8 + Opus 转码）
│   │   ├── sink.go          #   跨重启保持 RTP 序列号/时间戳连续
│   │   └── reorder.go       #   接收端诊断用乱序缓冲（jitsi-receive 使用）
│   └── conference/          # Jitsi 入会：XMPP/Jingle 协商、ICE/DTLS、桥接通道、presence
├── third_party/
│   ├── jitsi/               # Jitsi 信令客户端（固定版本 fork，修改见 UPSTREAM.md）
│   ├── go-rtmp/             # RTMP 协议库（固定版本 fork，修 32 位编译，见 UPSTREAM.md）
│   └── NOTICES.txt          # 所有链接进二进制的第三方许可证全文（make notices 生成）
├── deploy/
│   ├── compose.yaml         # Docker Compose：pull / ingest 两种模式
│   ├── env.example          # Compose 环境变量模板
│   └── systemd/             # 裸机 systemd 服务与环境文件模板
├── scripts/
│   ├── package.sh           # 交叉编译 7 个平台并打包（CI 与本地共用）
│   ├── verify.sh            # 真实 Jitsi 端到端验收（测试源 → 会议 → 接收 → 解码）
│   └── third-party-notices.py
├── docs/
│   ├── USAGE.md             # 使用说明书
│   ├── BUILD.md             # 编译、CI、发布
│   ├── ARCHITECTURE.md      # 本文
│   └── dev/                 # 开发记录：需求与方案（PLAN）、实测验证（VALIDATION）
├── .github/workflows/       # ci.yml（三平台测试 + 打包 + 镜像构建）、release.yml（tag 发布）
├── Dockerfile               # 多架构镜像（Go 原生交叉编译 + Debian FFmpeg 运行时）
└── Makefile                 # build / test / lint / verify / package / notices / docker
```

依赖方向（只允许向下）：`cmd → conference, media, rtmpserver, input → third_party`。`input` 与 `media` 不知道 Jitsi 的存在；`conference` 不知道输入协议。

## 数据流

```
                     ┌──────────────── internal/input ────────────────┐
HTTP(S)-FLV ──Go HTTP（代理/超时/签名）──┐                           │
RTMP 推流 ──internal/rtmpserver（鉴权）──┼─ FLV 字节流 → FFmpeg stdin │
RTMP/RTSP/HLS/文件 ──────────── FFmpeg 直接读取 ──────────────────┘
                                     │
                     internal/media: FFmpeg 转码 VP8 + Opus → 127.0.0.1 UDP RTP
                                     │  Sink：改写 SSRC 内的 seq/ts，跨重启连续
                                     ▼
                     internal/conference: Pion TrackLocalStaticRTP
                                     │  ICE + DTLS + SRTP（UDP）
                                     ▼
                              Jitsi JVB ──→ 会议中的浏览器/移动端
信令：XMPP over WSS → MUC 入会 → Jicofo Jingle session-initiate/accept → Colibri 桥接通道
```

## cmd/jitsi-stream 的 supervisor

```
loop:                               会议层
  Join（RequireMeeting）─── 会议不存在 → 退避 1s,2s,4s,8s,16s（--meeting-retries）→ 用尽退出
     │                      其他失败   → 计入 --retries，退避重试
     ▼
  session:                          媒体层（与会者不离会）
    presence 协程：有媒体 → 显示画面；静默 5 秒 → 静音头像
    for:
      media.Run（一个 FFmpeg 进程）
        有媒体送达 → 失败计数清零
        推流模式：推流端断开 → 等下一个推流端（不计失败）
        网络直播源正常结束 → 1 秒后重连
        文件/stdin/test 结束 → 正常退出
        401 且无法刷新凭据 → 立即退出并提示
        其他错误 → 计入 --retries，退避后重启输入
    会议断开（Jingle 终止/XMPP 断开/ICE 失败）→ 回到 loop 重新入会
```

## 关键设计决策

| 决策 | 原因 |
|---|---|
| 纯 Go + Pion + 外部 FFmpeg，不用浏览器/Jibri/lib-jitsi-meet | 需求要求纯命令行；FFmpeg 解决任意编解码；Pion 原生 WebRTC 可交叉编译到所有平台 |
| 输出 VP8 + Opus 转码，而不是 H.264 直通 | 浏览器/JVB 兼容性最稳；AAC 必须转 Opus；代价是 CPU |
| FFmpeg 只输出到回环 RTP，由 Go 写入 WebRTC | FFmpeg 不懂 ICE/DTLS；Go 侧统一处理序列号连续、计数、静音状态 |
| HTTP-FLV 由 Go 拉取而非 FFmpeg | 跨平台 TLS 一致、可用系统代理、可识别 401、可控超时 |
| 自写 RTMP 推流接收（基于 go-rtmp），不用 FFmpeg `-listen` | FFmpeg 只警告错误密钥仍接收媒体（安全漏洞），且重启间隙不监听 |
| 推流未就绪时丢弃而非缓冲 | 会议可能长时间无人，缓冲会卡住推流端并在就绪时突发旧画面 |
| 入会前用 disco#info 检查会议 | focus 分配会创建会议；不应由转换器开一个空会议 |
| presence 标记 `botType=poltergeist`，拒绝非 focus 的 Jingle | 站点开启了两人 P2P；转换器只实现 JVB 会话 |
| 依赖 fork 放在 third_party 并 replace | 固定协议行为、可修补，升级时有明确差异记录 |

## 扩展指南

**新增输入协议**：在 `internal/input/input.go` 的 `Source` 中加一个分支，返回 FFmpeg 输入参数（或一个 `io.ReadCloser` 喂给 FFmpeg stdin），并在 `source_test.go` 加参数路由测试。需要专门网络处理的协议（如 HTTP-FLV）放独立文件。

**新的签名/鉴权系统**：优先用 `--input-cmd`（无需改代码）；要内置时参考 `nodemedia.go`，并让 `Options.Refreshable()` 返回 true，使 401 后可以重连。

**新的输出编解码（如 H.264 直通）**：修改 `media.outputArgs` 和 `conference.Join` 中轨道的 `RTPCodecCapability`，并确认 Jicofo offer 中有该 payload type；用 `jitsi-receive` 验证。

**改动信令**：`third_party/jitsi` 中修改，同步更新 `UPSTREAM.md` 并补测试；之后必须用 `make verify` 对真实 Jitsi 做端到端验证。

## 测试分层

| 层次 | 内容 | 命令 |
|---|---|---|
| 单元 | 参数路由、签名、代理解析、RTP 连续性、信令解析 | `make test` |
| 集成（需 FFmpeg） | FFmpeg 流水线取消、RTMP 鉴权/投递/丢弃/超时（真实 RTMP 客户端） | `make test`（未装 FFmpeg 时自动跳过） |
| 端到端 | 真实 Jitsi：转换器 → JVB → `jitsi-receive` → 独立解码 | `make verify` |
| CI | Ubuntu/macOS/Windows 单元测试、7 平台打包、多架构镜像构建 | GitHub Actions |
