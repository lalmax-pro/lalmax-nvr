# Issue #66（Fork）首批验收操作

分支：`feat/issue-66-transcoding`。仅用于开发/隔离验证，不直接部署生产。

## 前置条件

- 使用独立 NVR 实例、独立数据库和单独媒体端口；备份现有 NVR 配置和录像元数据。
- 首次启动前保持 `transcoding.enabled: false`，确认旧流和录像不受影响。
- FFmpeg 和 ffprobe 可执行；GStreamer 为可选后端，应具备 x264enc、avenc_aac、h264parse、aacparse、flvmux、rtmpsink 等插件。
- 仅在嵌入式媒体引擎上启用转码；RTMP 回灌须开启。转码任务输入只允许当前已注册媒体流，不允许任意 URL 或命令。

## 隔离验收顺序

1. 验证转码关闭时原始 H.264/H.265 播放及录像行为；普通用户不能操作或预览派生转码输出。
2. 通过设置 → 通用转码能力检查 FFmpeg/GStreamer 安装情况，验证缺少可执行文件/编码器时拒绝启用。
3. 创建自定义模板（720p/1080p、AAC/无音轨、video copy），保存后执行实际媒体自检。
4. 创建、启用常驻任务；源推流后观察状态 `running`、出口画面规格，并使用 ffprobe/ffmpeg **真正取流解码**；不能仅以子进程存在为通过。
5. 在流详情页显式选择“原始流 / 派生流”；派生未就绪时必须显示错误、不能静默切回原始视频或原始截图。
6. 创建两个同源同有效模板的消费者；确认共用一个物理工作进程，关闭一个仍可解码，关闭最后一个后旧派生输出失效。
7. 测试源断流、恢复、NVR 重启、错误格式、RTMP 关闭、配额超限、错误音轨和权限拒绝；确认无遗留 FFmpeg/GStreamer 子进程。
8. 验证 HA 发布派生 RTSP 的逻辑源 ID、云台、事件和录像查询仍指向**原始摄像头**；不能暴露内部物理输出名称或流密钥。
9. 区分两条播放路径：StreamDetail 派生预览仍是 HTTP-FLV/mpegts.js，PCMA 音频不支持，不能冒充 WHEP；第六轮已用 Chromium 直接验证 NVR 侧 HA-WHEP 的 H.264 + PCMA/8000、ICE/DTLS、RTP、视频解码和 Web Audio 非静音采样。
10. 验收记录应包含：Commit、系统架构、后端版本、输入输出编解码、首次成功画面时间、异常恢复时间、进程数及失败记录。

## 自动化检查

Fork PR 的 Build、Transcode Windows、transcode-media、前端及跨平台构建必须通过。第六轮原生 Linux 证据还使用真实 FFmpeg/GStreamer 进程完成实际媒体和解码；这些证据仍不能替代生产源、真实 Home Assistant 和长期运行。

## 仍不能按首批通过推断的范围

真实 Home Assistant 实例集成仍为 `BLOCKED_ENVIRONMENT`；硬件编码、多档共享解码、派生录像/转推、独立 worker 认证隔离、历史录像离线转码，属于后续独立验收门禁，未经实际实现与验证不得标为通过。WebRTC 有声协商已在 NVR 侧 HA-WHEP 范围内通过，但不等同于真实 Home Assistant 客户端验收。
