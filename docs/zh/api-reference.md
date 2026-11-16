# NVR-05 API 与 OpenAPI 规范

## 1. 契约来源与优先级

默认 Web/API 地址为 `http://localhost:9090`，由 `server.listen` 修改。

1. **请求/响应 schema、已文档化状态码**：以运行实例 `/docs/` 加载的 `internal/docsportal/openapi.yaml` 为准。
2. **实际可用路由、权限和未覆盖行为**：以 `internal/api/handler.go` 的 `Routes()` 及各 handler 为准。
3. **本文**：规定通用调用约定和常用资源，不替代完整 OpenAPI 文档。

OpenAPI 当前覆盖主要业务接口，但并非 `Routes()` 中每条内部或实验路由都已建模。新增公开接口时必须同步补充 OpenAPI；不得把 Markdown 列表当作完整机器契约。

## 2. 认证

**API-001｜Basic Auth 是主认证方式。** 受保护接口使用：

```http
Authorization: Basic base64(username:password)
```

```bash
curl -u 'admin:replace-with-a-private-password' \
  http://localhost:9090/api/streams
```

**API-002｜查询令牌仅用于无法设置请求头的客户端。** `?token=` 的值是 `base64(username:password)`，不是登录接口签发的 Bearer Token。该值等价于账号密码，会进入 URL、历史和日志风险面；能使用请求头时不得使用查询令牌。

```bash
# 仅演示编码；不要把真实账号令牌写入共享日志或仓库。
printf 'admin:replace-with-a-private-password' | base64
```

**API-003｜登录不是会话签发。** `POST /api/auth/login` 复用认证中间件验证 Basic 凭据，成功返回 `{"status":"ok"}`；当前实现不设置会话 Cookie，也不返回访问令牌。

**API-004｜初始化接口有独立边界。** 未设置密码时受保护接口返回 `503 SETUP_REQUIRED`，而不是全部匿名可用。`POST /api/setup` 在已完成密码设置后返回 `409`。

## 3. 用户与权限

| 角色 | 读取受保护资源 | 操作权限 |
|------|----------------|----------|
| `super_admin` | 是 | 是；包括标有 `RequireOperatePermission` 的路由 |
| `user` | 是 | 默认否；受限写操作返回 `403` |

**API-101｜读取需要认证，写操作通常还需要操作权限。** 不得仅凭 HTTP 方法推断所有路由权限；例如个别设备控制或旧路由的注册方式可能不同，应以实际路由中间件为准。

**API-102｜旧单账号兼容。** 数据库没有用户时，系统回退到 YAML 中的全局账号，并赋予其 `super_admin` 语义；数据库已有用户时使用用户表、启用状态和角色。

**API-103｜失败限制。** 认证中间件按远端 IP 统计失败，达到当前阈值后返回 `429`。反向代理部署必须同时考虑真实客户端 IP 的识别和限流维度，不应把限流当作密码强度的替代。

## 4. 响应、错误与状态码

常见错误体：

```json
{ "error": "human-readable message" }
```

部分业务错误还包含机器码：

```json
{ "error": "setup required", "code": "SETUP_REQUIRED" }
```

| 状态 | 常见含义 |
|------|----------|
| `200` | 查询、更新或动作成功 |
| `201` | 资源创建成功，例如摄像头或录像计划 |
| `202` | 异步任务已接受，例如 IPTV 导入 |
| `400` | JSON、参数或业务校验失败 |
| `401` | 未认证或凭据错误 |
| `403` | 已认证但角色无操作权限 |
| `404` | 资源不存在 |
| `409` | 状态冲突，例如计划已存在、录像已锁定、初始化已完成 |
| `429` | 认证或接口限流 |
| `500` | 服务端或数据库操作失败 |
| `502` | 上游媒体、Emby、代理资源等访问失败 |
| `503` | 未初始化、未就绪或所需模块/模式不可用 |

**API-201｜错误文本不是稳定枚举。** 客户端应优先使用状态码和可选 `code`；除 OpenAPI 明确约定外，不得依赖英文错误句子做业务分支。

**API-202｜列表不是统一信封。** 不同资源使用不同响应结构，例如录像计划为 `{"plans":[]}`，流列表包含 `streams/total/limit/offset`，备份列表直接是数组。必须按接口 schema 解析。

## 5. 健康与公开能力

| 方法 | 路径 | 认证 | 语义 |
|------|------|------|------|
| GET | `/api/health` | 公开、限流 | 最小存活状态和 `setup_required` |
| GET | `/api/readyz` | 公开、限流 | 数据库、磁盘和 goroutine 就绪检查 |
| GET | `/api/health/cameras` | 当前公开路由 | 摄像头健康映射；部署者不应把它当作敏感详情的私有接口 |
| GET | `/api/capabilities` | 当前公开路由 | 协议能力摘要；具体端口和 URL 仍以流接口为准 |
| GET | `/models/{filename}` | 公开 | AI 浏览器模型静态资源 |
| GET | `/metrics` | 必须认证 | 独立 metrics 凭据或全局 NVR 凭据，见[安全规范](security.md) |
| GET | `/docs/` | 公开静态文档 | 当前实例的 OpenAPI 页面 |

`/api/readyz` 在数据库不可用、磁盘使用率至少 95% 或 goroutine 数量至少 5000 时返回 `503`。它不使用 `cleanup.disk_threshold_percent` 作为自身阈值。

## 6. 常用资源目录

下表是导航，不替代 OpenAPI 的 schema 和完整路由。

### 6.1 认证与用户

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/auth/login` | 验证 Basic 凭据 |
| POST | `/api/auth/logout` | 记录登出动作并返回状态；无服务端会话可销毁 |
| GET / POST | `/api/users` | 用户列表 / 创建用户（操作权限） |
| GET / PUT / DELETE | `/api/users/{id}` | 用户详情 / 更新 / 删除（操作权限） |
| GET / PUT | `/api/users/me/preferences/{namespace}` | 当前账号的 UI 偏好 |

### 6.2 设备与流

| 方法 | 路径 | 用途 |
|------|------|------|
| GET / POST | `/api/cameras` | 设备列表 / 添加设备 |
| GET / PUT / DELETE | `/api/cameras/{id}` | 设备详情 / 更新 / 归档式删除语义以 handler 为准 |
| DELETE | `/api/cameras/{id}/permanent` | 永久删除路径；操作前须确认录像处理语义 |
| POST | `/api/cameras/{id}/start` / `stop` | 启停设备 |
| POST | `/api/cameras/{id}/pause-recording` / `resume-recording` | 暂停/恢复该设备关联流的录像 |
| POST | `/api/cameras/test-connection` | 网络/端点连接测试，不证明可解码 |
| GET | `/api/streams` | 当前流及创建槽位，含状态和播放地址 |
| POST | `/api/streams` | 创建推流或拉流记录（操作权限） |
| GET / PUT / DELETE | `/api/streams/{stream_id}` | 流详情 / 更新 / 删除 |
| POST | `/api/streams/{stream_id}/promote` | 登记为设备，不自动录像 |
| POST | `/api/streams/{stream_id}/bind-camera` / `unbind-camera` | 设备绑定关系 |
| GET | `/api/streams/history` | 历史流 |
| GET / POST / DELETE | `/api/streams/bans`、`/api/streams/{stream_id}/ban` | 流封禁管理，具体方法以路由为准 |

### 6.3 实时播放

| 方法 | 路径 | 用途 |
|------|------|------|
| GET | `/api/streams/{stream_id}/stream/ws` | WebSocket/WebCodecs 媒体通道 |
| GET | `/api/streams/{stream_id}/stream.flv` | HTTP-FLV 代理 |
| GET | `/api/streams/{stream_id}/stream.m4s` | fMP4 代理 |
| GET | `/api/streams/{stream_id}/stream/*` | HLS 清单和资源代理 |
| POST | `/api/streams/{stream_id}/stream/webrtc` | 创建 WHEP 会话 |
| DELETE | `/api/streams/{stream_id}/stream/webrtc/{session}` | 删除 WHEP 会话 |

设备别名路径 `/api/cameras/{id}/...` 也存在。集成方应根据设备或纯流资源选择路径，并从响应获取实际 URL；不要拼接未经过路径转义的 ID。

### 6.4 录像计划、录像与回放

| 方法 | 路径 | 用途 |
|------|------|------|
| GET / POST | `/api/recording-plans` | 计划列表 / 创建计划 |
| GET / PUT / DELETE | `/api/recording-plans/{id}` | 计划详情 / 更新 / 删除 |
| GET | `/api/recordings` | 分页录像查询 |
| GET | `/api/recordings/timeline` | 时间轴 |
| GET | `/api/recordings/sources` | 录像来源；可包含归档 |
| GET / DELETE | `/api/recordings/{id}` | 录像详情 / 删除 |
| POST | `/api/recordings/{id}/lock` / `unlock` | 锁定 / 解锁 |
| GET | `/api/recordings/{id}/download` | 支持 Range 的下载/预览 |
| GET | `/api/cameras/{id}/playback/playlist.m3u8` | 连续 VOD 播放列表 |
| GET | `/api/cameras/{id}/playback/export.m3u8` | 时间范围导出播放列表 |

创建计划示例：

```bash
curl -u 'admin:replace-with-a-private-password' \
  -H 'Content-Type: application/json' \
  -d '{"stream_id":"front-door","name":"Front door","mode":"continuous","enabled":true}' \
  http://localhost:9090/api/recording-plans
```

### 6.5 IPTV 与 Emby

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/iptv/imports` | 导入 M3U，创建检测草稿 |
| GET | `/api/iptv/imports/{id}/items` | 检测项 |
| POST | `/api/iptv/imports/{id}/commit` | 纳管选中频道 |
| GET | `/api/iptv/channels` | 频道列表 |
| POST | `/api/iptv/channels/{id}/check` | 检测频道 |
| GET | `/api/iptv/channels/{id}/playback` | 浏览器代理播放信息 |
| PUT | `/api/iptv/channels/{id}` | 更新频道及 `publish_enabled` |
| GET / PUT | `/api/emby/config` | 查询/保存 Emby 连接 |
| GET | `/api/emby/library` | 搜索电影与剧集 |
| GET / POST | `/api/emby/items` | 已纳管项 / 添加媒体项 |
| PUT / DELETE | `/api/emby/items/{id}` | 更新发布/循环或移除 |

### 6.6 事件、观测与设置

| 方法 | 路径 | 用途 |
|------|------|------|
| GET | `/api/events` | 事件筛选与分页 |
| GET | `/api/events/stream` | SSE 实时事件 |
| GET / POST | `/api/events/rules` | 告警规则列表 / 创建 |
| POST | `/api/events/{id}/ack` | 确认事件 |
| GET | `/api/service-logs` / `/api/service-logs/stream` | 服务日志快照 / SSE（操作权限） |
| GET | `/api/observability/traces` | 本地调用链列表 |
| GET | `/api/observability/traces/{traceID}` | 调用链详情 |
| GET / PUT | `/api/settings` | 综合设置查询 / 更新 |
| GET / PUT | `/api/settings/hls`、`streaming`、`gb28181`、`voip`、`ai` | 分模块设置 |
| GET | `/api/config/check` | 检测配置文件外部变化 |
| POST | `/api/config/reload` | 显式重载配置；不等于所有监听器都自动重启 |
| POST | `/api/backup` | 创建数据库 SQLite 备份（操作权限） |
| GET | `/api/backups` | 备份文件名列表 |

### 6.7 专用业务

| 路径前缀 | 范围 | 文档 |
|----------|------|------|
| `/api/onvif`、`/api/cameras/{id}/onvif`、`.../ptz` | 发现、设备管理、录像和 PTZ | [ONVIF](onvif-guide.md) |
| `/api/gb28181` | 国标设备、目录、级联、回放、下载和对讲 | [GB28181](gb28181-guide.md) |
| `/api/voip`、`/api/settings/voip` | SIP 状态、外呼、接听、DTMF、对讲和历史 | [VoIP](voip.md) |
| `/api/jt808` | 实验性车载信令 | [配置规范](configuration.md#jt1078--jt808实验性) |
| `/api/transcoding` | 转码能力、模板、任务、租约和派生录像 | [转码部署](transcoding-deployment.md) |
| `/api/relay` | 中继任务与统计 | 代码路由和 OpenAPI |
| `/api/integrations/home-assistant` | HA 发布流、快照、录像、事件、WebRTC 和 PTZ | [Home Assistant](home-assistant.md) |
| `/api/sfu` | 实验性 SFU 房间、桥接和短期票据 | 实验文档与代码 |

## 7. 时间、分页和 URL 规则

**API-301｜时间使用明确格式。** 查询时间范围使用 RFC3339，除非接口另有约定。录像数据库存储使用 UTC；周计划窗口则使用服务器本地时间，两者不得混淆。

**API-302｜分页按响应执行。** 录像和流等接口使用 `limit` / `offset`，但默认值和最大值以 OpenAPI/handler 为准。客户端必须使用 `total` 判断是否继续，不应假定空页之前返回数量永远等于 limit。

**API-303｜路径参数必须转义。** `stream_id`、设备 ID 或国标通道可能包含特殊字符。使用 URL path segment 转义，并优先采用服务端返回的链接；不要把原始 ID 直接拼入路径或文件路径。

**API-304｜流式响应区分生命周期。** SSE、WebSocket、FLV、fMP4 和 WHEP 不是普通短请求。反向代理必须允许相应 Upgrade、长连接和足够超时，不能用一次性 JSON 请求的成功代替流式验收。

## 8. API 变更验收

1. 更新路由注册和 handler 测试。
2. 更新 OpenAPI 的路径、权限、请求体、响应体和错误状态。
3. 验证 `super_admin`、普通 `user`、匿名和错误密码四类调用。
4. 验证列表空值、分页、资源不存在、冲突和上游失败。
5. 对流式接口验证真实客户端连接、断开和代理行为，而不只检查路由编译。
6. 在本文补充资源导航；完整字段只维护一份 OpenAPI 事实源。
