# 文档（中文）

[English](../en/README.md) · [仓库 README](../../README.zh.md)

## 从这里开始

| 文档 | 说明 |
|------|------|
| [快速入门](getting-started.md) | 安装、第一个摄像头、Web UI |
| [架构](architecture.md) | 分层、直播/录像数据流、拉流 vs 推流、端口 |
| [配置说明](configuration.md) | YAML 全量参考 |
| [部署指南](deployment.md) | Docker、反向代理、交叉编译 |
| [故障排除](troubleshooting.md) | 常见问题 |

Release 包里的 [QUICKSTART](../QUICKSTART.md) 是精简版，细节以本文档为准。

## 设备与协议

| 文档 | 说明 |
|------|------|
| [摄像头指南](camera-guide.md) | RTSP / HTTP / 编码 |
| [ONVIF 指南](onvif-guide.md) | 发现、GetStreamUri 后 RTSP 拉流、云台 |
| [GB28181 指南](gb28181-guide.md) | SIP 上级、设备推 PS/RTP、回放、对讲 |
| [小米摄像头](xiaomi-setup.md) | CS2 P2P 取帧注入 |

## 集成

| 文档 | 说明 |
|------|------|
| [API 文档](api-reference.md) | REST API |
| [MQTT](mqtt-integration.md) | 事件触发录像 |
| [FTP](ftp-integration.md) | FTP 访问录像 |
| [WebDAV](webdav-integration.md) | WebDAV 访问录像 |
| [AI 检测](ai-setup-guide.md) | 推理与叠加 |
| [MediaMTX](mediamtx-guide.md) | CSI 等经 MediaMTX 再接入 |

## 设计笔记（实现向）

| 文档 | 说明 |
|------|------|
| [流管理](stream-management-design.md) | 推流与相机绑定 |
| [WebCodecs 播放器](wasm-player-design.md) | 低延迟直播 |
| [GB 目录](gb-catalog-design.md) | 国标目录树 |
| [海康 SDK](hikvision-sdk-integration.md) | 可选 SDK 路径 |
| [地图能力](map-feature-analysis.md) | 地图相关分析 |
