# ADR-004: Keep CGO_ENABLED=0; run media tools as subprocesses

- **Status**: Accepted
- **Date**: 2026-10
- **Related**: Architecture design, go.mod

## Context

The NVR needs to handle media files (snapshots, transcoding) and some functionality that historically required C libraries (AI detection, GStreamer dynamic pipelines). The question was whether to call C libraries directly from Go via CGO, or invoke external tools as subprocesses.

## Alternatives

**Option A: CGO to call C libraries**
- Best performance, no process startup overhead
- Supports libavcodec (FFmpeg), GStreamer C API, ONNX Runtime C API
- Tradeoffs:
  - Breaks cross-compilation: `GOARCH=arm64` needs an arm64 C toolchain
  - A C library crash (segfault) takes down the entire Go process
  - Static linking increases binary size; dynamic linking requires runtime `.so` files

**Option B: External subprocesses**
- `CGO_ENABLED=0`; Go binary is fully static, enabling cross-compilation to any target platform
- FFmpeg, GStreamer, and AI inference engines launched as subprocesses via stdout / pipe / socket
- Subprocess crashes don't affect the main process; tools can be upgraded independently
- Tradeoffs: process startup overhead; must manage subprocess lifecycle (kill, cleanup); platform-specific implementation for Unix process groups and Windows Job Objects

## Decision

**Option B**: `CGO_ENABLED=0`; all external tools run as subprocesses.

Implementation specifics:
- FFmpeg / GStreamer transcoding: subprocess, parameter allowlist, no shell interpolation
- AI inference (YOLO): standalone subprocess, ONNX Runtime
- GStreamer complex dynamic pipelines (future): standalone GStreamer worker process, no CGO in main process

## Tradeoffs and Known Limitations

- Subprocesses require a complete Wait() + forced-kill guarantee to prevent zombies
- Windows requires Job Objects for process tree isolation (cleanup child processes when parent exits abnormally)
- macOS GStreamer Homebrew install may require per-process `DYLD_LIBRARY_PATH` configuration
- Each transcode task incurs a process fork cost (generally acceptable; amortized over long-running tasks)
