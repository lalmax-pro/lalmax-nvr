# NVR-06 安全规范

## 1. 适用范围

本文规定账号、媒体发布/播放、公开端点、网络暴露、文件访问和配置密钥的当前行为与部署底线。安全约束以实际中间件和顶层路由为准，不能仅阅读配置结构体注释。

## 2. 身份与权限模型

**SEC-001｜初始化必须收敛到认证状态。** 未配置密码时，受保护 API 返回 `503 SETUP_REQUIRED`。首次设置完成后必须使用 bcrypt 哈希凭据；不得把“设置模式”描述为全部 API 长期匿名开放。

**SEC-002｜密码不明文持久化为常规流程。** `init` 和 Web 初始化生成 bcrypt 哈希。YAML 中仅设置 `auth.password` 时，启动会生成哈希、写回 `password_hash` 并清除明文；同时存在时哈希优先。

**SEC-003｜多用户启用后使用数据库角色。** 用户表无记录时回退 YAML 全局管理员；有用户时按用户启用状态和 `super_admin` / `user` 角色鉴权。普通用户不能被假定拥有配置、删除、PTZ 预设或其他操作权限。

**SEC-004｜Basic 查询令牌等同密码。** `?token=base64(username:password)` 只用于无法设置 Authorization 的媒体/SSE/WebSocket 客户端。必须优先使用请求头；不得不必要地复制、记录或公开该 URL。

## 3. 认证边界

| 入口 | 当前认证行为 | 部署要求 |
|------|--------------|----------|
| NVR `/api` 受保护路由 | Basic Auth；多用户或旧全局账号 | 必须配置强密码 |
| `/metrics` | 有独立 `metrics_auth` 时使用该凭据；否则回退全局 NVR 认证中间件 | 不得按旧注释当作公开端点 |
| embedded lalmax HTTP 播放 | `media.http_play_auth_enabled` 默认 true，使用 NVR 账号 | 公网或不可信网络不得关闭 |
| embedded 内部媒体控制 | 内部回环令牌或 NVR 用户认证 | 不得把内部令牌暴露给客户端 |
| RTMP 发布 | 默认按流令牌认证 | 不得为了省事默认关闭 |
| SRT / WHIP 发布 | 按流令牌 | 每个流使用独立高熵值 |
| RTSP 播放/发布 | 原生 Basic/Digest 与共享发布令牌是不同控制面 | 按实际协议分别配置 |
| WebDAV | 全局 NVR Basic Auth；默认只读 | 非必要不启用写模式 |
| FTP | 全局 `auth.username`，支持明文或 bcrypt 哈希校验；不支持匿名和 Web 多用户 | 默认开启但应仅在可信网络使用 |
| Home Assistant | 使用 NVR 集成的读/操作权限和独立播放配置 | 遵循最小权限及专用账号 |
| SFU | 实验性；信令使用短期、单次票据并检查 Origin | 仅启用所需监听，管理操作需权限 |

**SEC-101｜HTTP 播放开关不影响其他面。** 设置 `media.http_play_auth_enabled: false` 只放开对应 embedded HTTP 播放入口，不应被解释为同时关闭 NVR 管理、内部控制或所有发布认证。

**SEC-102｜播放令牌与账号令牌不是同一概念。** `auth.playback_tokens` 是按流媒体共享令牌；API 的 `?token=` 可表示 base64 账号凭据。二者同时使用时必须遵循实际 URL 构造，优先用 HTTP Basic 传账号、按流令牌传媒体授权，避免把两个“token”混为一谈。

## 4. 公开与最小信息接口

当前 `/api/health`、`/api/readyz`、`/api/health/cameras`、`/api/capabilities`、`/models/{filename}`、`/docs/` 以及首次设置相关路由存在公开入口；SFU 页面和信令另有票据/Origin 校验。

**SEC-201｜公开不等于无信息。** 健康摄像头映射、能力和设置向导可能暴露部署状态。公网部署应通过网关限制不需要公开的入口；不能仅依赖“健康接口”这一名称推断其信息含量。

**SEC-202｜存活与就绪公开只用于探针。** `/api/health` 不包含详细组件检查，`/api/readyz` 只返回就绪状态；详细健康数据位于受保护的 `/api/health/details`。

## 5. 网络与传输安全

**SEC-301｜管理面优先私网或受控网关。** 对公网提供 Web UI 时必须使用 HTTPS 反向代理、强认证和最小暴露。安全响应头不能替代 TLS。

**SEC-302｜只开放实际协议端口。** FTP、RTMP、RTSP、SRT、GB28181、JT808 和 DLNA 都有独立攻击面。未使用时应关闭模块或不映射端口，而不是仅依赖页面隐藏入口。

**SEC-303｜明文协议不得跨不可信网络。** FTP 没有 TLS；RTMP、RTSP、HTTP 和 SIP 是否加密取决于具体配置。跨公网或不可信网络应使用加密协议或受控隧道。

**SEC-304｜匿名发布是显式高风险例外。** `rtmp.publish_auth_enabled: false` 或 `media.rtsp_publish_auth_enabled: false` 仅可用于边界可信的网络；即使允许匿名，封禁检查也不能替代身份认证。

**SEC-305｜媒体地址与 NAT 必须真实可达。** GB28181、VoIP 和 WebRTC 的 SDP/ICE 通告地址若错误，可能既不可用又把会话导向错误目标。应使用最小端口范围、明确防火墙和端到端媒体测试。

## 6. 配置与媒体密钥

| 配置 | 用途 | 规则 |
|------|------|------|
| `auth.password_hash` | 全局 NVR 登录 | 使用 `hash-password` 或初始化生成 |
| `auth.publish_tokens` | 共享媒体发布令牌 | 按流独立设置，不与登录密码复用 |
| `auth.playback_tokens` | 按流播放共享令牌 | 与 API Basic 凭据区分 |
| `rtmp.stream_keys` / `srt.stream_keys` / `whip.publish_tokens` | 协议推流 | 只给需要发布的客户端 |
| `media.rtsp_username/password` | 原生 RTSP 认证 | 启用时必须成对配置；Digest/Basic 仍需考虑传输安全 |
| `metrics_auth` | 独立 Prometheus 认证 | 未配置时回退全局 NVR 认证，不是公开 |
| `NVR_ENCRYPTION_KEY` / `NVR_ENCRYPTION_KEY_FILE` | 配置敏感字段 AES-256-GCM 加密 | 密钥为 32 字节原始值或其 base64，必须与密文分离保管 |

**SEC-401｜密钥生命周期必须独立管理。** Web 安全设置可将密钥写入 `<config-path>.encryption-key` sidecar；该机制只允许设置一次。密钥丢失会导致已加密配置不可读，备份时必须安全地单独保存，不能与配置密文放在同一公开位置。

**SEC-402｜加密配置文件不等于仓库安全。** 真实配置、密钥、DSN、令牌和私钥不得提交到主仓库、Wiki 或 Issue。文档示例必须使用明显占位符。

**SEC-403｜Wiki 公开性跟随仓库。** 公共仓库 Wiki 对公众可见；不得将内部运维手册中的真实地址、恢复步骤密钥或凭据发布到 Wiki。

## 7. 文件访问与数据完整性

**SEC-501｜WebDAV 默认只读。** `read_write=true` 会开放 PUT、MKCOL、DELETE、COPY、MOVE、LOCK、UNLOCK 等能力；文件操作不一定同步所有数据库状态，必须评估完整性风险。

**SEC-502｜FTP 根目录是整个存储根。** FTP 支持写和删除，并使用全局账号而非 Web 多用户。虽然当前实现支持 `password_hash`，明文 FTP 仍会暴露传输中的凭据和内容；不需要时应设 `ftp.enabled: false`。

**SEC-503｜下载和上传必须约束路径。** 存储实现提供路径校验和来源 ID 编码；新增文件接口不得绕过这些边界、接受路径穿越或把用户输入直接拼为物理路径。

**SEC-504｜锁定不是访问控制。** 录像锁定用于防清理/删除，不表示对无权限用户保密。访问仍由 API 或文件服务认证决定。

## 8. 日志、追踪与隐私

- 服务日志和调用链应默认视为敏感运维数据；服务日志接口要求操作权限。
- 业务追踪应避免记录完整流 URL、查询参数、用户名、密码和令牌。
- 外部 OTLP/HTTP、远程日志和第三方 AI 会把数据发送到其他系统；启用前必须确认 endpoint、TLS、访问控制和数据保留。
- 不要在 Issue、Wiki、截图或测试附件中包含真实用户隐私、人脸画面、家庭地址或生产令牌。

## 9. 上线安全检查

1. 完成初始化，普通用户与管理员权限分别验证。
2. 所有管理 API 使用 HTTPS 或仅监听受控网络；验证匿名请求失败。
3. 用实际请求验证 `/metrics`、直连 `12090`、原生 RTSP/RTMP 和 WebDAV/FTP，而非只测试 Web UI。
4. 检查每个推流协议的发布令牌，确认不存在意外匿名发布。
5. 核对防火墙和端口映射，关闭未使用的 FTP、JT、SIP 和媒体入口。
6. 将配置密钥与密文分离备份，并验证恢复所需材料和权限。
7. 检查代理、应用日志、浏览器历史和 Wiki 中没有账号查询令牌或其他秘密。
8. 对公网部署明确限制健康详情、设置和文档等公开入口。
