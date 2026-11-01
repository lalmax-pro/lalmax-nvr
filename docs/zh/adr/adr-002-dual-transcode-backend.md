# ADR-002：转码双后端（FFmpeg / GStreamer）共享管理器

- **状态**：Accepted
- **日期**：2026-10
- **关联**：Issue #66，PR #67，[转码架构设计](../transcoding-design.md)

## 背景

NVR 需要实时转码能力，将摄像头原始流（H.265 / 非标音频等）转为下游兼容格式（H.264/AAC）。需要决定支持哪些转码后端，以及如何组织架构。

## 备选方案

**方案 A：仅支持 FFmpeg**
- 部署最简单，几乎所有平台都有预编译包
- 代价：与特定工具绑定，无法利用平台专属硬件加速 API（如 macOS VideoToolbox、Raspberry Pi MMAL）

**方案 B：FFmpeg + GStreamer 双后端，共享管理器**
- 统一的 `Backend` 接口，管理器不感知后端类型
- FFmpeg 为默认，GStreamer 为可选（需构建选项 `WITH_GSTREAMER=true`）
- 为后续硬件加速（GStreamer + 平台插件）和复杂动态管线（独立 GStreamer worker）保留接口
- 代价：增加测试矩阵复杂度，GStreamer 部署依赖更多插件

**方案 C：直接调用平台媒体 API（CGO）**
- 最优性能，零拷贝硬件路径
- 代价：破坏 `CGO_ENABLED=0` 约束，跨平台编译困难，见 ADR-004

## 决策

选择**方案 B**：FFmpeg + GStreamer 双后端，共享 `Manager` 和 `Service`。

核心接口：
```go
type Backend interface {
    Probe(ctx context.Context) (Capabilities, error)
    Start(ctx context.Context, spec JobSpec) (Process, error)
}
```

GStreamer 初期使用 `gst-launch-1.0` 固定管线，不在 Go 主进程引入 CGO，复杂动态管线升级为独立 worker。

## 权衡与已知限制

- 两套后端的测试矩阵独立维护，CI 中仅 FFmpeg 常驻，GStreamer 需要额外构建标志
- GStreamer 首批仅支持固定管线，动态 pad 和 EOS 处理需要独立验收
- 跨后端静默降级（FFmpeg 失败自动换 GStreamer）不允许，避免资源耗尽
- WebRTC 有声输出需要 Opus 封装，不能将 AAC 回灌路径直接用于 WebRTC（P4 单独验收）
