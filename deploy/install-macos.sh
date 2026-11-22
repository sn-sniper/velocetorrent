#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

echo "[1/3] Building Web Control Plane (web/dist)..."
(cd web && npm install --no-audit --no-fund && npm run build)

echo "[2/3] Compiling single-binary vtd and vt for macOS..."
go build -trimpath -ldflags="-s -w" -o vtd ./cmd/daemon
go build -trimpath -ldflags="-s -w" -o vt ./cmd/cli

echo "[3/3] Installing macOS LaunchAgent, ~/Applications/VeloceTorrent.app, and binaries..."
./vt install
./vt open
