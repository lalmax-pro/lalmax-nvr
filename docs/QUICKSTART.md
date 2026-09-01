# lalmax-nvr Quick Start

Web UI is **http://localhost:9090** (not 8080). Full guides: [English](en/getting-started.md) · [中文](zh/getting-started.md) · [Architecture](en/architecture.md) / [架构](zh/architecture.md).

## 1. Configure

From the repo root, copy the example config:

```bash
cp config/config.example.yaml config/lalmax-nvr.yaml
```

Or run `./lalmax-nvr init --password YOUR_PASSWORD` (creates `lalmax-nvr.yaml` in the current directory by default).

The binary default is `-config config/lalmax-nvr.yaml`. Scripts use the same path.

## 2. Run

### Linux / macOS

```bash
./scripts/unix/build.sh
./scripts/unix/start.sh      # background
# or:
./bin/lalmax-nvr -config config/lalmax-nvr.yaml
```

Other scripts: `stop.sh`, `restart.sh`, `status.sh`, `logs.sh`, `run.sh` (foreground) under `scripts/unix/`.

### Windows

```cmd
scripts\windows\build.bat
scripts\windows\start.bat
```

Or `lalmax-nvr.exe -config config\lalmax-nvr.yaml`.

PowerShell: `.\scripts\windows\nvr.ps1 start`. See [`scripts/README.md`](../scripts/README.md).

### Docker

```bash
docker compose up -d
```

## 3. Web UI

Open **http://localhost:9090**.

Credentials: `auth.username` / `auth.password` (or `password_hash`) in the YAML. First-run Docker/wizard can set them in the browser.

## 4. Add cameras

- **ONVIF**: Devices page → Discover (needs host network / multicast)
- **RTSP**: Cameras → Add Camera, paste RTSP URL
- **GB28181**: enable SIP in config; devices REGISTER inbound, then **push PS/RTP** after INVITE (`media_ip` must be reachable)

Live RTSP playback (lal, not the camera): `rtsp://HOST:15544/live/{camera_id}`. Set `media.lalmax_public_url` to a hostname clients can reach.

## Command line

```
./lalmax-nvr -h
```

| Flag | Description | Default |
|------|-------------|---------|
| `-config` | Config file path | `config/lalmax-nvr.yaml` |
| `-version` | Print version | |

## Ports (defaults)

| Port | Use |
|------|-----|
| **9090** | Web UI + NVR API + WebDAV |
| 12090 | lalmax HTTP (WHIP ingest, LL-HLS, WHEP, fMP4) |
| 4888 | WebRTC ICE mux (WHIP/WHEP) |
| 15544 | RTSP play |
| 18080 | HLS-TS / HTTP-FLV |
| 11935 | RTMP ingest |
| 19000 | SRT ingest |
| 2121 | FTP |
| 5060 | GB28181 SIP |

## More

- [README.md](../README.md) · [README.zh.md](../README.zh.md)
- Docs index: [docs/en](en/README.md) · [docs/zh](zh/README.md)
- GitHub: https://github.com/lalmax-pro/lalmax-nvr
