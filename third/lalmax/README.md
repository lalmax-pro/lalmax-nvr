# lalmax
lalmax是在lal的基础上集成第三方库，可以提供SRT、RTC、mp4、gb28181、jt1078、onvif等解决方案

# 编译
./build.sh

# 运行
./run.sh或者./lalmax -c conf/lalmax.conf.json

# 配置说明
lalmax.conf.json 配置主要由 2 部分组成

(1) lalmax: lalmax 扩展能力配置，例如 SRT、RTC、HTTP-FMP4、GB28181、JT1078 等，具体配置说明见[config.md](./document/config.md)

(2) lal: lal 原生配置，例如 RTMP、RTSP、HTTP-FLV、HLS-TS、录制、鉴权等，具体配置说明见[lal_config.md](./document/lal_config.md)。对外建议统一使用 lalmax 的 API Gateway、HTTP API 和 Hook API 门面；`lal.http_api` 仅建议在调试 lal 原生行为时临时开启，说明见[api_gateway.md](./document/api_gateway.md)、[lal_api.md](./document/lal_api.md)、[hook_api.md](./document/hook_api.md) 与 [hook_plugin_architecture.md](./document/hook_plugin_architecture.md)

旧版平铺配置和 lal_config_path 仍兼容，但推荐使用 lalmax/lal 两个顶层标签维护单个配置文件。

# docker运行
```
docker build -t lalmax:init ./

docker run -it -p 1935:1935 -p 8080:8080 -p 4433:4433 -p 5544:5544 -p 8084:8084 -p 30000-30100:30000-30100/udp -p 1290:1290 -p 6001:6001/udp -p 1078:1078 lalmax:init

```

# 架构

![图片](document/images/init.png)

# 支持的协议
## 推流
(1) RTSP 

(2) SRT

(3) RTMP

(4) RTC(WHIP)

(5) GB28181

(6) JT1078（实验性，尚未使用真实设备验证）

具体的推流 URL 地址见[流地址说明](./document/stream_url.md)

## 拉流
(1) RTSP

(2) SRT

(3) RTMP

(4) HLS(S)-TS

(5) HTTP(S)-FLV

(6) HTTP(S)-TS

(7) RTC(WHEP)

(8) HTTP(S)-FMP4

(9) HLS(S)-FMP4/LLHLS


具体的拉流 URL 地址见[流地址说明](./document/stream_url.md)

## [SRT](./document/srt.md)
（1）使用gosrt库

（2）暂时不支持SRT加密

（3）支持H264/H265/AAC

（4）可以对接OBS/VLC

```
推流url
srt://127.0.0.1:6001?streamid=#!::h=test110,m=publish

拉流url
srt://127.0.0.1:6001?streamid=#!::h=test110,m=request
```

## [WebRTC](./document/rtc.md)
（1）支持WHIP推流和WHEP拉流,暂时只支持POST信令

（2）支持H264/G711A/G711U/OPUS

（3）可以对接OBS、vue-wish

（4）WHEP支持对接Safari HEVC

（5）支持datachannel,只支持对接jessibuca播放器

（6）WHIP支持对接OBS 30.2 beta HEVC

datachannel播放地址：webrtc://127.0.0.1:1290/webrtc/play/live/test110

```
WHIP推流url
http(s)://127.0.0.1:1290/webrtc/whip?streamid=test110

WHEP拉流url
http(s)://127.0.0.1:1290/webrtc/whep?streamid=test110
```

## Http-fmp4
(1) 支持H264/H265/AAC/G711A/G711U

```
拉流url
http(s)://127.0.0.1:1290/live/m4s/test110.mp4
```

## HLS(fmp4/Low Latency)
(1) 支持H264/H265/AAC/OPUS

```
拉流url
http(s)://127.0.0.1:1290/live/hls/test110/index.m3u8
```

## [GB28181](./document/gb28181.md)
(1) 作为SIP服务器与设备进行SIP交互,使用单端口/多端口收流

(2) 提供相关API获取设备信息、播放某通道等功能

(3) 支持H264/H265/AAC/G711A/G711U

(4) 支持TCP/UDP


# QQ交流群
11818248




