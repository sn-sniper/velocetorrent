# Project Specification: VeloceTorrent (High-Throughput BitTorrent Engine, CLI & Local Web UI)

## 1. System Overview & Core Objectives
A high-performance, dual-interface BitTorrent client engineered to outperform standard desktop clients (e.g., qBittorrent) in raw throughput, disk I/O efficiency, and resource footprint. 

* **Core Engine:** Daemonized backend handling BitTorrent protocol logic, uTP/TCP multiplexing, DHT, PEX, magnet resolution, and zero-copy/pre-allocated disk I/O.
* **Dual Control Plane:** Both the **Terminal UI (TUI)** and **Local Web App** act as thin, reactive clients communicating with the core daemon over a local Unix Domain Socket / WebSocket + REST IPC layer. Starting a download in the CLI immediately reflects in the Web UI, and vice versa.

---

## 2. Recommended Tech Stack
* **Core Engine & CLI:** **Go** (`anacrolix/torrent` or custom `epoll`/`io_uring` piece-picker engine) OR **Rust** (`librqbit` + `tokio`). *(Go recommended for rapid concurrency primitives, single-binary compilation, and native `charmbracelet/bubbletea` TUI integration).*
* **TUI Framework:** `Bubble Tea` + `Lip Gloss` + `Bubbles` (Go) or `Ratatui` (Rust) for 60fps terminal rendering, braille throughput sparklines, and piece-map heatmaps.
* **IPC / API Layer:** Local HTTP/3 or HTTP/1.1 REST API + WebSockets (Server-Sent Events / binary frames for high-frequency telemetry) bound strictly to `127.0.0.1` / Unix socket.
* **Web Frontend:** React 19, TypeScript, Vite, Tailwind CSS, Shadcn UI (heavily customized per `unslop` rules), and TanStack Query.

---

## 3. Performance & Throughput Engineering (Outperforming Standard Clients)
To achieve maximum network saturation and minimal CPU/RAM overhead:
1. **Sequential & Endgame Rarest-First Hybrid Piece Selection:** Prioritize rarest pieces globally while switching to aggressive request pipelining (adaptive queue depth based on Bandwidth-Delay Product) for high-speed peers.
2. **Pre-Allocated Sparse Files & `fallocate`:** Eliminate filesystem fragmentation and write amplification by pre-allocating file extents on disk before piece writes begin.
3. **Memory-Mapped / Ring-Buffer Disk I/O:** Batch piece verification (SHA-1 / SHA-256 v2) across worker threads matching physical CPU core count, flushing contiguous blocks sequentially to NVMe/SSD storage.
4. **Aggressive Peer Choking/Unchoking:** Optimistic unchoke rotation every 10 seconds (vs. standard 30s) to rapidly drop slow peers and lock onto high-bandwidth seeders.
5. **TCP + uTP Dual-Stack with BBR Congestion Control:** Prefer TCP with window scaling for uncapped local/fiber throughput, falling back to uTP only when NAT traversal requires it.

---

## 4. Modular Directory Architecture
Strict separation of concerns is mandatory. Business logic, protocol state, and UI components must never be tightly coupled.

```text
velocetorrent/
├── cmd/
│   ├── daemon/                 # Headless background engine entrypoint
│   └── cli/                    # Terminal UI & CLI subcommand entrypoint
├── internal/
│   ├── engine/                 # Core BitTorrent session, swarm & peer management
│   │   ├── picker.go           # Adaptive BDP piece selection & endgame logic
│   │   ├── storage.go          # Pre-allocation, buffered disk writer, hash workers
│   │   └── telemetry.go        # Lock-free atomic counters (DL/UL speed, peers, IOPS)
│   ├── ipc/                    # Unix socket & localhost HTTP/WS server
│   │   ├── handlers.go         # REST endpoints (add torrent, set dir, pause/resume)
│   │   └── stream.go           # WebSocket telemetry broadcaster (100ms tick)
│   └── tui/                    # Terminal UI isolated from engine internals
│       ├── components/         # Reusable progress bars, piece heatmaps, speed graphs
│       ├── hooks/              # State polling & keyboard event dispatchers
│       └── views/              # Main dashboard, peer inspector, file selector
├── web/
│   ├── src/
│   │   ├── components/
│   │   │   ├── ui/             # Base primitives (buttons, inputs, dialogs)
│   │   │   ├── torrent/        # Domain components (TorrentRow, PieceMap, Dropzone)
│   │   │   └── layout/         # AppShell, TopTelemetryBar
│   │   ├── hooks/              # Custom hooks (useTelemetryStream, useTorrents, useDirs)
│   │   ├── lib/                # API client, byte/time formatters, validators
│   │   └── types/              # Strict TypeScript interfaces matching IPC payloads
└── AGENTS.md                   # This specification & unslop guardrails
```

## 5. Functional Requirements

### A. Terminal Interface (`vt`)

* **Direct Execution:** `vt add <file.torrent|magnet> --dir /path/to/output` starts the daemon (if not running), queues the torrent, and attaches the interactive TUI.
* **Headless Mode:** `vt add <file.torrent> --detach` queues the download and exits immediately.
* **TUI Telemetry & Animations:**
* Smooth, flicker-free progress bars with sub-character block precision (`▏▎▍▌▋▊▉█`).
* Live ASCII/Braille download & upload velocity sparkline.
* Real-time piece availability & completion bitfield grid.
* Keyboard-driven controls: `[Space]` Pause/Resume, `[d]` Change Output Dir, `[p]` Inspect Peers, `[q]` Detach TUI (keeps daemon running).



### B. Local Hosted Web App (`http://127.0.0.1:7878`)

* **Minimalist Control Scope:** Strictly focused on adding `.torrent` files/magnets, managing default & per-torrent output directories, starting/pausing transfers, and monitoring live telemetry.
* **Directory Management:** Quick-select saved output paths + filesystem path autocomplete/validator with free-space indicator.
* **Real-Time Telemetry:** WebSocket-driven updates at 100ms–250ms intervals showing throughput, ETA, active/total swarm peers, disk write queue depth, and piece completion grid.

---

## 6. Strict `unslop` Engineering & UI Guardrails

All generated code, copy, and UI components must adhere to the following anti-slop rules:

### UI & Visual Design Rules

1. **Zero AI-Cliché Aesthetics:** No purple/blue neon gradients, no generic glassmorphism cards, no floating SaaS hero sections, no decorative blobs, and no gratuitous drop shadows.
2. **Utilitarian Precision:** Design like a high-end hardware telemetry instrument (inspired by Teenage Engineering, Linear, or `btop`). High-contrast monochrome/neutral palette (`zinc-950` background, `zinc-900` surfaces, `1px` crisp `zinc-800` borders) with a single functional semantic accent color (e.g., emerald `#10B981` for active download, amber `#F59E0B` for metadata/verifying).
3. **Tabular Numerals Everywhere:** All speeds (`MB/s`), percentages, byte counts, ETAs, and peer counts MUST use `font-variant-numeric: tabular-nums` (`tabular-nums` in Tailwind) and monospace typography (`JetBrains Mono` / `Geist Mono`) so numbers never jitter during rapid updates.
4. **No Fluff Copy:** Never use marketing filler, empty motivational text, or verbose tooltips ("Welcome to VeloceTorrent! Easily manage your downloads..."). Use terse, exact technical labels (`DIR`, `SWARM`, `DOWN`, `UP`, `ETA`, `PIECES`).
5. **Intentional Empty States:** When no torrents are active, display a clean, functional `.torrent` file dropzone and magnet input bar—no cartoonish illustrations.

### Code & Architecture Rules

1. **No Monolithic Files:** Keep components and modules focused and single-purpose. Extract WebSocket connections, state transformations, and polling into dedicated hooks (`useTelemetryStream.ts`, `useTorrentActions.ts`).
2. **No Placeholder Stubs:** Every function, IPC handler, and UI control must be fully implemented. Do not leave `// TODO: implement actual download logic` comments.
3. **Explicit Error Boundaries:** Handle corrupt `.torrent` bencode payloads, permission-denied output directories, and daemon disconnects gracefully with actionable inline error messages.
4. **Zero Unnecessary Dependencies:** Do not install heavy charting libraries (like Recharts or Chart.js) for a simple throughput sparkline; render a lightweight custom `<svg>` polyline directly from the rolling 60-second telemetry buffer.
