# 贡献指南

欢迎参与 lalmax-nvr 的开发。本文说明如何搭建开发环境、提交代码和通过 CI 检查。

---

## 前置依赖

| 工具 | 版本要求 | 说明 |
|------|---------|------|
| Go | 见 [`.go-version`](../../.go-version) | `CGO_ENABLED=0` 构建，无需 cgo |
| Node.js | 见 [`.nvmrc`](../../.nvmrc) | 前端（Svelte）构建 |
| Docker | 20.10+ | 运行集成测试 / 容器构建 |
| FFmpeg | 任意版本 | 媒体相关测试（非必须，测试时按需安装） |

---

## 快速搭建

```bash
git clone https://github.com/lalmax-pro/lalmax-nvr.git
cd lalmax-nvr

# 构建后端 + 前端
./scripts/unix/build.sh

# 启动（首次自动从 config.example.yaml 复制配置）
./scripts/unix/run.sh
```

Windows 用户使用 `scripts/windows/` 下对应脚本，详见 [`scripts/README.md`](../../scripts/README.md)。

---

## 目录结构

```
cmd/lalmax-nvr/        # 程序入口、CLI、集成测试
internal/              # 核心业务包（见 module-guide.md）
  api/                 # HTTP 路由与 handler
  camera/              # 摄像头生命周期
  transcode/           # 转码管理器与后端
  media/               # lalmax 媒体引擎适配
  recorder/            # 组写入器与录像
  ...
web/                   # Svelte 前端源码
docs/zh/               # 中文文档（当前目录）
docs/en/               # 英文文档
config/                # 配置示例
scripts/               # 构建与运行脚本
```

各包职责详见 [模块职责说明](module-guide.md)。

---

## 测试

### 单元测试

```bash
# 全量（不含媒体 E2E）
go test ./...

# 带竞态检测（推荐，CI 要求）
go test -race ./...

# 特定包
go test -race -v ./internal/transcode/...
```

### 媒体集成测试

媒体相关测试通过环境变量启用，需要本机安装对应工具：

```bash
# FFmpeg 媒体自检（需要 ffmpeg + ffprobe）
NVR_TRANSCODE_MEDIA_TEST=1 go test -race -v ./internal/transcode/...

# GStreamer 集成测试（需要 gst-launch-1.0）
NVR_GSTREAMER_INTEGRATION_TEST=1 go test -race -v ./internal/transcode/...
```

媒体测试只使用合成流，不连接真实摄像机。测试策略详见 [测试规范](testing-guide.md)。

### 前端测试

```bash
cd web
npm install
npm test        # 单次运行（CI 模式）
```

---

## 代码规范

### Go

- `gofmt` 格式化（CI 强制检查）
- `go vet` 静态分析（CI 强制检查）
- 不引入 CGO 依赖，保持 `CGO_ENABLED=0` 可构建
- 新建包需在包头注释说明职责
- 接口定义放在消费方包内（Go 惯例），不在实现方

### 提交信息

遵循 [Conventional Commits](https://www.conventionalcommits.org/)：

```
<type>(<scope>): <简短描述>

[可选正文]

[可选 footer，如 Fixes #66]
```

常用 type：

| type | 含义 |
|------|------|
| `feat` | 新功能 |
| `fix` | Bug 修复 |
| `refactor` | 重构（不改变行为） |
| `docs` | 文档变更 |
| `test` | 测试相关 |
| `chore` | 构建、依赖、工具链 |

scope 使用受影响的包名，如 `transcode`、`api`、`recorder`。

示例：
```
feat(transcode): add GStreamer backend with fixed pipeline templates
fix(transcode): preserve explicit stop intent across restarts
docs: restructure docs index by reader type
```

---

## Pull Request 流程

1. **Fork → 功能分支**：分支名建议 `feat/<scope>-<brief>` 或 `fix/<scope>-<brief>`
2. **本地通过**：`go test -race ./...` + `go vet ./...` + 前端测试
3. **描述清楚**：PR 标题遵循提交规范；正文说明变更范围、验证方式、已知限制
4. **不要自动合并**：涉及媒体验收的功能 PR 保持 Draft 直到验收完成
5. **一个 PR 一件事**：大功能拆分为独立 P0/P1/P2 阶段分开提交

### PR 描述模板

```markdown
## 变更说明
<!-- 简述改了什么、为什么改 -->

## 验证方式
<!-- 单元测试 / 媒体 E2E / 手动验证步骤 -->

## 已知限制
<!-- 未覆盖的场景、TODO、后续 Issue -->

## 关联
<!-- Fixes #xx / Tracks #xx -->
```

---

## CI 说明

CI 由 `.github/workflows/build.yml` 驱动，覆盖：

- Go 全平台构建（Linux amd64/arm64/armv7、Windows amd64、macOS amd64/arm64）
- `go test -race`、`go vet`
- 前端构建与测试
- HA 集成包打包校验（`hacs-validation.yml`）
- Docker 容器构建与冒烟测试（`container-smoke.yml`）
- Windows 转码包交叉编译（`transcode-windows.yml`）

CI 通过是合并的必要条件，但不是媒体验收通过的充分条件——媒体 E2E 在 PR 正文中单独记录。

---

## 发版

版本发布流程见 [发布与自动构建](releases.md)。

---

## 问题与讨论

- Bug 和功能请求：[GitHub Issues](https://github.com/lalmax-pro/lalmax-nvr/issues)
- 设计讨论：在相关 Issue 或 PR 中进行，重大决策记录到 [ADR](adr/README.md)
