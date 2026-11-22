#!/usr/bin/env bash
# VeloceTorrent — Self-Contained macOS Finder Release Setup & Launcher (.command)
# Double-clickable from macOS Finder; builds web/dist and native darwin (arm64/amd64)
# vtd and vt binaries into ~/Desktop/VeloceTorrent/, starts vtd in background, and opens vt ui.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DESKTOP_DIR="${HOME}/Desktop"
INSTALL_DIR="${DESKTOP_DIR}/VeloceTorrent"
ADDR="127.0.0.1:7878"

mkdir -p "${INSTALL_DIR}"

ARCH_RAW="$(uname -m)"
if [ "${ARCH_RAW}" = "arm64" ]; then
    GOARCH_TARGET="arm64"
else
    GOARCH_TARGET="amd64"
fi

echo "[VELOCETORRENT MACOS SETUP] Target: darwin/${GOARCH_TARGET} -> ${INSTALL_DIR}"

# 1. Build embedded React 19 Web UI if source is present
if [ -f "${REPO_ROOT}/web/package.json" ] && command -v npm >/dev/null 2>&1; then
    echo "[VELOCETORRENT MACOS SETUP] Compiling Web UI (web/dist)..."
    (cd "${REPO_ROOT}/web" && npm install --no-audit --no-fund && npm run build)
fi

# 2. Build native darwin vtd and vt binaries into ~/Desktop/VeloceTorrent/
if [ -f "${REPO_ROOT}/go.mod" ] && command -v go >/dev/null 2>&1; then
    echo "[VELOCETORRENT MACOS SETUP] Compiling native darwin/${GOARCH_TARGET} binaries..."
    (cd "${REPO_ROOT}" && GOOS=darwin GOARCH="${GOARCH_TARGET}" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${INSTALL_DIR}/vtd" ./cmd/daemon)
    (cd "${REPO_ROOT}" && GOOS=darwin GOARCH="${GOARCH_TARGET}" CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${INSTALL_DIR}/vt" ./cmd/cli)
elif [ -f "${REPO_ROOT}/vtd" ] && [ -f "${REPO_ROOT}/vt" ]; then
    cp -f "${REPO_ROOT}/vtd" "${INSTALL_DIR}/vtd"
    cp -f "${REPO_ROOT}/vt" "${INSTALL_DIR}/vt"
else
    echo "[ERROR] Neither Go toolchain nor prebuilt vtd/vt binaries found." >&2
    exit 1
fi

chmod +x "${INSTALL_DIR}/vtd" "${INSTALL_DIR}/vt"

# Create quick double-clickable launcher inside ~/Desktop/VeloceTorrent/
LAUNCHER_CMD="${INSTALL_DIR}/VeloceTorrent.command"
cat > "${LAUNCHER_CMD}" <<EOF
#!/usr/bin/env bash
if ! curl -fsS "http://${ADDR}/api/health" >/dev/null 2>&1; then
    nohup "${INSTALL_DIR}/vtd" --addr "${ADDR}" >/dev/null 2>&1 &
    sleep 0.5
fi
exec "${INSTALL_DIR}/vt" ui --addr "${ADDR}"
EOF
chmod +x "${LAUNCHER_CMD}"

# 3. Start vtd in the background if not already running
if ! curl -fsS "http://${ADDR}/api/health" >/dev/null 2>&1; then
    echo "[VELOCETORRENT MACOS SETUP] Starting background daemon vtd on ${ADDR}..."
    nohup "${INSTALL_DIR}/vtd" --addr "${ADDR}" >/dev/null 2>&1 &
    for _ in $(seq 1 25); do
        if curl -fsS "http://${ADDR}/api/health" >/dev/null 2>&1; then
            break
        fi
        sleep 0.1
    done
fi

echo "[VELOCETORRENT MACOS SETUP] Installed to ${INSTALL_DIR} (Web UI: http://${ADDR})"

# 4. Launch vt ui
if [[ "${1:-}" != "--no-launch" ]]; then
    exec "${INSTALL_DIR}/vt" ui --addr "${ADDR}"
fi
