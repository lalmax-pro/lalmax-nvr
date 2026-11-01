# 测试规范

本文描述 lalmax-nvr 的测试策略、分层规范和媒体验收要求。测试工具链以 `go test` 和标准库为主，媒体 E2E 测试通过环境变量按需启用。

---

## 测试分层

```
单元测试        — 不依赖外部工具或网络，在任意环境运行
媒体集成测试    — 需要本机 FFmpeg / GStreamer，使用合成流
容器冒烟测试    — CI 在实际 Docker 容器中运行，验证链路可达
手动 / E2E 验收 — 实体摄像机、真实 HA 等，在 PR 正文中人工记录
```

---

## 单元测试规范

### 基本要求

- 所有单元测试必须通过 `go test -race ./...`
- 不依赖本机安装的 FFmpeg / GStreamer / 实体摄像机
- 外部依赖（数据库、媒体引擎、后端进程）通过接口注入，测试中使用 mock / stub
- 不持久化文件到测试目录之外，测试结束必须清理临时产物

### 可注入的测试边界

`internal/transcode` 包提供以下可注入点：

```go
// Executor 接口可替换为 mock，测试 Manager / Service 无需真实 FFmpeg
type Executor interface {
    Start(context.Context, SourceRef, Profile, string) (Process, error)
    Ready(context.Context, string) bool
}

// 时钟可注入，用于测试退避、超时等时间相关逻辑
```

### 竞态测试

涉及并发的包必须单独执行 race 回归：

```bash
go test -race -count=1 ./internal/transcode/...
go test -race -count=1 ./internal/api/...
go test -race -count=1 ./internal/media/...
```

重点关注场景：
- Manager.Acquire / Release 并发调用
- 任务 generation 并发更新与旧版本拒绝
- 服务关闭时的 context 取消传播

---

## 媒体集成测试

通过环境变量启用，需要本机安装对应工具。

### FFmpeg 媒体自检

```bash
NVR_TRANSCODE_MEDIA_TEST=1 go test -race -v ./internal/transcode/...
```

**前置条件**：`ffmpeg` 和 `ffprobe` 在 PATH 中可执行。

覆盖场景：
- 合成 H.264/AAC 输入 → H.264/AAC 输出
- 合成 H.265/AAC 输入 → H.264/AAC 转码
- 无音轨输入 → 纯视频派生输出
- video copy + 音频转码
- 视频 copy + 无音频（audio: none）

每个场景使用 FFmpeg 合成短时合成视频（不含真实摄像机画面），编码，再用 ffprobe 探测输出规格，最后用 FFmpeg 解码验证。

### GStreamer 集成测试

```bash
NVR_GSTREAMER_INTEGRATION_TEST=1 go test -race -v ./internal/transcode/...
```

**前置条件**：`gst-launch-1.0` 和 `gst-inspect-1.0` 在 PATH 中可执行，以及所需插件（base/good/bad）。

macOS 下 GStreamer Homebrew 安装可能需要设置动态库路径：

```bash
export DYLD_LIBRARY_PATH=/opt/homebrew/lib:$DYLD_LIBRARY_PATH
NVR_GSTREAMER_INTEGRATION_TEST=1 go test -race -v ./internal/transcode/...
```

---

## 媒体验收规范

媒体 E2E 验收**不以进程启动成功**作为通过标准，必须满足：

1. **实际取回**：派生输出经真实协议（RTSP / HTTP-FLV / HLS）实际取回
2. **实际解码**：使用 `ffmpeg` 或 `ffprobe` 解码，确认视频/音频编码规格
3. **exit code 0**：解码命令以 exit 0 退出，无解码错误日志
4. **协议一致**：不把"管理员 API 返回 200"等同于"播放链路通"

### 记录格式

每次阶段验收在 PR 正文或对应 Issue 中追加记录：

```markdown
### 验收记录 — YYYY-MM-DD

- **提交 / PR**：`abcdef1`
- **环境**：macOS ARM64 / Linux ARM64 容器 / ...
- **后端**：FFmpeg 8.0.1 / GStreamer 1.24.x
- **输入**：合成 H.264/AAC 640x360 / 海康 H.264/AAC 实体 / ...
- **输出**：H.264/AAC 派生 RTSP
- **验证命令**：
  ```sh
  ffmpeg -rtsp_transport tcp -i rtsp://... -t 3 -f null - 2>&1
  ```
- **结果**：exit 0，ffprobe 确认 H.264 640x360 / AAC 48000
- **未覆盖**：实体 H.265、长期运行、多架构
```

### 合成流 vs 实体摄像机

| 级别 | 说明 |
|------|------|
| 合成流验收 | 使用 FFmpeg/GStreamer 生成的测试流，可在 CI / 本机随时复现 |
| 实体设备验收 | 使用真实摄像机（注明品牌/型号/编码），不能以合成流替代 |
| 真实 HA 验收 | 在实际 Home Assistant 实例中验证 HLS 播放、快照、事件 |

实体验收结果不上传真实画面，使用命令行 exit code 和 ffprobe 统计作为证据。

---

## 前端测试

```bash
cd web
npm install
npm test        # 使用 vitest --run 单次执行（不进入 watch 模式）
```

- 组件测试覆盖主要用户交互流程
- 媒体播放器（mpegts.js / WebCodecs）的实际播放由手动验收完成

---

## CI 中的测试执行

CI（`build.yml`）执行：

```bash
go test -race ./...
go vet ./...
```

媒体集成测试（`NVR_TRANSCODE_MEDIA_TEST`、`NVR_GSTREAMER_INTEGRATION_TEST`）在 CI 中**不自动启用**，需要在带有对应工具的环境中手动运行，结果记录在 PR 中。

容器冒烟测试（`container-smoke.yml`）在 Docker 容器中验证 NVR 启动和基本 API 可达性，不执行媒体转码链路。

---

## 不应做的事

- 不以 CI 绿灯替代媒体验收通过
- 不以进程启动 / API 返回 200 替代解码成功
- 不把合成流验收记作实体摄像机验收
- 不在测试中引入外部网络依赖（下载 FFmpeg、连接公网摄像机）
- 不在测试代码中硬编码本机路径
- 不在 PR 合并后才发现 race 问题（提交前本地执行 `-race`）
