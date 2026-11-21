#!/usr/bin/env bash
set -euo pipefail

BIN_DIR="${HOME}/.local/bin"
SYSTEMD_USER_DIR="${HOME}/.config/systemd/user"
APPLICATIONS_DIR="${HOME}/.local/share/applications"

mkdir -p "${BIN_DIR}" "${SYSTEMD_USER_DIR}" "${APPLICATIONS_DIR}"

echo "[1/5] Building Web UI assets..."
(cd web && npm run build)

echo "[2/5] Compiling single-binary embedded vtd and vt executables..."
go build -o "${BIN_DIR}/vtd" ./cmd/daemon
go build -o "${BIN_DIR}/vt" ./cmd/cli

echo "[3/5] Applying kernel UDP buffer & TCP BBR sysctl config (if root/sudo available)..."
if [ -w /etc/sysctl.d ]; then
  cp deploy/sysctl/99-velocetorrent.conf /etc/sysctl.d/99-velocetorrent.conf
  sysctl --system >/dev/null 2>&1 || true
else
  echo "  -> Run 'sudo cp deploy/sysctl/99-velocetorrent.conf /etc/sysctl.d/ && sudo sysctl --system' to apply 16MB UDP buffers & BBR."
fi

echo "[4/5] Installing systemd --user service (velocetorrent.service)..."
cp deploy/systemd/velocetorrent.service "${SYSTEMD_USER_DIR}/velocetorrent.service"
systemctl --user daemon-reload
systemctl --user enable --now velocetorrent.service

echo "[5/5] Registering magnet: and .torrent desktop/browser MIME handlers..."
cp deploy/desktop/velocetorrent.desktop "${APPLICATIONS_DIR}/velocetorrent.desktop"
xdg-mime default velocetorrent.desktop x-scheme-handler/magnet
xdg-mime default velocetorrent.desktop application/x-bittorrent
update-desktop-database "${APPLICATIONS_DIR}" 2>/dev/null || true

echo "DONE // VeloceTorrent daemon online at http://127.0.0.1:7878"
