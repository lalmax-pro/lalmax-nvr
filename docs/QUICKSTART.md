# lalmax-nvr Quick Start

## 1. Configure

Copy the example config and edit it:

```bash
cp config.example.yaml lalmax-nvr.yaml
```

Edit `lalmax-nvr.yaml` to set your camera RTSP streams, storage path, etc.

## 2. Run

### Linux / macOS

```bash
# Run directly
./lalmax-nvr -c lalmax-nvr.yaml

# Or use scripts
./scripts/start.sh    # Start in background
./scripts/stop.sh     # Stop
./scripts/restart.sh  # Restart
./scripts/status.sh   # Check status
./scripts/logs.sh     # View logs
```

### Windows

```cmd
REM Run directly
lalmax-nvr.exe -c lalmax-nvr.yaml

REM Or use scripts
scripts\start.bat
scripts\stop.bat
scripts\restart.bat
scripts\status.bat
scripts\logs.bat
```

## 3. Access Web UI

Open browser: `http://localhost:8080`

Default login (if enabled): check `lalmax-nvr.yaml` for credentials.

## 4. Add Cameras

- **ONVIF cameras**: Go to Devices page, click "Discover" to auto-detect
- **RTSP cameras**: Go to Cameras page, click "Add Camera" and enter RTSP URL
- **GB28181 cameras**: Configure GB28181 settings, cameras auto-register

## Command Line Options

```
./lalmax-nvr -h
```

| Flag | Description | Default |
|------|-------------|---------|
| `-c` | Config file path | `lalmax-nvr.yaml` |
| `-v` | Show version | |

## More Documentation

- [README.md](README.md) - Full documentation (English)
- [README.zh.md](README.zh.md) - Full documentation (中文)
- GitHub: https://github.com/lalmax-pro/lalmax-nvr
