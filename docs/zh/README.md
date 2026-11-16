# lalmax-nvr 技术规范与知识库

[English guides](../en/README.md) · [项目说明](../../README.zh.md) · [GitHub Wiki 发布](../GITHUB_WIKI.md)

本目录是项目的知识库源文件。核心文档按技术规范编写，专题文档提供具体设备或第三方服务的操作步骤；运行中的 NVR 在 `/docs/` 提供 OpenAPI 交互文档。

## 1. 文档效力与阅读约定

- **代码是实现事实源**：配置以 `internal/config`、路由以 `internal/api/handler.go`、接口模型以 `internal/docsportal/openapi.yaml` 为准。
- **核心规范优先于历史设计稿**：`design`、`plan`、`validation`、`tracking-issue` 等文档记录设计或验证过程，不作为当前行为承诺。
- **必须 / 不得**表示实现约束或安全底线；**应 / 不应**表示无特殊原因时应遵守的部署约定；**可**表示可选能力。
- 规范条目的 `ARCH`、`CFG`、`MEDIA`、`REC`、`API`、`SEC`、`OPS`、`DEV` 编号用于评审和追踪，不表示 HTTP 状态码。
- 文档中的端口均为默认值；实际监听地址、开关和播放地址必须以运行配置及 API 响应为准。

## 2. 核心规范

| 编号 | 文档 | 规定内容 | 主要读者 |
|------|------|----------|----------|
| NVR-01 | [系统架构](architecture.md) | 进程边界、核心实体、数据流、模块和端口 | 架构师、开发者、运维 |
| NVR-02 | [媒体与流](media-sources.md) | 接入方式、流标识、播放协议、来源生命周期 | 开发者、集成方 |
| NVR-03 | [录像与回放](recording-plans.md) | 录像计划、调度、片段、合并、清理和 VOD | 开发者、运维 |
| NVR-04 | [配置规范](configuration.md) | 配置加载、默认值、字段约束和敏感信息 | 运维、开发者 |
| NVR-05 | [API 与 OpenAPI](api-reference.md) | 认证、权限、错误、资源边界和常用接口 | 集成方、开发者 |
| NVR-06 | [安全规范](security.md) | 账号、媒体凭据、公开端点、网络和密钥 | 运维、安全评审 |
| NVR-07 | [部署与运行](deployment.md) | 部署拓扑、反向代理、备份、升级和观测 | 运维 |
| NVR-08 | [开发与验证](development.md) | 代码地图、修改约束、构建和测试基线 | 贡献者 |

## 3. 使用入口

| 任务 | 入口 |
|------|------|
| 首次安装并接入一路流 | [快速入门](getting-started.md) |
| 判断设备应使用哪种接入协议 | [媒体与流](media-sources.md) |
| 排查“有直播但没有录像” | [录像与回放](recording-plans.md) → [故障排除](troubleshooting.md) |
| 修改 YAML 或环境变量 | [配置规范](configuration.md) + [完整配置示例](../../config/config.example.yaml) |
| 调用 REST 接口 | [API 规范](api-reference.md) + 运行实例的 `/docs/` |
| 上线到非本机或公网环境 | [安全规范](security.md) + [部署与运行](deployment.md) |
| 发布或维护 GitHub Wiki | [Wiki 发布指南](../GITHUB_WIKI.md) |

## 4. 设备、协议与业务专题

以下专题是核心规范的操作补充；设备兼容性和第三方服务行为以对应专题及实测为准。

### 4.1 设备与媒体源

- [RTSP / HTTP 摄像头](camera-guide.md)
- [ONVIF 发现、取流与 PTZ](onvif-guide.md)
- [GB28181 设备、回放与对讲](gb28181-guide.md)
- [SIP VoIP 呼叫与对讲](voip.md)
- [小米摄像头](xiaomi-setup.md)
- [IPTV](iptv.md)、[Emby](emby.md)、[MediaMTX](mediamtx-guide.md)
- [实时转码部署](transcoding-deployment.md)与[转码设计](transcoding-design.md)

### 4.2 集成与文件访问

- [Home Assistant / HACS](home-assistant.md)
- [MQTT 事件触发](mqtt-integration.md)与[事件中心](events.md)
- [WebDAV](webdav-integration.md)、[FTP](ftp-integration.md)、[DLNA](dlna.md)
- [AI 检测](ai-setup-guide.md)

### 4.3 实现与运维补充

- [录像内部流程](recording-flow.md)：组写入器、样本时间戳和内部调用顺序。
- [数据库配置与迁移](database-migration.md)：SQLite、PostgreSQL、MySQL。
- [故障排除](troubleshooting.md)：按“进程 → 来源 → 流 → 播放 → 录像”定位。
- [发布与自动构建](releases.md)。

## 5. 文档维护规则

1. 修改默认行为、路由或数据模型时，必须在同一变更中更新对应核心规范。
2. 新增配置字段时，必须同时更新 `internal/config`、`config/config.example.yaml` 和[配置规范](configuration.md)。
3. 新增或变更公开 API 时，必须同步更新 `internal/docsportal/openapi.yaml`；不要仅在 Markdown 中补一条路径。
4. 实验能力、未经真实设备验证的能力及“配置存在但未生效”的字段必须明确标注。
5. 不得在文档中提交真实密码、令牌、DSN、私钥或可用内网地址。
6. Wiki 页面由本目录生成；不要在 GitHub 网页端长期维护同名页面，以免下一次同步覆盖修改。
