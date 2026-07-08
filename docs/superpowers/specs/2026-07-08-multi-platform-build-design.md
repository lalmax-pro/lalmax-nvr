# Multi-Platform Build GitHub Actions Design

## Overview

This design document outlines the implementation of a unified GitHub Actions workflow that builds executables for multiple platforms (Linux, Windows, macOS) and manages Docker image builds and releases.

## Goals

1. Build executables for 6 platform combinations on every push to main
2. Create GitHub Releases when version tags are pushed
3. Maintain Docker image build functionality
4. Embed version information in binaries
5. Optimize build performance with caching

## Architecture

### Workflow Structure

**Single unified workflow file:** `.github/workflows/build.yml`

This replaces both existing `ci.yml` and `release.yml` files.

### Platform Matrix

| OS | Architecture | Binary Name |
|---------|--------------|--------------------------------------|
| Linux | amd64 | lalmax-nvr-linux-amd64 |
| Linux | arm64 | lalmax-nvr-linux-arm64 |
| Linux | armv7 | lalmax-nvr-linux-armv7 |
| Windows | amd64 | lalmax-nvr-windows-amd64.exe |
| macOS | amd64 | lalmax-nvr-darwin-amd64 |
| macOS | arm64 | lalmax-nvr-darwin-arm64 |

## Trigger Conditions

1. **Push to main branch:** Build all platforms, upload as Artifacts
2. **Push v* tag:** Build all platforms, upload as Artifacts, create GitHub Release
3. **Manual trigger:** Support manual workflow dispatch

## Build Workflow

### Stage 1: Test and Lint

1. Checkout code
2. Setup Go 1.26
3. Setup Node.js 22
4. Run `go vet ./...`
5. Run `go test -race -coverprofile=coverage.out ./...`
6. Check coverage threshold (55%)

### Stage 2: Build Frontend

1. Checkout code
2. Setup Node.js 22 with npm cache
3. Run `cd web && npm ci && npm run build`
4. Copy frontend assets: `cp -r web/dist/* internal/ui/static/`

### Stage 3: Build Binaries (Matrix)

Use matrix strategy to build 6 platform combinations in parallel:

```yaml
strategy:
  matrix:
    include:
      - goos: linux
        goarch: amd64
        suffix: linux-amd64
      - goos: linux
        goarch: arm64
        suffix: linux-arm64
      - goos: linux
        goarch: arm
        goarm: 7
        suffix: linux-armv7
      - goos: windows
        goarch: amd64
        suffix: windows-amd64
        ext: .exe
      - goos: darwin
        goarch: amd64
        suffix: darwin-amd64
      - goos: darwin
        goarch: arm64
        suffix: darwin-arm64
```

Build command:
```bash
CGO_ENABLED=0 GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} \
  go build -ldflags="-s -w \
  -X main.appVersion=${{ github.ref_name }} \
  -X main.commitHash=${{ github.sha }} \
  -X main.buildTime=$(date -u +%Y%m%d%H%M%S)" \
  -o lalmax-nvr-${{ matrix.suffix }}${{ matrix.ext }} \
  ./cmd/lalmax-nvr/
```

**Note:** `github.sha` provides the full commit hash. For short hash, use `${GITHUB_SHA::7}` in the shell.

### Stage 4: Upload Artifacts

Upload all binaries as GitHub Artifacts using `actions/upload-artifact@v4`.

### Stage 5: Create Release (Conditional)

Only execute when a version tag is pushed:

1. Download all artifacts
2. Create GitHub Release using `softprops/action-gh-release@v2`
3. Attach all binaries to the release

### Stage 6: Build Docker Image (Conditional)

Only execute when a version tag is pushed:

1. Login to GitHub Container Registry
2. Extract metadata and generate tags
3. Setup QEMU for cross-platform builds
4. Setup Docker Buildx
5. Build and push multi-arch image (linux/amd64, linux/arm64, linux/armv7)

## Version Information

### Embedded via `-ldflags`

- **appVersion:** Git tag (e.g., v1.0.0) or "dev" if no tag
- **commitHash:** Short Git commit hash
- **buildTime:** UTC timestamp in YYYYMMDDHHMMSS format

### Binary Naming Convention

```
lalmax-nvr-{os}-{arch}[.ext]
```

Where:
- `{os}`: linux, windows, darwin
- `{arch}`: amd64, arm64, armv7
- `.ext`: .exe for Windows only

## Caching Strategy

1. **Go modules:** Cache using `actions/cache` with key based on `go.sum`
2. **Node.js modules:** Cache using `setup-node` built-in cache
3. **Docker layers:** Use GitHub Actions cache backend

## Optimization

1. **Parallel builds:** All 6 platforms build simultaneously
2. **Test/build separation:** Build jobs depend on test job passing
3. **Conditional execution:** Docker builds only on tag push
4. **Binary size reduction:** Use `-s -w` flags to strip debug info

## Files to Create/Modify

1. **Delete:** `.github/workflows/ci.yml`
2. **Delete:** `.github/workflows/release.yml`
3. **Create:** `.github/workflows/build.yml`

## Success Criteria

1. All 6 platform binaries build successfully
2. Frontend assets are embedded correctly
3. Version information is embedded in binaries
4. Artifacts are uploaded for every push to main
5. GitHub Release is created for version tags
6. Docker images are built and pushed for version tags
7. Build time is optimized with caching

## Dependencies

- Go 1.26
- Node.js 22
- GitHub Actions v4
- Docker Buildx
- QEMU for cross-platform Docker builds

## Security Considerations

1. Use `GITHUB_TOKEN` for registry authentication
2. No secrets embedded in binaries
3. All dependencies are pinned by lock files