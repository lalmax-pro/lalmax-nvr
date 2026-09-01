# Deployment Guide

This guide covers installing, configuring, and maintaining lalmax-nvr in production. Layers and default ports: [Architecture](architecture.md).

## Installation Methods

### Docker

#### Prerequisites

- Docker Engine 20.10+ and Docker Compose v2 (or Podman equivalent)
- Check versions:
  ```bash
  docker --version
  docker compose version
  ```

#### Quick Start

```bash
# Option A: Just run — auto-initialization (recommended)
docker run -d \
  --name lalmax-nvr \
  --restart unless-stopped \
  -p 9090:9090 \
  -v ./data:/data \
  ghcr.io/lalmax-pro/lalmax-nvr:latest

# Option B: With initial password
docker run -d \
  --name lalmax-nvr \
  --restart unless-stopped \
  -p 9090:9090 \
  -e NVR_PASSWORD=yourpassword \
  -v ./data:/data \
  ghcr.io/lalmax-pro/lalmax-nvr:latest

# Option C: With docker-compose.yml
mkdir -p data
docker compose up -d
```

> **First-time setup**: When started without a config file, lalmax-nvr auto-generates a default configuration and runs in **setup mode** — all API endpoints are accessible without authentication. Set a password via the Web UI Settings page or the `NVR_PASSWORD` environment variable. Once a password is set, authentication is enforced.

#### Configuration Notes

- **Auto-initialization**: If no config file exists at `/data/lalmax-nvr.yaml`, one is generated automatically with sensible defaults. No manual setup required.
- **Initial password**: Set via `NVR_PASSWORD` environment variable. If not set, the app starts in setup mode (no auth) — set a password through the Web UI Settings page.
- **Data directory**: `storage.root_dir` is automatically set to `/data` inside Docker containers via the `NVR_DATA_DIR` environment variable.

#### docker-compose.yml Reference

Full configuration with annotated fields:

```yaml
services:
  lalmax-nvr:
    # Docker image — official pre-built image
    image: ghcr.io/lalmax-pro/lalmax-nvr:latest

    # Container name (for easier management and log viewing)
    container_name: lalmax-nvr

    # Auto-restart policy: always restart unless manually stopped
    restart: unless-stopped

    # Port mapping: host_port:container_port
    ports:
      - "9090:9090"               # Web UI and REST API
      - "12090:12090"             # lalmax HTTP (WHIP ingest)
      - "4888:4888/udp"           # WebRTC ICE
      - "4888:4888/tcp"
      - "15544:15544"             # lal RTSP playback
      - "5060:5060/tcp"           # GB28181 SIP
      - "5060:5060/udp"
      - "2121:2121"               # FTP server
      - "2122-2140:2122-2140"     # FTP passive mode ports

    # ONVIF multicast discovery needs network_mode: host (then remove ports)

    # Volume mount: map host ./data to container /data
    # Persists config, recordings, and database
    volumes:
      - ./data:/data

    # Environment variables
    environment:
      - NVR_DATA_DIR=/data         # Data directory path
      - TZ=Asia/Shanghai            # Timezone

    # Health check: verifies service status every 30 seconds
    healthcheck:
      test: ["CMD", "lalmax-nvr", "health"]  # Health check command
      interval: 30s                           # Check interval
      timeout: 5s                             # Timeout
      start_period: 10s                       # Grace period after start
      retries: 3                              # Retry count
```

#### Pre-built Images vs Local Build

**Option A: Use pre-built image (recommended)**

- Image: `ghcr.io/lalmax-pro/lalmax-nvr:latest`
- Architecture tags: `latest (multi-arch: amd64 + arm64)`

No extra steps needed — the `docker-compose.yml` uses the pre-built image by default.

**Option B: Build locally**

If you need custom builds or want the latest source code:

```bash
# Multi-stage build (compiles frontend + backend inside container, requires network)
docker build -t lalmax-nvr .

# Cross-compile ARM64 binary on the host
GOOS=linux GOARCH=arm64 ./scripts/unix/build.sh
```

After building locally, replace the `image:` field in `docker-compose.yml` with your local tag.

#### Common Docker Operations

```bash
# View logs (follow mode)
docker compose logs -f lalmax-nvr

# View recent logs (last 100 lines)
docker compose logs --tail 100 lalmax-nvr

# Restart container
docker compose restart lalmax-nvr

# Stop container (preserves data)
docker compose down

# Stop and remove volumes (WARNING: deletes all data!)
docker compose down -v

# Update to latest image
docker compose pull
docker compose up -d

# Container status
docker compose ps

# Resource usage
docker stats lalmax-nvr

# Health check status
docker inspect --format='{{.State.Health.Status}}' lalmax-nvr
```

> **Note**: The container uses a distroless/scratch base image, so `docker exec` shell access is not available. Use `docker compose logs` for debugging.

#### Using Docker CLI

If you prefer not to use Docker Compose, you can run the container directly:

```bash
# 1. Login to GHCR (required for private images)
echo YOUR_GITHUB_TOKEN | docker login ghcr.io -u USERNAME --password-stdin

# 2. Pull the image
docker pull ghcr.io/lalmax-pro/lalmax-nvr:latest

# 3. Run the container
docker run -d \
  --name lalmax-nvr \
  --restart unless-stopped \
  -p 9090:9090 \
  -p 2121:2121 \
  -p 2122-2140:2122-2140 \
  -v ./data:/data \
  -e NVR_DATA_DIR=/data \
  -e TZ=Asia/Shanghai \
  ghcr.io/lalmax-pro/lalmax-nvr:latest

# 4. Check status
docker ps
docker logs -f lalmax-nvr
docker inspect --format='{{.State.Health.Status}}' lalmax-nvr
```

**Run a specific version:**

```bash
docker pull ghcr.io/lalmax-pro/lalmax-nvr:v0.2.0
docker run -d --name lalmax-nvr ... ghcr.io/lalmax-pro/lalmax-nvr:v0.2.0
```

**Stop and remove:**

```bash
docker stop lalmax-nvr
docker rm lalmax-nvr
```

**Update to latest:**

```bash
docker stop lalmax-nvr
docker rm lalmax-nvr
docker pull ghcr.io/lalmax-pro/lalmax-nvr:latest
docker run -d ... ghcr.io/lalmax-pro/lalmax-nvr:latest
```
#### Data Backup and Restore

**Backup:**

```bash
# 1. Stop container
docker compose stop

# 2. Backup data directory
tar czf nvr-backup-$(date +%Y%m%d).tar.gz data/

# 3. Restart
docker compose start
```

**Restore:**

```bash
# 1. Stop and remove container
docker compose down

# 2. Extract backup
tar xzf nvr-backup-20240101.tar.gz

# 3. Start with restored data
docker compose up -d
```

#### Running on Raspberry Pi

Raspberry Pi requires the ARM64 image:

```yaml
# docker-compose.yml — Raspberry Pi configuration
services:
  lalmax-nvr:
    image: ghcr.io/lalmax-pro/lalmax-nvr:latest
    deploy:
      resources:
        limits:
          memory: 512m      # Prevent OOM on RPi 3B
```

Important notes:

- Segment duration must stay at 30s (`segment_duration: "30s"`)
- Use an external USB disk (ext4) for recording storage
- Limit concurrent recording to 2-3 cameras depending on resolution and bitrate

#### Docker Troubleshooting

**Permission errors**

The container runs as nonroot (UID 65534). Fix mount permission issues:

```bash
chown -R 65534:65534 ./data
```

**Port conflicts**

Change the left-side (host) port in `docker-compose.yml`:

```yaml
ports:
  - "8090:9090"   # Change host port to 8090
```

**Container keeps restarting**

Usually a config file error. Check logs:

```bash
docker compose logs lalmax-nvr
```

**FTP won't connect**

Ensure passive port range (2122-2140) is mapped and not blocked by firewall.

**Wrong timezone**

Add the `TZ` environment variable to `docker-compose.yml`:

```yaml
environment:
  - TZ=America/New_York
```

**Docker Compose v1 vs v2**

- Use `docker compose` (with space, v2)
- Not `docker-compose` (with hyphen, v1, deprecated)

**ONVIF device discovery doesn't work in Docker**

ONVIF auto-discovery uses WS-Discovery (UDP multicast to `239.255.255.250:3702`). Docker's default bridge network blocks multicast traffic, so auto-discovery won't find devices.

Solutions:

1. **Host networking** (recommended for discovery): Uncomment `network_mode: host` in `docker-compose.yml` and remove the `ports` section. The container shares the host's network stack, enabling multicast.

2. **Manual probe** (works in any network mode): In the Web UI camera page, use the "Manual Probe" section to enter a device IP address directly. This bypasses multicast and works in any Docker configuration.

3. **Manual camera addition**: Add cameras by specifying the ONVIF endpoint URL directly (e.g., `http://192.168.1.100/onvif/device_service`) in the camera form. ONVIF connection, PTZ control, and streaming work normally in bridge mode — only auto-discovery is affected.

### Manual Installation

If you prefer full control without Docker:

```bash
# 1. Download binary from GitHub Releases
#    https://github.com/lalmax-pro/lalmax-nvr/releases
chmod +x lalmax-nvr

# 2. Create a data directory
mkdir -p ./data

# 3. Initialize config (prompts for admin password)
./lalmax-nvr init \
    --password <your-password> \
    --data-dir ./data \
    --config ./data/lalmax-nvr.yaml \
    --listen ":9090"

# 4. Start in the foreground
./lalmax-nvr -config ./data/lalmax-nvr.yaml
```

### Building from Source

```bash
git clone https://github.com/lalmax-pro/lalmax-nvr.git
cd lalmax-nvr

# Build for current architecture
./scripts/unix/build.sh

# Cross-compile for ARM64 (e.g., Raspberry Pi)
GOOS=linux GOARCH=arm64 ./scripts/unix/build.sh

# Run tests
go test ./...
```

## Reverse Proxy

### Caddy

Caddy provides automatic HTTPS with minimal configuration:

```caddyfile
nvr.example.com {
    reverse_proxy localhost:9090
}
```

For TLS with explicit email:

```caddyfile
{
    email admin@example.com
}

nvr.example.com {
    reverse_proxy localhost:9090
}
```

### Nginx

```nginx
server {
    listen 80;
    server_name nvr.example.com;

    location / {
        proxy_pass http://localhost:9090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /dav/ {
        proxy_pass http://localhost:9090;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_request_buffering off;
        proxy_buffering off;
    }
}
```

## RPi 3B Notes

The Raspberry Pi 3B has 905MB RAM. For stable operation:

- **Segment duration**: Use 30s (`segment_duration: "30s"`). Longer durations hold more frames in RAM (e.g., 120s = 60-80MB per segment).
- **Memory limit**: If using Docker, set a container memory limit such as `512m`; otherwise use your process supervisor's memory limit.
- **Storage**: Use an external USB disk (ext4) for recordings. The SD card will wear out quickly with continuous writes.
- **Cameras**: Limit to 2-3 concurrent H.264/H.265 streams depending on resolution and bitrate.

## Updating

### Docker Update

```bash
docker compose pull
docker compose up -d
```

### Binary Update

```bash
# Stop the running process, replace the binary, then start it again
cp lalmax-nvr /path/to/current/lalmax-nvr
chmod +x /path/to/current/lalmax-nvr
/path/to/current/lalmax-nvr -config ./data/lalmax-nvr.yaml
```

Always back up your config before updating:

```bash
cp ./data/lalmax-nvr.yaml ./data/lalmax-nvr.yaml.backup
```

## Monitoring

### Logs

```bash
docker compose logs --tail 100 lalmax-nvr
docker compose logs -f lalmax-nvr
```

### Health Check

```bash
curl -f http://localhost:9090/api/health
./lalmax-nvr health
```

### Disk Usage

```bash
df -h ./data
du -sh ./data/recordings
```

### Prometheus Metrics

Metrics are available at `/metrics` (public, no auth required):

```bash
curl http://localhost:9090/metrics
```

## Troubleshooting

### App won't start

```bash
# Verify config syntax
./lalmax-nvr -config ./data/lalmax-nvr.yaml
```

### Camera connection failures

```bash
# Test RTSP connection
ffmpeg -rtsp_transport tcp -i "rtsp://admin:pass@192.168.1.100:554/stream" -t 5 -f null -

# Check network
ping 192.168.1.100
```

### Port conflicts

```bash
sudo lsof -i :9090
sudo lsof -i :2121
```

### Permission errors

```bash
ls -la ./data/
```

### High memory usage

Reduce `segment_duration` to 30s. On RPi 3B, prefer Docker or another supervisor with a 512MB memory limit.
