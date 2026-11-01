# ADR-001：嵌入式媒体引擎（embedded mode）为默认模式

- **状态**：Accepted
- **日期**：2026-10
- **关联**：架构设计，见 [architecture.md](../architecture.md)

## 背景

lalmax-nvr 依赖 lalmax / lal 媒体引擎处理收流、转协议和分发。引擎可以和 NVR 同进程运行（embedded），也可以作为独立进程通过 HTTP 连接（http mode）。需要确定哪种模式作为默认推荐。

## 备选方案

**方案 A：embedded mode（同进程）**
- Go 代码直接调用 lalmax API
- 无网络开销，进程内通信
- 单文件部署，无需额外进程管理
- 代价：NVR 与媒体引擎同生命周期，一方崩溃影响另一方

**方案 B：http mode（独立进程）**
- NVR 通过 HTTP API 与外部 lalmax 通信
- 可独立升级媒体引擎
- 代价：需要额外部署和端口配置，延迟更高，运维复杂度上升

## 决策

选择 **embedded mode** 为默认模式。

理由：
1. 目标用户场景（家庭 / 小型企业 NVR）更重视单文件简单部署
2. embedded 模式性能更好，无 HTTP 序列化开销
3. http mode 仍保留作为高级选项，供需要独立扩展媒体引擎的场景使用

## 权衡与已知限制

- MJPEG / HTTP JPEG 源仍由 NVR 直接拉帧，不经过 lalmax，因为 lalmax 不处理这类输入
- ONVIF 组播发现（WS-Discovery）在 Docker bridge 网络下不可用，需要 `network_mode: host`
- embedded 模式下媒体引擎端口（12090、4888 等）由 NVR 进程持有，需一并对外暴露
