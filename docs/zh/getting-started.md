# 快速入门

NVR 默认在 `:9090` 提供 Web UI 和 API。媒体引擎默认使用 `media.mode: embedded`。摄像头、IPTV 频道、流和录像计划分别管理；仅添加设备不会自动录像。

## Docker Compose

在仓库根目录运行：

```bash
docker compose up -d
```

打开 `http://localhost:9090`，按页面提示完成首次设置。`docker-compose.yml` 默认把宿主机 `./data` 挂到容器内 `/data`。若要换磁盘，修改卷挂载左侧路径，并确保容器对目录有写权限。局域网 ONVIF 自动发现或 DLNA SSDP 发现需要组播网络；Docker bridge 模式通常无法让这类发现正常工作，相关说明见 [部署指南](deployment.md)。

## 从仓库构建并运行

在仓库根目录执行：

```bash
./scripts/unix/build.sh
./scripts/unix/run.sh
```

构建脚本把程序写到 `bin/lalmax-nvr`。`run.sh` 在缺少 `config/lalmax-nvr.yaml` 时从 `config/config.example.yaml` 复制一份，并把数据目录指向仓库的 `data/`。也可以直接使用已构建的二进制：

```bash
./bin/lalmax-nvr init --password '至少八位的密码' --config config/lalmax-nvr.yaml --data-dir ./data
./bin/lalmax-nvr -config config/lalmax-nvr.yaml
```

`init` 默认输出到当前目录的 `lalmax-nvr.yaml`；主程序默认读取 `config/lalmax-nvr.yaml`，所以手动初始化时应显式指定 `--config`。现有文件不会被覆盖，除非向 `init` 传入 `--force`。

Windows 构建和启动脚本位于 [`scripts/windows`](../../scripts/windows/)；脚本用法见 [`scripts/README.md`](../../scripts/README.md)。

## 添加来源

1. 在 **设备** 页添加 RTSP 或 ONVIF 摄像头。连接测试只检查地址可达，不验证解码；见[摄像头接入](camera-guide.md)。
2. 在 **IPTV** 页导入 M3U 并检测频道。频道可直接在浏览器播放；需要其他协议或录像时，再启用发布或创建计划；见 [IPTV](iptv.md)。
3. 在 **录像计划** 页为要录制的 `stream_id` 创建计划；见[录像计划](recording-plans.md)。

运行中的 NVR 在 `/docs/` 提供 API 文档。[WebDAV](webdav-integration.md) 默认只读并使用 Web/API 认证。[FTP](ftp-integration.md) 默认开启，但当前明文密码认证与初始化后的哈希密码存在限制，请先阅读其指南。

## 常用地址与端口

| 默认端口 | 用途 |
|----------|------|
| `9090` | Web UI、API、WebDAV |
| `12090` | lalmax HTTP |
| `15544` | RTSP 播放 |
| `8200` | DLNA HTTP（启用时；另需 UDP `1900` 组播） |

其他端口及接入关系见[架构](architecture.md)。启动或播放异常时见[故障排除](troubleshooting.md)。
