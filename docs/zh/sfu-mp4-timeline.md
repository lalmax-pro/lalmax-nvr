# SFU 桥接录像时间轴边界

MP4 封装器为多条轨道选择最早的首个解码时间作为公共起点，并用 edit list 表示其他轨道的首包延迟。Opus 轨道的空编辑还包含现有的 80 ms 解码预跳过；音视频先后顺序都可表达。轨道起始时间必须来自同一个时间基准，不同来源的时钟不能直接比较。

可复现的合成样本：

```sh
SFU_OFFSET_TEST_MP4=/tmp/sfu-offset.mp4 go test ./internal/muxer -run '^TestMP4TrackEditPreservesLateOpusOffset$' -count=1
ffprobe -v quiet -show_entries stream=codec_name,start_time,duration -of json /tmp/sfu-offset.mp4
```

样本的视频和音频输入首包分别位于同一时钟的 1000 ms 与 1200 ms。`ffprobe` 识别视频起始 0 ms、Opus 起始 280 ms，其中 200 ms 为轨道延迟，80 ms 为预跳过。样本视频 NAL 仅用于结构测试，不应作为真实视频播放验证。

这只验证 MP4 对已提供的首包时间差的处理。通用 H.264 录像器仍按接收时的墙钟计算视频时间，无法保证在突发或排队时保留源媒体时间轴；完整桥接录像的持续同步、真实解码和长期误差仍需实现及验证。
