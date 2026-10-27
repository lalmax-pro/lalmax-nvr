# 通用转码部署与当前边界

关联 [Issue #66](https://github.com/lalmax-pro/lalmax-nvr/issues/66)、[架构设计](transcoding-design.md)、[验收记录](transcoding-testing.md)。

> 实验性实现，尚未发布，也未完成真实源/Docker/GStreamer 端到端验收。下面描述当前分支的配置与操作，不代表已发布版本可用。

## 本地运行

将以下配置合并到 NVR 配置中，首次保留 `enabled: false`：

```yaml
transcoding:
  enabled: false
  max_concurrent: 2
  threads_per_job: 2
  ffmpeg_path: /opt/homebrew/bin/ffmpeg
  ffprobe_path: /opt/homebrew/bin/ffprobe
  gst_launch_path: /opt/homebrew/bin/gst-launch-1.0
  gst_inspect_path: /opt/homebrew/bin/gst-inspect-1.0
```

以上路径仅为 macOS Homebrew 示例；Linux/容器应使用各自安装路径，空字符串表示 PATH 查找。不要把宿主机 macOS 二进制挂载到 Linux 容器使用。

目前路径与转码运行配置在启动时固定，修改后需重启。缺失工具不会阻止 NVR 启动，检测页显示失败。

## Docker 可选后端

默认 [Dockerfile](../../Dockerfile) 已安装 FFmpeg。可选 GStreamer 构建参数：

```sh
docker build --build-arg WITH_GSTREAMER=true -t lalmax-nvr:transcode-gst .
```

默认参数为 false。镜像保留 FFmpeg/ffprobe，因为快照、媒体检查与 GStreamer 源尺寸探测仍使用它们。不要挂载 Docker socket。

插件依赖包括 gstreamer-tools、base/good/bad/ugly 和 libav 包；具体目标架构可用性必须通过构建与实际检测确认。包含 x264 的构建涉及对应许可证，请在分发镜像时核对许可证要求。linux/arm64 可选镜像构建和有限编码 smoke test 已通过；其他架构和完整容器 API 媒体链路仍未验收，详见[记录](transcoding-testing.md)。

## 首次使用步骤

1. 用管理员账户打开“设置 → 通用转码能力”，点击检测，不会修改源或启动录像。
2. 添加 H.264 720p 模板，然后对**已保存模板**再次检测；后端默认模板的结果不能替代不同配置模板的结果。
3. 输入已接入的源流 ID、实际应用名和模板，保存禁用手动任务草稿。
4. 检查原始流在线，媒体模式为 embedded，RTMP 监听已由管理员显式开启。
5. 配置 `transcoding.enabled: true` 并重启后，重新检测保存的模板，再启用、启动任务。当前内存检测缓存重启即失效。
6. 查询任务源的 variants，并通过管理员播放接口取得带临时令牌的 RTSP 地址；不要把此地址放入公开日志或 Issue。
7. HA 发布设置可加载该源的转码任务作为输出；身份仍为原源 ID，选择操作不会启动任务。
8. 结束测试先停止任务；原流与原录像不因任务删除而修改。

## 已知限制

- 全局开启目前通过配置文件完成，网页不会自动开启。
- GStreamer 固定模板的本机和可选 linux/arm64 镜像自检已通过；运行中的 H.264/HEVC 合成发布源已经通过 API 管理的 RTSP → GStreamer → RTMP → RTSP 解码；实体摄像机和生产稳定性仍未验收。macOS Homebrew 本机自检需为进程设置 `DYLD_FALLBACK_LIBRARY_PATH=/opt/homebrew/lib`，该环境变量并未写入 NVR 全局配置。
- FFmpeg copy 模式使用内置的合成 H.264/AAC 样本执行直拷、媒体检查与解码自检；真实源仍须符合 H.264/AAC 的 copy 限制，自检不等于实际源或媒体回灌已验证。GStreamer copy 模式自检仍未支持。
- 管理员 RTSP 派生地址、同源 FLV 接口以及 HA 目录地址均已实际取回解码；真实 HA HLS/浏览器和网页 MSE 仍待验收；HA 派生 WebRTC 暂不开放。
- 检测有效期 10 分钟；已运行任务不会因检测过期被停止，但失败后的重新启动需要新的有效检测。
- 常驻策略 API 可配置；当前网页创建的是手动任务。持久化的已启用常驻任务在重启或检测过期后，由有界后台协调重新自检再恢复；自检失败遵循退避并记录安全错误码，运行中合成源的常驻任务实际进程重启恢复和解码已通过，实体海康源的视频 copy + 音频转码已有有限实际解码证据；其他实体源场景及长时间运行仍待验证。
- 不支持自动硬件加速、跨后端回退、任意命令、任意外部输入 URL 或离线录像转码。
- 独立 [ARM64 scratch Dockerfile](../../Dockerfile.arm64) 不包含后端工具，不应当作转码镜像使用。

## 安全验收要求

上线前必须实际验证源拉取、派生发布、RTSP 播放的鉴权；拒绝无令牌和跨源播放；验证取消 HA 发布与全局停止的不同作用域。不得以参数构造、进程启动或前端构建成功替代实际媒体解码验收。
