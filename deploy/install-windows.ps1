$ErrorActionPreference = "Stop"
Set-Location (Split-Path -Parent $MyInvocation.MyCommand.Path)
Set-Location ..

Write-Host "[1/3] Building Web Control Plane (web/dist)..." -ForegroundColor Cyan
Push-Location web
npm install --no-audit --no-fund
npm run build
Pop-Location

Write-Host "[2/3] Compiling single-binary vtd.exe and vt.exe..." -ForegroundColor Cyan
go build -trimpath -ldflags="-s -w" -o vtd.exe ./cmd/daemon
go build -trimpath -ldflags="-s -w" -o vt.exe ./cmd/cli

Write-Host "[3/3] Installing Windows integration (PATH, Startup, Desktop Shortcuts, Magnet Handler)..." -ForegroundColor Cyan
.\vt.exe install
.\vt.exe open
