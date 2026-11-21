## Summary of Changes

<!-- Concise technical description of what this PR changes and why. -->

## Related Issue

Fixes #

## Verification & Benchmark Checklist

- [ ] `cd web && npm run build` compiles with zero TypeScript or Vite warnings
- [ ] `go test -v ./...` passes all unit, IPC, MCP, and 15-swarm goroutine leak tests
- [ ] `go test -fuzz=FuzzBencodeAndMetainfoParser -fuzztime=5s ./internal/engine` passes
- [ ] Adheres strictly to [AGENTS.md](../AGENTS.md) `unslop` UI and architecture rules (tabular-nums, monochrome `#09090B`/`#18181B` + `#10B981`/`#F59E0B` palette, no placeholder stubs, no heavy chart libraries)

## Throughput / Memory Impact (`vt bench --size-mb 16`)

```text
# Paste output of `vt bench --size-mb 16` here if touching internal/engine:
```
