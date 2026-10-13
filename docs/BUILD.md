# 编译、CI 与发布

## 本地编译

需要 Go ≥ 1.24（推荐 1.25）。纯 Go，`CGO_ENABLED=0`，无需 C 编译器。

```bash
make build        # 当前平台 → bin/
make test         # 单元测试 + race；装有 FFmpeg 时额外跑媒体流水线和 RTMP 鉴权集成测试
make lint         # gofmt + go vet
make package      # 全部平台 → dist/
```

国内网络可设置：`export GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.google.cn`

### 交叉编译（任意平台编译任意目标）

```bash
GOOS=windows GOARCH=amd64 go build -o jitsi-stream.exe ./cmd/jitsi-stream
GOOS=linux   GOARCH=arm   GOARM=7 go build ./cmd/jitsi-stream
```

`scripts/package.sh`（CI 使用同一脚本）生成的目标：

| 包 | GOOS/GOARCH |
|---|---|
| `jitsi-stream_<ver>_linux_amd64.tar.gz` | linux/amd64 |
| `jitsi-stream_<ver>_linux_arm64.tar.gz` | linux/arm64 |
| `jitsi-stream_<ver>_linux_armv7.tar.gz` | linux/arm, GOARM=7 |
| `jitsi-stream_<ver>_darwin_amd64.tar.gz` | darwin/amd64 |
| `jitsi-stream_<ver>_darwin_arm64.tar.gz` | darwin/arm64 |
| `jitsi-stream_<ver>_windows_amd64.zip` | windows/amd64 |
| `jitsi-stream_<ver>_windows_arm64.zip` | windows/arm64 |

只编译部分目标：`TARGETS="linux/amd64 windows/amd64" scripts/package.sh`。版本号默认取 `git describe`，可用 `VERSION=v1.2.3` 覆盖，写入 `--version`。

## GitHub Actions

### CI（`.github/workflows/ci.yml`）

每次推送到 `main` 和每个 Pull Request：

1. **test**：Ubuntu / macOS / Windows 三平台 `gofmt`、`go vet`、单元测试（Linux/macOS 带 race 检测；Linux 安装 FFmpeg，运行媒体与 RTMP 集成测试）。
2. **package**：交叉编译上表全部 7 个包，Linux 包做 `--version` 冒烟测试，上传为 Actions 产物 `jitsi-stream-packages`（保留 14 天）。
3. **docker**：buildx 构建 linux/amd64 + linux/arm64 镜像（只验证，不推送）。

### 发布（`.github/workflows/release.yml`）

```bash
git tag v1.0.0
git push origin v1.0.0
```

自动：运行测试 → 编译全部平台 → 创建 GitHub Release 并上传压缩包与 `SHA256SUMS` → 构建并推送多架构镜像 `ghcr.io/<owner>/<repo>:v1.0.0` 与 `:latest`（linux/amd64、arm64、arm/v7）。

也可在 Actions 页面手动运行 Release，填写已存在的 tag 重新发布。

注意：

- 私有仓库的 GHCR 镜像默认私有；拉取前需 `docker login ghcr.io`，或在 Packages 设置中改为公开。
- 推送包含 `.github/workflows/` 的提交需要具备 `workflow` 权限的凭据（SSH key 或带 `workflow` scope 的 token）。
- macOS 包未做 Apple 签名/公证；如需分发给普通用户，可在 Release 流程中加入 `codesign` + `notarytool`（需要 Apple Developer 账号）。

## 依赖更新

- Go 依赖版本由 `go.mod`/`go.sum` 固定；更新后运行 `make notices` 重新生成 `third_party/NOTICES.txt`。
- `third_party/jitsi` 是固定版本的 Jitsi 信令 fork，修改记录见 `third_party/jitsi/UPSTREAM.md`。升级 Jitsi 服务端版本后请用 `scripts/verify.sh` 重新做端到端验证。
