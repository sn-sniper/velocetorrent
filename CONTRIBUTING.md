# Contributing to VeloceTorrent

Thank you for contributing to **VeloceTorrent** (`vt` / `vtd`). To keep the engine fast, modular, and strictly free of UI/architectural bloat, all contributors and fork pull requests must follow the guidelines below.

---

## 1. Fork & Pull Request Workflow

1. **Fork the Repository:** Click **Fork** on GitHub and clone your fork locally:
   ```bash
   git clone https://github.com/<your-username>/velocetorrent.git
   cd velocetorrent
   ```
2. **Create a Feature Branch:** Branch from `main` using a descriptive prefix (`feat/`, `fix/`, `perf/`, `docs/`):
   ```bash
   git checkout -b perf/nvme-ring-coalesce
   ```
3. **Build & Verify Locally:**
   ```bash
   # 1. Build embedded React 19 frontend
   cd web && npm ci && npm run build && cd ..

   # 2. Run unit, integration, and 15-swarm soak tests
   go test -v -race ./...

   # 3. Run bencode/metainfo fuzz tests
   go test -fuzz=FuzzBencodeAndMetainfoParser -fuzztime=5s ./internal/engine

   # 4. Run throughput & disk I/O benchmark
   go run ./cmd/cli bench --size-mb 16
   ```
4. **Open a Pull Request:** Fill out all sections of `.github/PULL_REQUEST_TEMPLATE.md`. GitHub Actions (`CI`) will automatically run against your fork PR across Linux, macOS, and Windows.

---

## 2. Architecture & `unslop` Guardrails

Every pull request is reviewed against [AGENTS.md](./AGENTS.md):

- **Strict Modularity:** Keep `internal/engine`, `internal/ipc`, `internal/mcp`, `internal/tui`, and `web/src` decoupled. Never couple UI rendering to protocol state.
- **Zero Placeholder Stubs:** Every IPC handler, CLI flag, and UI control must be completely wired and tested.
- **Utilitarian Hardware UI:** Use the high-contrast monochrome palette (`zinc-950` `#09090B`, `zinc-900` `#18181B`, `1px` `zinc-800` `#27272A` borders) with functional semantic accents (`#10B981` emerald for active/verified, `#F59E0B` amber for metadata/verifying, `#EF4444` red for error).
- **Tabular Numerals Everywhere:** All speeds, byte counts, percentages, ETAs, and peer metrics must use `tabular-nums` and monospace typography (`JetBrains Mono`).
- **Zero Heavy Charting Libraries:** Render sparklines via lightweight custom `<svg>` polylines (Web) or Unicode Braille matrices (TUI).
