# lalmax-nvr 文档中心

[English](../en/README.md) · [项目 README](../../README.zh.md)

本中心按**读者视角**组织，分三个层次。

---

## 👤 用户文档

面向运维人员和最终用户。

### 入门与运维

| 文档 | 内容 |
|------|------|
| [快速入门](getting-started.md) | 安装、启动和添加第一个摄像头 |
| [部署指南](deployment.md) | Docker、二进制部署和反向代理 |
| [配置说明](configuration.md) | 主要配置项与示例；实际配置文件见 [`config/config.example.yaml`](../../config/config.example.yaml) |
| [架构概览](architecture.md) | 分层设计、接入路径、默认端口、模块职责 |
| [数据库迁移](database-migration.md) | PostgreSQL / MySQL 配置，以及从 SQLite 在线迁移 |
| [故障排除](troubleshooting.md) | 常见运行问题 |
| [发布与自动构建](releases.md) | 版本发布流程，升级建议 |

### 媒体接入

| 文档 | 内容 |
|------|------|
| [媒体接入总览](media-sources.md) | 摄像头、直播地址、IPTV、Emby 和编码器推流的统一入口 |
| [摄像头指南](camera-guide.md) | RTSP、HTTP 摄像头及编码配置 |
| [ONVIF](onvif-guide.md) | 设备发现、取流和云台控制 |
| [GB28181](gb28181-guide.md) | 国标设备接入与相关操作 |
| [JT1078 / JT808](configuration.md#jt1078-推流配置) | `:1078` 媒体收流，以及可选的 `:808` 信令 |
| [VoIP 呼叫与对讲](voip.md) | SIP 终端与 PBX、网页外呼、WebRTC 双向对讲、通话记录及终端接入 |
| [小米摄像头](xiaomi-setup.md) | 小米摄像头（CS2）接入 |
| [MediaMTX](mediamtx-guide.md) | 通过可选的 MediaMTX 接入摄像头 |
| [IPTV](iptv.md) | 导入和检测 M3U、浏览器播放；按需发布到 NVR 供其他协议播放或创建录像计划 |
| [Emby 影音库](emby.md) | 浏览电影和剧集并发布为直播流；片尾下线或自动循环 |

### 录像与存储

| 文档 | 内容 |
|------|------|
| [录像计划](recording-plans.md) | 为流设置连续、定时或事件录像 |
| [录制流程](recording-flow.md) | 组写入器如何订阅 lalmax group，计划如何拨写盘开关 |
| [WebDAV](webdav-integration.md) | 浏览存储文件 |
| [FTP](ftp-integration.md) | FTP 访问，当前认证限制说明 |

### 功能与集成

| 文档 | 内容 |
|------|------|
| [Home Assistant](home-assistant.md) | 流发布、实时预览、缩略图、在线状态、云台与事件 |
| [AI 检测](ai-setup-guide.md) | 检测服务的部署与配置 |
| [MQTT](mqtt-integration.md) | 事件触发录像 |
| [DLNA](dlna.md) | 在局域网播放器中发现并观看实时流、录像 |
| [事件中心](events.md) | 健康、录制恢复、AI 和 MQTT 事件的筛选、实时更新与确认 |

### API

| 文档 | 内容 |
|------|------|
| [API 参考](api-reference.md) | REST API 说明；OpenAPI 规范见 [`openapi.yaml`](../../internal/docsportal/openapi.yaml) |

> 运行中的 NVR 也在 `/docs/` 提供可浏览的 OpenAPI 文档。

---

## 🔧 开发者文档

面向贡献者和内部开发者。

| 文档 | 内容 |
|------|------|
| [贡献指南](contributing.md) | 环境搭建、PR 流程、提交规范、CI 说明 |
| [模块职责说明](module-guide.md) | `internal/` 各包的职责、接口边界与扩展点 |
| [测试规范](testing-guide.md) | 测试策略、race 测试、媒体验收用例规范 |
| [ADR 架构决策记录](adr/README.md) | 重大架构决策索引 |

---

## 📐 设计文档

内部设计草案和技术决策，供评审和参考。

> 这类文档反映某个时间点的设计意图，部分功能尚未完成或仍在演进中；阅读时注意文档内的状态说明。

### 通用实时转码（Issue #66）

| 文档 | 状态 |
|------|------|
| [转码架构设计](transcoding-design.md) | Proposed，P0 已合并 |
| [P0 决策记录](transcoding-p0-decisions.md) | 已完成技术自审 |
| [转码部署说明](transcoding-deployment.md) | 随 P1 实现更新 |
| [转码验收记录](transcoding-testing.md) | 持续更新 |
| [转码跟踪草稿（Issue 备份）](transcoding-tracking-issue.md) | 参考用 |

### 其他设计草案

| 文档 | 状态 |
|------|------|
| [流管理设计](stream-management-design.md) | 草案，P1 方向 |
| [GB28181 目录（行政区划 & 业务分组）设计](gb-catalog-design.md) | 草案 |
| [WASM 播放器与 AI 检测设计](wasm-player-design.md) | 草案 |
| [海康设备 SDK 集成方案](hikvision-sdk-integration.md) | 草案 |
| [地图功能分析](map-feature-analysis.md) | 分析备忘 |
| [Home Assistant 对接测试记录](home-assistant-testing.md) | 验收记录 |
