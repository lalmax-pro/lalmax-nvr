# Getting started

The NVR serves its Web UI and API on `:9090` by default. The media engine defaults to `media.mode: embedded`. Cameras, IPTV channels, streams, and recording plans are managed separately; adding a device alone does not start recording.

## Docker Compose

From the repository root:

```bash
docker compose up -d
```

Open `http://localhost:9090` and complete the first-run setup in the browser. `docker-compose.yml` maps the host's `./data` to `/data` in the container. To use another disk, change the host side of the volume mount and ensure the container can write there. LAN discovery through ONVIF or DLNA SSDP needs multicast networking; Docker bridge mode usually prevents such discovery. See [Deployment](deployment.md).

## Build and run from the repository

From the repository root:

```bash
./scripts/unix/build.sh
./scripts/unix/run.sh
```

The build script writes `bin/lalmax-nvr`. When `config/lalmax-nvr.yaml` is absent, `run.sh` copies `config/config.example.yaml` and points storage to the repository's `data/` directory. You can also run the built binary directly:

```bash
./bin/lalmax-nvr init --password 'at-least-eight-characters' --config config/lalmax-nvr.yaml --data-dir ./data
./bin/lalmax-nvr -config config/lalmax-nvr.yaml
```

`init` writes `lalmax-nvr.yaml` in the current directory by default, while the main program reads `config/lalmax-nvr.yaml` by default. Specify `--config` when initializing manually. An existing file is not overwritten unless `init` receives `--force`.

Windows build and start scripts are under [`scripts/windows`](../../scripts/windows/); see [`scripts/README.md`](../../scripts/README.md).

## Add sources

1. Add an RTSP or ONVIF camera on the **Devices** page. The connection check tests reachability, not decoding; see [Camera ingest](camera-guide.md).
2. Import an M3U playlist and probe channels on the **IPTV** page. Channels play directly in the browser; enable publication or create a plan when other protocols or recording are needed. See [IPTV](iptv.md).
3. Create a plan for the desired `stream_id` on the **Recording plans** page. See [Recording plans](recording-plans.md).

A running NVR serves API documentation at `/docs/`. [WebDAV](webdav-integration.md) is read-only by default and uses Web/API authentication. [FTP](ftp-integration.md) is enabled by default, but its plaintext password authentication currently conflicts with the hash saved after initialization; read its guide before relying on it.

## Common addresses and ports

| Default port | Purpose |
|--------------|---------|
| `9090` | Web UI, API, WebDAV |
| `12090` | lalmax HTTP |
| `15544` | RTSP playback |
| `8200` | DLNA HTTP when enabled; discovery also needs UDP `1900` multicast |

See [Architecture](architecture.md) for other ports and ingest paths, and [Troubleshooting](troubleshooting.md) for startup or playback issues.
