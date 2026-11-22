#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

if [ ! -x "./vt" ]; then
  echo "[1/2] Compiling VeloceTorrent binaries for macOS..."
  go build -trimpath -ldflags="-s -w" -o vtd ./cmd/daemon
  go build -trimpath -ldflags="-s -w" -o vt ./cmd/cli
fi

echo "Starting VeloceTorrent Daemon and opening Web Control Plane (http://127.0.0.1:7878)..."
./vt open
./vt ui
