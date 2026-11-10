# SFU 桥接录像验证

以下可选测试需要本机安装带 `libx264`、`libopus` 的 FFmpeg 和 ffprobe；测试生成合成画面及音频，离线解码，不打开摄像头、麦克风、屏幕或扬声器。

```sh
SFU_DECODE_RECORDING_TEST=1 go test -race ./internal/recorder -run '^TestSFUDecodableBridgeRecording' -count=3
SFU_AV_SYNC_TEST=1 go test -race ./internal/recorder -run '^TestSFUBridgeRecordingKeepsEncodedAVMarkersAligned$' -count=3
SFU_AV_LONG_TEST=1 go test -race ./internal/recorder -run '^TestSFULongBridgeRecordingKeepsEncodedAVMarkersAligned$' -count=1
SFU_NVR_RECORDING_TEST=1 go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUBridgeRecordingPlanEndToEnd$' -count=1
SFU_NVR_RECORDING_TEST=1 SFU_NVR_RECORDING_LONG_TEST=1 go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUBridgeRecordingPlanEndToEnd$' -count=1
SFU_NVR_RECORDING_TEST=1 SFU_NVR_RECORDING_MARKERS_TEST=1 go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUBridgeRecordingPlanEndToEnd$' -count=5
SFU_NVR_RECORDING_TEST=1 SFU_NVR_SOURCE_END_TEST=1 go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUBridgeRecordingPlanEndToEnd$' -count=3
```

第一组测试生成 H.264 画面和 Opus 静音，按 RTP 送入 SFU 音视频桥接，再经 lal 重封装、真实 lalmax Group、帧订阅和录像器生成 MP4。测试覆盖一秒连续音视频，以及四秒视频中 2.4 秒的音频间隙，并由 FFmpeg 离线解码。Opus 样本表中包含一个用于解码预热的合成静音包；它会被 edit list 跳过，不应算作源音频包。可通过 `SFU_DECODE_RECORDING_MP4=/绝对路径/输出.mp4` 保留产物；同时运行多个用例时应避免使用同一输出路径。

第二组测试在源视频和 Opus 音频的相同时间放置标记，离线解码后比较时间；覆盖同源、音频领先或落后 200 毫秒及接近实时速率。视频解码使用 passthrough 模式，检查全部帧，避免默认补帧掩盖丢帧。第三组以接近实时速率运行约一分钟，音频设定晚 200 毫秒；一次本机运行中录像器收到 3000 个音频包，解码得到 1500 帧视频，视频标记为 0.48、30、59 秒，音频标记为 0.68、30.2、59.2 秒。

前三组使用测试发布适配器。第四组启动本机 NVR，经受保护的票据和信令发布合成视频与静音音频，再调用桥接和录像计划 API，检查录像计划在视频编码信息稍后就绪时能自动启动，最终用 ffprobe 检查音视频包数并用 FFmpeg 离线解码 MP4。第五组在同一 NVR 链路中接近实时运行一分钟，以 2 分钟录像分段避免测试中途切段，检查音视频包数及轨道时长差；它不验证标记对齐或听感。第六组用合成闪屏和离线生成的音频信号比较两次时间标记；使用视频帧时间戳及离线解码的音频，整个过程不使用扬声器。第七组在录像计划仍启用时通过信令撤销视频发布，检查桥接结束、播放授权撤销、录像任务停止及录像文件离线解码。可通过 `SFU_NVR_RECORDING_MP4=/绝对路径/输出.mp4` 保留产物；测试结束时会向测试进程发送 SIGTERM 以关闭 NVR，不应与其他依赖进程信号的测试同时运行。

2026-11-10 的 NVR 端到端验收在 race 构建下完成：一分钟录像用例通过，ffprobe 统计 1475 个视频包、59.000 秒视频轨和 2949 个音频包、58.980 秒音频轨，轨道时长相差 20 毫秒；FFmpeg 成功解码。另以 `SFU_NVR_RECORDING_MARKERS_TEST=1` 重复运行三次，音视频解码标记每次均为 0.48 秒和 1.48 秒。源视频撤销用例也通过：GroupBridge 结束、播放授权接口返回 404、录像计划停止，录像文件离线解码成功。

2026-11-10 以 `SFU_NVR_RECORDING_TEST=1 SFU_NVR_RECORDING_LONG_TEST=1 go test -race ./cmd/lalmax-nvr -run '^TestNVRSFUBridgeRecordingPlanEndToEnd$' -count=1 -v` 复跑一分钟 NVR 链路，再次通过。ffprobe 统计 1459 个视频包、58.360 秒视频轨和 2917 个音频包、58.340 秒音频轨，时长差仍为 20 毫秒；FFmpeg 全轨离线解码成功。运行包含约一分钟源媒体和 NVR 启停，总耗时约 68 秒。包数和时长随测试启动/停止边界略有变化。

2026-11-10 再次以 race 模式运行同一一分钟 NVR 链路，通过；ffprobe 统计 1477 个视频包、59.080 秒视频轨和 2953 个音频包、59.060 秒音频轨，差值仍为 20 毫秒，FFmpeg 全轨解码通过。同步标记用例和视频撤销/授权撤销/录像停止用例均通过。另将 NVR 鉴权三成员动态订阅测试重复 3 次，全部通过。以上媒体源均为合成 RTP，不访问物理设备。

这些测试不能代替真实设备、真实网络、其他播放器、人工听感和更长时间同步验收。不同 FFmpeg 构建需要提供上述编码器。

## 真实设备本机验收（2026-11-10）

在本机 NVR 的 Meet 页面以 Chrome 真实摄像头和麦克风入会，通过受保护的 GroupBridge/WHEP 播放，并启动后删除一条临时录像计划。录像生成五段 MP4，每段均包含 H.264 视频和 Opus 音频；四段完整片长约 32 秒，最后一段约 10.3 秒。FFprobe 检查到音视频包时间戳各自严格递增；使用 `ffmpeg -v error -xerror -i <片段.mp4> -fps_mode vfr -f null -` 对五段逐一全轨解码，全部返回成功。FFmpeg 默认 null 输出的同步方式曾打印非递增 DTS 告警；改为 VFR 离线解码后无告警，且输入包 DTS 本身严格递增。

首次分段测得音视频轨起始偏差约 839 毫秒；其余四段约为 125–176 毫秒。该单次实机结果不能证明稳定音画同步，首段偏差仍需在重复启停和更长录制中查明；本次也未做人工听音或独立同步仪器测量。验收中还修复了 GroupBridge 对 Simulcast SSRC 切换的处理：Sender Report 改为按 publication/SSRC 保存和读取，AV 桥接在视频 SSRC 变化时重置解码器并等待新 IDR。

同次 Chrome 本机双标签验收由发送端共享当前 Meet 标签页，另一只仅接收标签收到独立 H.264 屏幕轨，接收统计约 272 kb/s、0% 丢包、回环 RTT 约 0 毫秒。停止共享后接收端的屏幕轨消失；随后停止摄像头 GroupBridge、两端离会，房间人数回到 0。浏览器摄像头 Simulcast 诊断仍只显示 l/m 有发送、h 为 0，因此这不是完整三层或弱网选层验收。临时录像和 NVR 数据目录在验收后清理。

以上仅覆盖本机 Chrome、回环网络和一次真实设备录像。公网 TURN/跨网络、目标机器容量、人工听音、第三方播放器及长时间同步仍需相应环境和验收条件。
