# Changelog

All notable changes to **VeloceTorrent** are documented in this file.

## [v1.0.0] - 2026-11-21

### Added
- **Core BitTorrent Engine (`internal/engine`):**
  - Adaptive Bandwidth-Delay Product (BDP) request pipelining (`8..256` x 16 KiB blocks per peer).
  - Rarest-First + Sequential + 16-Piece Streaming Lookahead + Endgame duplicate block cancellation picker (`picker.go`).
  - Sparse file pre-allocation (`FSCTL_SET_SPARSE` / `Truncate`), parallel `runtime.NumCPU()` SHA-1 / SHA-256 verification pool, and 4–16 MiB contiguous NVMe write coalescing (`storage.go`).
  - 10-second aggressive optimistic unchoke rotation and 4 MiB TCP+BBR window scaling (`peer.go`, `engine.go`).
  - Automatic UPnP IGD and RFC 6886 NAT-PMP router port mapping (`nat.go`).
  - Transactional CRC32-framed `state.db` persistence with 5-second debounce and `< 5 ms` `mtime` + extent startup verification (`state.go`).
  - In-memory binary Radix Tree IP blocklist supporting CIDR and PeerGuardian ranges (`blocklist.go`).
  - BEP 27 `private: 1` flag enforcement (automatic DHT & PEX suppression) and configurable seed-ratio / seeding-time auto-stop limits.
  - Watch-folder (`~/Downloads/torrents/`) and regex RSS auto-downloader (`autodownloader.go`).
  - Post-download `on_complete` lifecycle script and webhook runner (`hooks.go`).
- **Dual Control Plane (`internal/ipc`, `internal/tui`, `web/`):**
  - Local Unix Domain Socket + `127.0.0.1:7878` REST & 100ms RFC 6455 WebSocket server with 256-bit Bearer Token auth, DNS rebinding protection, and Tailscale CGNAT (`100.64.0.0/10`) support.
  - Prometheus `/metrics` exposition and `/debug/pprof/goroutineleak` diagnostics.
  - 60fps `Bubble Tea` + `Lip Gloss` Terminal UI (`vt`) with 1/8th sub-character progress bars, 2-row Unicode Braille velocity sparklines, piece bitfield heatmaps, peer inspector, and selective file priority controls.
  - Single-binary embedded React 19 + TypeScript + Tailwind CSS Web UI (`//go:embed all:dist`) with custom `<svg>` sparklines and selective file priority controls.
- **Ecosystem Integrations:**
  - Model Context Protocol stdio server (`vt mcp` in `internal/mcp/server.go`).
  - Manifest V3 Chrome/Brave/Edge Browser Extension (`extensions/chrome-mv3/`).
  - Dynamic shell completions (`vt completion bash|zsh|fish|powershell`).
