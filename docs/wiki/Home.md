# lalmax-nvr 知识库

lalmax-nvr 是基于 lal/lalmax 媒体引擎的网络视频录像机。本 Wiki 面向部署、集成和开发人员，内容由主仓库的技术规范生成。

## 快速导航

- **第一次使用**：[快速入门](Getting-Started) → [配置规范](Configuration) → [录像与回放](Recording-and-Playback)
- **理解系统**：[系统架构](Architecture) → [媒体与流](Media-and-Streams)
- **系统集成**：[API 与 OpenAPI](API) → [安全规范](Security)
- **生产运行**：[部署与运行](Deployment-and-Operations) → [故障排除](Troubleshooting)
- **参与开发**：[开发与验证](Development)

## 阅读规则

- **必须 / 不得**是实现约束或安全底线；**应 / 不应**是默认部署约定；**可**是可选项。
- 默认端口、默认值和功能开关以当前配置及运行响应为准。
- 完整接口 schema 以运行实例 `/docs/` 的 OpenAPI 页面为准。
- 历史 design、plan、validation 和 issue 跟踪文档不代表当前行为承诺。
- JT1078/JT808、SFU 等实验能力不应被视为已完成所有真实设备验证。

## 常用专题

| 分类 | 页面 |
|------|------|
| 摄像头 | [RTSP / HTTP](Camera-Guide)、[ONVIF](ONVIF)、[小米](Xiaomi) |
| 国标与语音 | [GB28181](GB28181)、[VoIP](VoIP) |
| 内容源 | [IPTV](IPTV)、[Emby](Emby)、[MediaMTX](MediaMTX) |
| 智能家居 | [Home Assistant](Home-Assistant)、[MQTT](MQTT)、[事件中心](Events) |
| 文件与局域网 | [WebDAV](WebDAV)、[FTP](FTP)、[DLNA](DLNA) |
| 高级媒体 | [转码部署](Transcoding)、[录像内部流程](Recording-Internals) |
| 数据 | [数据库配置与迁移](Database-Migration) |

## 文档源与反馈

本 Wiki 的同名页面由主仓库同步。请通过主仓库修改 `docs/zh` 或 `docs/wiki` 并提交变更；不要只在 Wiki 网页端修改受管页面，否则下次同步会覆盖修改。
