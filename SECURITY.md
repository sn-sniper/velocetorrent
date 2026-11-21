# Security Policy

## Supported Versions

| Version | Supported          |
| ------- | ------------------ |
| `1.0.x` | :white_check_mark: |

## Security Model & Hardening

1. **Local Control Plane Isolation:**
   - By default, `vtd` binds strictly to `127.0.0.1:7878` and a local Unix Domain Socket (`velocetorrent.sock`).
   - All requests are checked by `isAllowedLocalHost` (`internal/ipc/auth.go`) to reject non-loopback `Host` headers and defeat **DNS Rebinding** attacks.
2. **256-Bit Cryptographic Bearer Token:**
   - On first boot, `vtd` generates a 32-byte (64-hex-char) token in `~/.config/velocetorrent/token` with `0600` permissions.
   - Cross-origin requests and remote Tailscale CGNAT (`100.64.0.0/10`) requests must supply `Authorization: Bearer <token>` or `X-VT-Token: <token>`, verified in constant time via `crypto/subtle.ConstantTimeCompare`.
3. **Untrusted Bencode & Path Traversal Defenses:**
   - All `.torrent` files and tracker responses are parsed by a zero-dependency bencode parser (`internal/engine/bencode.go`) continuously fuzz-tested via `FuzzBencodeAndMetainfoParser`.
   - All single-file and multi-file paths are sanitized via `sanitizePathComponent` (`internal/engine/metainfo.go`) to prevent `..` directory traversal.
4. **Socket-Level Radix Tree IP Blocklist:**
   - Inbound and outbound peer connections are filtered against an in-memory CIDR / PeerGuardian binary Radix Tree (`internal/engine/blocklist.go`) before the 68-byte BitTorrent wire handshake executes.

## Reporting a Vulnerability

Please report suspected security vulnerabilities privately via **GitHub Security Advisories** (`Security` tab $\to$ `Report a vulnerability`) rather than opening a public issue. Include:
- Affected component (`internal/engine`, `internal/ipc`, `web/`, or `extensions/chrome-mv3`)
- Proof-of-concept input or reproduction steps
- Impact assessment
