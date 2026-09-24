# 文档导航

[English](../en/README.md) · [项目 README](../../README.zh.md)

按使用场景查找文档。配置示例见 [`config/config.example.yaml`](../../config/config.example.yaml)；运行中的 NVR 在 `/docs/` 提供 OpenAPI 文档。

## 入门与运维

| 文档 | 内容 |
|------|------|
| [快速入门](getting-started.md) | 安装、启动和添加第一个摄像头 |
| [部署指南](deployment.md) | Docker、二进制部署和反向代理 |
| [配置说明](configuration.md) | 主要配置项与示例；实际配置文件可对照上方配置示例 |
| [故障排除](troubleshooting.md) | 常见运行问题 |
| [架构](architecture.md) | NVR、媒体引擎与常见接入路径 |

## 媒体接入与播放

| 文档 | 内容 |
|------|------|
| [摄像头指南](camera-guide.md) | RTSP、HTTP 摄像头及编码配置 |
| [ONVIF](onvif-guide.md) | 设备发现、取流和云台控制 |
| [GB28181](gb28181-guide.md) | 国标设备接入与相关操作 |
| [IPTV](iptv.md) | 导入和检测 M3U、浏览器播放；按需发布到 NVR 供其他协议播放，或创建录像计划 |
| [小米摄像头](xiaomi-setup.md) | 小米摄像头接入 |
| [MediaMTX](mediamtx-guide.md) | 通过可选的 MediaMTX 接入摄像头 |
| [DLNA](dlna.md) | 在局域网播放器中发现并观看实时流、录像 |

## 录像与集成

| 文档 | 内容 |
|------|------|
| [录像计划](recording-plans.md) | 为流设置连续、定时或事件录像 |
| [录制流程](recording-flow.md) | 录像任务的启动、写盘和停止 |
| [API 参考](api-reference.md) | REST API 使用说明；OpenAPI 规范见 [`openapi.yaml`](../../internal/docsportal/openapi.yaml) |
| [MQTT](mqtt-integration.md) | 事件触发录像 |
| [WebDAV](webdav-integration.md) / [FTP](ftp-integration.md) | 浏览存储文件；FTP 指南说明当前认证限制 |
| [AI 检测](ai-setup-guide.md) | 检测服务的部署与配置 |
