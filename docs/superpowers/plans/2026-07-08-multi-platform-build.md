# Multi-Platform Build GitHub Actions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create a unified GitHub Actions workflow that builds executables for 6 platforms (Linux, Windows, macOS) and manages Docker image builds and releases.

**Architecture:** Single workflow file using matrix strategy for parallel builds, conditional execution for releases and Docker images, with proper caching for optimization.

**Tech Stack:** GitHub Actions, Go 1.26, Node.js 22, Docker Buildx, QEMU

---

## File Structure

**Files to create:**
- `.github/workflows/build.yml` - Main workflow file

**Files to delete:**
- `.github/workflows/ci.yml` - Replaced by build.yml
- `.github/workflows/release.yml` - Replaced by build.yml

---

### Task 1: Delete existing workflow files

**Files:**
- Delete: `.github/workflows/ci.yml`
- Delete: `.github/workflows/release.yml`

- [ ] **Step 1: Delete ci.yml**

```bash
rm .github/workflows/ci.yml
```

- [ ] **Step 2: Delete release.yml**

```bash
rm .github/workflows/release.yml
```

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/ci.yml .github/workflows/release.yml
git commit -m "chore: remove old workflow files"
```

---

### Task 2: Create build.yml - Workflow structure and triggers

**Files:**
- Create: `.github/workflows/build.yml`

- [ ] **Step 1: Create build.yml with basic structure**

Create `.github/workflows/build.yml` with the following content:

```yaml
name: Build

on:
  push:
    branches: [main]
    tags:
      - 'v*'
  workflow_dispatch:

permissions:
  contents: write
  packages: write

env:
  REGISTRY: ghcr.io
  IMAGE_NAME: ${{ github.repository }}
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "feat: create build.yml with triggers"
```

---

### Task 3: Add test job

**Files:**
- Modify: `.github/workflows/build.yml`

- [ ] **Step 1: Add test job**

Add the following job to `.github/workflows/build.yml` after the `env` section:

```yaml
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'

      - uses: actions/setup-node@v4
        with:
          node-version: '22'
          cache: 'npm'
          cache-dependency-path: web/package-lock.json

      - name: Lint
        run: go vet ./...

      - name: Test
        run: go test -race -coverprofile=coverage.out ./...

      - name: Display coverage
        run: go tool cover -func=coverage.out

      - name: Check coverage threshold
        run: |
          echo "Checking coverage threshold..."
          total=$(go tool cover -func=coverage.out | grep 'total:' | awk '{print $3}' | sed 's/%//' )
          echo "Total coverage: $total%"
          if (( $(echo "$total < 55" | bc -l) )); then
            echo "Coverage $total% is below threshold of 55%"
            exit 1
          fi
          echo "Coverage threshold check passed"
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "feat: add test job to build.yml"
```

---

### Task 4: Add frontend build job

**Files:**
- Modify: `.github/workflows/build.yml`

- [ ] **Step 1: Add frontend build job**

Add the following job after the `test` job:

```yaml
  build-frontend:
    runs-on: ubuntu-latest
    needs: test
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-node@v4
        with:
          node-version: '22'
          cache: 'npm'
          cache-dependency-path: web/package-lock.json

      - name: Build frontend
        run: |
          cd web
          npm ci
          npm run build
          cp -r dist/* ../internal/ui/static/

      - name: Upload frontend artifact
        uses: actions/upload-artifact@v4
        with:
          name: frontend
          path: internal/ui/static/
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "feat: add frontend build job to build.yml"
```

---

### Task 5: Add matrix build job

**Files:**
- Modify: `.github/workflows/build.yml`

- [ ] **Step 1: Add matrix build job**

Add the following job after the `build-frontend` job:

```yaml
  build-binaries:
    runs-on: ubuntu-latest
    needs: build-frontend
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
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version: '1.26'

      - name: Download frontend artifact
        uses: actions/download-artifact@v4
        with:
          name: frontend
          path: internal/ui/static/

      - name: Build binary
        run: |
          CGO_ENABLED=0 GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} \
            go build -ldflags="-s -w \
            -X main.appVersion=${{ github.ref_name }} \
            -X main.commitHash=${GITHUB_SHA::7} \
            -X main.buildTime=$(date -u +%Y%m%d%H%M%S)" \
            -o lalmax-nvr-${{ matrix.suffix }}${{ matrix.ext }} \
            ./cmd/lalmax-nvr/

      - name: Upload binary artifact
        uses: actions/upload-artifact@v4
        with:
          name: lalmax-nvr-${{ matrix.suffix }}
          path: lalmax-nvr-${{ matrix.suffix }}${{ matrix.ext }}
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "feat: add matrix build job to build.yml"
```

---

### Task 6: Add release job

**Files:**
- Modify: `.github/workflows/build.yml`

- [ ] **Step 1: Add release job**

Add the following job after the `build-binaries` job:

```yaml
  release:
    runs-on: ubuntu-latest
    needs: build-binaries
    if: startsWith(github.ref, 'refs/tags/v')
    steps:
      - uses: actions/checkout@v4

      - name: Download all artifacts
        uses: actions/download-artifact@v4
        with:
          path: artifacts

      - name: Create release
        uses: softprops/action-gh-release@v2
        with:
          generate_release_notes: true
          append_body: true
          files: |
            artifacts/lalmax-nvr-linux-amd64/lalmax-nvr-linux-amd64
            artifacts/lalmax-nvr-linux-arm64/lalmax-nvr-linux-arm64
            artifacts/lalmax-nvr-linux-armv7/lalmax-nvr-linux-armv7
            artifacts/lalmax-nvr-windows-amd64/lalmax-nvr-windows-amd64.exe
            artifacts/lalmax-nvr-darwin-amd64/lalmax-nvr-darwin-amd64
            artifacts/lalmax-nvr-darwin-arm64/lalmax-nvr-darwin-arm64
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "feat: add release job to build.yml"
```

---

### Task 7: Add Docker build job

**Files:**
- Modify: `.github/workflows/build.yml`

- [ ] **Step 1: Add Docker build job**

Add the following job after the `release` job:

```yaml
  docker:
    runs-on: ubuntu-latest
    needs: build-binaries
    if: startsWith(github.ref, 'refs/tags/v')
    steps:
      - uses: actions/checkout@v4

      - name: Log in to Container Registry
        uses: docker/login-action@v3
        with:
          registry: ${{ env.REGISTRY }}
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}

      - name: Extract metadata
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.REGISTRY }}/${{ env.IMAGE_NAME }}
          tags: |
            type=semver,pattern={{version}}
            type=semver,pattern={{major}}.{{minor}}
            type=semver,pattern={{major}}
            type=raw,value=latest

      - name: Set up QEMU
        uses: docker/setup-qemu-action@v3

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Build and push Docker image
        uses: docker/build-push-action@v6
        with:
          context: .
          platforms: linux/amd64,linux/arm64,linux/arm/v7
          push: true
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          build-args: |
            VERSION=${{ github.ref_name }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

- [ ] **Step 2: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "feat: add Docker build job to build.yml"
```

---

### Task 8: Add Go module cache

**Files:**
- Modify: `.github/workflows/build.yml`

- [ ] **Step 1: Add Go module cache to test job**

In the `test` job, add the following step after `actions/setup-go`:

```yaml
      - name: Cache Go modules
        uses: actions/cache@v4
        with:
          path: ~/go/pkg/mod
          key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
          restore-keys: |
            ${{ runner.os }}-go-
```

- [ ] **Step 2: Add Go module cache to build-binaries job**

In the `build-binaries` job, add the following step after `actions/setup-go`:

```yaml
      - name: Cache Go modules
        uses: actions/cache@v4
        with:
          path: ~/go/pkg/mod
          key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
          restore-keys: |
            ${{ runner.os }}-go-
```

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/build.yml
git commit -m "feat: add Go module cache to build.yml"
```

---

### Task 9: Final verification and commit

**Files:**
- Modify: `.github/workflows/build.yml`

- [ ] **Step 1: Verify complete workflow file**

Read the complete `.github/workflows/build.yml` file and verify it contains:
- Triggers for main branch, tags, and manual dispatch
- Test job with lint, test, and coverage check
- Frontend build job
- Matrix build job for 6 platforms
- Release job (conditional on tags)
- Docker build job (conditional on tags)
- Go module caching

- [ ] **Step 2: Final commit**

```bash
git add .github/workflows/build.yml
git commit -m "feat: complete multi-platform build workflow"
```

---

## Self-Review Checklist

1. **Spec coverage:**
   - ✓ Build executables for 6 platforms
   - ✓ Create GitHub Releases for version tags
   - ✓ Build Docker images for version tags
   - ✓ Embed version information in binaries
   - ✓ Cache Go modules and Node.js modules
   - ✓ Upload artifacts for every push to main

2. **Placeholder scan:**
   - ✓ No TBD, TODO, or incomplete sections
   - ✓ All code blocks are complete
   - ✓ All commands are exact

3. **Type consistency:**
   - ✓ Binary naming convention is consistent
   - ✓ Artifact names match matrix suffixes
   - ✓ Version variables are consistent

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-07-08-multi-platform-build.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

Which approach?