#!/usr/bin/env bash
# Package a runnable bundle for the current OS/ARCH.
# The binary is NOT self-contained: it loads frontend assets from a "web"
# directory next to it at runtime (see backend/app/server.go resolveStaticDir).
# This script always bundles the built frontend so the artifact works standalone.
# Usage: bash scripts/package.sh [VERSION]
set -euo pipefail

VERSION="${1:-dev}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.."; pwd)"
DIST_DIR="dist"

GOOS="$(go env GOOS)"
GOARCH="$(go env GOARCH)"
BIN_NAME="curvature"
[[ "$GOOS" == "windows" ]] && BIN_NAME="curvature.exe"

PLATFORM_DIR="curvature_${VERSION}_${GOOS}_${GOARCH}"
OUT_DIR="${ROOT}/${DIST_DIR}/${PLATFORM_DIR}"

echo "==> Packaging curvature ${VERSION} for ${GOOS}/${GOARCH}"
echo "    Output: ${OUT_DIR}/"
echo

rm -rf "${OUT_DIR}"
mkdir -p "${OUT_DIR}/web"

CGO_ENABLED=0 go build \
  -trimpath \
  -ldflags "-s -w -X main.version=${VERSION}" \
  -o "${OUT_DIR}/${BIN_NAME}" \
  ./cli/cmd

# Bundle the built frontend. copy web/dist files directly under OUT_DIR/web
# (NOT OUT_DIR/web/dist) to match resolveStaticDir's <exe>/web layout.
cp -r "${ROOT}/web/dist"/. "${OUT_DIR}/web/"
if [[ -f "${ROOT}/agents.json" ]]; then
  cp "${ROOT}/agents.json" "${OUT_DIR}/agents.json"
fi
if [[ -f "${ROOT}/task_template.json" ]]; then
  cp "${ROOT}/task_template.json" "${OUT_DIR}/task_template.json"
fi

echo "    Stage:"
find "${OUT_DIR}" -maxdepth 3 | sed "s#${ROOT}/##" | sort | head -40
echo

cd "${ROOT}/${DIST_DIR}"
if [[ "$GOOS" == "windows" ]]; then
  ARCHIVE="${PLATFORM_DIR}.zip"
  rm -f "${ARCHIVE}"
  if command -v zip >/dev/null 2>&1; then
    zip -Xqr "${ARCHIVE}" "${PLATFORM_DIR}"
  elif command -v tar >/dev/null 2>&1 && tar --version 2>/dev/null | grep -qi bsdtar; then
    tar -a -c -f "${ARCHIVE}" "${PLATFORM_DIR}"
  elif command -v powershell >/dev/null 2>&1 || command -v pwsh >/dev/null 2>&1; then
    PS="$(command -v powershell || command -v pwsh)"
    "$PS" -NoProfile -NonInteractive -Command \
      "Compress-Archive -Path '${PLATFORM_DIR}' -DestinationPath '${ARCHIVE}' -Force"
  else
    echo "error: neither 'zip', bsdtar, nor PowerShell available to create ${ARCHIVE}" >&2
    exit 1
  fi
  echo "==> Archived: ${DIST_DIR}/${ARCHIVE}"
else
  ARCHIVE="${PLATFORM_DIR}.tar.gz"
  rm -f "${ARCHIVE}"
  tar --no-xattrs -czf "${ARCHIVE}" "${PLATFORM_DIR}"
  echo "==> Archived: ${DIST_DIR}/${ARCHIVE}"
fi

echo
echo "==> Done. Bundle ready to ship:"
echo "    ${OUT_DIR}/       (exe + web/ frontend assets, works standalone)"
echo "    ${DIST_DIR}/${ARCHIVE}"