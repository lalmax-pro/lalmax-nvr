# lalmax-nvr Build and Runtime Scripts

## One-command complete builds

After updating Git, run the appropriate script from the repository root (or any working directory).
Each build runs **npm ci**, **Vite production build**, **copies the current web/dist into internal/ui/static**,
**go build ./...** for root, third/lal and third/lalmax, then **builds the final Go executable**.
Builds fail immediately if npm, Go, a required artifact, or any build command is missing or fails.
Old embedded frontend assets are never used as an implicit fallback.

### On a Windows workstation — build both targets

```powershell
.\scripts\windows\build.bat          # Windows amd64 -> bin/lalmax-nvr.exe
.\scripts\windows\build-linux.bat    # Linux amd64 -> bin/lalmax-nvr
```

Both .bat wrappers invoke PowerShell directly, without Git Bash or scripts/unix/build.sh.
The build targets are passed as -BuildOS/-BuildArch parameters. nvr.ps1 sets
GOOS/GOARCH/CGO_ENABLED only during its build and restores their prior values.
Windows and Linux outputs coexist in bin/ as lalmax-nvr.exe and lalmax-nvr.
For other architectures, use the same configurable PowerShell entrypoint:

```powershell
.\scripts\windows\nvr.ps1 build -BuildOS linux -BuildArch arm64   # -> bin/lalmax-nvr
```

### On Linux / macOS

```bash
./scripts/unix/build.sh              # native -> bin/lalmax-nvr
GOOS=linux GOARCH=arm64 ./scripts/unix/build.sh   # -> bin/lalmax-nvr
```

`scripts/unix/build.sh` is for Unix hosts only; on Windows use the PowerShell/batch entrypoints.
Cross-compilation on Unix also uses the stable filename `lalmax-nvr`, so different Unix architectures
will replace that output rather than creating suffixed files.

### Prerequisites and notes

- Go matching root go.mod (CI uses Go 1.26.2) and Node/npm (CI uses Node 22).
- npm ci runs on every build so Git updates to web/package-lock.json are reflected.
- GO_BUILD_JOBS defaults to 1 to reduce memory use; set a larger value on faster machines.
- Configurable entrypoints continue to accept GOOS, GOARCH, GOARM, CGO_ENABLED, BIN_PATH, VERSION.
- Building is **not testing**: use scripts/windows/nvr.ps1 test or scripts/unix/test.sh for tests.
- Building does not deploy, change the runtime configuration, or restart a service.

## Runtime commands

Windows: .\scripts\windows\nvr.ps1 run|start|stop|restart|status|logs|test.

Linux: scripts/unix/run.sh, start.sh, stop.sh, restart.sh, status.sh, logs.sh, test.sh.

Runtime output defaults: bin/lalmax-nvr (Windows: .exe), run/lalmax-nvr.pid, logs/lalmax-nvr.log, data/.

Override example: `CONFIG_FILE=./lalmax-nvr.dev.yaml ./scripts/unix/start.sh`.

## Documentation and Wiki utilities

From the repository root, use Python 3 without third-party packages:

```bash
python3 scripts/check_docs.py                 # check repository-local Markdown links
python3 scripts/build_wiki.py --output tmp/wiki  # generate GitHub Wiki pages locally
```

The Wiki builder does not push to GitHub. Initialization, manual publication, and the main-branch Actions workflow are documented in `docs/GITHUB_WIKI.md`.
