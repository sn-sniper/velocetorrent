package ipc

import (
	"bufio"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"velocetorrent/internal/engine"
)

func TestIPCServerRESTAndWebSocketTelemetry(t *testing.T) {
	tmpDir := t.TempDir()
	eng, err := engine.NewEngine(engine.Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	sockPath := filepath.Join(tmpDir, "vt-test.sock")
	srv := NewServer(eng, "127.0.0.1:0", sockPath)
	if err := srv.Start(); err != nil {
		t.Fatalf("failed to start IPC server: %v", err)
	}
	defer srv.Close()

	client := NewClient(srv.TCPAddr(), sockPath)
	if !client.Ping() {
		t.Fatalf("expected IPC client Ping() to succeed")
	}

	// 1. Test corrupt .torrent error boundary over IPC
	corruptFile := filepath.Join(tmpDir, "corrupt.torrent")
	_, err = client.AddTorrent("magnet:?xt=urn:btih:invalid_hash", tmpDir, false)
	if err == nil {
		t.Fatalf("expected error for invalid magnet URI")
	}
	_ = corruptFile

	// 2. Add a valid magnet URI and verify state synchronization
	magnetURI := "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01&dn=debian-netinst.iso&xl=262144"
	addedSnap, err := client.AddTorrent(magnetURI, tmpDir, false)
	if err != nil {
		t.Fatalf("unexpected error adding magnet URI: %v", err)
	}
	if addedSnap.Name != "debian-netinst.iso" {
		t.Fatalf("expected name debian-netinst.iso, got %s", addedSnap.Name)
	}

	// 3. Test Pause & Resume via IPC
	pausedSnap, err := client.TogglePause(addedSnap.ID)
	if err != nil {
		t.Fatalf("failed to toggle pause: %v", err)
	}
	if pausedSnap.Status != engine.StatusPaused {
		t.Fatalf("expected PAUSED status, got %s", pausedSnap.Status)
	}
	resumedSnap, err := client.TogglePause(addedSnap.ID)
	if err != nil {
		t.Fatalf("failed to toggle resume: %v", err)
	}
	if resumedSnap.Status == engine.StatusPaused {
		t.Fatalf("expected resumed status, still PAUSED")
	}

	// 4. Test WebSocket 100ms telemetry stream upgrade & frame reception
	conn, err := net.DialTimeout("tcp", srv.TCPAddr(), 3*time.Second)
	if err != nil {
		t.Fatalf("failed to dial WebSocket TCP: %v", err)
	}
	defer conn.Close()

	wsKey := base64.StdEncoding.EncodeToString([]byte("velocetorrent-key"))
	req := strings.Join([]string{
		"GET /ws HTTP/1.1",
		"Host: " + srv.TCPAddr(),
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Key: " + wsKey,
		"Sec-WebSocket-Version: 13",
		"",
		"",
	}, "\r\n")
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("failed to write WS upgrade request: %v", err)
	}

	br := bufio.NewReader(conn)
	statusLine, err := br.ReadString('\n')
	if err != nil || !strings.Contains(statusLine, "101 Switching Protocols") {
		t.Fatalf("expected HTTP 101 Switching Protocols, got %q (err=%v)", statusLine, err)
	}
	// Read remaining HTTP response headers
	for {
		line, err := br.ReadString('\n')
		if err != nil || line == "\r\n" {
			break
		}
	}

	// Read first WebSocket telemetry frame
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var hdr [2]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		t.Fatalf("failed to read WS frame header: %v", err)
	}
	payloadLen := int(hdr[1] & 0x7f)
	if payloadLen == 126 {
		var ext [2]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			t.Fatalf("failed to read extended 16-bit WS length: %v", err)
		}
		payloadLen = int(binary.BigEndian.Uint16(ext[:]))
	} else if payloadLen == 127 {
		var ext [8]byte
		if _, err := io.ReadFull(br, ext[:]); err != nil {
			t.Fatalf("failed to read extended 64-bit WS length: %v", err)
		}
		payloadLen = int(binary.BigEndian.Uint64(ext[:]))
	}
	framePayload := make([]byte, payloadLen)
	if _, err := io.ReadFull(br, framePayload); err != nil {
		t.Fatalf("failed to read WS frame payload: %v", err)
	}

	var wsSnap engine.EngineTelemetrySnapshot
	if err := json.Unmarshal(framePayload, &wsSnap); err != nil {
		t.Fatalf("failed to unmarshal WS telemetry JSON: %v", err)
	}
	if len(wsSnap.Torrents) != 1 {
		t.Fatalf("expected 1 torrent in WS telemetry snapshot, got %d", len(wsSnap.Torrents))
	}

	// 5. Test Phase 7 IPC Security: DNS Rebinding rejection, Invalid Bearer Token rejection, and Cross-Origin CSRF rejection
	httpClient := &http.Client{Timeout: 3 * time.Second}

	// 5a. Forged Host header (DNS rebinding attempt) -> HTTP 403
	rebindReq, _ := http.NewRequest(http.MethodGet, "http://"+srv.TCPAddr()+"/api/state", nil)
	rebindReq.Host = "evil-dns-rebind.attacker.com"
	rebindResp, err := httpClient.Do(rebindReq)
	if err != nil {
		t.Fatalf("failed to execute rebind test request: %v", err)
	}
	_ = rebindResp.Body.Close()
	if rebindResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for DNS rebinding Host header, got %d", rebindResp.StatusCode)
	}

	// 5b. Invalid Bearer Token -> HTTP 401
	badTokReq, _ := http.NewRequest(http.MethodGet, "http://"+srv.TCPAddr()+"/api/state", nil)
	badTokReq.Header.Set("Authorization", "Bearer deadbeef_wrong_token")
	badTokResp, err := httpClient.Do(badTokReq)
	if err != nil {
		t.Fatalf("failed to execute bad token request: %v", err)
	}
	_ = badTokResp.Body.Close()
	if badTokResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected HTTP 401 Unauthorized for invalid bearer token, got %d", badTokResp.StatusCode)
	}

	// 5c. Foreign Origin CSRF attempt without token -> HTTP 403
	csrfReq, _ := http.NewRequest(http.MethodPost, "http://"+srv.TCPAddr()+"/api/torrents", strings.NewReader(`{}`))
	csrfReq.Header.Set("Origin", "https://malicious-site.example")
	csrfResp, err := httpClient.Do(csrfReq)
	if err != nil {
		t.Fatalf("failed to execute CSRF test request: %v", err)
	}
	_ = csrfResp.Body.Close()
	if csrfResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected HTTP 403 Forbidden for cross-origin CSRF attempt, got %d", csrfResp.StatusCode)
	}

	// 6. Test Phase 10 Prometheus /metrics endpoint
	metricsResp, err := httpClient.Get("http://" + srv.TCPAddr() + "/metrics")
	if err != nil {
		t.Fatalf("failed to fetch /metrics: %v", err)
	}
	metricsBody, _ := io.ReadAll(metricsResp.Body)
	_ = metricsResp.Body.Close()
	if metricsResp.StatusCode != http.StatusOK || !strings.Contains(string(metricsBody), "velocetorrent_global_download_bytes_per_second") {
		t.Fatalf("unexpected /metrics response (status=%d): %s", metricsResp.StatusCode, string(metricsBody))
	}

	// 7. Test Phase 10 Tailscale CGNAT Host header (100.64.0.0/10) with valid Bearer Token
	tsReq, _ := http.NewRequest(http.MethodGet, "http://"+srv.TCPAddr()+"/api/state", nil)
	tsReq.Host = "100.100.42.10:7878"
	tsReq.Header.Set("Authorization", "Bearer "+srv.AuthToken())
	tsResp, err := httpClient.Do(tsReq)
	if err != nil {
		t.Fatalf("failed to execute Tailscale host test request: %v", err)
	}
	_ = tsResp.Body.Close()
	if tsResp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK for authenticated Tailscale CGNAT Host header, got %d", tsResp.StatusCode)
	}

	// 8. Test Phase 9 Bandwidth Throttler toggle over IPC
	thSnap, err := client.SetThrottle("", true, 0, 0)
	if err != nil || thSnap.Profile != engine.ProfileBackground {
		t.Fatalf("expected BACKGROUND profile after toggle, got %+v (err=%v)", thSnap, err)
	}
}
