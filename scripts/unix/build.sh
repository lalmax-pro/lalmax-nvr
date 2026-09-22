#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/env.sh"

VERSION="${VERSION:-dev}"
COMMIT="${COMMIT:-}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"

# Suppress Node.js deprecation warnings (e.g., module.register)
export NODE_OPTIONS="${NODE_OPTIONS:-} --no-deprecation"
# Cursor sets npm_config_devdir, which npm 11 prints as an unknown config.
unset npm_config_devdir NPM_CONFIG_DEVDIR || true

# Cross-compilation: auto-detect or override via GOOS/GOARCH
# Examples:
#   ./scripts/unix/build.sh                        # build for current platform
#   GOOS=linux GOARCH=arm64 ./scripts/unix/build.sh  # cross-compile for linux/arm64
#   GOOS=linux GOARCH=arm GOARM=7 ./scripts/unix/build.sh  # armv7
TARGET_OS="${GOOS:-$(go env GOOS)}"
TARGET_ARCH="${GOARCH:-$(go env GOARCH)}"

if [[ -z "${COMMIT}" ]] && command -v git >/dev/null 2>&1 && git -C "${ROOT_DIR}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  COMMIT="$(git -C "${ROOT_DIR}" rev-parse --short HEAD)"
fi
COMMIT="${COMMIT:-none}"

# When cross-compiling, append arch suffix to binary name
HOST_OS="$(go env GOOS)"
HOST_ARCH="$(go env GOARCH)"
if [[ "${TARGET_OS}" != "${HOST_OS}" || "${TARGET_ARCH}" != "${HOST_ARCH}" ]]; then
  OUTPUT_BIN="${BIN_PATH}-${TARGET_ARCH}"
  if [[ "${TARGET_ARCH}" == "arm" && -n "${GOARM:-}" ]]; then
    OUTPUT_BIN="${BIN_PATH}-armv${GOARM}"
  fi
else
  OUTPUT_BIN="${BIN_PATH}"
fi

echo "building ${APP_NAME}"
echo "  target: ${TARGET_OS}/${TARGET_ARCH}"
echo "  output: ${OUTPUT_BIN}"
echo "  cgo:    ${CGO_ENABLED}"

cd "${ROOT_DIR}"

if [[ -f "${ROOT_DIR}/web/package.json" ]] && command -v npm >/dev/null 2>&1; then
  if [[ ! -d "${ROOT_DIR}/web/node_modules" ]]; then
    echo "installing frontend dependencies"
    npm --prefix web install
  fi
  echo "building frontend"
  npm --prefix web run build
  rm -rf "${ROOT_DIR}/internal/ui/static/assets"
  cp -r "${ROOT_DIR}/web/dist/"* "${ROOT_DIR}/internal/ui/static/"
fi

CGO_ENABLED="${CGO_ENABLED}" GOOS="${TARGET_OS}" GOARCH="${TARGET_ARCH}" \
  go build \
  -ldflags "-s -w -X main.appVersion=${VERSION}" \
  -o "${OUTPUT_BIN}" \
  ./cmd/lalmax-nvr

"${OUTPUT_BIN}" -version
