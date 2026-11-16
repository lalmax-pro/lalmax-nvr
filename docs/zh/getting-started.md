# 快速入门

本文给出一条可验收的最小路径：启动服务、完成认证初始化、接入媒体、创建录像计划。完整约束见[配置规范](configuration.md)、[媒体与流](media-sources.md)和[录像与回放](recording-plans.md)。

## 1. 前置条件

| 方式 | 要求 |
|------|------|
| Docker | 支持 Docker Compose v2 的 Docker 环境；宿主机数据目录可写 |
| 预构建二进制 | 与操作系统、CPU 架构匹配的 Release 产物 |
| 源码构建 | Go 版本与根目录 `go.mod` 一致，Node.js 22 与 npm；构建脚本需要可用的网络下载依赖 |

生产环境不应把未使用的媒体、FTP 或设备协议端口暴露到不可信网络。首次上线前先阅读[安全规范](security.md)。

## 2. 启动

### 2.1 Docker Compose

在仓库根目录执行：

```bash
docker compose up -d
docker compose ps
```

默认使用 `ghcr.io/lalmax-pro/lalmax-nvr:latest`，将宿主机 `./data` 挂载到容器 `/data`，配置文件为 `/data/lalmax-nvr.yaml`。生产部署应固定到经过验证的版本标签，而不是持续跟随 `latest`。

Compose 示例映射了多种可选协议端口。只开放实际需要的端口；ONVIF 组播发现和 DLNA/SSDP 通常需要 host 网络或等效的组播网络配置，详见[部署与运行](deployment.md)。

### 2.2 二进制初始化与启动

`init` 默认写入当前目录的 `lalmax-nvr.yaml`，而主程序默认读取 `config/lalmax-nvr.yaml`。为避免两个默认路径不一致，应显式传递配置路径：

```bash
./lalmax-nvr init \
  --username admin \
  --password 'replace-with-a-private-password' \
  --data-dir /var/lib/lalmax-nvr \
  --config config/lalmax-nvr.yaml
./lalmax-nvr -config config/lalmax-nvr.yaml
```

密码至少 8 个字符。`init` 保存 bcrypt 哈希，不保存该明文密码；已存在的配置文件默认不会被覆盖，`--force` 仅用于明确需要重建配置的场景。

### 2.3 从源码构建

Linux / macOS：

```bash
./scripts/unix/build.sh
./scripts/unix/run.sh
```

输出为 `bin/lalmax-nvr`。`run.sh` 使用 `config/lalmax-nvr.yaml`；文件不存在时从 `config/config.example.yaml` 创建，并把存储根目录同步到仓库的 `data/`。Windows 入口和其他脚本参数见[`scripts/README.md`](../../scripts/README.md)。

## 3. 完成首次认证设置

打开 `http://localhost:9090`。

- 已通过 `init` 或 `NVR_PASSWORD` 设置密码时，使用对应账号登录。
- 未设置密码时，按 Web 设置向导创建管理员并选择数据目录。
- 未完成设置不代表 API 全部匿名开放：受保护接口返回 `503` 和 `SETUP_REQUIRED`；公开的初始化接口只用于首次设置。

`POST /api/auth/login` 只验证凭据，不创建会话 Cookie。浏览器和 API 客户端后续仍使用 Basic Auth；集成细节见[API 规范](api-reference.md)。

## 4. 接入第一路媒体

### 4.1 RTSP 摄像头

1. 在 **设备** 页添加摄像头。
2. 协议选择 `rtsp`，填写完整 RTSP URL 和设备凭据。
3. 编码按实际源流选择 `h264`、`h265` 或 `mjpeg`；不确定时使用 `ffprobe` 检查。
4. RTSP 传输默认 `tcp`，只有网络和设备确有需要时才改用 `udp`。
5. 启动设备并在 **流管理** 中确认对应 `stream_id` 已上线。

“连接测试”只证明网络或端点可达，不证明认证、解码和播放均成功。

### 4.2 ONVIF 摄像头

可使用设备页的发现/探测能力，或直接填写 ONVIF Device Service URL。NVR 通过 ONVIF 获取流地址后再拉取媒体；ONVIF 本身不是视频传输协议。组播发现不可用时可手动探测或手动添加。

### 4.3 编码器推流

创建推流类型的流记录，再把 API 返回的 RTMP、SRT 或 WHIP 接入地址配置到编码器。相应监听器必须已启用，且每个流应配置独立的高熵凭据。不要根据旧文档手工猜测 URL，以 `ingest_urls` 响应为准。

### 4.4 其他来源

GB28181、VoIP、IPTV、Emby、小米、JT1078/JT808 的信令方向和部署要求不同，使用[媒体与流](media-sources.md)中的专题入口，不要把它们统一当作 RTSP 拉流。

## 5. 创建录像计划

**添加设备或把流登记为设备不会自动录像。** 必须为该流的 `stream_id` 创建计划：

1. 打开 **录像计划**。
2. 选择目标 `stream_id`。
3. 最小验证选择 `continuous` 和 `enabled: true`。
4. 等源流上线并产生至少一个已关闭片段，再到录像页检查。

也可调用 API：

```bash
curl -u 'admin:replace-with-a-private-password' \
  -H 'Content-Type: application/json' \
  -d '{"stream_id":"front-door","name":"Front door","mode":"continuous","enabled":true}' \
  http://localhost:9090/api/recording-plans
```

一条流最多一份计划；重复创建返回 `409`。定时、事件和自适应模式的时间窗及例外见[录像与回放](recording-plans.md)。

## 6. 最小验收

```bash
curl -i http://localhost:9090/api/health
curl -i http://localhost:9090/api/readyz
curl -u 'admin:replace-with-a-private-password' http://localhost:9090/api/streams
curl -u 'admin:replace-with-a-private-password' http://localhost:9090/api/recording-plans
```

- `/api/health` 返回 `200`，只表示进程存活。
- `/api/readyz` 返回 `200` 才表示数据库、磁盘和 goroutine 数量通过就绪检查。
- 流详情中的 `active`、编码和 `play_urls` 用于判断实时播放能力。
- 录像验收必须看到已结束的录像记录或文件；仅有“录制中”状态不能证明片段已可回放。

失败时按[故障排除](troubleshooting.md)逐层定位。
