# ADR-004：保持 CGO_ENABLED=0，媒体工具以子进程运行

- **状态**：Accepted
- **日期**：2026-10
- **关联**：架构设计，go.mod

## 背景

NVR 需要处理媒体文件（快照、转码）和部分需要 C 库的功能（AI 检测、GStreamer 动态管线）。需要决定是通过 CGO 在 Go 进程内调用 C 库，还是通过子进程调用外部工具。

## 备选方案

**方案 A：CGO 调用 C 库**
- 性能最优，零进程启动开销
- 支持 libavcodec（FFmpeg）、GStreamer C API、ONNX Runtime C API
- 代价：
  - 破坏交叉编译：`GOARCH=arm64` 需要 arm64 的 C 工具链
  - C 库崩溃（段错误）会拉垮整个 Go 进程
  - 静态链接体积增大，动态链接需要运行时 .so

**方案 B：外部子进程**
- `CGO_ENABLED=0`，Go 二进制完全静态，任意目标平台交叉编译
- FFmpeg、GStreamer、AI 推断引擎均以子进程启动，通过 stdout / pipe / socket 通信
- 子进程崩溃不影响主进程；可独立升级工具版本
- 代价：进程启动开销；需要管理子进程生命周期（kill、清理）；Unix 进程组 / Windows Job Object 分平台实现

## 决策

选择**方案 B**：`CGO_ENABLED=0`，所有外部工具以子进程运行。

具体实现：
- FFmpeg / GStreamer 转码：子进程，参数白名单，不经 shell
- AI 推断（YOLO）：独立子进程，ONNX Runtime
- GStreamer 复杂动态管线（未来）：独立 GStreamer worker 进程，不在主进程引入 CGO

## 权衡与已知限制

- 子进程需要完整的 Wait() + 强杀保障，避免僵尸进程
- Windows 平台需要 Job Object 实现进程树隔离（主进程异常退出时清理子进程）
- macOS GStreamer Homebrew 路径需要进程级 DYLD_LIBRARY_PATH 设置
- 每次转码任务启动有进程 fork 开销（通常可接受，长期运行任务摊薄成本）
