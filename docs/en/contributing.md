# Contributing Guide

Welcome to lalmax-nvr development. This document covers environment setup, submitting code, and passing CI checks.

---

## Prerequisites

| Tool | Version | Notes |
|------|---------|-------|
| Go | see [`.go-version`](../../.go-version) | Built with `CGO_ENABLED=0`, no cgo required |
| Node.js | see [`.nvmrc`](../../.nvmrc) | For frontend (Svelte) build |
| Docker | 20.10+ | Run integration tests / container builds |
| FFmpeg | any | For media-related tests (optional, install as needed) |

---

## Quick Setup

```bash
git clone https://github.com/lalmax-pro/lalmax-nvr.git
cd lalmax-nvr

# Build backend + frontend
./scripts/unix/build.sh

# Start (auto-copies config from config.example.yaml on first run)
./scripts/unix/run.sh
```

Windows users: use scripts under `scripts/windows/`, see [`scripts/README.md`](../../scripts/README.md).

---

## Directory Structure

```
cmd/lalmax-nvr/        # Entry point, CLI, integration tests
internal/              # Core business packages (see module-guide.md)
  api/                 # HTTP routing and handlers
  camera/              # Camera lifecycle
  transcode/           # Transcode manager and backends
  media/               # lalmax media engine adapter
  recorder/            # Group writer and recording
  ...
web/                   # Svelte frontend source
docs/zh/               # Chinese documentation
docs/en/               # English documentation (this directory)
config/                # Configuration examples
scripts/               # Build and run scripts
```

See [Module Guide](module-guide.md) for package responsibilities.

---

## Testing

### Unit Tests

```bash
# Full suite (excluding media E2E)
go test ./...

# With race detection (required, enforced by CI)
go test -race ./...

# Specific package
go test -race -v ./internal/transcode/...
```

### Media Integration Tests

Enabled via environment variable; requires local tool installation:

```bash
# FFmpeg media self-test (requires ffmpeg + ffprobe)
NVR_TRANSCODE_MEDIA_TEST=1 go test -race -v ./internal/transcode/...

# GStreamer integration tests (requires gst-launch-1.0)
NVR_GSTREAMER_INTEGRATION_TEST=1 go test -race -v ./internal/transcode/...
```

Media tests use synthetic streams only — no real cameras involved. See [Testing Guide](testing-guide.md).

### Frontend Tests

```bash
cd web
npm install
npm test        # single-run mode (CI mode)
```

---

## Code Standards

### Go

- `gofmt` formatting (CI enforced)
- `go vet` static analysis (CI enforced)
- No CGO dependencies; `CGO_ENABLED=0` build must always work
- New packages must include a package-level doc comment describing responsibility
- Interfaces belong in the consumer package (Go convention), not the implementation

### Commit Messages

Follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>(<scope>): <short description>

[optional body]

[optional footer, e.g. Fixes #66]
```

Common types:

| type | meaning |
|------|---------|
| `feat` | new feature |
| `fix` | bug fix |
| `refactor` | refactoring (no behavior change) |
| `docs` | documentation only |
| `test` | test-related changes |
| `chore` | build, deps, toolchain |

Use the affected package name as scope: `transcode`, `api`, `recorder`.

Examples:
```
feat(transcode): add GStreamer backend with fixed pipeline templates
fix(transcode): preserve explicit stop intent across restarts
docs: restructure docs index by reader type
```

---

## Pull Request Process

1. **Fork → feature branch**: suggested naming `feat/<scope>-<brief>` or `fix/<scope>-<brief>`
2. **Pass locally**: `go test -race ./...` + `go vet ./...` + frontend tests
3. **Describe clearly**: PR title follows commit conventions; body covers scope, verification, known limitations
4. **Don't auto-merge**: feature PRs with media acceptance requirements stay Draft until acceptance is complete
5. **One PR, one thing**: large features should be split into P0/P1/P2 stages

### PR Description Template

```markdown
## Changes
<!-- Brief description of what changed and why -->

## Verification
<!-- Unit tests / media E2E / manual steps -->

## Known Limitations
<!-- Uncovered scenarios, TODOs, follow-up issues -->

## Related
<!-- Fixes #xx / Tracks #xx -->
```

---

## CI Overview

CI is driven by `.github/workflows/build.yml` and covers:

- Go cross-platform build (Linux amd64/arm64/armv7, Windows amd64, macOS amd64/arm64)
- `go test -race`, `go vet`
- Frontend build and tests
- HA integration package validation (`hacs-validation.yml`)
- Docker container build and smoke test (`container-smoke.yml`)
- Windows transcode package cross-compilation (`transcode-windows.yml`)

CI passing is a necessary condition for merging, but not sufficient for media acceptance — media E2E is documented separately in the PR body.

---

## Releases

See [Releases & CI](releases.md) for the release workflow.

---

## Issues & Discussion

- Bugs and feature requests: [GitHub Issues](https://github.com/lalmax-pro/lalmax-nvr/issues)
- Design discussions: in the relevant Issue or PR; major decisions recorded in [ADRs](adr/README.md)
