# lalmax-nvr 快速入门

## lalmax-nvr 是什么

lalmax-nvr 是一层业务 NVR，叠在内嵌的 lal / lalmax 媒体引擎上。H.264 / H.265 只进引擎一次：RTSP/ONVIF **拉流**、GB28181 设备 **推** PS/RTP、RTMP/SRT/WHIP **推流** 都进同一套直播分发。NVR 负责设备、录像、存储和 Web UI。分层与端口图见 [架构](architecture.md)。

**主要功能：**

- 支持 RTSP（H.264、H.265、MJPEG）、HTTP JPEG、ONVIF、GB28181、小米 CS2
- Web 管理界面：多协议直播、24 小时时间轴、连续 VOD（HLS fMP4）
- 录像模式：连续 / 计划 / 事件 / 自适应 / 关闭；滚动小时合并
- WebDAV、FTP 访问录像；MQTT 事件触发录制
- 单一静态二进制，内嵌 Web 界面，无外部运行时依赖

文档目录：[docs/zh/README.md](README.md)。

## 快速开始（5 分钟）

### 方式一：下载预编译二进制文件（推荐）

从 [GitHub Releases](https://github.com/lalmax-pro/lalmax-nvr/releases) 下载对应架构的最新版本：

```bash
# AMD64（大多数 PC/服务器）
wget https://github.com/lalmax-pro/lalmax-nvr/releases/latest/download/lalmax-nvr-amd64
chmod +x lalmax-nvr-amd64

# ARM64（树莓派等）
wget https://github.com/lalmax-pro/lalmax-nvr/releases/latest/download/lalmax-nvr-arm64
chmod +x lalmax-nvr-arm64
```

初始化配置并启动：

```bash
./lalmax-nvr-amd64 init --password 你的密码
./lalmax-nvr-amd64 -config lalmax-nvr.yaml
```

在浏览器打开 http://localhost:9090。

### 方式二：Docker

```bash
docker compose up -d
```

在浏览器打开 http://localhost:9090。

> **注意**：无需准备配置文件！lalmax-nvr 在没有配置文件的情况下启动时会自动初始化。

#### 修改录像存储位置

默认情况下，录像存储在宿主机的 `./data` 目录（映射到容器内的 `/data`）。
如果要将录像存储到外部硬盘或其他目录，请修改 `docker-compose.yml`：

```yaml
    volumes:
      - /mnt/external/nvr:/data    # ← 改为你的宿主机路径
    environment:
      - NVR_DATA_DIR=/data          # 必须与卷挂载的右侧一致
      # - NVR_UID=1000               # 与宿主机目录所有者的 UID 一致
      # - NVR_GID=1000               # 与宿主机目录所有者的 GID 一致
```

> **重要**：卷挂载的右侧（`:data`）和 `NVR_DATA_DIR` 必须始终一致。
> 如果容器无法启动或不断重启，请检查宿主机目录是否存在，以及配置的 UID/GID（默认 1000:1000）是否有写入权限。

### 方式三：从源码编译

需要 Go 1.26+ 和 Node.js（编译前端）：

```bash
git clone https://github.com/lalmax-pro/lalmax-nvr.git
cd lalmax-nvr
./scripts/unix/build.sh
./lalmax-nvr init --password 你的密码
./lalmax-nvr -config lalmax-nvr.yaml
```

交叉编译到 ARM64（如树莓派）：

```bash
GOOS=linux GOARCH=arm64 ./scripts/unix/build.sh
```

## 首次配置

### 使用 `lalmax-nvr init`

`init` 子命令创建带有安全默认值的配置文件：

```bash
./lalmax-nvr init --password 你的密码
```

选项：

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--password` | （交互输入） | Web UI 管理员密码 |
| `--username` | `admin` | 管理员用户名 |
| `--data-dir` | `/var/lib/lalmax-nvr` | 录像和数据库的数据目录 |
| `--listen` | `:9090` | HTTP 监听地址 |
| `--config` | `lalmax-nvr.yaml` | 配置文件路径 |
| `--force` | false | 覆盖已有配置文件 |

### 密码设置

设置管理员密码有三种方式：

1. **`lalmax-nvr init --password <密码>`** — 初始化时直接设置哈希密码（推荐）
2. **配置文件明文** — 在 YAML 中设置 `auth.password`，首次启动时自动转换为 `password_hash`
3. **手动生成哈希** — 使用 `lalmax-nvr hash-password <密码>` 生成，粘贴到 `auth.password_hash`

### 默认路径

| 路径 | 说明 |
|------|------|
| `/var/lib/lalmax-nvr/` | 数据目录（录像、数据库） |
| `/var/lib/lalmax-nvr/lalmax-nvr.db` | SQLite 数据库 |
| `lalmax-nvr.yaml` | 配置文件 |

## 添加第一个摄像头

lalmax-nvr 使用**独立的传输协议 + 编码格式**配置摄像头：

- **传输协议**：`rtsp`、`http`、`onvif`、`xiaomi`
- **编码格式**：`h264`、`h265`、`mjpeg`、`jpeg`

> 旧版组合格式（`rtsp_h264`、`rtsp_h265`、`rtsp_mjpeg`、`http_jpeg`）仍然支持，保持向后兼容。

### RTSP H.264 摄像头

```yaml
cameras:
  - id: "front-door"
    name: "前门"
    protocol: "rtsp"
    encoding: "h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

### RTSP H.265 摄像头

```yaml
cameras:
  - id: "driveway"
    name: "车道"
    protocol: "rtsp"
    encoding: "h265"
    url: "rtsp://192.168.1.103:554/stream"
    enabled: true
```

### HTTP JPEG 摄像头

```yaml
cameras:
  - id: "garage"
    name: "车库"
    protocol: "http"
    encoding: "jpeg"
    url: "http://192.168.1.102:8080/snapshot"
    enabled: true
```

### ONVIF 摄像头

```yaml
cameras:
  - id: "lobby"
    name: "大厅"
    protocol: "onvif"
    url: "http://192.168.1.104:80/onvif/device_service"
    username: "admin"
    password: "camera123"
    enabled: true
```

> ONVIF 会自动检测编码格式，可以省略 `encoding` 字段。

### 使用旧版组合格式

以下格式仍然可用：

```yaml
cameras:
  - id: "cam1"
    name: "旧格式摄像头"
    protocol: "rtsp_h264"
    url: "rtsp://192.168.1.100:554/stream"
    enabled: true
```

编辑配置后重启 lalmax-nvr，或通过 Web UI 在运行时添加摄像头。国标设备见 [GB28181 指南](gb28181-guide.md)。

## 访问 lalmax-nvr

### Web 管理界面

在浏览器打开 http://你的服务器地址:9090，使用配置的凭据登录。通过 Web UI 可以：

- 查看摄像头实时画面（HLS / FLV / WebRTC / fMP4 / WebCodecs，可复制 RTSP）
- 回放和下载录像（单文件或按天连续 VOD）
- 添加、编辑和删除摄像头；ONVIF 发现与云台
- 查看存储统计和趋势
- 配置系统设置（含滚动合并）

### WebDAV

WebDAV 默认启用（只读模式），访问路径为 `/dav/`：

```bash
curl -u admin:密码 http://你的服务器地址:9090/dav/
```

在文件管理器中挂载：`davs://你的服务器地址:9090/dav/`

要启用读写访问，在配置中设置 `webdav.read_write: true`。

### FTP

FTP 默认启用，端口为 2121：

```bash
ftp 你的服务器地址 2121
# 用户名：admin
# 密码：（你设置的密码）
```

## 常见问题

### 服务无法启动

- 检查配置文件语法：`cat lalmax-nvr.yaml`
- 确认数据目录存在且可写：`ls -la /var/lib/lalmax-nvr/`
- 如果使用 Docker，运行 `docker logs lalmax-nvr` 查看日志
- 如果直接运行二进制文件，查看启动进程所在终端或你重定向的日志文件

### 权限错误

- 确保配置的存储目录对运行 lalmax-nvr 的用户或容器 UID/GID 可写。

### 端口冲突

- 默认端口为 9090。如果被占用，在配置中修改 `server.listen`（如 `":8080"`）

### 无法连接摄像头

- 使用 VLC 或 ffplay 验证摄像头 URL：`ffplay rtsp://192.168.1.100:554/stream`
- 检查网络连通性：`ping 192.168.1.100`
- 确认摄像头用户名和密码正确
- 部分 H.265 摄像头可能需要使用特定的子码流 URL

### 树莓派内存占用过高

- 将 `segment_duration` 设为 `30s`（默认值）。更长的时长会在内存中缓存更多数据。
- 树莓派 3B 约 900MB 内存，4 个摄像头 30 秒片段时稳定运行约占 300MB。
