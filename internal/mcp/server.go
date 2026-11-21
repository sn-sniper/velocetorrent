package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"velocetorrent/internal/engine"
	"velocetorrent/internal/ipc"
	"velocetorrent/internal/tui/components"
)

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      any           `json:"id"`
	Result  any           `json:"result,omitempty"`
	Error   *jsonRPCError `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// Server implements a Model Context Protocol (MCP) JSON-RPC 2.0 server over stdio (`vt mcp`).
type Server struct {
	client *ipc.Client
}

// NewServer constructs an MCP stdio server backed by the VeloceTorrent IPC client.
func NewServer(client *ipc.Client) *Server {
	return &Server{client: client}
}

// ServeStdio reads newline-delimited JSON-RPC 2.0 MCP requests from in and writes responses to out.
func (s *Server) ServeStdio(in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      nil,
				Error:   &jsonRPCError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}

		// Notifications have no ID and expect no response
		if req.ID == nil && strings.HasPrefix(req.Method, "notifications/") {
			continue
		}

		resp := s.HandleRequest(req)
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// HandleRequest processes a single MCP JSON-RPC 2.0 request.
func (s *Server) HandleRequest(req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo": map[string]any{
					"name":    "velocetorrent-mcp",
					"version": "1.0.0",
				},
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
			},
		}

	case "ping":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}

	case "tools/list":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": mcpToolDefinitions(),
			},
		}

	case "tools/call":
		var params toolCallParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &jsonRPCError{Code: -32602, Message: "invalid tool call params: " + err.Error()},
			}
		}
		text, isErr := s.executeTool(params.Name, params.Arguments)
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"isError": isErr,
				"content": []map[string]any{
					{
						"type": "text",
						"text": text,
					},
				},
			},
		}

	default:
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &jsonRPCError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func (s *Server) executeTool(name string, args map[string]any) (string, bool) {
	switch name {
	case "list_torrents":
		snap, err := s.client.FetchState()
		if err != nil {
			return fmt.Sprintf("Failed to query VeloceTorrent daemon: %v", err), true
		}
		raw, _ := json.MarshalIndent(snap, "", "  ")
		return string(raw), false

	case "add_torrent":
		source, _ := args["source"].(string)
		outputDir, _ := args["outputDir"].(string)
		stream, _ := args["stream"].(bool)
		if strings.TrimSpace(source) == "" {
			return "Missing required 'source' (.torrent path or magnet:? URI)", true
		}
		snap, err := s.client.AddTorrentWithOptions(source, outputDir, stream, stream)
		if err != nil {
			return fmt.Sprintf("Failed to add torrent: %v", err), true
		}
		return fmt.Sprintf("Queued %s (InfoHash: %s, Dir: %s, Status: %s)",
			snap.Name, snap.InfoHash, snap.OutputDir, snap.Status), false

	case "pause_resume_torrent":
		id, _ := args["id"].(string)
		action, _ := args["action"].(string)
		if strings.TrimSpace(id) == "" {
			return "Missing required 'id' (info_hash, prefix, or torrent name)", true
		}
		var snap *engine.TorrentTelemetrySnapshot
		var err error
		switch strings.ToLower(strings.TrimSpace(action)) {
		case "pause":
			snap, err = s.client.Pause(id)
		case "resume":
			snap, err = s.client.Resume(id)
		default:
			snap, err = s.client.TogglePause(id)
		}
		if err != nil {
			return fmt.Sprintf("Failed to update torrent %s: %v", id, err), true
		}
		return fmt.Sprintf("Torrent %s is now %s (Progress: %.1f%%)", snap.Name, snap.Status, snap.Progress), false

	case "check_disk_space":
		snap, err := s.client.FetchState()
		if err != nil {
			return fmt.Sprintf("Failed to query daemon: %v", err), true
		}
		return fmt.Sprintf("Default Dir: %s | Free Space: %s / %s | Saved Dirs: %s",
			snap.DefaultOutputDir,
			strings.TrimSpace(components.FormatBytes(int64(snap.DiskFreeBytes))),
			strings.TrimSpace(components.FormatBytes(int64(snap.DiskTotalBytes))),
			strings.Join(snap.SavedDirs, ", "),
		), false

	case "set_file_priority":
		id, _ := args["id"].(string)
		fileIdxFloat, _ := args["fileIndex"].(float64)
		prioStr, _ := args["priority"].(string)
		snap, err := s.client.SetFilePriority(id, int(fileIdxFloat), engine.FilePriority(strings.ToLower(prioStr)))
		if err != nil {
			return fmt.Sprintf("Failed to set file priority: %v", err), true
		}
		return fmt.Sprintf("Updated file #%d priority on %s to %s", int(fileIdxFloat), snap.Name, prioStr), false

	case "set_bandwidth_throttle":
		profileStr, _ := args["profile"].(string)
		prof := engine.ProfileUncapped
		if strings.EqualFold(profileStr, "background") {
			prof = engine.ProfileBackground
		}
		snap, err := s.client.SetThrottle(prof, false, 0, 0)
		if err != nil {
			return fmt.Sprintf("Failed to set throttle profile: %v", err), true
		}
		return fmt.Sprintf("Bandwidth profile set to %s (MaxDown: %d B/s, MaxUp: %d B/s)",
			snap.Profile, snap.MaxDownBytesPs, snap.MaxUpBytesPs), false

	default:
		return fmt.Sprintf("Unknown MCP tool %q", name), true
	}
}

func mcpToolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_torrents",
			"description": "Fetch real-time telemetry for all active/seeding torrents, global speeds, IOPS, and disk space.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "add_torrent",
			"description": "Queue a .torrent file path or magnet:? URI on VeloceTorrent with optional output directory and streaming mode.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"source":    map[string]any{"type": "string", "description": "Path to .torrent file or magnet:?xt=urn:btih:... URI"},
					"outputDir": map[string]any{"type": "string", "description": "Optional target output directory"},
					"stream":    map[string]any{"type": "boolean", "description": "Enable 16-piece linear lookahead streaming mode"},
				},
				"required": []string{"source"},
			},
		},
		{
			"name":        "pause_resume_torrent",
			"description": "Pause, resume, or toggle an active transfer by info_hash, short hash prefix, or torrent name.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":     map[string]any{"type": "string", "description": "Torrent info_hash, prefix, or name"},
					"action": map[string]any{"type": "string", "enum": []string{"pause", "resume", "toggle"}},
				},
				"required": []string{"id"},
			},
		},
		{
			"name":        "check_disk_space",
			"description": "Check available disk free space and saved output directories.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "set_file_priority",
			"description": "Set selective download priority (skip, normal, high) for a specific file index in a torrent.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":        map[string]any{"type": "string", "description": "Torrent info_hash or prefix"},
					"fileIndex": map[string]any{"type": "integer", "description": "Zero-based file index"},
					"priority":  map[string]any{"type": "string", "enum": []string{"skip", "normal", "high"}},
				},
				"required": []string{"id", "fileIndex", "priority"},
			},
		},
		{
			"name":        "set_bandwidth_throttle",
			"description": "Switch global bandwidth scheduler profile between uncapped and background (2 MiB/s) mode.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"profile": map[string]any{"type": "string", "enum": []string{"uncapped", "background"}},
				},
				"required": []string{"profile"},
			},
		},
	}
}
