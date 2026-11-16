# NVR-08 开发与验证规范

## 1. 工程事实源

| 范围 | 工具/版本来源 |
|------|---------------|
| Go | 根目录 `go.mod` 与 `.go-version`；当前模块声明 Go 1.26.2 |
| Web | `web/package.json`、`web/package-lock.json`、`.nvmrc`；CI 使用 Node.js 22 |
| 前端 | Svelte 5、Vite、TypeScript/JavaScript 混合现有代码 |
| HTTP | chi；中间件位于 `internal/middleware` |
| 数据库 | `database/sql`；SQLite（modernc）、PostgreSQL（pgx）、MySQL |
| 媒体 | vendored `third/lal`、`third/lalmax`，根 `go.mod` 使用 replace |
| API 文档 | `internal/docsportal/openapi.yaml`，OpenAPI 3.1 |
| 官网 | `site/`，GitHub Pages 工作流独立部署 |

不要假定未在 manifest/lockfile 中出现的库可用。新增依赖必须说明必要性并匹配项目现有实现方式。

## 2. 代码修改地图

| 变更类型 | 必看位置 | 同步文档/验证 |
|----------|----------|---------------|
| 启动、CLI、监听、模块组装 | `cmd/lalmax-nvr/main.go` | CLI/启动测试；架构、配置或部署规范 |
| 配置字段、默认值、校验 | `internal/config` | 配置测试、示例 YAML、NVR-04 |
| API 路由和权限 | `internal/api/handler.go` 与对应 handler | handler 测试、OpenAPI、NVR-05 |
| 用户认证与 RBAC | `internal/auth`、`internal/middleware`、`internal/model/user.go` | 匿名/管理员/普通用户测试、NVR-06 |
| 设备生命周期 | `internal/camera`、`internal/media` | 设备与媒体测试、NVR-01/02 |
| 录像策略 | `internal/storage/db_recording_plan.go`、`internal/recorder` | 计划/调度/真实片段测试、NVR-03 |
| 文件和数据库 | `internal/storage` | 路径、原子生命周期、UTC、三数据库契约 |
| 连续回放 | `internal/vod`、`internal/api/handlers_vod.go` | 时间范围、缺口、Range/播放列表测试 |
| 前端 | `web/src` | 现有 Vitest 测试和生产构建 |
| 内嵌 API 页面 | `internal/docsportal` | OpenAPI 解析和 `/docs/` 实际访问 |
| GitHub Wiki | `docs/zh`、`docs/wiki`、`scripts/build_wiki.py` | 本地构建、链接检查、工作流语法 |

子目录存在自己的 `AGENTS.md` 时，必须阅读并遵守其局部约定。它们记录具体实现约束，不应仅因重写公共文档而忽略。

## 3. 核心不变量

**DEV-001｜设备存在不等于录像。** 录像计划绑定 `stream_id`，不得通过新增设备或 promote 隐式开录。

**DEV-002｜媒体接入与写盘解耦。** 停止写盘不应无故中断直播；组写入器覆盖路径使用写开关，record task 由调度器管理，二者不得双写。

**DEV-003｜媒体回调不得阻塞上游。** 现有 StreamHub/组订阅采用有界队列和慢消费者保护。新增回调必须明确 Clone 所有权、队列满策略和停止时的锁顺序。

**DEV-004｜片段在关键帧边界收尾。** 新段需要参数集和关键帧；目标时长不是严格墙钟承诺。临时文件/目录关闭后原子重命名，再建立索引。

**DEV-005｜存储时间统一 UTC。** 数据库存储使用 UTC 兼容格式，解析保留旧格式兼容。不得写入带 Go monotonic clock 的 `time.Time.String()`。周计划窗口是业务本地时间，需在 API 文档中区分。

**DEV-006｜文件路径不可信。** 使用存储层路径校验和来源 ID 编码；不得直接拼接用户提供的 ID，不得引入 `..`、绝对路径或 symlink 绕过。

**DEV-007｜认证以实际路由为准。** 新敏感路由默认放入受保护组，写操作显式评估 `RequireOperatePermission`。不得因相邻旧路由遗漏权限而复制其例外。

## 4. API 开发规则

1. 在 `Handler.Routes()` 注册，选择公开、认证或操作权限边界。
2. 使用项目 `writeJSON` / `writeError` / `writeAPIError` 等现有响应方式，不另建平行的通用框架。
3. 在 handler 测试中覆盖成功、错误 JSON、缺字段、未认证、无权限、资源不存在和冲突。
4. 更新 `internal/docsportal/openapi.yaml` 的路径、schema、权限、安全要求和响应。
5. 流式接口必须验证真实 SSE/WebSocket/FLV/fMP4/WHEP 生命周期；只调用 handler 返回 200 不足以覆盖代理和断开。
6. 密码、令牌、完整含秘密 URL 和 DSN 不得写入响应、日志、追踪或测试快照。

OpenAPI 当前未覆盖每条实验/内部路由；不能把“未出现在规范”当作公开稳定 API 的证据。

## 5. 构建与测试命令

### 5.1 标准前端与后端构建

```bash
./scripts/unix/build.sh
```

该脚本安装前端依赖、构建并复制到嵌入目录、编译 Go 代码和最终二进制。Windows 使用项目 PowerShell/batch 入口，不在 Windows 上调用 Unix shell 脚本。

单独前端：

```bash
cd web
npm ci
npm test
npm run build
```

### 5.2 Go 基线

```bash
go vet ./...
go test ./...
```

CI 的核心测试更严格，使用 race、coverage 和超时：

```bash
go test -race -timeout 30m ./...
```

vendored 引擎是独立 Go module，根 `./...` 不一定覆盖其全部测试；修改 `third/lalmax` 或 `third/lal` 时必须进入对应模块运行受影响包测试。CI 还覆盖 SFU、RTC、server、VoIP 和 lal RTMP 等特定套件。

### 5.3 文档与 Wiki

```bash
python3 scripts/build_wiki.py --output tmp/wiki
python3 scripts/check_docs.py
```

文档构建只生成 Wiki 文件，不会自动 push；推送由显式手动操作或主分支 Wiki 工作流完成。命令的完整参数见[Wiki 发布指南](../GITHUB_WIKI.md)。

## 6. 变更所需验证等级

| 变更 | 最低验证 | 必须增加的场景 |
|------|----------|----------------|
| 仅文字且不改行为 | Markdown 链接和事实源检查 | 文档中的命令/路径存在 |
| 配置 | 配置单元测试、启动校验 | 默认值、显式 false、非法值、保存/重载 |
| API | 相关 handler 测试、OpenAPI 检查 | 四种认证角色/状态和实际响应 |
| 媒体接入 | 相关 media/camera 测试 | 至少一次真实协议推拉流（可用测试服务） |
| 录像/封装 | recorder/storage/muxer 测试 | 生成可解码片段，覆盖关闭、断流、恢复 |
| 前端播放 | Vitest、生产构建 | 真实浏览器播放及断开/重连 |
| 数据/schema | 存储测试和目标 DB 契约 | 迁移、重启、备份/恢复；不能只测 SQLite 后声称其他 DB 通过 |
| 安全/认证 | 中间件和路由测试 | 直接媒体端口、查询令牌、权限提升和敏感日志 |
| Wiki 发布 | 本地生成和链接检查 | 初始化后的 Wiki 克隆、推送和页面展示 |

**DEV-101｜不以编译代替端到端验收。** 最终交付必须运行覆盖变更的标准构建/测试，并执行用户场景中的真实调用。若外部设备或第三方服务不可用，必须明确说明未验证部分，不能把模拟结果写成真实设备兼容证明。

## 7. 文档工程规则

- 核心规范以当前实现为准，历史方案放在 design/plan/validation 文档并明确其非承诺属性。
- 规范条目使用稳定编号；修改含义时同步相关链接和验收清单。
- 同一字段的完整 schema 只在代码/OpenAPI 维护，Markdown 提供规则和导航，避免复制整份定义。
- 文档中的命令不得包含真实凭据；使用 `replace-with-...` 占位符。
- 文件路径、默认值和“必须”表述必须能从代码或测试定位；无法确认的行为标为限制或假设，不能写成事实。
- Wiki 正文从主仓库生成。修改 Wiki 展示结构时，修改 `docs/wiki` 和构建映射，而不是只改线上页面。

## 8. 提交前检查

1. `git status` 中仅包含预期文件；构建/test 产生的临时产物不得混入文档变更。
2. 相关构建与测试通过；失败中与变更无关的问题应单独说明，不得顺手修改无关业务。
3. 新配置、路由、角色或数据模型已同步核心规范和 OpenAPI。
4. 无真实秘密、内部敏感地址或隐私样本。
5. 实验性、第三方依赖、真实设备未验证和性能边界已明确标注。
