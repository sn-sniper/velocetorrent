package mcp

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"velocetorrent/internal/engine"
	"velocetorrent/internal/ipc"
)

func TestMCPStdioServerTools(t *testing.T) {
	tmpDir := t.TempDir()
	eng, err := engine.NewEngine(engine.Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer eng.Close()

	sockPath := filepath.Join(tmpDir, "vt-mcp.sock")
	ipcSrv := ipc.NewServer(eng, "127.0.0.1:0", sockPath)
	if err := ipcSrv.Start(); err != nil {
		t.Fatalf("failed to start IPC server: %v", err)
	}
	defer ipcSrv.Close()

	client := ipc.NewClient(ipcSrv.TCPAddr(), sockPath)
	mcpSrv := NewServer(client)

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"check_disk_space","arguments":{}}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := mcpSrv.ServeStdio(strings.NewReader(input), &out); err != nil {
		t.Fatalf("ServeStdio failed: %v", err)
	}

	outStr := out.String()
	if !strings.Contains(outStr, "velocetorrent-mcp") {
		t.Fatalf("expected initialize response with velocetorrent-mcp, got: %s", outStr)
	}
	if !strings.Contains(outStr, "add_torrent") || !strings.Contains(outStr, "check_disk_space") {
		t.Fatalf("expected tools/list response with add_torrent and check_disk_space, got: %s", outStr)
	}
	if !strings.Contains(outStr, "Free Space:") {
		t.Fatalf("expected check_disk_space tool output, got: %s", outStr)
	}
}
