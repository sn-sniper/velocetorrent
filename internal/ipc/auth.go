package ipc

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DefaultAuthTokenPath returns ~/.config/velocetorrent/token.
func DefaultAuthTokenPath() string {
	if envPath := strings.TrimSpace(os.Getenv("VT_AUTH_TOKEN_FILE")); envPath != "" {
		return envPath
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "velocetorrent", "token")
	}
	return filepath.Join(home, ".config", "velocetorrent", "token")
}

// LoadOrCreateAuthToken loads the local IPC bearer token from ~/.config/velocetorrent/token
// or generates a new 256-bit cryptographic random token with 0600 permissions.
func LoadOrCreateAuthToken(tokenPath string) (string, string, error) {
	if envTok := strings.TrimSpace(os.Getenv("VT_AUTH_TOKEN")); envTok != "" {
		return envTok, "env:VT_AUTH_TOKEN", nil
	}
	if strings.TrimSpace(tokenPath) == "" {
		tokenPath = DefaultAuthTokenPath()
	}
	absPath, err := filepath.Abs(tokenPath)
	if err == nil {
		tokenPath = absPath
	}

	if raw, err := os.ReadFile(tokenPath); err == nil {
		existing := strings.TrimSpace(string(raw))
		if len(existing) >= 32 {
			return existing, tokenPath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		return "", tokenPath, fmt.Errorf("cannot create token directory: %w", err)
	}

	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", tokenPath, err
	}
	token := hex.EncodeToString(entropy[:])

	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0o600); err != nil {
		return "", tokenPath, fmt.Errorf("cannot write auth token file: %w", err)
	}
	return token, tokenPath, nil
}

// isTailscaleIP returns true if ip belongs to the Tailscale CGNAT range (100.64.0.0/10) or Tailscale ULA (fd7a:115c:a1e0::/48).
func isTailscaleIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
	}
	ip16 := ip.To16()
	if ip16 != nil {
		return ip16[0] == 0xfd && ip16[1] == 0x7a && ip16[2] == 0x11 && ip16[3] == 0x5c && ip16[4] == 0xa1 && ip16[5] == 0xe0
	}
	return false
}

// isTailscaleHost returns true if hostHeader is a Tailscale CGNAT IP or MagicDNS (.ts.net) hostname.
func isTailscaleHost(hostHeader string) bool {
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	if strings.HasSuffix(host, ".ts.net") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return isTailscaleIP(ip)
	}
	return false
}

// isAllowedLocalHost validates the HTTP Host header to allow loopback (127.0.0.1 / SSH tunnel) and Tailscale interfaces,
// while rejecting arbitrary public DNS hostnames to defeat DNS rebinding attacks.
func isAllowedLocalHost(hostHeader string) bool {
	if hostHeader == "" {
		return true
	}
	host := hostHeader
	if h, _, err := net.SplitHostPort(hostHeader); err == nil {
		host = h
	}
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch host {
	case "127.0.0.1", "localhost", "::1", "unix":
		return true
	}
	return isTailscaleHost(hostHeader)
}

// isAllowedLocalOrigin checks whether an Origin header originates from localhost / 127.0.0.1 or a Tailscale node.
func isAllowedLocalOrigin(origin string) bool {
	if origin == "" {
		return true
	}
	lower := strings.ToLower(origin)
	if strings.HasPrefix(lower, "http://127.0.0.1:") ||
		strings.HasPrefix(lower, "http://localhost:") ||
		lower == "http://127.0.0.1" ||
		lower == "http://localhost" {
		return true
	}
	trimmed := strings.TrimPrefix(strings.TrimPrefix(lower, "https://"), "http://")
	return isTailscaleHost(trimmed)
}

// DiscoverTailscaleIP scans local network interfaces for an active Tailscale 100.64.0.0/10 address.
func DiscoverTailscaleIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			if ipNet, ok := a.(*net.IPNet); ok && isTailscaleIP(ipNet.IP) {
				if ip4 := ipNet.IP.To4(); ip4 != nil {
					return ip4.String(), nil
				}
			}
		}
	}
	return "", errors.New("no active Tailscale interface (100.64.0.0/10) found")
}

// extractRequestToken extracts a bearer token from Authorization, X-VT-Token, query param, or SameSite=Strict cookie.
func extractRequestToken(r *http.Request) (token string, provided bool) {
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
			return strings.TrimSpace(auth[7:]), true
		}
		return auth, true
	}
	if xTok := strings.TrimSpace(r.Header.Get("X-VT-Token")); xTok != "" {
		return xTok, true
	}
	if qTok := strings.TrimSpace(r.URL.Query().Get("token")); qTok != "" {
		return qTok, true
	}
	if c, err := r.Cookie("vt_token"); err == nil && strings.TrimSpace(c.Value) != "" {
		return strings.TrimSpace(c.Value), true
	}
	return "", false
}

// ConstantTimeTokenMatch compares two tokens in constant time.
func ConstantTimeTokenMatch(expected, actual string) bool {
	if expected == "" || len(expected) != len(actual) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) == 1
}
