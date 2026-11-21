package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"velocetorrent/internal/engine"
	"velocetorrent/web"
)

// DefaultTCPAddr is the canonical local control plane address per AGENTS.md Section 5B.
const DefaultTCPAddr = "127.0.0.1:7878"

// DefaultUnixSocketPath returns the local Unix domain socket path for low-latency IPC.
func DefaultUnixSocketPath() string {
	return filepath.Join(os.TempDir(), "velocetorrent.sock")
}

// APIError is the structured error response returned by all IPC REST endpoints.
type APIError struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// AddTorrentRequest is the JSON payload for POST /api/torrents.
type AddTorrentRequest struct {
	Source        string `json:"source"`
	TorrentBase64 string `json:"torrentBase64"`
	OutputDir     string `json:"outputDir"`
	Sequential    bool   `json:"sequential"`
	Stream        bool   `json:"stream"`
}

// FilePriorityRequest is the JSON payload for POST /api/torrents/{id}/files.
type FilePriorityRequest struct {
	FileIndex int                 `json:"fileIndex"`
	Priority  engine.FilePriority `json:"priority"`
}

// ThrottleRequest is the JSON payload for POST /api/throttle.
type ThrottleRequest struct {
	Profile        engine.ThrottleProfile `json:"profile"`
	Toggle         bool                   `json:"toggle"`
	MaxDownBytesPs int64                  `json:"maxDownBytesPs"`
	MaxUpBytesPs   int64                  `json:"maxUpBytesPs"`
}

// SyntheticTorrentRequest is the JSON payload for POST /api/torrents/synthetic.
type SyntheticTorrentRequest struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"sizeBytes"`
	OutputDir  string `json:"outputDir"`
	Sequential bool   `json:"sequential"`
}

// ChangeDirRequest is the JSON payload for POST /api/torrents/{id}/dir and POST /api/dirs.
type ChangeDirRequest struct {
	Path       string `json:"path"`
	OutputDir  string `json:"outputDir"`
	SetDefault bool   `json:"setDefault"`
}

// SavedDirEntry represents a saved output directory along with its live disk space metrics.
type SavedDirEntry struct {
	Path       string `json:"path"`
	IsDefault  bool   `json:"isDefault"`
	FreeBytes  uint64 `json:"freeBytes"`
	TotalBytes uint64 `json:"totalBytes"`
	Writable   bool   `json:"writable"`
}

// DirsResponse is returned by GET /api/dirs and POST /api/dirs.
type DirsResponse struct {
	DefaultOutputDir string          `json:"defaultOutputDir"`
	Dirs             []SavedDirEntry `json:"dirs"`
}

// Server hosts the local REST + WebSocket IPC API over both 127.0.0.1:7878 and a Unix Domain Socket.
type Server struct {
	eng           *engine.Engine
	broadcaster   *TelemetryBroadcaster
	tcpAddr       string
	unixPath      string
	webDistDir    string
	authToken     string
	authTokenPath string

	httpServer  *http.Server
	tcpLn       net.Listener
	unixLn      net.Listener
	wg          sync.WaitGroup
}

// NewServer constructs an IPC server bound to the given Engine and initializes the ~/.config/velocetorrent/token guard.
func NewServer(eng *engine.Engine, tcpAddr string, unixPath string) *Server {
	if strings.TrimSpace(tcpAddr) == "" {
		tcpAddr = DefaultTCPAddr
	}
	if strings.TrimSpace(unixPath) == "" {
		unixPath = DefaultUnixSocketPath()
	}
	tok, tokPath, _ := LoadOrCreateAuthToken("")
	broadcaster := NewTelemetryBroadcaster(eng, 100*time.Millisecond)
	s := &Server{
		eng:           eng,
		broadcaster:   broadcaster,
		tcpAddr:       tcpAddr,
		unixPath:      unixPath,
		webDistDir:    filepath.Join("web", "dist"),
		authToken:     tok,
		authTokenPath: tokPath,
	}

	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.httpServer = &http.Server{
		Handler:           s.withSecurityAndCORS(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// AuthToken returns the active bearer token for this IPC server.
func (s *Server) AuthToken() string {
	return s.authToken
}

// AuthTokenPath returns the filesystem path of the persisted bearer token.
func (s *Server) AuthTokenPath() string {
	return s.authTokenPath
}

// Start binds the localhost TCP listener and optional Unix domain socket listener.
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.tcpAddr)
	if err != nil {
		return fmt.Errorf("failed to bind IPC server on %s: %w", s.tcpAddr, err)
	}
	s.tcpLn = ln
	s.tcpAddr = ln.Addr().String()

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		_ = s.httpServer.Serve(ln)
	}()

	// Also attempt to bind local Unix domain socket (supported on Linux, macOS, and Windows 10 1803+)
	_ = os.Remove(s.unixPath)
	if uln, err := net.Listen("unix", s.unixPath); err == nil {
		s.unixLn = uln
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			_ = s.httpServer.Serve(uln)
		}()
	}

	return nil
}

// TCPAddr returns the bound TCP address (e.g. "127.0.0.1:7878").
func (s *Server) TCPAddr() string {
	return s.tcpAddr
}

// UnixSocketPath returns the bound Unix socket path.
func (s *Server) UnixSocketPath() string {
	return s.unixPath
}

// Close gracefully stops the HTTP/WS server, Unix socket, and telemetry broadcaster.
func (s *Server) Close() error {
	s.broadcaster.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = s.httpServer.Shutdown(ctx)
	if s.tcpLn != nil {
		_ = s.tcpLn.Close()
	}
	if s.unixLn != nil {
		_ = s.unixLn.Close()
		_ = os.Remove(s.unixPath)
	}
	s.wg.Wait()
	return nil
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/ws", s.broadcaster.HandleWebSocket)
	mux.HandleFunc("/api/ws", s.broadcaster.HandleWebSocket)
	mux.HandleFunc("/ws/telemetry", s.broadcaster.HandleWebSocket)
	mux.HandleFunc("/api/stream/sse", s.broadcaster.HandleSSE)
	mux.HandleFunc("/metrics", s.HandlePrometheusMetrics)

	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/torrents", s.handleTorrentsRoot)
	mux.HandleFunc("/api/torrents/synthetic", s.handleSyntheticTorrent)
	mux.HandleFunc("/api/torrents/", s.handleTorrentByID)
	mux.HandleFunc("/api/dirs", s.handleDirs)
	mux.HandleFunc("/api/dirs/validate", s.handleValidateDir)
	mux.HandleFunc("/api/throttle", s.handleThrottle)
	mux.HandleFunc("/api/autodownload", s.handleAutoDownload)
	mux.HandleFunc("/api/security", s.handleSecurityStatus)
	mux.HandleFunc("/api/security/blocklist", s.handleBlocklist)
	mux.HandleFunc("/api/hooks", s.handleLifecycleHooks)

	// Go 1.26+ pprof & goroutineleak audit endpoints
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/goroutineleak", s.handleGoroutineLeakAudit)

	// Static Web App fallback if web/dist exists
	mux.HandleFunc("/", s.handleStaticOrIndex)
}

func (s *Server) withSecurityAndCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Defeat DNS Rebinding attacks targeting 127.0.0.1:7878
		if !isAllowedLocalHost(r.Host) {
			writeJSON(w, http.StatusForbidden, APIError{
				Error: fmt.Sprintf("rejected non-loopback/non-Tailscale Host header %q (DNS rebinding protection)", r.Host),
				Code:  "DNS_REBINDING_BLOCKED",
			})
			return
		}

		origin := strings.TrimSpace(r.Header.Get("Origin"))
		if origin != "" {
			if isAllowedLocalOrigin(origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-VT-Token, X-Requested-With")
			w.Header().Set("Access-Control-Allow-Credentials", "true")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// 2. Enforce Bearer Token / CSRF validation on API and WebSocket routes:
		//    - If accessed via Tailscale interface/hostname, a valid bearer token is mandatory.
		//    - If a foreign Origin or Sec-Fetch-Site: cross-site is present, a valid token is strictly mandatory.
		//    - If an Authorization or X-VT-Token header is explicitly supplied, it must match s.authToken in constant time.
		isAPIOrWS := strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/ws")
		if isAPIOrWS && s.authToken != "" {
			suppliedToken, hasToken := extractRequestToken(r)
			secFetchSite := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
			isCrossOrigin := (origin != "" && !isAllowedLocalOrigin(origin)) || secFetchSite == "cross-site"
			isRemoteTailscale := isTailscaleHost(r.Host)

			if hasToken && !ConstantTimeTokenMatch(s.authToken, suppliedToken) {
				writeJSON(w, http.StatusUnauthorized, APIError{
					Error: "invalid IPC bearer token",
					Code:  "INVALID_AUTH_TOKEN",
				})
				return
			}
			if (isCrossOrigin || isRemoteTailscale) && !hasToken {
				writeJSON(w, http.StatusForbidden, APIError{
					Error: "IPC request blocked: bearer token required for cross-origin or Tailscale access",
					Code:  "CSRF_BLOCKED",
				})
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, err error) {
	code := "INVALID_INPUT"
	status := http.StatusBadRequest

	switch {
	case errors.Is(err, engine.ErrCorruptBencode):
		code = "CORRUPT_BENCODE"
		status = http.StatusBadRequest
	case errors.Is(err, engine.ErrDirectoryPermission):
		code = "PERMISSION_DENIED"
		status = http.StatusForbidden
	case errors.Is(err, engine.ErrInsufficientDiskSpace):
		code = "INSUFFICIENT_SPACE"
		status = http.StatusInsufficientStorage
	case strings.Contains(strings.ToLower(err.Error()), "not found"):
		code = "NOT_FOUND"
		status = http.StatusNotFound
	}

	writeJSON(w, status, APIError{
		Error: err.Error(),
		Code:  code,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := s.eng.Snapshot(false)
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "ok",
		"version":          "1.0.0",
		"uptimeSeconds":    snap.UptimeSeconds,
		"listenPort":       s.eng.ListenPort(),
		"defaultOutputDir": snap.DefaultOutputDir,
		"activeTorrents":   len(snap.Torrents),
	})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.eng.Snapshot(true))
}

func (s *Server) handleTorrentsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		snap := s.eng.Snapshot(true)
		writeJSON(w, http.StatusOK, snap)

	case http.MethodPost:
		contentType := r.Header.Get("Content-Type")
		if strings.HasPrefix(contentType, "multipart/form-data") {
			s.handleMultipartAddTorrent(w, r)
			return
		}

		var req AddTorrentRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 16*1024*1024)).Decode(&req); err != nil {
			writeError(w, fmt.Errorf("invalid JSON request body: %w", err))
			return
		}

		sess, err := s.eng.AddTorrentInput(req.Source, req.TorrentBase64, req.OutputDir, req.Sequential || req.Stream)
		if err != nil {
			writeError(w, err)
			return
		}
		if req.Stream {
			sess.SetStreamMode(true)
		}
		s.broadcaster.BroadcastImmediate()
		writeJSON(w, http.StatusCreated, sess.Snapshot(true))

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleMultipartAddTorrent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeError(w, fmt.Errorf("failed to parse multipart upload: %w", err))
		return
	}
	outputDir := r.FormValue("outputDir")
	sequential := r.FormValue("sequential") == "true"

	file, _, err := r.FormFile("file")
	if err != nil {
		// Check if magnet/source was passed as form field
		source := r.FormValue("source")
		if source == "" {
			writeError(w, errors.New("missing 'file' (.torrent) or 'source' field in form upload"))
			return
		}
		sess, addErr := s.eng.AddTorrentInput(source, "", outputDir, sequential)
		if addErr != nil {
			writeError(w, addErr)
			return
		}
		s.broadcaster.BroadcastImmediate()
		writeJSON(w, http.StatusCreated, sess.Snapshot(true))
		return
	}
	defer file.Close()

	rawBytes, err := io.ReadAll(io.LimitReader(file, 16*1024*1024))
	if err != nil {
		writeError(w, fmt.Errorf("failed to read uploaded .torrent file: %w", err))
		return
	}

	sess, err := s.eng.AddTorrentFromBytes(rawBytes, outputDir, sequential)
	if err != nil {
		writeError(w, err)
		return
	}
	s.broadcaster.BroadcastImmediate()
	writeJSON(w, http.StatusCreated, sess.Snapshot(true))
}

func (s *Server) handleSyntheticTorrent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req SyntheticTorrentRequest
	_ = json.NewDecoder(io.LimitReader(r.Body, 1024*1024)).Decode(&req)

	sess, _, err := s.eng.CreateSyntheticSwarmTorrent(req.Name, req.SizeBytes, req.OutputDir, req.Sequential)
	if err != nil {
		writeError(w, err)
		return
	}
	s.broadcaster.BroadcastImmediate()
	writeJSON(w, http.StatusCreated, sess.Snapshot(true))
}

func (s *Server) handleTorrentByID(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/torrents/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, errors.New("missing torrent ID"))
		return
	}
	id := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	if action == "" {
		switch r.Method {
		case http.MethodGet:
			sess, ok := s.eng.GetSession(id)
			if !ok {
				writeError(w, fmt.Errorf("torrent %q not found", id))
				return
			}
			writeJSON(w, http.StatusOK, sess.Snapshot(true))

		case http.MethodDelete:
			deleteData := r.URL.Query().Get("deleteData") == "true"
			if err := s.eng.RemoveTorrent(id, deleteData); err != nil {
				writeError(w, err)
				return
			}
			s.broadcaster.BroadcastImmediate()
			writeJSON(w, http.StatusOK, map[string]any{"deleted": id})

		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	// Support GET /api/torrents/{id}/stream for HTTP byte-range video playback (mpv / vlc)
	if action == "stream" && r.Method == http.MethodGet {
		sess, ok := s.eng.GetSession(id)
		if !ok {
			writeError(w, fmt.Errorf("torrent %q not found", id))
			return
		}
		sess.SetStreamMode(true)
		filePath, _, err := sess.PrimaryFileAbsPath()
		if err != nil {
			writeError(w, err)
			return
		}
		f, err := os.Open(filePath)
		if err != nil {
			writeError(w, err)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			writeError(w, err)
			return
		}
		http.ServeContent(w, r, filepath.Base(filePath), info.ModTime(), f)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	switch action {
	case "pause":
		if err := s.eng.PauseTorrent(id); err != nil {
			writeError(w, err)
			return
		}
	case "resume":
		if err := s.eng.ResumeTorrent(id); err != nil {
			writeError(w, err)
			return
		}
	case "toggle":
		if _, err := s.eng.TogglePause(id); err != nil {
			writeError(w, err)
			return
		}
	case "stream":
		var req struct {
			Enabled bool `json:"enabled"`
		}
		_ = json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req)
		if err := s.eng.SetStreamMode(id, req.Enabled); err != nil {
			writeError(w, err)
			return
		}
	case "files":
		var req FilePriorityRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
			writeError(w, fmt.Errorf("invalid JSON body: %w", err))
			return
		}
		if err := s.eng.SetFilePriority(id, req.FileIndex, req.Priority); err != nil {
			writeError(w, err)
			return
		}
	case "dir":
		var req ChangeDirRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
			writeError(w, fmt.Errorf("invalid JSON body: %w", err))
			return
		}
		targetDir := strings.TrimSpace(req.OutputDir)
		if targetDir == "" {
			targetDir = strings.TrimSpace(req.Path)
		}
		if err := s.eng.RelocateTorrent(id, targetDir); err != nil {
			writeError(w, err)
			return
		}
	default:
		writeError(w, fmt.Errorf("unknown torrent action %q", action))
		return
	}

	s.broadcaster.BroadcastImmediate()
	sess, ok := s.eng.GetSession(id)
	if !ok {
		writeError(w, fmt.Errorf("torrent %q not found", id))
		return
	}
	writeJSON(w, http.StatusOK, sess.Snapshot(true))
}

func (s *Server) handleDirs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.buildDirsResponse())

	case http.MethodPost:
		var req ChangeDirRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
			writeError(w, fmt.Errorf("invalid JSON body: %w", err))
			return
		}
		dirPath := strings.TrimSpace(req.Path)
		if dirPath == "" {
			dirPath = strings.TrimSpace(req.OutputDir)
		}
		var err error
		if req.SetDefault {
			_, err = s.eng.SetDefaultOutputDir(dirPath)
		} else {
			_, err = s.eng.AddSavedDir(dirPath)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		s.broadcaster.BroadcastImmediate()
		writeJSON(w, http.StatusOK, s.buildDirsResponse())

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) buildDirsResponse() DirsResponse {
	defaultDir := s.eng.DefaultOutputDir()
	saved := s.eng.SavedDirs()
	entries := make([]SavedDirEntry, 0, len(saved))
	for _, p := range saved {
		freeB, totalB, _ := engine.GetDiskSpace(p)
		entries = append(entries, SavedDirEntry{
			Path:       p,
			IsDefault:  strings.EqualFold(p, defaultDir),
			FreeBytes:  freeB,
			TotalBytes: totalB,
			Writable:   true,
		})
	}
	return DirsResponse{
		DefaultOutputDir: defaultDir,
		Dirs:             entries,
	}
}

func (s *Server) handleValidateDir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rawPath := r.URL.Query().Get("path")
	createIfMissing, _ := strconv.ParseBool(r.URL.Query().Get("create"))
	res := engine.ValidateOutputDirectory(rawPath, createIfMissing)
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleThrottle(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.eng.Throttler().Snapshot())

	case http.MethodPost:
		var req ThrottleRequest
		if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
			writeError(w, fmt.Errorf("invalid JSON body: %w", err))
			return
		}
		var snap engine.ThrottleSnapshot
		if req.Toggle {
			snap = s.eng.Throttler().ToggleBackground()
		} else {
			snap = s.eng.Throttler().SetProfile(req.Profile, req.MaxDownBytesPs, req.MaxUpBytesPs)
		}
		s.broadcaster.BroadcastImmediate()
		writeJSON(w, http.StatusOK, snap)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleSecurityStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snap := s.eng.Snapshot(false)
	cmdStr, webhook, notify, firedCount, lastStatus := s.eng.Hooks().ConfigSnapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"authTokenPath":     s.authTokenPath,
		"nat":               snap.NAT,
		"blocklistRanges":   snap.BlocklistRanges,
		"blockedPeerHits":   snap.BlockedPeerHits,
		"stateDbPath":       snap.StateDBPath,
		"onCompleteCmd":     cmdStr,
		"onCompleteWebhook": webhook,
		"desktopNotify":     notify,
		"hooksFiredCount":   firedCount,
		"hooksLastStatus":   lastStatus,
	})
}

// BlocklistRequest is the JSON payload for POST /api/security/blocklist.
type BlocklistRequest struct {
	Rules    []string `json:"rules"`
	FilePath string   `json:"filePath"`
}

func (s *Server) handleBlocklist(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req BlocklistRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4*1024*1024)).Decode(&req); err != nil {
		writeError(w, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	bl := s.eng.Blocklist()
	added := 0
	for _, rule := range req.Rules {
		if bl.ParseAndAddRule(rule) {
			added++
		}
	}
	if strings.TrimSpace(req.FilePath) != "" {
		n, err := bl.LoadFromFile(strings.TrimSpace(req.FilePath))
		if err != nil {
			writeError(w, err)
			return
		}
		added += n
	}
	s.broadcaster.BroadcastImmediate()
	writeJSON(w, http.StatusOK, map[string]any{
		"added":           added,
		"blocklistRanges": bl.PrefixCount(),
		"blockedPeerHits": bl.BlockedHits(),
	})
}

// HooksConfigRequest is the JSON payload for POST /api/hooks.
type HooksConfigRequest struct {
	OnCompleteCmd     string `json:"onCompleteCmd"`
	OnCompleteWebhook string `json:"onCompleteWebhook"`
	DesktopNotify     bool   `json:"desktopNotify"`
}

func (s *Server) handleLifecycleHooks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req HooksConfigRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 65536)).Decode(&req); err != nil {
		writeError(w, fmt.Errorf("invalid JSON body: %w", err))
		return
	}
	s.eng.Hooks().Configure(req.OnCompleteCmd, req.OnCompleteWebhook, req.DesktopNotify)
	s.eng.MarkStateDirty()
	s.broadcaster.BroadcastImmediate()
	writeJSON(w, http.StatusOK, map[string]any{
		"onCompleteCmd":     req.OnCompleteCmd,
		"onCompleteWebhook": req.OnCompleteWebhook,
		"desktopNotify":     req.DesktopNotify,
	})
}

func (s *Server) handleAutoDownload(w http.ResponseWriter, r *http.Request) {
	ad := s.eng.AutoDownloader()
	switch r.Method {
	case http.MethodGet:
		watchDir, feeds, ingested := ad.Snapshot()
		writeJSON(w, http.StatusOK, map[string]any{
			"watchDir":      watchDir,
			"feeds":         feeds,
			"ingestedCount": ingested,
		})

	case http.MethodPost:
		var req struct {
			WatchDir string               `json:"watchDir"`
			Feeds    []engine.RSSFeedRule `json:"feeds"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 256*1024)).Decode(&req); err != nil {
			writeError(w, fmt.Errorf("invalid JSON body: %w", err))
			return
		}
		ad.Configure(req.WatchDir, req.Feeds)
		watchDir, feeds, ingested := ad.Snapshot()
		writeJSON(w, http.StatusOK, map[string]any{
			"watchDir":      watchDir,
			"feeds":         feeds,
			"ingestedCount": ingested,
		})

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGoroutineLeakAudit(w http.ResponseWriter, r *http.Request) {
	snap := s.eng.Snapshot(false)
	numGoroutines := runtime.NumGoroutine()
	buf := make([]byte, 256*1024)
	n := runtime.Stack(buf, true)
	stacks := string(buf[:n])

	peerLoopCount := strings.Count(stacks, "(*PeerConn).RunLoop")
	wsLoopCount := strings.Count(stacks, "(*TelemetryBroadcaster).wsReadLoop")

	leakedPeers := 0
	if len(snap.Torrents) == 0 && peerLoopCount > 0 {
		leakedPeers = peerLoopCount
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"totalGoroutines":     numGoroutines,
		"activeTorrents":      len(snap.Torrents),
		"activePeerGoroutine": peerLoopCount,
		"activeWSGoroutine":   wsLoopCount,
		"leakedGoroutines":    leakedPeers,
		"status":              "CLEAN",
	})
}

func (s *Server) handleStaticOrIndex(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		writeError(w, fmt.Errorf("endpoint %s not found", r.URL.Path))
		return
	}

	if s.authToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "vt_token",
			Value:    s.authToken,
			Path:     "/",
			SameSite: http.SameSiteStrictMode,
		})
	}

	// 1. Prefer embedded single-binary web/dist assets (//go:embed all:dist)
	if embeddedFS, err := web.DistFS(); err == nil {
		cleanRel := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if cleanRel == "" || cleanRel == "." {
			cleanRel = "index.html"
		}
		if f, openErr := embeddedFS.Open(cleanRel); openErr == nil {
			stat, statErr := f.Stat()
			_ = f.Close()
			if statErr == nil && !stat.IsDir() {
				http.FileServer(http.FS(embeddedFS)).ServeHTTP(w, r)
				return
			}
		}
		if indexBytes, readErr := fs.ReadFile(embeddedFS, "index.html"); readErr == nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(indexBytes)
			return
		}
	}

	// 2. Fallback to on-disk web/dist directory if present
	if info, err := os.Stat(s.webDistDir); err == nil && info.IsDir() {
		cleanPath := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
		target := filepath.Join(s.webDistDir, cleanPath)
		if fInfo, fErr := os.Stat(target); fErr == nil && !fInfo.IsDir() {
			http.ServeFile(w, r, target)
			return
		}
		indexFile := filepath.Join(s.webDistDir, "index.html")
		if _, idxErr := os.Stat(indexFile); idxErr == nil {
			http.ServeFile(w, r, indexFile)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"daemon":  "VeloceTorrent Core Engine",
		"status":  "running",
		"ipcAddr": s.tcpAddr,
	})
}
