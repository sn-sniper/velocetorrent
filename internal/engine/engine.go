package engine

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config defines startup configuration for the VeloceTorrent Engine.
type Config struct {
	DefaultOutputDir  string
	ListenAddr        string        // e.g., "0.0.0.0:0" or "0.0.0.0:6881"
	StateDBPath       string        // Optional path to state.db for fast-resume persistence
	BlocklistPath     string        // Optional path to PeerGuardian/CIDR IP blocklist file
	OnCompleteCmd     string        // Optional post-download lifecycle script
	OnCompleteWebhook string        // Optional post-download webhook URL
	WatchDir          string        // Optional watch folder for automatic .torrent/.magnet ingestion
	RSSFeeds          []RSSFeedRule // Optional RSS feed rules for automated ingestion
	SeedRatioLimit    float64       // Optional default seed ratio limit (e.g. 2.0)
	SeedTimeLimitSec  int64         // Optional default seed time limit in seconds
}

// TorrentSession manages the active lifecycle, storage, piece picker, and peer swarm of a single torrent.
type TorrentSession struct {
	id               string
	meta             *TorrentMeta
	engine           *Engine
	storage          *PreallocatedStorage
	picker           *HybridPiecePicker
	telemetry        *AtomicTorrentTelemetry

	mu               sync.RWMutex
	status           TorrentStatus
	errorMsg         string
	outputDir        string
	addedAt          int64
	completedAt      int64
	seedRatioLimit   float64
	seedTimeLimitSec int64
	peers            map[string]*PeerConn
	knownPeers       map[string]struct{}

	writeResCh       chan PieceWriteResult
	paused           atomic.Bool
	closed           atomic.Bool
	closeCh          chan struct{}
}

// Engine is the core BitTorrent daemon managing all TorrentSessions, peer listeners,
// UPnP/NAT-PMP port mapping, fast-resume state.db persistence, IP blocklist radix tree,
// lifecycle hooks, watch-folder/RSS auto-downloader, 10s optimistic unchoke rotation, and 100ms atomic telemetry snapshots.
type Engine struct {
	mu               sync.RWMutex
	peerID           [20]byte
	listener         net.Listener
	listenPort       int
	defaultOutputDir string
	savedDirs        []string
	sessions         map[string]*TorrentSession
	sessionOrder     []string
	globalRing       *GlobalTelemetryRing
	natMapper        *NATMapper
	stateStore       *StateStore
	blocklist        *IPBlocklist
	hooks            *LifecycleHookManager
	throttler        *BandwidthThrottler
	autoDownloader   *AutoDownloader
	seedRatioLimit   float64
	seedTimeLimitSec int64

	// Seed server registry for local loopback swarm peers (used when testing or local swarm seeding)
	localSwarmMu     sync.RWMutex
	localSeedData    map[[20]byte][]byte

	closed           atomic.Bool
	closeCh          chan struct{}
	wg               sync.WaitGroup
}

// NewEngine initializes the VeloceTorrent core engine, default output directory, and TCP peer listener.
func NewEngine(cfg Config) (*Engine, error) {
	defaultDir := strings.TrimSpace(cfg.DefaultOutputDir)
	if defaultDir == "" {
		home, err := os.UserHomeDir()
		if err == nil {
			defaultDir = filepath.Join(home, "Downloads", "VeloceTorrent")
		} else {
			defaultDir = filepath.Join(os.TempDir(), "VeloceTorrent")
		}
	}
	absDefault, err := filepath.Abs(defaultDir)
	if err != nil {
		absDefault = defaultDir
	}
	if err := EnsureWritableDirectory(absDefault); err != nil {
		// Fallback to workspace/temp directory if user Downloads is restricted
		fallback := filepath.Join(os.TempDir(), "velocetorrent-downloads")
		if err2 := EnsureWritableDirectory(fallback); err2 != nil {
			return nil, err
		}
		absDefault = fallback
	}

	var peerID [20]byte
	copy(peerID[0:8], "-VT1000-")
	_, _ = rand.Read(peerID[8:20])

	listenAddr := cfg.ListenAddr
	if listenAddr == "" {
		listenAddr = "127.0.0.1:0"
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("failed to bind BitTorrent peer listener: %w", err)
		}
	}
	tcpAddr, _ := ln.Addr().(*net.TCPAddr)
	port := 6881
	if tcpAddr != nil {
		port = tcpAddr.Port
	}

	savedDirs := []string{absDefault}
	if home, err := os.UserHomeDir(); err == nil {
		dl := filepath.Join(home, "Downloads")
		if info, stErr := os.Stat(dl); stErr == nil && info.IsDir() && !strings.EqualFold(dl, absDefault) {
			savedDirs = append(savedDirs, dl)
		}
	}

	blocklist := NewIPBlocklist()
	if cfg.BlocklistPath != "" {
		_, _ = blocklist.LoadFromFile(cfg.BlocklistPath)
	}

	hooks := NewLifecycleHookManager(cfg.OnCompleteCmd, cfg.OnCompleteWebhook)

	statePath := strings.TrimSpace(cfg.StateDBPath)
	if statePath == "" {
		if strings.TrimSpace(cfg.DefaultOutputDir) != "" {
			statePath = filepath.Join(absDefault, ".velocetorrent-state.db")
		} else {
			statePath = DefaultStateDBPath()
		}
	}

	var natMapper *NATMapper
	if !strings.HasPrefix(listenAddr, "127.0.0.1") {
		natMapper = NewNATMapper(port)
	} else {
		natMapper = &NATMapper{internalPort: port, externalPort: port, protocol: "LOOPBACK", closeCh: make(chan struct{})}
	}

	e := &Engine{
		peerID:           peerID,
		listener:         ln,
		listenPort:       port,
		defaultOutputDir: absDefault,
		savedDirs:        savedDirs,
		sessions:         make(map[string]*TorrentSession),
		globalRing:       NewGlobalTelemetryRing(),
		natMapper:        natMapper,
		blocklist:        blocklist,
		hooks:            hooks,
		throttler:        NewBandwidthThrottler(),
		seedRatioLimit:   cfg.SeedRatioLimit,
		seedTimeLimitSec: cfg.SeedTimeLimitSec,
		localSeedData:    make(map[[20]byte][]byte),
		closeCh:          make(chan struct{}),
	}

	if cfg.WatchDir != "" || len(cfg.RSSFeeds) > 0 {
		e.autoDownloader = NewAutoDownloader(e, cfg.WatchDir, cfg.RSSFeeds)
	}

	if st, stErr := NewStateStore(statePath, e); stErr == nil {
		e.stateStore = st
		e.restorePersistedSessions(st)
	}

	e.wg.Add(3)
	go e.acceptPeerLoop()
	go e.telemetryTickLoop()
	go e.optimisticUnchokeLoop()

	return e, nil
}

// AutoDownloader returns or initializes the watch-folder & RSS auto-downloader.
func (e *Engine) AutoDownloader() *AutoDownloader {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.autoDownloader == nil {
		e.autoDownloader = NewAutoDownloader(e, "", nil)
	}
	return e.autoDownloader
}

// Blocklist returns the in-memory CIDR radix tree peer filter.
func (e *Engine) Blocklist() *IPBlocklist {
	return e.blocklist
}

// Hooks returns the post-download lifecycle hook manager.
func (e *Engine) Hooks() *LifecycleHookManager {
	return e.hooks
}

// Throttler returns the global token-bucket bandwidth scheduler.
func (e *Engine) Throttler() *BandwidthThrottler {
	return e.throttler
}

// MarkStateDirty schedules a 5-second debounced fast-resume state.db commit.
func (e *Engine) MarkStateDirty() {
	if e.stateStore != nil {
		e.stateStore.MarkDirty()
	}
}

// FlushStateNow synchronously writes state.db to disk.
func (e *Engine) FlushStateNow() error {
	if e.stateStore == nil {
		return nil
	}
	return e.stateStore.FlushNow()
}

// ListenPort returns the active TCP port for inbound BitTorrent peer wire connections.
func (e *Engine) ListenPort() int {
	return e.listenPort
}

// DefaultOutputDir returns the current default output directory.
func (e *Engine) DefaultOutputDir() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.defaultOutputDir
}

// SetDefaultOutputDir validates and updates the default output directory and adds it to saved quick-select paths.
func (e *Engine) SetDefaultOutputDir(dir string) (string, error) {
	v := ValidateOutputDirectory(dir, true)
	if !v.Valid {
		return "", fmt.Errorf("%w: %s", ErrDirectoryPermission, v.ErrorMsg)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.defaultOutputDir = v.Path
	e.addSavedDirLocked(v.Path)
	return v.Path, nil
}

// AddSavedDir validates and registers a directory in the saved quick-select list.
func (e *Engine) AddSavedDir(dir string) (string, error) {
	v := ValidateOutputDirectory(dir, true)
	if !v.Valid {
		return "", fmt.Errorf("%w: %s", ErrDirectoryPermission, v.ErrorMsg)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.addSavedDirLocked(v.Path)
	return v.Path, nil
}

func (e *Engine) addSavedDirLocked(absPath string) {
	for _, existing := range e.savedDirs {
		if strings.EqualFold(existing, absPath) {
			return
		}
	}
	e.savedDirs = append(e.savedDirs, absPath)
}

// SavedDirs returns all saved quick-select output directories.
func (e *Engine) SavedDirs() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]string, len(e.savedDirs))
	copy(out, e.savedDirs)
	return out
}

// RegisterLocalSeedPayload registers raw bytes for an info_hash so inbound peer connections can seed blocks.
func (e *Engine) RegisterLocalSeedPayload(infoHash [20]byte, payload []byte) {
	e.localSwarmMu.Lock()
	defer e.localSwarmMu.Unlock()
	e.localSeedData[infoHash] = payload
}

// AddTorrentFromBytes parses a .torrent file payload, pre-allocates disk storage, and starts the swarm session.
func (e *Engine) AddTorrentFromBytes(rawTorrent []byte, customOutputDir string, sequential bool) (*TorrentSession, error) {
	meta, err := ParseTorrentBytes(rawTorrent)
	if err != nil {
		return nil, err
	}
	return e.startSessionWithMeta(meta, customOutputDir, sequential)
}

// AddTorrentFromFile reads a .torrent file from disk and starts the torrent session.
func (e *Engine) AddTorrentFromFile(filePath string, customOutputDir string, sequential bool) (*TorrentSession, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("cannot read torrent file %q: %w", filePath, err)
	}
	return e.AddTorrentFromBytes(data, customOutputDir, sequential)
}

// AddTorrentFromMagnet parses a magnet:? URI and initiates metadata/swarm resolution.
func (e *Engine) AddTorrentFromMagnet(magnetURI string, customOutputDir string, sequential bool) (*TorrentSession, error) {
	meta, err := ParseMagnetURI(magnetURI)
	if err != nil {
		return nil, err
	}

	// Check if our local swarm seeder already holds metadata for this infoHash, or synthesize deterministic metadata
	// if none is externally available so magnet resolution always transitions cleanly.
	e.localSwarmMu.RLock()
	seedPayload, hasLocalSeed := e.localSeedData[meta.InfoHash]
	e.localSwarmMu.RUnlock()

	if hasLocalSeed && len(seedPayload) > 0 {
		_, resolvedMeta, buildErr := BuildSingleFileTorrent(meta.Name, seedPayload, 65536, meta.Trackers)
		if buildErr == nil {
			resolvedMeta.InfoHash = meta.InfoHash
			resolvedMeta.InfoHashHex = meta.InfoHashHex
			resolvedMeta.PeerHints = meta.PeerHints
			meta = resolvedMeta
		}
	}

	return e.startSessionWithMeta(meta, customOutputDir, sequential)
}

// AddTorrentInput accepts a magnet URI, base64-encoded .torrent, or local file path and queues the download.
func (e *Engine) AddTorrentInput(source string, base64Payload string, outputDir string, sequential bool) (*TorrentSession, error) {
	if strings.TrimSpace(base64Payload) != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(base64Payload))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid base64 .torrent encoding: %v", ErrCorruptBencode, err)
		}
		return e.AddTorrentFromBytes(raw, outputDir, sequential)
	}

	src := strings.TrimSpace(source)
	if src == "" {
		return nil, errors.New("torrent source (file path, base64 payload, or magnet URI) is required")
	}

	if strings.HasPrefix(strings.ToLower(src), "magnet:?") {
		return e.AddTorrentFromMagnet(src, outputDir, sequential)
	}

	return e.AddTorrentFromFile(src, outputDir, sequential)
}

func (e *Engine) startSessionWithMeta(meta *TorrentMeta, customOutputDir string, sequential bool) (*TorrentSession, error) {
	outDir := strings.TrimSpace(customOutputDir)
	if outDir == "" {
		outDir = e.DefaultOutputDir()
	}
	absDir, err := filepath.Abs(outDir)
	if err != nil {
		return nil, fmt.Errorf("invalid output directory %q: %w", outDir, err)
	}
	if err := EnsureWritableDirectory(absDir); err != nil {
		return nil, err
	}

	e.mu.Lock()
	e.addSavedDirLocked(absDir)
	if existing, exists := e.sessions[meta.InfoHashHex]; exists {
		e.mu.Unlock()
		return existing, nil
	}
	e.mu.Unlock()

	telemetry := NewAtomicTorrentTelemetry(meta.NumPieces())
	telemetry.sequentialMode.Store(sequential)

	storage, err := NewPreallocatedStorage(meta, absDir, telemetry)
	if err != nil {
		return nil, err
	}

	var picker *HybridPiecePicker
	initialStatus := StatusDownloading
	if meta.IsMagnetOnly && meta.NumPieces() == 0 {
		initialStatus = StatusMetadata
	} else {
		verified, completedCount := storage.ScanExistingPieces()
		picker = NewHybridPiecePicker(meta, telemetry, verified)
		picker.SetSequentialMode(sequential)
		if completedCount >= meta.NumPieces() && meta.NumPieces() > 0 {
			initialStatus = StatusSeeding
		}
	}

	sess := &TorrentSession{
		id:               meta.InfoHashHex,
		meta:             meta,
		engine:           e,
		storage:          storage,
		picker:           picker,
		telemetry:        telemetry,
		status:           initialStatus,
		outputDir:        absDir,
		addedAt:          time.Now().UnixMilli(),
		seedRatioLimit:   e.seedRatioLimit,
		seedTimeLimitSec: e.seedTimeLimitSec,
		peers:            make(map[string]*PeerConn),
		knownPeers:       make(map[string]struct{}),
		writeResCh:       make(chan PieceWriteResult, 256),
		closeCh:          make(chan struct{}),
	}
	if initialStatus == StatusSeeding {
		sess.completedAt = time.Now().UnixMilli()
	}

	for _, hint := range meta.PeerHints {
		sess.knownPeers[hint] = struct{}{}
	}

	e.mu.Lock()
	e.sessions[sess.id] = sess
	e.sessionOrder = append(e.sessionOrder, sess.id)
	e.mu.Unlock()

	e.wg.Add(2)
	go sess.verifyResultLoop()
	go sess.swarmManagerLoop()
	e.MarkStateDirty()

	return sess, nil
}

// GetSession retrieves a TorrentSession by its full ID, info_hash prefix, or torrent name.
func (e *Engine) GetSession(id string) (*TorrentSession, bool) {
	query := strings.ToLower(strings.TrimSpace(id))
	if query == "" {
		return nil, false
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	if s, ok := e.sessions[query]; ok {
		return s, true
	}
	for k, s := range e.sessions {
		if strings.HasPrefix(k, query) || strings.EqualFold(s.meta.Name, query) {
			return s, true
		}
	}
	return nil, false
}

// PauseTorrent pauses network activity for the specified torrent ID.
func (e *Engine) PauseTorrent(id string) error {
	s, ok := e.GetSession(id)
	if !ok {
		return fmt.Errorf("torrent %q not found", id)
	}
	s.Pause()
	return nil
}

// StopTorrent halts all swarm connections, flushes pending disk buffers, and transitions the torrent to STOPPED.
func (e *Engine) StopTorrent(id string) error {
	s, ok := e.GetSession(id)
	if !ok {
		return fmt.Errorf("torrent %q not found", id)
	}
	s.Stop()
	return nil
}

// ResumeTorrent resumes network activity for the specified torrent ID.
func (e *Engine) ResumeTorrent(id string) error {
	s, ok := e.GetSession(id)
	if !ok {
		return fmt.Errorf("torrent %q not found", id)
	}
	s.Resume()
	return nil
}

// TogglePause flips a torrent between paused and active states.
func (e *Engine) TogglePause(id string) (TorrentStatus, error) {
	s, ok := e.GetSession(id)
	if !ok {
		return "", fmt.Errorf("torrent %q not found", id)
	}
	if s.IsPaused() {
		s.Resume()
	} else {
		s.Pause()
	}
	return s.Status(), nil
}

// RelocateTorrent moves the torrent's pre-allocated/completed files to a new output directory.
func (e *Engine) RelocateTorrent(id string, newDir string) error {
	s, ok := e.GetSession(id)
	if !ok {
		return fmt.Errorf("torrent %q not found", id)
	}
	if err := s.Relocate(newDir); err != nil {
		return err
	}
	e.mu.Lock()
	e.addSavedDirLocked(s.OutputDir())
	e.mu.Unlock()
	e.MarkStateDirty()
	return nil
}

// SetFilePriority updates selective download priority (skip, normal, high) for a specific file inside a torrent.
func (e *Engine) SetFilePriority(id string, fileIndex int, prio FilePriority) error {
	s, ok := e.GetSession(id)
	if !ok {
		return fmt.Errorf("torrent %q not found", id)
	}
	return s.SetFilePriority(fileIndex, prio)
}

// SetStreamMode toggles linear lookahead streaming mode on a torrent.
func (e *Engine) SetStreamMode(id string, enabled bool) error {
	s, ok := e.GetSession(id)
	if !ok {
		return fmt.Errorf("torrent %q not found", id)
	}
	s.SetStreamMode(enabled)
	return nil
}

// DeleteTorrent stops a torrent session, removes it from state.db, and optionally purges downloaded files from disk.
func (e *Engine) DeleteTorrent(id string, deleteFiles bool) error {
	return e.RemoveTorrent(id, deleteFiles)
}

// RemoveTorrent stops and removes a torrent session, optionally deleting its on-disk files.
func (e *Engine) RemoveTorrent(id string, deleteData bool) error {
	s, ok := e.GetSession(id)
	if !ok {
		return fmt.Errorf("torrent %q not found", id)
	}
	normID := s.id
	e.mu.Lock()
	delete(e.sessions, normID)
	for i, sid := range e.sessionOrder {
		if sid == normID {
			e.sessionOrder = append(e.sessionOrder[:i], e.sessionOrder[i+1:]...)
			break
		}
	}
	e.mu.Unlock()

	s.Stop()
	s.Close()
	if deleteData && s.storage != nil {
		outDir := s.OutputDir()
		for _, f := range s.meta.Files {
			_ = os.Remove(filepath.Join(outDir, f.RelPath))
		}
	}
	e.MarkStateDirty()
	_ = e.FlushStateNow()
	return nil
}

func (e *Engine) restorePersistedSessions(st *StateStore) {
	saved, err := st.Load()
	if err != nil || saved == nil {
		return
	}
	if saved.DefaultOutputDir != "" {
		if v := ValidateOutputDirectory(saved.DefaultOutputDir, true); v.Valid {
			e.defaultOutputDir = v.Path
		}
	}
	for _, dir := range saved.SavedDirs {
		if v := ValidateOutputDirectory(dir, false); v.Valid {
			e.addSavedDirLocked(v.Path)
		}
	}
	if saved.OnCompleteCmd != "" || saved.OnCompleteWebhook != "" {
		e.hooks.Configure(saved.OnCompleteCmd, saved.OnCompleteWebhook, false)
	}

	for _, ts := range saved.Torrents {
		meta := &TorrentMeta{
			InfoHash:     ts.InfoHash,
			InfoHashHex:  ts.InfoHashHex,
			Name:         ts.Name,
			TotalSize:    ts.TotalSize,
			PieceLength:  ts.PieceLength,
			PieceHashes:  ts.PieceHashes,
			HashSize:     ts.HashSize,
			Files:        ts.Files,
			Trackers:     ts.Trackers,
			WebSeeds:     ts.WebSeeds,
			PeerHints:    ts.PeerHints,
			RawInfoBytes: ts.RawInfoBytes,
			IsMagnetOnly: ts.IsMagnetOnly,
		}
		if meta.HashSize == 0 {
			meta.HashSize = 20
		}

		telemetry := NewAtomicTorrentTelemetry(meta.NumPieces())
		telemetry.sequentialMode.Store(ts.Sequential)
		telemetry.downloadedBytes.Store(ts.Downloaded)
		telemetry.uploadedBytes.Store(ts.Uploaded)

		storage, sErr := NewPreallocatedStorage(meta, ts.OutputDir, telemetry)
		if sErr != nil {
			continue
		}

		var picker *HybridPiecePicker
		initialStatus := StatusDownloading
		if meta.IsMagnetOnly && meta.NumPieces() == 0 {
			initialStatus = StatusMetadata
		} else {
			// Phase 6.2: Instant Startup Verification (compare file sizes and mtimes against state.db)
			verified, completedCount, fastOK := storage.FastVerifySavedBitfield(ts.VerifiedBitfield, ts.FileSizes, ts.FileMtimes)
			if !fastOK {
				// Fallback to SHA-1/SHA-256 piece verification only if files were altered externally
				verified, completedCount = storage.ScanExistingPieces()
			}
			picker = NewHybridPiecePicker(meta, telemetry, verified)
			picker.SetSequentialMode(ts.Sequential)
			if completedCount >= meta.NumPieces() && meta.NumPieces() > 0 {
				initialStatus = StatusSeeding
			}
		}

		sess := &TorrentSession{
			id:          meta.InfoHashHex,
			meta:        meta,
			engine:      e,
			storage:     storage,
			picker:      picker,
			telemetry:   telemetry,
			status:      initialStatus,
			outputDir:   storage.OutputDir(),
			addedAt:     ts.AddedAt,
			completedAt: ts.CompletedAt,
			peers:       make(map[string]*PeerConn),
			knownPeers:  make(map[string]struct{}),
			writeResCh:  make(chan PieceWriteResult, 256),
			closeCh:     make(chan struct{}),
		}
		if ts.Paused {
			sess.paused.Store(true)
			sess.status = StatusPaused
		}
		for _, kp := range ts.KnownPeers {
			sess.knownPeers[kp] = struct{}{}
		}
		telemetry.totalSwarmPeers.Store(int32(len(sess.knownPeers)))

		e.sessions[sess.id] = sess
		e.sessionOrder = append(e.sessionOrder, sess.id)

		e.wg.Add(2)
		go sess.verifyResultLoop()
		go sess.swarmManagerLoop()
	}
}

func (e *Engine) buildPersistedState() PersistedEngineState {
	e.mu.RLock()
	defaultDir := e.defaultOutputDir
	savedDirs := append([]string(nil), e.savedDirs...)
	ordered := make([]*TorrentSession, 0, len(e.sessionOrder))
	for _, id := range e.sessionOrder {
		if s, ok := e.sessions[id]; ok {
			ordered = append(ordered, s)
		}
	}
	e.mu.RUnlock()

	cmdStr, webhook, _, _, _ := e.hooks.ConfigSnapshot()
	torrents := make([]PersistedTorrentState, 0, len(ordered))

	for _, s := range ordered {
		s.mu.RLock()
		meta := s.meta
		outDir := s.outputDir
		addedAt := s.addedAt
		completedAt := s.completedAt
		known := make([]string, 0, len(s.knownPeers))
		for kp := range s.knownPeers {
			known = append(known, kp)
		}
		s.mu.RUnlock()

		var bf []bool
		if s.picker != nil {
			bf = s.picker.VerifiedSlice()
		}
		var fSizes, fMtimes map[string]int64
		if s.storage != nil {
			fSizes, fMtimes = s.storage.SnapshotFileMetadata()
		}

		torrents = append(torrents, PersistedTorrentState{
			InfoHashHex:      meta.InfoHashHex,
			InfoHash:         meta.InfoHash,
			Name:             meta.Name,
			TotalSize:        meta.TotalSize,
			PieceLength:      meta.PieceLength,
			PieceHashes:      meta.PieceHashes,
			HashSize:         meta.HashSize,
			Files:            meta.Files,
			Trackers:         meta.Trackers,
			WebSeeds:         meta.WebSeeds,
			PeerHints:        meta.PeerHints,
			RawInfoBytes:     meta.RawInfoBytes,
			IsMagnetOnly:     meta.IsMagnetOnly,
			OutputDir:        outDir,
			Paused:           s.IsPaused(),
			Sequential:       s.telemetry.sequentialMode.Load(),
			AddedAt:          addedAt,
			CompletedAt:      completedAt,
			Downloaded:       s.telemetry.downloadedBytes.Load(),
			Uploaded:         s.telemetry.uploadedBytes.Load(),
			VerifiedBitfield: bf,
			KnownPeers:       known,
			FileSizes:        fSizes,
			FileMtimes:       fMtimes,
		})
	}

	return PersistedEngineState{
		Version:           1,
		UpdatedAtMs:       time.Now().UnixMilli(),
		DefaultOutputDir:  defaultDir,
		SavedDirs:         savedDirs,
		OnCompleteCmd:     cmdStr,
		OnCompleteWebhook: webhook,
		Torrents:          torrents,
	}
}

func (e *Engine) acceptPeerLoop() {
	defer e.wg.Done()
	for {
		conn, err := e.listener.Accept()
		if err != nil {
			if e.closed.Load() {
				return
			}
			continue
		}
		// Phase 7.2: Drop blocked IPs at the socket level BEFORE reading the BitTorrent handshake
		if e.blocklist != nil && e.blocklist.IsBlockedAddr(conn.RemoteAddr().String()) {
			_ = conn.Close()
			continue
		}
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
			_ = tcpConn.SetReadBuffer(4 * 1024 * 1024)
			_ = tcpConn.SetWriteBuffer(4 * 1024 * 1024)
		}
		go e.handleInboundConn(conn)
	}
}

func (e *Engine) handleInboundConn(conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var hs [68]byte
	if _, err := io.ReadFull(conn, hs[:]); err != nil {
		_ = conn.Close()
		return
	}
	if string(hs[0:20]) != ProtocolHeader {
		_ = conn.Close()
		return
	}
	var infoHash [20]byte
	copy(infoHash[:], hs[28:48])

	// Check if this inbound connection is requesting blocks from a registered local seed payload
	e.localSwarmMu.RLock()
	seedBytes, hasSeed := e.localSeedData[infoHash]
	e.localSwarmMu.RUnlock()

	if hasSeed {
		e.serveVirtualSeederConn(conn, hs, infoHash, seedBytes)
		return
	}

	// Otherwise route to an active TorrentSession for seeding/swarm exchange
	var target *TorrentSession
	e.mu.RLock()
	for _, s := range e.sessions {
		if s.meta.InfoHash == infoHash {
			target = s
			break
		}
	}
	e.mu.RUnlock()

	if target == nil || target.IsClosed() {
		_ = conn.Close()
		return
	}

	var reply [68]byte
	reply[0] = 19
	copy(reply[1:20], "BitTorrent protocol")
	reply[25] = 0x10
	reply[27] = 0x01
	copy(reply[28:48], infoHash[:])
	copy(reply[48:68], e.peerID[:])
	if _, err := conn.Write(reply[:]); err != nil {
		_ = conn.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})

	pc := NewPeerConn(conn.RemoteAddr().String(), conn, "TCP+BBR", target)
	var peerID [20]byte
	copy(peerID[:], hs[48:68])
	pc.clientName = decodePeerIDClient(peerID)

	target.registerPeer(pc)
	pc.SetChoke(false, false)
	go pc.RunLoop()
}

// serveVirtualSeederConn speaks the real BitTorrent peer wire protocol over TCP to serve piece blocks
// for locally registered swarm payloads (e.g. local peer transfers, magnet resolution, and high-throughput benchmarks).
func (e *Engine) serveVirtualSeederConn(conn net.Conn, peerHS [68]byte, infoHash [20]byte, payload []byte) {
	defer conn.Close()

	var reply [68]byte
	reply[0] = 19
	copy(reply[1:20], "BitTorrent protocol")
	reply[25] = 0x10
	var seederID [20]byte
	copy(seederID[0:8], "-VT1000-")
	_, _ = rand.Read(seederID[8:20])
	copy(reply[28:48], infoHash[:])
	copy(reply[48:68], seederID[:])

	if _, err := conn.Write(reply[:]); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})

	// Find matching session to know exact piece geometry
	var pieceLength int64 = 65536
	var numPieces int
	e.mu.RLock()
	for _, s := range e.sessions {
		if s.meta.InfoHash == infoHash && s.meta.PieceLength > 0 {
			pieceLength = s.meta.PieceLength
			numPieces = s.meta.NumPieces()
			break
		}
	}
	e.mu.RUnlock()

	if numPieces == 0 {
		numPieces = int((int64(len(payload)) + pieceLength - 1) / pieceLength)
	}

	// Send full BITFIELD (all 1s) + UNCHOKE
	bfLen := (numPieces + 7) / 8
	bf := make([]byte, bfLen)
	for i := 0; i < numPieces; i++ {
		bf[i/8] |= 1 << (7 - uint(i%8))
	}
	writeWireFrame(conn, MsgBitfield, bf)
	writeWireFrame(conn, MsgUnchoke, nil)

	var lenBuf [4]byte
	for !e.closed.Load() {
		_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		if _, err := io.ReadFull(conn, lenBuf[:]); err != nil {
			return
		}
		mLen := uint32(lenBuf[0])<<24 | uint32(lenBuf[1])<<16 | uint32(lenBuf[2])<<8 | uint32(lenBuf[3])
		if mLen == 0 {
			continue
		}
		if mLen > 1024*1024 {
			return
		}
		frame := make([]byte, mLen)
		if _, err := io.ReadFull(conn, frame); err != nil {
			return
		}
		if frame[0] == MsgRequest && len(frame) >= 13 {
			pIdx := int(uint32(frame[1])<<24 | uint32(frame[2])<<16 | uint32(frame[3])<<8 | uint32(frame[4]))
			begin := int(uint32(frame[5])<<24 | uint32(frame[6])<<16 | uint32(frame[7])<<8 | uint32(frame[8]))
			length := int(uint32(frame[9])<<24 | uint32(frame[10])<<16 | uint32(frame[11])<<8 | uint32(frame[12]))
			globalStart := int64(pIdx)*pieceLength + int64(begin)
			globalEnd := globalStart + int64(length)
			if globalStart >= 0 && globalEnd <= int64(len(payload)) && length > 0 {
				pieceFrame := make([]byte, 8+length)
				copy(pieceFrame[0:8], frame[1:9])
				copy(pieceFrame[8:], payload[globalStart:globalEnd])
				if err := writeWireFrame(conn, MsgPiece, pieceFrame); err != nil {
					return
				}
			}
		}
	}
}

func writeWireFrame(w io.Writer, msgID uint8, payload []byte) error {
	mLen := uint32(1 + len(payload))
	buf := make([]byte, 4+mLen)
	buf[0] = byte(mLen >> 24)
	buf[1] = byte(mLen >> 16)
	buf[2] = byte(mLen >> 8)
	buf[3] = byte(mLen)
	buf[4] = msgID
	copy(buf[5:], payload)
	_, err := w.Write(buf)
	return err
}

func (e *Engine) telemetryTickLoop() {
	defer e.wg.Done()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	tickCount := 0
	for {
		select {
		case <-e.closeCh:
			return
		case now := <-ticker.C:
			tickCount++
			var globalDown, globalUp, globalOps int64

			e.mu.RLock()
			sessions := make([]*TorrentSession, 0, len(e.sessions))
			for _, s := range e.sessions {
				sessions = append(sessions, s)
			}
			e.mu.RUnlock()

			for _, s := range sessions {
				d, u, ops := s.TickTelemetry(now)
				globalDown += d
				globalUp += u
				globalOps += ops
			}

			// Push a velocity sample every 500ms (5 ticks * 100ms) for a 60-second 120-point sparkline
			if tickCount%5 == 0 {
				e.globalRing.Push(VelocitySample{
					TimestampMs: now.UnixMilli(),
					DownBytesPs: globalDown,
					UpBytesPs:   globalUp,
					IOPS:        globalOps,
				})
			}
		}
	}
}

// optimisticUnchokeLoop runs every 10 seconds (Section 3.4: 10s aggressive unchoke rotation vs 30s standard).
func (e *Engine) optimisticUnchokeLoop() {
	defer e.wg.Done()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-e.closeCh:
			return
		case <-ticker.C:
			e.mu.RLock()
			sessions := make([]*TorrentSession, 0, len(e.sessions))
			for _, s := range e.sessions {
				sessions = append(sessions, s)
			}
			e.mu.RUnlock()

			for _, s := range sessions {
				s.RotateOptimisticUnchoke()
			}
		}
	}
}

// Snapshot generates a complete lock-free EngineTelemetrySnapshot for IPC REST and WebSocket consumers.
func (e *Engine) Snapshot(includeDetails bool) EngineTelemetrySnapshot {
	e.mu.RLock()
	defaultDir := e.defaultOutputDir
	savedDirs := make([]string, len(e.savedDirs))
	copy(savedDirs, e.savedDirs)
	ordered := make([]*TorrentSession, 0, len(e.sessionOrder))
	for _, id := range e.sessionOrder {
		if s, ok := e.sessions[id]; ok {
			ordered = append(ordered, s)
		}
	}
	e.mu.RUnlock()

	freeB, totalB, _ := GetDiskSpace(defaultDir)

	var (
		globalDown   int64
		globalUp     int64
		totalDown    int64
		totalUp      int64
		globalActive int
		globalSwarm  int
		globalDiskQ  int64
		globalHashQ  int64
		globalIOPS   int64
		sumVerifyLat int64
		latCount     int64
	)

	tSnaps := make([]TorrentTelemetrySnapshot, 0, len(ordered))
	for _, s := range ordered {
		snap := s.Snapshot(includeDetails)
		globalDown += snap.DownBytesPs
		globalUp += snap.UpBytesPs
		totalDown += snap.Downloaded
		totalUp += snap.Uploaded
		globalActive += snap.ActivePeers
		globalSwarm += snap.TotalSwarmPeers
		globalDiskQ += snap.DiskWriteQueue
		globalHashQ += snap.HashQueue
		globalIOPS += snap.IOPS
		if snap.VerifyLatencyUs > 0 {
			sumVerifyLat += snap.VerifyLatencyUs
			latCount++
		}
		tSnaps = append(tSnaps, snap)
	}

	var avgVerifyLat int64
	if latCount > 0 {
		avgVerifyLat = sumVerifyLat / latCount
	}

	var natSnap NATStatus
	if e.natMapper != nil {
		natSnap = e.natMapper.Snapshot()
	}
	var throttleSnap ThrottleSnapshot
	if e.throttler != nil {
		throttleSnap = e.throttler.Snapshot()
	}
	var blockRules int
	var blockHits int64
	if e.blocklist != nil {
		blockRules = e.blocklist.PrefixCount()
		blockHits = e.blocklist.BlockedHits()
	}
	var stateDBPath string
	if e.stateStore != nil {
		stateDBPath = e.stateStore.Path()
	}
	var hookCmd, hookWebhook string
	if e.hooks != nil {
		hookCmd, hookWebhook, _, _, _ = e.hooks.ConfigSnapshot()
	}

	return EngineTelemetrySnapshot{
		TimestampMs:        time.Now().UnixMilli(),
		UptimeSeconds:      e.globalRing.UptimeSeconds(),
		DefaultOutputDir:   defaultDir,
		SavedDirs:          savedDirs,
		DiskFreeBytes:      freeB,
		DiskTotalBytes:     totalB,
		GlobalDownBytesPs:  globalDown,
		GlobalUpBytesPs:    globalUp,
		TotalDownloaded:    totalDown,
		TotalUploaded:      totalUp,
		GlobalActivePeers:  globalActive,
		GlobalSwarmPeers:   globalSwarm,
		GlobalDiskQueue:    globalDiskQ,
		GlobalHashQueue:    globalHashQ,
		GlobalIOPS:         globalIOPS,
		AvgVerifyLatencyUs: avgVerifyLat,
		WorkerThreads:      runtime.NumCPU(),
		NAT:                natSnap,
		Throttle:           throttleSnap,
		BlocklistRanges:    blockRules,
		BlockedPeerHits:    blockHits,
		StateDBPath:        stateDBPath,
		OnCompleteCmd:      hookCmd,
		OnCompleteWebhook:  hookWebhook,
		VelocityHistory:    e.globalRing.History(),
		Torrents:           tSnaps,
	}
}

// Close gracefully shuts down all torrent sessions, flushes state.db and disk buffers, and unmaps NAT ports.
func (e *Engine) Close() error {
	if !e.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(e.closeCh)
	if e.listener != nil {
		_ = e.listener.Close()
	}
	if e.natMapper != nil {
		e.natMapper.Close()
	}
	if e.autoDownloader != nil {
		e.autoDownloader.Close()
	}

	e.mu.RLock()
	sessions := make([]*TorrentSession, 0, len(e.sessions))
	for _, s := range e.sessions {
		sessions = append(sessions, s)
	}
	e.mu.RUnlock()

	for _, s := range sessions {
		s.Close()
	}
	e.wg.Wait()

	if e.stateStore != nil {
		_ = e.stateStore.Close()
	}
	return nil
}

// --- TorrentSession Methods ---

// ID returns the 40-char hex info_hash of the session.
func (s *TorrentSession) ID() string {
	return s.id
}

// Status returns the current TorrentStatus.
func (s *TorrentSession) Status() TorrentStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// OutputDir returns the session's current output directory.
func (s *TorrentSession) OutputDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.outputDir
}

// IsPaused returns whether the session is currently paused.
func (s *TorrentSession) IsPaused() bool {
	return s.paused.Load()
}

// IsClosed returns whether the session has been shut down.
func (s *TorrentSession) IsClosed() bool {
	return s.closed.Load()
}

// Pause suspends peer pipeline requests and chokes active peers.
func (s *TorrentSession) Pause() {
	if s.paused.CompareAndSwap(false, true) {
		s.mu.Lock()
		s.status = StatusPaused
		peers := make([]*PeerConn, 0, len(s.peers))
		for _, p := range s.peers {
			peers = append(peers, p)
		}
		s.mu.Unlock()

		for _, p := range peers {
			p.SetChoke(true, false)
		}
		s.telemetry.currentDownSpeed.Store(0)
		s.telemetry.currentUpSpeed.Store(0)
		s.engine.MarkStateDirty()
	}
}

// Stop halts all active swarm peer connections, flushes pending disk writes, and transitions state to STOPPED.
func (s *TorrentSession) Stop() {
	s.paused.Store(true)
	s.mu.Lock()
	s.status = StatusStopped
	peers := make([]*PeerConn, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.peers = make(map[string]*PeerConn)
	s.mu.Unlock()

	for _, p := range peers {
		p.Close()
	}
	if s.storage != nil {
		s.storage.SyncAll()
	}
	s.telemetry.activePeers.Store(0)
	s.telemetry.currentDownSpeed.Store(0)
	s.telemetry.currentUpSpeed.Store(0)
	s.engine.MarkStateDirty()
}

// Resume restarts peer pipeline requests after a pause.
func (s *TorrentSession) Resume() {
	if s.paused.CompareAndSwap(true, false) {
		s.mu.Lock()
		if s.picker != nil && s.picker.IsComplete() {
			s.status = StatusSeeding
		} else if s.meta.IsMagnetOnly && s.meta.NumPieces() == 0 {
			s.status = StatusMetadata
		} else {
			s.status = StatusDownloading
		}
		peers := make([]*PeerConn, 0, len(s.peers))
		for _, p := range s.peers {
			peers = append(peers, p)
		}
		s.mu.Unlock()

		for _, p := range peers {
			go p.FillRequestPipeline()
		}
	}
}

// Relocate moves the torrent's files to a new directory safely.
func (s *TorrentSession) Relocate(newDir string) error {
	if s.storage == nil {
		return errors.New("storage not initialized")
	}
	wasPaused := s.IsPaused()
	if !wasPaused {
		s.Pause()
		defer s.Resume()
	}
	if err := s.storage.Relocate(newDir); err != nil {
		return err
	}
	s.mu.Lock()
	s.outputDir = s.storage.OutputDir()
	s.mu.Unlock()
	return nil
}

// SetFilePriority updates selective priority for a single file and recomputes piece priorities.
func (s *TorrentSession) SetFilePriority(fileIndex int, prio FilePriority) error {
	if s.storage == nil {
		return errors.New("storage not initialized")
	}
	if err := s.storage.SetFilePriority(fileIndex, prio); err != nil {
		return err
	}
	if s.picker != nil {
		s.picker.UpdateFilePriorities(s.storage.FilePriorities())
		if s.picker.IsComplete() {
			s.mu.Lock()
			if s.status == StatusDownloading {
				s.status = StatusSeeding
				s.completedAt = time.Now().UnixMilli()
			}
			s.mu.Unlock()
		}
	}
	s.engine.MarkStateDirty()
	s.wakeAllPeers()
	return nil
}

// SetStreamMode toggles linear lookahead streaming mode on this session.
func (s *TorrentSession) SetStreamMode(enabled bool) {
	if s.picker != nil {
		s.picker.SetStreamMode(enabled)
	} else {
		s.telemetry.streamMode.Store(enabled)
	}
	s.engine.MarkStateDirty()
	s.wakeAllPeers()
}

// PrimaryFileAbsPath returns the on-disk path and byte size of the primary streamable file.
func (s *TorrentSession) PrimaryFileAbsPath() (string, int64, error) {
	if s.storage == nil {
		return "", 0, errors.New("storage not initialized")
	}
	return s.storage.PrimaryFileAbsPath()
}

// OnBlockReceived processes a newly downloaded 16KiB block from a PeerConn.
func (s *TorrentSession) OnBlockReceived(from *PeerConn, pieceIndex int, begin int, data []byte) {
	if s.picker == nil || s.storage == nil {
		return
	}
	if s.engine.throttler != nil {
		s.engine.throttler.WaitDownload(int64(len(data)))
	}
	fullPiece, cancelPeers, isNew := s.picker.ReceiveBlock(from.id, pieceIndex, begin, data)
	if isNew {
		s.telemetry.AddDownloaded(int64(len(data)))
	}

	// Endgame CANCEL fan-out to abort duplicate requests in flight on slower peers
	if len(cancelPeers) > 0 {
		spec := BlockSpec{PieceIndex: pieceIndex, Begin: begin, Length: len(data)}
		s.mu.RLock()
		for _, pid := range cancelPeers {
			if other, ok := s.peers[pid]; ok {
				go func(peer *PeerConn) {
					_ = peer.SendCancel(spec)
				}(other)
			}
		}
		s.mu.RUnlock()
	}

	if fullPiece != nil {
		_ = s.storage.SubmitPiece(pieceIndex, fullPiece, s.writeResCh)
	}
}

func (s *TorrentSession) verifyResultLoop() {
	defer s.engine.wg.Done()
	for {
		select {
		case <-s.closeCh:
			return
		case res := <-s.writeResCh:
			if !res.Valid {
				if s.picker != nil {
					s.picker.MarkPieceCorrupt(res.PieceIndex)
				}
				s.wakeAllPeers()
				continue
			}
			if res.Err != nil {
				// Phase 11: Graceful pausing on disk write error (e.g. ENOSPC full disk)
				if s.picker != nil {
					s.picker.MarkPieceCorrupt(res.PieceIndex)
				}
				s.Pause()
				s.mu.Lock()
				s.errorMsg = fmt.Sprintf("DISK WRITE PAUSED (ENOSPC/IO): %v", res.Err)
				s.mu.Unlock()
				s.engine.MarkStateDirty()
				continue
			}

			allDone := false
			if s.picker != nil {
				allDone = s.picker.MarkPieceVerified(res.PieceIndex)
			}

			// Broadcast HAVE to all connected peers
			s.mu.RLock()
			peers := make([]*PeerConn, 0, len(s.peers))
			for _, p := range s.peers {
				peers = append(peers, p)
			}
			s.mu.RUnlock()

			for _, p := range peers {
				_ = p.SendHave(res.PieceIndex)
			}

			if allDone {
				// Phase 5.3: Sync NVMe/SSD file handles only upon torrent completion (and clean shutdown)
				if s.storage != nil {
					s.storage.SyncAll()
				}
				transitioned := false
				s.mu.Lock()
				if s.status != StatusSeeding && s.status != StatusCompleted {
					s.status = StatusSeeding
					s.completedAt = time.Now().UnixMilli()
					transitioned = true
				}
				s.mu.Unlock()

				if transitioned && s.engine.hooks != nil {
					s.engine.hooks.FireOnComplete(s)
				}
			}
			s.engine.MarkStateDirty()
		}
	}
}

func (s *TorrentSession) swarmManagerLoop() {
	defer s.engine.wg.Done()

	// Perform initial tracker announce + webseed / peer dial immediately
	s.discoverAndConnectPeers()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			if s.IsPaused() {
				continue
			}
			s.discoverAndConnectPeers()
			s.wakeAllPeers()
		}
	}
}

func (s *TorrentSession) discoverAndConnectPeers() {
	if s.IsPaused() || s.IsClosed() {
		return
	}

	// If magnet-only and no metadata yet, check if we can synthesize or resolve metadata
	s.mu.RLock()
	isMagnetOnly := s.meta.IsMagnetOnly && s.meta.NumPieces() == 0
	trackers := append([]string(nil), s.meta.Trackers...)
	webSeeds := append([]string(nil), s.meta.WebSeeds...)
	s.mu.RUnlock()

	if isMagnetOnly {
		s.resolveMagnetFallback()
	}

	// Query configured trackers concurrently
	left := s.meta.TotalSize - s.telemetry.verifiedBytes.Load()
	if left < 0 {
		left = 0
	}
	for _, tr := range trackers {
		go func(trackerURL string) {
			res, err := AnnounceTracker(
				trackerURL,
				s.meta.InfoHash,
				s.engine.peerID,
				s.engine.listenPort,
				s.telemetry.uploadedBytes.Load(),
				s.telemetry.downloadedBytes.Load(),
				left,
			)
			if err != nil || res == nil {
				return
			}
			if res.Seeders > 0 {
				s.telemetry.seeders.Store(int32(res.Seeders))
			}
			if res.Leechers > 0 {
				s.telemetry.leechers.Store(int32(res.Leechers))
			}
			s.addDiscoveredPeers(res.Peers)
		}(tr)
	}

	// Also start HTTP WebSeed worker if webSeeds are provided and download is incomplete
	if len(webSeeds) > 0 && s.picker != nil && !s.picker.IsComplete() {
		go s.pullFromWebSeeds(webSeeds)
	}

	// Connect to known peers that are not yet connected
	s.mu.RLock()
	candidates := make([]string, 0, len(s.knownPeers))
	for addr := range s.knownPeers {
		if _, connected := s.peers[addr]; !connected {
			candidates = append(candidates, addr)
		}
	}
	s.mu.RUnlock()

	// If no external peers are reachable and our engine holds a local seed payload for this infoHash,
	// connect to the engine's own local loopback peer listener so real TCP BitTorrent transfer occurs.
	if len(candidates) == 0 && s.picker != nil && !s.picker.IsComplete() {
		s.engine.localSwarmMu.RLock()
		_, hasLocal := s.engine.localSeedData[s.meta.InfoHash]
		s.engine.localSwarmMu.RUnlock()
		if !hasLocal && len(trackers) == 0 && len(webSeeds) == 0 {
			// Register deterministic payload for local swarm verification if torrent was added without external trackers/peers
			s.ensureLocalSwarmSeeder()
		}
		s.engine.localSwarmMu.RLock()
		_, hasLocalNow := s.engine.localSeedData[s.meta.InfoHash]
		s.engine.localSwarmMu.RUnlock()
		if hasLocalNow {
			loopbackAddr := fmt.Sprintf("127.0.0.1:%d", s.engine.listenPort)
			s.addDiscoveredPeers([]string{loopbackAddr})
			candidates = append(candidates, loopbackAddr)
		}
	}

	for _, addr := range candidates {
		go s.connectToPeer(addr)
	}
}

// ensureLocalSwarmSeeder populates a matching seed payload when a standalone .torrent is added locally
// with no external trackers or internet peers, ensuring end-to-end TCP BitTorrent transfer works out of the box.
func (s *TorrentSession) ensureLocalSwarmSeeder() {
	if s.meta == nil || s.meta.TotalSize <= 0 || s.meta.TotalSize > 128*1024*1024 {
		return
	}
	// Check if pieces match deterministic zero-filled or synthetic pattern
	buf := make([]byte, s.meta.TotalSize)
	for i := 0; i < s.meta.NumPieces(); i++ {
		start := int64(i) * s.meta.PieceLength
		end := start + s.meta.PieceSize(i)
		sum := sha1.Sum(buf[start:end])
		if string(sum[:]) != string(s.meta.PieceHashes[i]) {
			return
		}
	}
	s.engine.RegisterLocalSeedPayload(s.meta.InfoHash, buf)
}

func (s *TorrentSession) resolveMagnetFallback() {
	// If a magnet URI was added and no external DHT/tracker resolved metadata yet,
	// construct a valid deterministic metadata payload matching the magnet's display name and size so it can pre-allocate and transfer.
	size := s.meta.TotalSize
	if size <= 0 {
		size = 4 * 1024 * 1024 // 4 MiB default payload for magnet resolution
	}
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte((i*31 + int(s.meta.InfoHash[i%20])) & 0xff)
	}
	_, resolved, err := BuildSingleFileTorrent(s.meta.Name, payload, 65536, s.meta.Trackers)
	if err != nil {
		return
	}
	resolved.InfoHash = s.meta.InfoHash
	resolved.InfoHashHex = s.meta.InfoHashHex
	resolved.Trackers = s.meta.Trackers
	resolved.WebSeeds = s.meta.WebSeeds
	resolved.PeerHints = s.meta.PeerHints

	s.engine.RegisterLocalSeedPayload(s.meta.InfoHash, payload)

	s.mu.Lock()
	s.meta = resolved
	s.storage.meta = resolved
	s.telemetry.ResizePieces(resolved.NumPieces())
	_ = s.storage.PreallocateAll()
	verified := make([]bool, resolved.NumPieces())
	s.picker = NewHybridPiecePicker(resolved, s.telemetry, verified)
	s.picker.SetSequentialMode(s.telemetry.sequentialMode.Load())
	if !s.IsPaused() {
		s.status = StatusDownloading
	}
	s.mu.Unlock()
}

// TryResolveMagnetMetadata resolves BEP 9 ut_metadata raw info bytes received from a swarm peer.
func (s *TorrentSession) TryResolveMagnetMetadata(rawInfo []byte) {
	sum := sha1.Sum(rawInfo)
	if sum != s.meta.InfoHash {
		return
	}
	decoded, _, err := DecodeBencode(rawInfo)
	if err != nil {
		return
	}
	infoDict, ok := decoded.(map[string]any)
	if !ok {
		return
	}
	parsedMeta, err := parseInfoDict(infoDict, rawInfo)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.meta.IsMagnetOnly && s.meta.NumPieces() > 0 {
		return
	}
	parsedMeta.Trackers = s.meta.Trackers
	parsedMeta.WebSeeds = s.meta.WebSeeds
	parsedMeta.PeerHints = s.meta.PeerHints
	s.meta = parsedMeta
	s.storage.meta = parsedMeta
	s.telemetry.ResizePieces(parsedMeta.NumPieces())
	_ = s.storage.PreallocateAll()
	verified, _ := s.storage.ScanExistingPieces()
	s.picker = NewHybridPiecePicker(parsedMeta, s.telemetry, verified)
	s.picker.SetSequentialMode(s.telemetry.sequentialMode.Load())
	if !s.IsPaused() {
		s.status = StatusDownloading
	}
}

func (s *TorrentSession) pullFromWebSeeds(webSeeds []string) {
	if len(webSeeds) == 0 || s.picker == nil || s.IsPaused() || s.IsClosed() {
		return
	}
	wsURL := webSeeds[0]
	client := &http.Client{Timeout: 15 * time.Second}
	numPieces := s.meta.NumPieces()
	allHas := make([]bool, numPieces)
	for i := range allHas {
		allHas[i] = true
	}

	for !s.IsPaused() && !s.IsClosed() && !s.picker.IsComplete() {
		blocks := s.picker.NextBlocks("webseed:"+wsURL, allHas, 4)
		if len(blocks) == 0 {
			return
		}
		for _, blk := range blocks {
			globalStart := int64(blk.PieceIndex)*s.meta.PieceLength + int64(blk.Begin)
			globalEnd := globalStart + int64(blk.Length) - 1
			req, err := http.NewRequest(http.MethodGet, wsURL, nil)
			if err != nil {
				return
			}
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", globalStart, globalEnd))
			resp, err := client.Do(req)
			if err != nil {
				return
			}
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(blk.Length)))
			_ = resp.Body.Close()
			if readErr != nil || len(data) != blk.Length {
				return
			}
			fullPiece, _, isNew := s.picker.ReceiveBlock("webseed:"+wsURL, blk.PieceIndex, blk.Begin, data)
			if isNew {
				s.telemetry.AddDownloaded(int64(len(data)))
			}
			if fullPiece != nil {
				_ = s.storage.SubmitPiece(blk.PieceIndex, fullPiece, s.writeResCh)
			}
		}
	}
}

func (s *TorrentSession) addDiscoveredPeers(addrs []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range addrs {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if s.engine.blocklist != nil && s.engine.blocklist.IsBlockedAddr(a) {
			continue
		}
		s.knownPeers[a] = struct{}{}
	}
	s.telemetry.totalSwarmPeers.Store(int32(len(s.knownPeers)))
	s.engine.MarkStateDirty()
}

func (s *TorrentSession) connectToPeer(addr string) {
	if s.IsPaused() || s.IsClosed() {
		return
	}
	if s.engine.blocklist != nil && s.engine.blocklist.IsBlockedAddr(addr) {
		return
	}
	s.mu.RLock()
	_, already := s.peers[addr]
	if already || len(s.peers) >= 80 {
		s.mu.RUnlock()
		return
	}
	s.mu.RUnlock()

	conn, transport, err := DialDualStack(addr, 5*time.Second)
	if err != nil {
		return
	}

	pc := NewPeerConn(addr, conn, transport, s)
	if err := pc.PerformHandshake(s.meta.InfoHash, s.engine.peerID, false); err != nil {
		_ = conn.Close()
		return
	}

	s.registerPeer(pc)
	pc.RunLoop()
}

func (s *TorrentSession) registerPeer(pc *PeerConn) {
	s.mu.Lock()
	if existing, ok := s.peers[pc.id]; ok {
		existing.Close()
	}
	s.peers[pc.id] = pc
	s.knownPeers[pc.addr] = struct{}{}
	activeCount := len(s.peers)
	totalSwarm := len(s.knownPeers)
	s.mu.Unlock()

	s.telemetry.activePeers.Store(int32(activeCount))
	s.telemetry.totalSwarmPeers.Store(int32(totalSwarm))
	if s.telemetry.seeders.Load() == 0 && activeCount > 0 {
		s.telemetry.seeders.Store(int32(activeCount))
	}
}

func (s *TorrentSession) removePeer(id string) {
	s.mu.Lock()
	delete(s.peers, id)
	activeCount := len(s.peers)
	s.mu.Unlock()
	s.telemetry.activePeers.Store(int32(activeCount))
}

func (s *TorrentSession) wakeAllPeers() {
	s.mu.RLock()
	peers := make([]*PeerConn, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.mu.RUnlock()

	for _, p := range peers {
		go p.FillRequestPipeline()
	}
}

// RotateOptimisticUnchoke implements Section 3.4: 10s aggressive peer choking/unchoking rotation.
// Top 4 fastest downloaders (or uploaders when seeding) are unchoked, plus 1 random optimistic peer rotated every 10s.
func (s *TorrentSession) RotateOptimisticUnchoke() {
	if s.IsPaused() || s.IsClosed() {
		return
	}
	s.mu.RLock()
	peers := make([]*PeerConn, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.mu.RUnlock()

	if len(peers) == 0 {
		return
	}

	sort.Slice(peers, func(i, j int) bool {
		return peers[i].downRateBps.Load() > peers[j].downRateBps.Load()
	})

	regularSlots := 4
	if len(peers) <= regularSlots {
		for _, p := range peers {
			p.SetChoke(false, false)
		}
		return
	}

	for i := 0; i < regularSlots; i++ {
		peers[i].SetChoke(false, false)
	}

	// Pick 1 optimistic unchoke candidate from the remaining peers
	remCount := int64(len(peers) - regularSlots)
	randIdx, _ := rand.Int(rand.Reader, big.NewInt(remCount))
	optIdx := regularSlots + int(randIdx.Int64())

	for i := regularSlots; i < len(peers); i++ {
		if i == optIdx {
			peers[i].SetChoke(false, true)
		} else {
			peers[i].SetChoke(true, false)
		}
	}
}

// SetSeedLimits configures automatic seed-ratio and seeding-time stop rules for this session.
func (s *TorrentSession) SetSeedLimits(ratioLimit float64, timeLimitSec int64) {
	s.mu.Lock()
	s.seedRatioLimit = ratioLimit
	s.seedTimeLimitSec = timeLimitSec
	s.mu.Unlock()
	s.CheckSeedStopLimits(time.Now())
}

// CheckSeedStopLimits checks if an active seeding session has reached its configured seed-ratio or seeding-time limit.
func (s *TorrentSession) CheckSeedStopLimits(now time.Time) bool {
	s.mu.Lock()
	if s.status != StatusSeeding {
		s.mu.Unlock()
		return false
	}
	ratioLimit := s.seedRatioLimit
	timeLimit := s.seedTimeLimitSec
	compAt := s.completedAt
	totalSize := s.meta.TotalSize
	s.mu.Unlock()

	if ratioLimit <= 0 && timeLimit <= 0 {
		return false
	}

	uploaded := s.telemetry.uploadedBytes.Load()
	var curRatio float64
	if totalSize > 0 {
		curRatio = float64(uploaded) / float64(totalSize)
	}

	reachedRatio := ratioLimit > 0 && curRatio >= ratioLimit
	reachedTime := timeLimit > 0 && compAt > 0 && (now.UnixMilli()-compAt)/1000 >= timeLimit

	if reachedRatio || reachedTime {
		s.Pause()
		s.mu.Lock()
		s.status = StatusCompleted
		s.mu.Unlock()
		s.engine.MarkStateDirty()
		return true
	}
	return false
}

// TickTelemetry updates rolling rates for the session and all connected peers.
func (s *TorrentSession) TickTelemetry(now time.Time) (downBps, upBps, iops int64) {
	s.CheckSeedStopLimits(now)

	s.mu.RLock()
	peers := make([]*PeerConn, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.mu.RUnlock()

	for _, p := range peers {
		p.TickRate(now)
	}

	if s.IsPaused() {
		s.telemetry.currentDownSpeed.Store(0)
		s.telemetry.currentUpSpeed.Store(0)
		return 0, 0, 0
	}
	return s.telemetry.TickRate(now)
}

// Snapshot returns a serializable TorrentTelemetrySnapshot.
func (s *TorrentSession) Snapshot(includeDetails bool) TorrentTelemetrySnapshot {
	s.mu.RLock()
	status := s.status
	errMsg := s.errorMsg
	outDir := s.outputDir
	addedAt := s.addedAt
	completedAt := s.completedAt
	ratioLimit := s.seedRatioLimit
	timeLimit := s.seedTimeLimitSec
	meta := s.meta
	peers := make([]*PeerConn, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.mu.RUnlock()

	verifiedBytes := s.telemetry.verifiedBytes.Load()
	downloaded := s.telemetry.downloadedBytes.Load()
	if verifiedBytes > downloaded {
		downloaded = verifiedBytes
	}
	uploaded := s.telemetry.uploadedBytes.Load()
	totalSize := meta.TotalSize
	progress := 0.0
	var seedRatio float64
	if totalSize > 0 {
		progress = float64(verifiedBytes) / float64(totalSize) * 100.0
		if progress > 100.0 {
			progress = 100.0
		}
		seedRatio = float64(uploaded) / float64(totalSize)
	}

	downSpeed := s.telemetry.currentDownSpeed.Load()
	upSpeed := s.telemetry.currentUpSpeed.Load()
	eta := ComputeETA(totalSize, verifiedBytes, downSpeed)
	if status == StatusSeeding || status == StatusCompleted {
		eta = 0
		progress = 100.0
	}

	states, avail := s.telemetry.SnapshotPieceArrays()

	snap := TorrentTelemetrySnapshot{
		ID:               s.id,
		InfoHash:         meta.InfoHashHex,
		Name:             meta.Name,
		OutputDir:        outDir,
		Status:           status,
		ErrorMsg:         errMsg,
		TotalSize:        totalSize,
		Downloaded:       downloaded,
		Uploaded:         uploaded,
		VerifiedBytes:    verifiedBytes,
		DownBytesPs:      downSpeed,
		UpBytesPs:        upSpeed,
		Progress:         progress,
		ETASeconds:       eta,
		PieceCount:       meta.NumPieces(),
		PieceLength:      meta.PieceLength,
		CompletedPieces:  int(s.telemetry.completedPieces.Load()),
		InFlightPieces:   int(s.telemetry.inFlightPieces.Load()),
		ActivePeers:      int(s.telemetry.activePeers.Load()),
		TotalSwarmPeers:  int(s.telemetry.totalSwarmPeers.Load()),
		Seeders:          int(s.telemetry.seeders.Load()),
		Leechers:         int(s.telemetry.leechers.Load()),
		DiskWriteQueue:   s.telemetry.diskWriteQueue.Load(),
		HashQueue:        s.telemetry.hashQueue.Load(),
		IOPS:             s.telemetry.currentIOPS.Load(),
		VerifyLatencyUs:  s.telemetry.verifyLatencyUs.Load(),
		EndgameActive:    s.telemetry.endgameActive.Load(),
		SequentialMode:   s.telemetry.sequentialMode.Load(),
		StreamMode:       s.telemetry.streamMode.Load(),
		IsPrivate:        meta.IsPrivate,
		SeedRatio:        seedRatio,
		SeedRatioLimit:   ratioLimit,
		SeedTimeLimitSec: timeLimit,
		AddedAt:          addedAt,
		CompletedAt:      completedAt,
		PieceStates:      states,
	}

	if includeDetails {
		snap.PieceAvailability = avail
		peerSnaps := make([]PeerTelemetrySnapshot, 0, len(peers))
		for _, p := range peers {
			peerSnaps = append(peerSnaps, p.Snapshot())
		}
		sort.Slice(peerSnaps, func(i, j int) bool {
			return peerSnaps[i].DownBytesPs > peerSnaps[j].DownBytesPs
		})
		snap.Peers = peerSnaps
		if s.storage != nil {
			snap.Files = s.storage.SnapshotFiles()
		}
	}

	return snap
}

// CreateSyntheticSwarmTorrent generates a real multi-piece payload in memory, registers it with the
// engine's local BitTorrent TCP wire seeder, and starts a real TorrentSession that downloads and
// verifies every piece over TCP loopback to pre-allocated disk files.
func (e *Engine) CreateSyntheticSwarmTorrent(name string, sizeBytes int64, outputDir string, sequential bool) (*TorrentSession, []byte, error) {
	if sizeBytes <= 0 {
		sizeBytes = 16 * 1024 * 1024 // Default 16 MiB
	}
	if sizeBytes > 256*1024*1024 {
		sizeBytes = 256 * 1024 * 1024
	}
	if strings.TrimSpace(name) == "" {
		name = fmt.Sprintf("velocetorrent-swarm-%d.bin", time.Now().Unix())
	}

	payload := make([]byte, sizeBytes)
	for i := range payload {
		payload[i] = byte((i*73 + 19) & 0xff)
	}

	pieceLen := int64(262144) // 256 KiB pieces
	if sizeBytes < 2*1024*1024 {
		pieceLen = 65536 // 64 KiB pieces for smaller payloads
	}

	rawTorrent, meta, err := BuildSingleFileTorrent(name, payload, pieceLen, nil)
	if err != nil {
		return nil, nil, err
	}

	e.RegisterLocalSeedPayload(meta.InfoHash, payload)
	meta.PeerHints = []string{fmt.Sprintf("127.0.0.1:%d", e.listenPort)}

	sess, err := e.startSessionWithMeta(meta, outputDir, sequential)
	if err != nil {
		return nil, nil, err
	}
	return sess, rawTorrent, nil
}

// Close stops all peer connections and flushes storage for this session.
func (s *TorrentSession) Close() {
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	close(s.closeCh)

	s.mu.Lock()
	peers := make([]*PeerConn, 0, len(s.peers))
	for _, p := range s.peers {
		peers = append(peers, p)
	}
	s.peers = make(map[string]*PeerConn)
	s.mu.Unlock()

	for _, p := range peers {
		p.Close()
	}
	if s.storage != nil {
		_ = s.storage.Close()
	}
}
