package engine

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// PieceStatus represents the lifecycle state of a single torrent piece for UI heatmaps.
type PieceStatus uint8

const (
	PieceMissing   PieceStatus = 0
	PieceInFlight  PieceStatus = 1
	PieceVerifying PieceStatus = 2
	PieceComplete  PieceStatus = 3
)

// TorrentStatus represents the operational state of a torrent session.
type TorrentStatus string

const (
	StatusMetadata    TorrentStatus = "METADATA"
	StatusPrealloc    TorrentStatus = "PREALLOCATING"
	StatusDownloading TorrentStatus = "DOWNLOADING"
	StatusVerifying   TorrentStatus = "VERIFYING"
	StatusSeeding     TorrentStatus = "SEEDING"
	StatusPaused      TorrentStatus = "PAUSED"
	StatusStopped     TorrentStatus = "STOPPED"
	StatusCompleted   TorrentStatus = "COMPLETED"
	StatusError       TorrentStatus = "ERROR"
)

const (
	// RollingHistoryLength stores 60 seconds of velocity samples (at 250ms resolution = 240 samples, or 60 1s samples).
	// We maintain 120 samples at 500ms / 100ms ticks for high-resolution sparklines.
	RollingHistoryLength = 120
)

// VelocitySample records a point-in-time throughput measurement.
type VelocitySample struct {
	TimestampMs int64 `json:"timestampMs"`
	DownBytesPs int64 `json:"downBytesPs"`
	UpBytesPs   int64 `json:"upBytesPs"`
	IOPS        int64 `json:"iops"`
}

// PeerTelemetrySnapshot represents real-time metrics for an individual connected peer.
type PeerTelemetrySnapshot struct {
	ID             string  `json:"id"`
	Addr           string  `json:"addr"`
	Client         string  `json:"client"`
	Transport      string  `json:"transport"` // "TCP+BBR" or "uTP"
	DownBytesPs    int64   `json:"downBytesPs"`
	UpBytesPs      int64   `json:"upBytesPs"`
	Downloaded     int64   `json:"downloaded"`
	Uploaded       int64   `json:"uploaded"`
	RTTMs          float64 `json:"rttMs"`
	BDPQueueDepth  int     `json:"bdpQueueDepth"`
	InFlightBlocks int     `json:"inFlightBlocks"`
	AmChoking      bool    `json:"amChoking"`
	AmInterested   bool    `json:"amInterested"`
	PeerChoking    bool    `json:"peerChoking"`
	PeerInterested bool    `json:"peerInterested"`
	IsOptimistic   bool    `json:"isOptimistic"`
	Progress       float64 `json:"progress"`
}

// FilePriority represents selective download priority for a single file in a torrent.
type FilePriority string

const (
	PrioritySkip   FilePriority = "skip"
	PriorityNormal FilePriority = "normal"
	PriorityHigh   FilePriority = "high"
)

// FileTelemetrySnapshot represents a file inside the torrent and its allocation/completion status.
type FileTelemetrySnapshot struct {
	Index        int          `json:"index"`
	Path         string       `json:"path"`
	Length       int64        `json:"length"`
	Offset       int64        `json:"offset"`
	BytesOnDisk  int64        `json:"bytesOnDisk"`
	Preallocated bool         `json:"preallocated"`
	Priority     FilePriority `json:"priority"`
	Progress     float64      `json:"progress"`
}

// TorrentTelemetrySnapshot is the wire format sent over IPC/WebSocket every 100ms.
type TorrentTelemetrySnapshot struct {
	ID                string                  `json:"id"`
	InfoHash          string                  `json:"infoHash"`
	Name              string                  `json:"name"`
	OutputDir         string                  `json:"outputDir"`
	Status            TorrentStatus           `json:"status"`
	ErrorMsg          string                  `json:"errorMsg,omitempty"`
	TotalSize         int64                   `json:"totalSize"`
	Downloaded        int64                   `json:"downloaded"`
	Uploaded          int64                   `json:"uploaded"`
	VerifiedBytes     int64                   `json:"verifiedBytes"`
	DownBytesPs       int64                   `json:"downBytesPs"`
	UpBytesPs         int64                   `json:"upBytesPs"`
	Progress          float64                 `json:"progress"`
	ETASeconds        int64                   `json:"etaSeconds"` // -1 means infinite/unknown
	PieceCount        int                     `json:"pieceCount"`
	PieceLength       int64                   `json:"pieceLength"`
	CompletedPieces   int                     `json:"completedPieces"`
	InFlightPieces    int                     `json:"inFlightPieces"`
	ActivePeers       int                     `json:"activePeers"`
	TotalSwarmPeers   int                     `json:"totalSwarmPeers"`
	Seeders           int                     `json:"seeders"`
	Leechers          int                     `json:"leechers"`
	DiskWriteQueue    int64                   `json:"diskWriteQueue"`
	HashQueue         int64                   `json:"hashQueue"`
	IOPS              int64                   `json:"iops"`
	VerifyLatencyUs   int64                   `json:"verifyLatencyUs"`
	EndgameActive     bool                    `json:"endgameActive"`
	SequentialMode    bool                    `json:"sequentialMode"`
	StreamMode        bool                    `json:"streamMode"`
	IsPrivate         bool                    `json:"isPrivate"`
	SeedRatio         float64                 `json:"seedRatio"`
	SeedRatioLimit    float64                 `json:"seedRatioLimit,omitempty"`
	SeedTimeLimitSec  int64                   `json:"seedTimeLimitSec,omitempty"`
	AddedAt           int64                   `json:"addedAt"`
	CompletedAt       int64                   `json:"completedAt,omitempty"`
	PieceStates       []uint8                 `json:"pieceStates"`
	PieceAvailability []uint16                `json:"pieceAvailability,omitempty"`
	Peers             []PeerTelemetrySnapshot `json:"peers,omitempty"`
	Files             []FileTelemetrySnapshot `json:"files,omitempty"`
}

// EngineTelemetrySnapshot aggregates global daemon telemetry and all active torrent snapshots.
type EngineTelemetrySnapshot struct {
	TimestampMs        int64                      `json:"timestampMs"`
	UptimeSeconds      int64                      `json:"uptimeSeconds"`
	DefaultOutputDir   string                     `json:"defaultOutputDir"`
	SavedDirs          []string                   `json:"savedDirs"`
	DiskFreeBytes      uint64                     `json:"diskFreeBytes"`
	DiskTotalBytes     uint64                     `json:"diskTotalBytes"`
	GlobalDownBytesPs  int64                      `json:"globalDownBytesPs"`
	GlobalUpBytesPs    int64                      `json:"globalUpBytesPs"`
	TotalDownloaded    int64                      `json:"totalDownloaded"`
	TotalUploaded      int64                      `json:"totalUploaded"`
	GlobalActivePeers  int                        `json:"globalActivePeers"`
	GlobalSwarmPeers   int                        `json:"globalSwarmPeers"`
	GlobalDiskQueue    int64                      `json:"globalDiskQueue"`
	GlobalHashQueue    int64                      `json:"globalHashQueue"`
	GlobalIOPS         int64                      `json:"globalIOPS"`
	AvgVerifyLatencyUs int64                      `json:"avgVerifyLatencyUs"`
	WorkerThreads      int                        `json:"workerThreads"`
	NAT                NATStatus                  `json:"nat"`
	Throttle           ThrottleSnapshot           `json:"throttle"`
	BlocklistRanges    int                        `json:"blocklistRanges"`
	BlockedPeerHits    int64                      `json:"blockedPeerHits"`
	StateDBPath        string                     `json:"stateDbPath,omitempty"`
	OnCompleteCmd      string                     `json:"onCompleteCmd,omitempty"`
	OnCompleteWebhook  string                     `json:"onCompleteWebhook,omitempty"`
	VelocityHistory    []VelocitySample           `json:"velocityHistory"`
	Torrents           []TorrentTelemetrySnapshot `json:"torrents"`
}

// AtomicTorrentTelemetry holds lock-free atomic counters for a single torrent.
type AtomicTorrentTelemetry struct {
	downloadedBytes   atomic.Int64
	uploadedBytes     atomic.Int64
	verifiedBytes     atomic.Int64
	lastSampledDown   atomic.Int64
	lastSampledUp     atomic.Int64
	lastSampleTimeNs  atomic.Int64
	currentDownSpeed  atomic.Int64
	currentUpSpeed    atomic.Int64
	activePeers       atomic.Int32
	totalSwarmPeers   atomic.Int32
	seeders           atomic.Int32
	leechers          atomic.Int32
	completedPieces   atomic.Int32
	inFlightPieces    atomic.Int32
	diskWriteQueue    atomic.Int64
	hashQueue         atomic.Int64
	diskOpsTotal      atomic.Int64
	lastSampledOps    atomic.Int64
	currentIOPS       atomic.Int64
	verifyLatencyUs   atomic.Int64
	endgameActive     atomic.Bool
	sequentialMode    atomic.Bool
	streamMode        atomic.Bool

	piecesMu          sync.RWMutex
	pieceStates       []uint8
	pieceAvailability []uint16
}

// NewAtomicTorrentTelemetry initializes lock-free telemetry counters for a torrent with numPieces.
func NewAtomicTorrentTelemetry(numPieces int) *AtomicTorrentTelemetry {
	t := &AtomicTorrentTelemetry{
		pieceStates:       make([]uint8, numPieces),
		pieceAvailability: make([]uint16, numPieces),
	}
	t.lastSampleTimeNs.Store(time.Now().UnixNano())
	return t
}

// ResizePieces updates piece state slices when metadata is resolved (e.g., from a magnet link).
func (t *AtomicTorrentTelemetry) ResizePieces(numPieces int) {
	t.piecesMu.Lock()
	defer t.piecesMu.Unlock()
	if len(t.pieceStates) != numPieces {
		t.pieceStates = make([]uint8, numPieces)
		t.pieceAvailability = make([]uint16, numPieces)
	}
}

// AddDownloaded atomically increments downloaded bytes.
func (t *AtomicTorrentTelemetry) AddDownloaded(n int64) {
	t.downloadedBytes.Add(n)
}

// AddUploaded atomically increments uploaded bytes.
func (t *AtomicTorrentTelemetry) AddUploaded(n int64) {
	t.uploadedBytes.Add(n)
}

// AddVerifiedBytes atomically increments verified on-disk bytes.
func (t *AtomicTorrentTelemetry) AddVerifiedBytes(n int64) {
	t.verifiedBytes.Add(n)
}

// SetVerifiedBytes atomically sets verified on-disk bytes.
func (t *AtomicTorrentTelemetry) SetVerifiedBytes(n int64) {
	t.verifiedBytes.Store(n)
}

// RecordDiskOp increments the lock-free IOPS counter.
func (t *AtomicTorrentTelemetry) RecordDiskOp(ops int64) {
	t.diskOpsTotal.Add(ops)
}

// AddDiskQueue adjusts the current pending disk write queue depth.
func (t *AtomicTorrentTelemetry) AddDiskQueue(delta int64) {
	t.diskWriteQueue.Add(delta)
}

// AddHashQueue adjusts the current pending hash worker queue depth.
func (t *AtomicTorrentTelemetry) AddHashQueue(delta int64) {
	t.hashQueue.Add(delta)
}

// SetPieceStatus updates the status of a specific piece and maintains completed/in-flight counters.
func (t *AtomicTorrentTelemetry) SetPieceStatus(index int, status PieceStatus) {
	t.piecesMu.Lock()
	if index < 0 || index >= len(t.pieceStates) {
		t.piecesMu.Unlock()
		return
	}
	prev := PieceStatus(t.pieceStates[index])
	if prev == status {
		t.piecesMu.Unlock()
		return
	}
	t.pieceStates[index] = uint8(status)
	t.piecesMu.Unlock()

	if prev == PieceComplete && status != PieceComplete {
		t.completedPieces.Add(-1)
	} else if prev != PieceComplete && status == PieceComplete {
		t.completedPieces.Add(1)
	}

	if prev == PieceInFlight && status != PieceInFlight {
		t.inFlightPieces.Add(-1)
	} else if prev != PieceInFlight && status == PieceInFlight {
		t.inFlightPieces.Add(1)
	}
}

// GetPieceStatus returns the current status of a piece.
func (t *AtomicTorrentTelemetry) GetPieceStatus(index int) PieceStatus {
	t.piecesMu.RLock()
	defer t.piecesMu.RUnlock()
	if index < 0 || index >= len(t.pieceStates) {
		return PieceMissing
	}
	return PieceStatus(t.pieceStates[index])
}

// UpdatePieceAvailability sets the swarm availability count for each piece.
func (t *AtomicTorrentTelemetry) UpdatePieceAvailability(avail []uint16) {
	t.piecesMu.Lock()
	defer t.piecesMu.Unlock()
	if len(t.pieceAvailability) != len(avail) {
		t.pieceAvailability = make([]uint16, len(avail))
	}
	copy(t.pieceAvailability, avail)
}

// TickRate computes instantaneous and EWMA-smoothed throughput and IOPS over the elapsed interval.
func (t *AtomicTorrentTelemetry) TickRate(now time.Time) (downBps, upBps, iops int64) {
	nowNs := now.UnixNano()
	prevNs := t.lastSampleTimeNs.Swap(nowNs)
	elapsedSec := float64(nowNs-prevNs) / 1e9
	if elapsedSec <= 0.001 {
		return t.currentDownSpeed.Load(), t.currentUpSpeed.Load(), t.currentIOPS.Load()
	}

	curDown := t.downloadedBytes.Load()
	prevDown := t.lastSampledDown.Swap(curDown)
	instDown := int64(math.Max(0, float64(curDown-prevDown)/elapsedSec))

	curUp := t.uploadedBytes.Load()
	prevUp := t.lastSampledUp.Swap(curUp)
	instUp := int64(math.Max(0, float64(curUp-prevUp)/elapsedSec))

	curOps := t.diskOpsTotal.Load()
	prevOps := t.lastSampledOps.Swap(curOps)
	instOps := int64(math.Max(0, float64(curOps-prevOps)/elapsedSec))

	// Apply fast-tracking EWMA (alpha = 0.45) to prevent sub-second jitter while reacting immediately to bursts.
	oldDown := t.currentDownSpeed.Load()
	var smoothDown int64
	if instDown == 0 && oldDown < 512 {
		smoothDown = 0
	} else if oldDown == 0 {
		smoothDown = instDown
	} else {
		smoothDown = int64(0.45*float64(instDown) + 0.55*float64(oldDown))
	}
	t.currentDownSpeed.Store(smoothDown)

	oldUp := t.currentUpSpeed.Load()
	var smoothUp int64
	if instUp == 0 && oldUp < 512 {
		smoothUp = 0
	} else if oldUp == 0 {
		smoothUp = instUp
	} else {
		smoothUp = int64(0.45*float64(instUp) + 0.55*float64(oldUp))
	}
	t.currentUpSpeed.Store(smoothUp)

	oldOps := t.currentIOPS.Load()
	var smoothOps int64
	if instOps == 0 && oldOps <= 2 {
		smoothOps = 0
	} else {
		smoothOps = int64(0.5*float64(instOps) + 0.5*float64(oldOps))
	}
	t.currentIOPS.Store(smoothOps)

	return smoothDown, smoothUp, smoothOps
}

// SnapshotPieceArrays returns copies of the piece state and availability slices for safe serialization.
func (t *AtomicTorrentTelemetry) SnapshotPieceArrays() (states []uint8, avail []uint16) {
	t.piecesMu.RLock()
	defer t.piecesMu.RUnlock()
	states = make([]uint8, len(t.pieceStates))
	copy(states, t.pieceStates)
	avail = make([]uint16, len(t.pieceAvailability))
	copy(avail, t.pieceAvailability)
	return states, avail
}

// ComputeETA returns the estimated seconds remaining, or -1 if speed is zero.
func ComputeETA(totalSize, verifiedBytes, downBytesPs int64) int64 {
	remaining := totalSize - verifiedBytes
	if remaining <= 0 {
		return 0
	}
	if downBytesPs <= 0 {
		return -1
	}
	eta := remaining / downBytesPs
	if eta > 86400*365 {
		return -1
	}
	return eta
}

// GlobalTelemetryRing maintains the rolling 60-second velocity buffer and global counters.
type GlobalTelemetryRing struct {
	mu        sync.RWMutex
	samples   []VelocitySample
	startedAt time.Time
}

// NewGlobalTelemetryRing creates a new global velocity buffer.
func NewGlobalTelemetryRing() *GlobalTelemetryRing {
	now := time.Now()
	samples := make([]VelocitySample, RollingHistoryLength)
	for i := 0; i < RollingHistoryLength; i++ {
		samples[i] = VelocitySample{
			TimestampMs: now.Add(-time.Duration(RollingHistoryLength-i) * 500 * time.Millisecond).UnixMilli(),
			DownBytesPs: 0,
			UpBytesPs:   0,
			IOPS:        0,
		}
	}
	return &GlobalTelemetryRing{
		samples:   samples,
		startedAt: now,
	}
}

// Push records a new velocity sample into the ring buffer.
func (r *GlobalTelemetryRing) Push(sample VelocitySample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.samples) >= RollingHistoryLength {
		copy(r.samples[0:], r.samples[1:])
		r.samples[RollingHistoryLength-1] = sample
	} else {
		r.samples = append(r.samples, sample)
	}
}

// History returns a snapshot copy of the rolling velocity samples.
func (r *GlobalTelemetryRing) History() []VelocitySample {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]VelocitySample, len(r.samples))
	copy(out, r.samples)
	return out
}

// UptimeSeconds returns seconds elapsed since engine startup.
func (r *GlobalTelemetryRing) UptimeSeconds() int64 {
	return int64(time.Since(r.startedAt).Seconds())
}
