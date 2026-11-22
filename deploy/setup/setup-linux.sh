#!/usr/bin/env bash
# VeloceTorrent — Self-Contained Linux Release Setup & Desktop Launcher
# Builds web/dist, compiles stripped vtd and vt binaries into ~/Desktop/VeloceTorrent/,
# creates ~/Desktop/VeloceTorrent.desktop, starts vtd detached in the background, and launches vt ui.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
DESKTOP_DIR="${HOME}/Desktop"
INSTALL_DIR="${DESKTOP_DIR}/VeloceTorrent"
ADDR="127.0.0.1:7878"

mkdir -p "${INSTALL_DIR}"

echo "[VELOCETORRENT SETUP] Target install directory: ${INSTALL_DIR}"

# 1. Build embedded React 19 Web UI if source is present
if [ -f "${REPO_ROOT}/web/package.json" ] && command -v npm >/dev/null 2>&1; then
    echo "[VELOCETORRENT SETUP] Compiling Web UI (web/dist)..."
    (cd "${REPO_ROOT}/web" && npm install --no-audit --no-fund && npm run build)
fi

# 2. Compile stripped vtd and vt binaries into ~/Desktop/VeloceTorrent/
if [ -f "${REPO_ROOT}/go.mod" ] && command -v go >/dev/null 2>&1; then
    echo "[VELOCETORRENT SETUP] Building stripped vtd and vt binaries..."
    (cd "${REPO_ROOT}" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${INSTALL_DIR}/vtd" ./cmd/daemon)
    (cd "${REPO_ROOT}" && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o "${INSTALL_DIR}/vt" ./cmd/cli)
elif [ -f "${REPO_ROOT}/vtd" ] && [ -f "${REPO_ROOT}/vt" ]; then
    cp -f "${REPO_ROOT}/vtd" "${INSTALL_DIR}/vtd"
    cp -f "${REPO_ROOT}/vt" "${INSTALL_DIR}/vt"
else
    echo "[ERROR] Neither Go toolchain nor prebuilt vtd/vt binaries found." >&2
    exit 1
fi

chmod +x "${INSTALL_DIR}/vtd" "${INSTALL_DIR}/vt"

# Copy icon asset if present
if [ -f "${REPO_ROOT}/assets/icon.svg" ]; then
    cp -f "${REPO_ROOT}/assets/icon.svg" "${INSTALL_DIR}/icon.svg"
fi

# 3. Generate Desktop Launcher on ~/Desktop/VeloceTorrent.desktop
DESKTOP_FILE="${DESKTOP_DIR}/VeloceTorrent.desktop"
cat > "${DESKTOP_FILE}" <<EOF
[Desktop Entry]
Version=1.0
Type=Application
Name=VeloceTorrent
GenericName=BitTorrent Telemetry Client
Comment=High-Throughput BitTorrent Engine, 60fps Terminal UI & Local Web Control Plane
Exec=${INSTALL_DIR}/vt ui --addr ${ADDR}
Icon=${INSTALL_DIR}/icon.svg
Terminal=true
Categories=Network;FileTransfer;P2P;ConsoleOnly;
Keywords=bittorrent;torrent;p2p;cli;tui;
StartupNotify=false
EOF
chmod +x "${DESKTOP_FILE}"

# 4. Start vtd detached in the background if not already running
if ! curl -fsS "http://${ADDR}/api/health" >/dev/null 2>&1; then
    echo "[VELOCETORRENT SETUP] Starting background daemon vtd on ${ADDR}..."
    nohup "${INSTALL_DIR}/vtd" --addr "${ADDR}" >/dev/null 2>&1 &
    for _ in $(seq 1 25); do
        if curl -fsS "http://${ADDR}/api/health" >/dev/null 2>&1; then
            break
        fi
        sleep 0.1
    done
fi

echo "[VELOCETORRENT SETUP] Installed to ${INSTALL_DIR} (Web UI: http://${ADDR})"

# 5. Launch vt ui in the current terminal (unless --no-launch is passed)
if [[ "${1:-}" != "--no-launch" ]]; then
    exec "${INSTALL_DIR}/vt" ui --addr "${ADDR}"
fi
