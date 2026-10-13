# jitsi-stream 说明书

把外部视频流作为一个与会者加入 Jitsi 会议。本手册覆盖安装、全部输入模式、参数、Docker 部署和故障排查。

- [1. 安装](#1-安装)
- [2. 基本概念](#2-基本概念)
- [3. 输入模式](#3-输入模式)
- [4. 参数参考](#4-参数参考)
- [5. Docker / 服务器部署](#5-docker--服务器部署)
- [6. 验收：不用浏览器确认音视频](#6-验收不用浏览器确认音视频)
- [7. 常见问题](#7-常见问题)
- [8. 安全建议](#8-安全建议)

---

## 1. 安装

### 1.1 下载程序

从 GitHub Releases 下载与系统/CPU 对应的包（见 README 表格），解压即可，无需安装。包内：

| 文件 | 说明 |
|---|---|
| `jitsi-stream` / `jitsi-stream.exe` | 转换器 |
| `jitsi-receive` / `jitsi-receive.exe` | 验收工具：以与会者身份加入，保存收到的音视频 |
| `docs/USAGE.md` | 本说明书 |
| `env.example` | Docker Compose 环境变量样例 |

确认版本：`jitsi-stream --version`

### 1.2 安装 FFmpeg（必需）

FFmpeg 负责解码输入、编码 VP8/Opus，需要包含 `libvpx` 和 `libopus`（主流发行版均包含）。

| 系统 | 安装 |
|---|---|
| Debian / Ubuntu | `sudo apt install ffmpeg` |
| CentOS / RHEL / Rocky | 启用 RPM Fusion 后 `sudo dnf install ffmpeg` |
| Alpine | `apk add ffmpeg` |
| macOS | `brew install ffmpeg` |
| Windows | `winget install Gyan.FFmpeg`（或 `choco install ffmpeg`、从 gyan.dev 下载 full build 并加入 PATH） |

检查编码器：

```bash
ffmpeg -hide_banner -encoders | grep -E 'libvpx |libopus'
```

FFmpeg 不在 PATH 中时，用 `--ffmpeg /path/to/ffmpeg`（Windows：`--ffmpeg C:\ffmpeg\bin\ffmpeg.exe`）。

### 1.3 平台注意事项

- **macOS**：下载的程序未签名，首次运行若提示“无法验证开发者”，执行
  `xattr -d com.apple.quarantine ./jitsi-stream ./jitsi-receive`。
- **Windows**：首次运行时防火墙可能询问网络访问，需允许（推流模式需要入站 1935/TCP）。PowerShell 中 URL 请用双引号包裹（含 `&` 时必须）。
- **Linux ARM**：树莓派 64 位系统用 `linux_arm64`，32 位系统用 `linux_armv7`。软件编码 VP8，树莓派建议 `--height 480 --fps 20 --bitrate 800`。

---

## 2. 基本概念

- **一个进程 = 会议中的一个与会者**，显示名为 `--name`。
- **房间**：`--room my-room` 对应 `https://<host>/my-room`。房间名不区分大小写，不能含 `/ ? # @` 或空格。
- **需要第二个人**：Jitsi 在会议中只有一个人时不会建立媒体连接。工具入会后会打印
  `waiting for Jingle (empty room needs a second participant)`，有人进入后自动开始。等待上限 `--join-timeout`（默认 5 分钟）。
- **音视频处理**：输入任意编码（H.264/H.265/AAC/MP3…）→ 输出 VP8 视频 + Opus 48 kHz 立体声。默认 720p、25fps、1500 kbps。
- **无媒体时**：与会者显示为静音头像；媒体恢复后自动显示画面。
- **会议不存在时**：默认先检查会议是否已经有人（不会自己“开”一个空会议）。不存在则按二进制指数退避重试 5 次（1s、2s、4s、8s、16s），仍不存在则退出（退出码 1）。会议结束（所有人离开）后同样按此规则处理。`--meeting-retries 0` 关闭检查，恢复为“自己创建会议并等待别人进入”。
- **断线重连**：网络输入结束或出错时自动重连输入，与会者保持在会议中；连续无媒体的失败次数超过 `--retries` 才退出。Jitsi 连接断开时自动重新入会。
- **一次性签名 URL**：重连时旧签名会被拒绝（HTTP 401）。提供签名密钥（3.4）或 `--input-cmd`（3.5）即可每次重连自动获得新地址；两者都没有时，遇到 401 立即退出并提示，不再白白重试。

---

## 3. 输入模式

### 3.1 RTMP 拉流（从直播服务器拉取）

```bash
jitsi-stream --host meet.example.com --room my-room --name '直播' \
  --input 'rtmp://live.example.com/app/stream'
```

- 支持 `rtmp://`、`rtmps://`（TLS）、`rtmpt://`。URL 可带查询参数（如 `?token=...`）。
- 按直播模式播放（`-rtmp_live live`），15 秒无数据判定断流并自动重连。

### 3.2 RTMP 推流接收（本工具作为 RTMP 服务器）

适用于 OBS、vMix、硬件编码器、手机推流 App 直接把画面推进会议：

```bash
jitsi-stream --host meet.example.com --room my-room --name 'OBS 画面' \
  --listen --input 'rtmp://0.0.0.0:1935/live/Xq7v2kP9mZ'
```

URL 格式：`rtmp://<监听地址>:<端口>/<app>/<推流密钥>`

| 部分 | 说明 |
|---|---|
| 监听地址 | `0.0.0.0` 所有 IPv4 网卡；`127.0.0.1` 仅本机；`[::]` IPv6 |
| 端口 | 省略默认 1935 |
| app | 如 `live`，可多级（`a/b`） |
| 推流密钥 | 最后一段路径；**请使用长随机字符串**，它就是推流口令 |

**OBS 设置**：设置 → 直播 → 服务选“自定义”：

- 服务器：`rtmp://<运行 jitsi-stream 的机器 IP>:1935/live`
- 串流密钥：`Xq7v2kP9mZ`

推荐 OBS 输出：x264、关键帧间隔 1–2 秒、码率 2500–4000 kbps、AAC 128 kbps。

行为说明：

- app 或密钥错误 → 握手阶段拒绝（OBS 显示连接失败），不会有任何画面进入会议。
- 同一时间只接受一个推流端；已有推流时，第二个会被拒绝。
- 推流端可以先于会议就绪开始推流：未就绪期间的画面被丢弃（不积压、OBS 不会卡顿），就绪后从下一个关键帧开始播放。
- 推流端断开后，与会者保持在会议中并显示静音头像，等待下一次推流。
- 仅支持明文 RTMP；跨公网推流请经 VPN/内网，或在前面加反向代理做 TLS 终止。

### 3.3 HTTP(S)-FLV

```bash
jitsi-stream --room my-room --input 'https://cdn.example.com/live/stream.flv?auth=...'
```

URL 路径以 `.flv` 结尾时，由程序内置 HTTP 客户端拉取（证书校验开启）。

**代理自动选择**（与浏览器一致）：`--input-proxy` 未指定时，依次使用 `HTTPS_PROXY`/`HTTP_PROXY` 环境变量 → macOS/Windows **系统代理**（系统设置里的 HTTP/HTTPS/SOCKS 代理及例外列表；PAC 自动配置脚本不支持）→ 直连。日志会打印实际路径，如 `HTTP-FLV connecting via="system proxy http://127.0.0.1:7897"`。`--input-proxy direct` 强制直连，`--input-proxy http://host:port` / `socks5://host:port` 指定代理。Linux 只读环境变量。

> 浏览器能播放、命令行却 0 包或很快 `unexpected EOF`，通常就是浏览器走了系统代理而命令行没有。本版本已自动处理；若用的是 PAC 或代理软件的“规则模式”，请显式加 `--input-proxy`。

### 3.4 NodeMedia-Server 签名（自动刷新）

NodeMedia 的播放签名是一次性/有时效的。只要提供密钥，程序在**每次连接时**自动生成新签名，无需手动去面板复制链接：

```bash
export NODEMEDIA_AUTH_KEY='你的 Auth Key'     # 不是面板登录密码
jitsi-stream --room my-room --input 'rtmp://stream.example.com/app/stream'           # RTMP
jitsi-stream --room my-room --input 'https://stream.example.com:8443/app/stream.flv'  # HTTP-FLV
```

签名算法：`sign = <过期时间戳>-MD5("/app/stream-<过期时间戳>-<key>")`，有效期 1 小时。直接粘贴面板里复制的已签名 URL 也可以（程序会用密钥替换其中的 `sign`）。

密钥也可放在文件中（避免出现在环境/历史中）：`--sign-key-file /etc/jitsi-stream.key`（文件内容即密钥，建议 `chmod 600`）。

Windows PowerShell：`$env:NODEMEDIA_AUTH_KEY = "你的 Auth Key"`

> **只有一次性 URL、没有密钥时**：首次连接正常，但网络一断，重连会得到 HTTP 401，程序会立即退出并提示。要么提供密钥，要么用下一节的 `--input-cmd`。

### 3.5 一次性 URL：每次重连前执行命令获取新地址（`--input-cmd`）

适用于任何“每次播放都要新地址”的系统（不限于 NodeMedia）。命令的标准输出**最后一行非空文本**作为本次连接的输入 URL，每次（重）连接前都会重新执行，超时 30 秒：

```bash
# 调用你自己的接口/脚本获取新的播放地址
jitsi-stream --room my-room --input-cmd 'curl -fsS https://api.example.com/play-url?stream=VVD008'
jitsi-stream --room my-room --input-cmd './get-url.sh VVD008'
```

Linux/macOS 用 `/bin/sh -c` 执行，Windows 用 `cmd /C`。`--input` 与 `--input-cmd` 二选一。

### 3.6 本地文件、标准输入、测试源

```bash
jitsi-stream --room my-room --input ./video.mp4           # 按实时速率播放，结束后退出
jitsi-stream --room my-room --input 'C:\Videos\a.flv'      # Windows 路径
some-encoder | jitsi-stream --room my-room --input -       # stdin（不能重连）
jitsi-stream --room my-room --input test                   # 测试图 + 440 Hz 提示音
```

### 3.7 其他协议（FFmpeg）

`rtsp://`（默认 TCP）、`rtsps://`、HLS（`https://…/index.m3u8`）、`srt://`、`udp://` 交给 FFmpeg 处理，用法相同。这些协议已实现但尚未用真实源做端到端验证，生产使用前请先用第 6 节方法验收。

### 3.8 签名 URL 不想出现在进程列表中

```bash
export STREAM_INPUT='rtmp://live.example.com/app/stream?token=secret'
jitsi-stream --room my-room          # 未指定 --input 时读取 STREAM_INPUT
```

---

## 4. 参数参考

| 参数 | 默认 | 说明 |
|---|---|---|
| `--host` | `m.yuanjinapp.com` | Jitsi 域名或 `https://域名` |
| `--room` | （必填） | 房间名 |
| `--name` | `Live Stream` | 与会者显示名（支持中文/emoji） |
| `--input` | 环境变量 `STREAM_INPUT` | 输入 URL / 文件 / `-` / `test` |
| `--listen` | 关 | RTMP 推流接收模式（见 3.2） |
| `--input-cmd` | 无 | 每次（重）连接前执行的命令，输出新的输入 URL（3.5） |
| `--input-proxy` | 自动 | 仅 HTTP-FLV：空=自动（环境变量→系统代理→直连）、`direct`、或 `http://`/`https://`/`socks5://` 代理 |
| `--sign-key-file` | 无 | NodeMedia 签名密钥文件（替代 `NODEMEDIA_AUTH_KEY`） |
| `--meeting-retries` | `5` | 会议不存在时的重试次数（二进制指数退避），用尽退出；`0` = 不检查，自己创建会议并等待 |
| `--meeting-backoff` | `1s` | 首次退避时长，之后每次翻倍：1s、2s、4s、8s、16s |
| `--ffmpeg` | `ffmpeg` | FFmpeg 路径 |
| `--height` | `720` | 输出高度（偶数，144–2160），宽度按比例 |
| `--fps` | `25` | 输出帧率（1–60） |
| `--bitrate` | `1500` | VP8 码率 kbps（100–20000） |
| `--no-audio` | 关 | 只发视频（输入无音轨时必须加） |
| `--join-timeout` | `5m` | 等待会议中第二个人的上限 |
| `--connect-timeout` | `30s` | ICE/DTLS 媒体连接上限 |
| `--duration` | `0` | 总运行时长，0 = 一直运行 |
| `--retries` | `3` | 连续无媒体的失败次数上限（有媒体送达即清零） |
| `--debug-xmpp` | 关 | 打印原始信令（含 ICE 密码等敏感信息，勿外传） |
| `--version` | | 打印版本 |

环境变量：`STREAM_INPUT`（输入）、`NODEMEDIA_AUTH_KEY`（签名密钥）、`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`（HTTP-FLV 未指定 `--input-proxy` 时使用）。

退出码：`0` 正常结束（文件播完、`--duration` 到时且已发送过媒体、收到 Ctrl+C/SIGTERM）；`1` 参数错误、重试耗尽、会议不存在、一次性 URL 无法重连。

---

## 5. Docker / 服务器部署

### 5.1 docker run

```bash
docker run -d --name live-to-jitsi --restart unless-stopped --init --network host \
  --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges \
  -e NODEMEDIA_AUTH_KEY \
  ghcr.io/sbinfz/jitsi_rtmp_to_conference:latest \
  --host meet.example.com --room my-room --name '直播' \
  --input 'rtmp://live.example.com/app/stream'
```

RTMP 推流模式（host 网络下直接监听宿主机 1935）：

```bash
docker run -d --name obs-to-jitsi --restart unless-stopped --init --network host \
  ghcr.io/sbinfz/jitsi_rtmp_to_conference:latest \
  --host meet.example.com --room my-room --name 'OBS' \
  --listen --input 'rtmp://0.0.0.0:1935/live/长随机密钥'
```

- 推荐 `--network host`（Linux）：WebRTC 使用 UDP，host 网络最简单。不用 host 网络时需确保容器能访问 Jitsi 的 443/TCP 和 JVB 的 UDP 端口（docker-jitsi-meet 默认 10000/UDP）；推流模式另需 `-p 1935:1935`。
- 镜像非 root 运行，只读根文件系统可用。

### 5.2 Docker Compose

```bash
cp env.example .env     # 编辑 JITSI_ROOM、STREAM_INPUT 等
docker compose up -d
```

### 5.3 自行构建镜像

```bash
docker build -t jitsi-stream .
docker buildx build --platform linux/amd64,linux/arm64 -t <registry>/jitsi-stream --push .
```

### 5.4 systemd（裸机）

```ini
# /etc/systemd/system/jitsi-stream.service
[Unit]
Description=Live stream to Jitsi
After=network-online.target
Wants=network-online.target

[Service]
EnvironmentFile=/etc/jitsi-stream.env
ExecStart=/usr/local/bin/jitsi-stream --host meet.example.com --room my-room --name 直播 --retries 10
Restart=always
RestartSec=10
DynamicUser=yes

[Install]
WantedBy=multi-user.target
```

`/etc/jitsi-stream.env` 中写 `STREAM_INPUT=...`、`NODEMEDIA_AUTH_KEY=...`（`chmod 600`）。

### 5.5 网络受限时

- 服务器无法直连源站：把转换器部署到能访问源站的机器（它只需要能访问 Jitsi 的 HTTPS 和 JVB UDP），或通过 `--input-proxy` 使用代理（仅 HTTP-FLV）。
- 开发调试时临时借用本机代理（只绑定服务器回环地址，不要暴露公网）：

  ```bash
  ssh -NT -o ExitOnForwardFailure=yes -R 127.0.0.1:17897:127.0.0.1:7897 <user>@<server>
  # 服务器上：--input-proxy http://127.0.0.1:17897
  ```

---

## 6. 验收：不用浏览器确认音视频

`jitsi-receive` 以另一个与会者身份加入，请求视频转发，把收到的 VP8/Opus 存为文件：

```bash
jitsi-receive --host meet.example.com --room test-room --duration 60s --out recordings &
jitsi-stream  --host meet.example.com --room test-room --input test --duration 45s
wait
ffprobe recordings/video-*.ivf      # VP8 分辨率
ffmpeg -v error -i recordings/video-<ssrc>.ivf -f null -   # 无输出 = 解码无误
ffmpeg -v error -i recordings/audio-<ssrc>.ogg -f null -
```

`jitsi-receive` 退出码 0 表示音视频都收到了（只验视频加 `--video-only`）。仓库中的 `scripts/verify.sh` 自动完成以上步骤。

---

## 7. 常见问题

| 现象 | 原因 / 处理 |
|---|---|
| `meeting does not exist yet; retrying` 然后退出 | 会议还没人开。先在浏览器进入房间再启动；或增大 `--meeting-retries`/`--meeting-backoff`；或 `--meeting-retries 0` 让转换器自己开会议并等待 |
| 一直 `waiting for Jingle` | （`--meeting-retries 0` 时）会议里只有转换器一个人。打开浏览器加入同一房间，或延长 `--join-timeout` |
| `FFmpeg executable not found` | 安装 FFmpeg 或用 `--ffmpeg` 指定路径 |
| `ICE/DTLS failed` / connection timeout | 到 JVB 的 UDP 不通：检查防火墙、JVB 公网地址（`JVB_ADVERTISE_IPS`）、10000/UDP |
| `Stream map matches no streams` | 输入没有音轨，加 `--no-audio` |
| HTTP-FLV `401`，提示 one-time URL cannot be reused | 一次性签名重连时被拒：设置 `NODEMEDIA_AUTH_KEY`/`--sign-key-file` 自动签名，或用 `--input-cmd` 获取新地址 |
| 0 包很久，然后 `input read: unexpected EOF` | 到源站直连网络很差。看日志 `HTTP-FLV connecting via=`：若为 direct 而浏览器走代理，用 `--input-proxy http://127.0.0.1:端口` |
| RTMP 拉流一直 0 包 / 卡顿 | 到 1935 端口的网络慢或被限速（实测跨网络时出现），换 HTTP-FLV + 代理，或就近部署 |
| OBS 连接失败 | 检查 app/密钥是否与 `--input` 一致、端口是否放行、是否已有其他推流端在推 |
| `RTMP listen ...: address already in use` | 端口被占用（如另一个实例或 RTMP 服务器），换端口 |
| 画面有声音没画面 | 等待下一个关键帧（≤1–2 秒）；推流端关键帧间隔过大时调小 |
| CPU 占用高 | 降低 `--height`/`--fps`/`--bitrate` |
| 看不到/听不到但日志 `media sent` 在增长 | 用第 6 节方法排除浏览器端问题；检查浏览器是否把该与会者设为仅显示头像/静音 |

---

## 8. 安全建议

- 推流密钥、NodeMedia 密钥、带 token 的 URL 用环境变量传递，不写入命令行脚本或仓库。
- `--debug-xmpp` 输出含 ICE 凭据和桥接地址，只在排障时短时开启，不要上传日志。
- RTMP 推流模式为明文协议，密钥可被同网段嗅探：公网使用请走 VPN/专线，或仅监听 `127.0.0.1` 并在前置代理处做 TLS。
- 普通日志会隐藏 URL 中 `sign/sig/token/key/password/pwd/auth/jwt/secret` 参数及 `user:pass@`，但不保证 FFmpeg 所有自由格式输出都无敏感内容。
