@echo off
title VeloceTorrent // High-Throughput Control Plane
cd /d "%~dp0"

if not exist "vt.exe" (
    echo [1/2] Compiling VeloceTorrent binaries...
    go build -trimpath -ldflags="-s -w" -o vtd.exe ./cmd/daemon
    go build -trimpath -ldflags="-s -w" -o vt.exe ./cmd/cli
)

echo Starting VeloceTorrent Daemon and opening Web Control Plane (http://127.0.0.1:7878)...
"%~dp0vt.exe" open
"%~dp0vt.exe" ui
