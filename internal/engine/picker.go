package engine

import (
	"math"
	"sort"
	"sync"
	"time"
)

const (
	// StandardBlockSize is the 16 KiB sub-piece request unit defined by BEP 3.
	StandardBlockSize = 16384
	// MinPipelineDepth is the baseline in-flight block request queue depth for new/slow peers.
	MinPipelineDepth = 8
	// MaxPipelineDepth is the upper bound for high-bandwidth BDP pipelining (256 * 16KiB = 4 MiB in flight per peer).
	MaxPipelineDepth = 256
	// StaleRequestTimeout defines when an unfulfilled block request can be reassigned even before endgame.
	StaleRequestTimeout = 8 * time.Second
)

// BlockSpec uniquely identifies a 16 KiB sub-piece block request.
type BlockSpec struct {
	PieceIndex int `json:"pieceIndex"`
	Begin      int `json:"begin"`
	Length     int `json:"length"`
}

type blockState struct {
	spec        BlockSpec
	downloaded  bool
	requestedAt time.Time
	requesters  map[string]struct{} // peerIDs currently holding an active request for this block
}

type pieceTracker struct {
	index          int
	size           int64
	priority       FilePriority
	blocks         []blockState
	completedCount int
	inFlightCount  int
	verified       bool
	verifying      bool
	buffer         []byte
}

// ComputeAdaptiveBDPDepth calculates the optimal request pipeline depth for a peer based on its
// measured Bandwidth-Delay Product (BDP = rateBytesPerSec * rttSeconds).
func ComputeAdaptiveBDPDepth(rateBytesPerSec int64, rttMs float64) int {
	if rateBytesPerSec <= 0 {
		return MinPipelineDepth
	}
	if rttMs <= 1.0 {
		rttMs = 15.0 // Default local/LAN RTT baseline
	}
	rttSec := rttMs / 1000.0
	// Multiply BDP by 2.5x headroom to keep TCP/uTP socket buffers saturated across jitter
	bdpBytes := float64(rateBytesPerSec) * rttSec * 2.5
	blocks := int(math.Ceil(bdpBytes / float64(StandardBlockSize)))
	if blocks < MinPipelineDepth {
		return MinPipelineDepth
	}
	if blocks > MaxPipelineDepth {
		return MaxPipelineDepth
	}
	return blocks
}

// HybridPiecePicker implements Rarest-First + Sequential Hybrid piece selection,
// adaptive BDP request pipelining, and automatic Endgame duplicate request + cancel fan-out.
type HybridPiecePicker struct {
	mu               sync.Mutex
	meta             *TorrentMeta
	telemetry        *AtomicTorrentTelemetry
	pieces           []*pieceTracker
	availability     []uint16
	completedCount   int
	endgameThreshold int
	endgameActive    bool
	sequentialMode   bool
	streamMode       bool
}

// NewHybridPiecePicker creates a piece picker initialized with torrent metadata and existing verified state.
func NewHybridPiecePicker(meta *TorrentMeta, telemetry *AtomicTorrentTelemetry, verified []bool) *HybridPiecePicker {
	numPieces := meta.NumPieces()
	pieces := make([]*pieceTracker, numPieces)
	completed := 0

	for i := 0; i < numPieces; i++ {
		pSize := meta.PieceSize(i)
		numBlocks := int((pSize + StandardBlockSize - 1) / StandardBlockSize)
		blocks := make([]blockState, numBlocks)
		for b := 0; b < numBlocks; b++ {
			begin := b * StandardBlockSize
			length := StandardBlockSize
			if int64(begin+length) > pSize {
				length = int(pSize) - begin
			}
			blocks[b] = blockState{
				spec:       BlockSpec{PieceIndex: i, Begin: begin, Length: length},
				requesters: make(map[string]struct{}),
			}
		}

		isDone := i < len(verified) && verified[i]
		if isDone {
			completed++
			for b := range blocks {
				blocks[b].downloaded = true
			}
		}

		pieces[i] = &pieceTracker{
			index:          i,
			size:           pSize,
			priority:       PriorityNormal,
			blocks:         blocks,
			completedCount: func() int { if isDone { return numBlocks }; return 0 }(),
			verified:       isDone,
		}
	}

	threshold := numPieces / 20
	if threshold < 2 {
		threshold = 2
	}
	if threshold > 16 {
		threshold = 16
	}

	p := &HybridPiecePicker{
		meta:             meta,
		telemetry:        telemetry,
		pieces:           pieces,
		availability:     make([]uint16, numPieces),
		completedCount:   completed,
		endgameThreshold: threshold,
	}
	p.checkEndgameLocked()
	return p
}

// SetSequentialMode toggles sequential piece prioritization vs rarest-first hybrid mode.
func (p *HybridPiecePicker) SetSequentialMode(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sequentialMode = enabled
	p.telemetry.sequentialMode.Store(enabled)
}

// SetStreamMode toggles linear streaming mode (0 -> N with a 16-piece sliding lookahead window for mpv/vlc).
func (p *HybridPiecePicker) SetStreamMode(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.streamMode = enabled
	if enabled {
		p.sequentialMode = true
		p.telemetry.sequentialMode.Store(true)
	}
	p.telemetry.streamMode.Store(enabled)
}

// UpdateFilePriorities recomputes per-piece priorities (skip, normal, high) from selective file priorities.
func (p *HybridPiecePicker) UpdateFilePriorities(filePriorities []FilePriority) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.meta == nil || len(p.pieces) == 0 || len(filePriorities) == 0 {
		return
	}
	pieceLen := p.meta.PieceLength
	if pieceLen <= 0 {
		return
	}

	for _, pt := range p.pieces {
		pStart := int64(pt.index) * pieceLen
		pEnd := pStart + pt.size

		hasHigh := false
		hasNormal := false
		for fIdx, fEntry := range p.meta.Files {
			if fEntry.Length <= 0 {
				continue
			}
			fStart := fEntry.Offset
			fEnd := fStart + fEntry.Length
			if pEnd <= fStart || pStart >= fEnd {
				continue
			}
			prio := PriorityNormal
			if fIdx < len(filePriorities) && filePriorities[fIdx] != "" {
				prio = filePriorities[fIdx]
			}
			switch prio {
			case PriorityHigh:
				hasHigh = true
			case PriorityNormal:
				hasNormal = true
			}
		}

		switch {
		case hasHigh:
			pt.priority = PriorityHigh
		case hasNormal:
			pt.priority = PriorityNormal
		default:
			pt.priority = PrioritySkip
		}
	}
	p.checkEndgameLocked()
}

// ApplyPeerBitfield increments swarm availability counts for all pieces the peer possesses.
func (p *HybridPiecePicker) ApplyPeerBitfield(bitfield []bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	limit := len(p.availability)
	if len(bitfield) < limit {
		limit = len(bitfield)
	}
	for i := 0; i < limit; i++ {
		if bitfield[i] {
			p.availability[i]++
		}
	}
	p.telemetry.UpdatePieceAvailability(p.availability)
}

// RemovePeerBitfield decrements swarm availability and releases any in-flight requests held by a disconnected peer.
func (p *HybridPiecePicker) RemovePeerBitfield(peerID string, bitfield []bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	limit := len(p.availability)
	if len(bitfield) < limit {
		limit = len(bitfield)
	}
	for i := 0; i < limit; i++ {
		if bitfield[i] && p.availability[i] > 0 {
			p.availability[i]--
		}
	}
	for _, pt := range p.pieces {
		if pt.verified || pt.verifying {
			continue
		}
		for bIdx := range pt.blocks {
			blk := &pt.blocks[bIdx]
			if _, had := blk.requesters[peerID]; had {
				delete(blk.requesters, peerID)
				if len(blk.requesters) == 0 && !blk.downloaded {
					pt.inFlightCount--
				}
			}
		}
	}
	p.telemetry.UpdatePieceAvailability(p.availability)
}

// RecordPeerHave increments availability for a single piece index announced via HAVE.
func (p *HybridPiecePicker) RecordPeerHave(pieceIndex int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pieceIndex >= 0 && pieceIndex < len(p.availability) {
		p.availability[pieceIndex]++
		p.telemetry.UpdatePieceAvailability(p.availability)
	}
}

// NextBlocks selects up to maxNeeded 16KiB blocks for a peer based on Rarest-First + Sequential Hybrid priority.
func (p *HybridPiecePicker) NextBlocks(peerID string, peerHas []bool, maxNeeded int) []BlockSpec {
	if maxNeeded <= 0 {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isAllWantedCompleteLocked() {
		return nil
	}

	now := time.Now()
	selected := make([]BlockSpec, 0, maxNeeded)

	// Determine streaming playhead (lowest unverified non-skipped piece index) for 16-piece lookahead window
	playhead := -1
	for i, pt := range p.pieces {
		if !pt.verified && pt.priority != PrioritySkip {
			playhead = i
			break
		}
	}

	// Build candidate list of unverified non-skipped pieces the peer possesses
	candidates := make([]*pieceTracker, 0, len(p.pieces))
	for i, pt := range p.pieces {
		if pt.verified || pt.verifying || pt.priority == PrioritySkip {
			continue
		}
		if len(peerHas) > i && !peerHas[i] {
			continue
		}
		candidates = append(candidates, pt)
	}

	if len(candidates) == 0 {
		return nil
	}

	// Sort candidates:
	// 1. Streaming 16-piece lookahead window (when streamMode is active)
	// 2. Partially started pieces first (to complete and flush pieces rapidly, minimizing RAM footprint)
	// 3. Selective High Priority files (PriorityHigh > PriorityNormal)
	// 4. Boundary pieces (first piece and last piece for instant container moov/Cues header parsing)
	// 5. Sequential index order (if sequentialMode/streamMode) OR Rarest-First availability order
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]

		if p.streamMode && playhead >= 0 {
			aInWindow := a.index >= playhead && a.index < playhead+16
			bInWindow := b.index >= playhead && b.index < playhead+16
			if aInWindow != bInWindow {
				return aInWindow
			}
			if aInWindow && bInWindow {
				return a.index < b.index
			}
		}

		aStarted := a.completedCount > 0 || a.inFlightCount > 0
		bStarted := b.completedCount > 0 || b.inFlightCount > 0
		if aStarted != bStarted {
			return aStarted
		}

		aHigh := a.priority == PriorityHigh
		bHigh := b.priority == PriorityHigh
		if aHigh != bHigh {
			return aHigh
		}

		if p.sequentialMode || p.streamMode {
			return a.index < b.index
		}

		// Boost first and last piece for instant container header parsing
		aBoundary := a.index == 0 || a.index == len(p.pieces)-1
		bBoundary := b.index == 0 || b.index == len(p.pieces)-1
		if aBoundary != bBoundary {
			return aBoundary
		}

		availA := p.availability[a.index]
		availB := p.availability[b.index]
		if availA != availB {
			return availA < availB
		}
		return a.index < b.index
	})

	// Pass 1: Assign unrequested (or stale timed-out) blocks from sorted candidates
	for _, pt := range candidates {
		for bIdx := range pt.blocks {
			blk := &pt.blocks[bIdx]
			if blk.downloaded {
				continue
			}
			_, alreadyByPeer := blk.requesters[peerID]
			if alreadyByPeer {
				continue
			}

			isStale := len(blk.requesters) > 0 && now.Sub(blk.requestedAt) > StaleRequestTimeout
			if len(blk.requesters) == 0 || isStale {
				if len(blk.requesters) == 0 {
					pt.inFlightCount++
				}
				blk.requesters[peerID] = struct{}{}
				blk.requestedAt = now
				selected = append(selected, blk.spec)
				if pt.completedCount == 0 && pt.inFlightCount == 1 {
					p.telemetry.SetPieceStatus(pt.index, PieceInFlight)
				}
				if len(selected) >= maxNeeded {
					return selected
				}
			}
		}
	}

	// Check if all remaining blocks already have active requests -> enter Endgame mode
	p.checkEndgameLocked()

	// Pass 2: Endgame duplicate block assignment for high-speed completion
	if p.endgameActive && len(selected) < maxNeeded {
		for _, pt := range candidates {
			for bIdx := range pt.blocks {
				blk := &pt.blocks[bIdx]
				if blk.downloaded {
					continue
				}
				if _, alreadyByPeer := blk.requesters[peerID]; alreadyByPeer {
					continue
				}
				// Limit duplicate endgame requests to at most 4 concurrent peers per block
				if len(blk.requesters) < 4 {
					blk.requesters[peerID] = struct{}{}
					selected = append(selected, blk.spec)
					if len(selected) >= maxNeeded {
						return selected
					}
				}
			}
		}
	}

	return selected
}

// ReceiveBlock writes an incoming block into the piece assembly buffer.
// Returns:
// - pieceData: non-nil full piece slice when all blocks of the piece have arrived
// - cancelPeers: list of other peerIDs that held duplicate endgame requests for this block (for CANCEL messages)
// - isNewBytes: true if this block was not previously downloaded
func (p *HybridPiecePicker) ReceiveBlock(fromPeerID string, pieceIndex int, begin int, data []byte) (pieceData []byte, cancelPeers []string, isNewBytes bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if pieceIndex < 0 || pieceIndex >= len(p.pieces) {
		return nil, nil, false
	}
	pt := p.pieces[pieceIndex]
	if pt.verified || pt.verifying {
		return nil, nil, false
	}

	blockIdx := begin / StandardBlockSize
	if blockIdx < 0 || blockIdx >= len(pt.blocks) {
		return nil, nil, false
	}
	blk := &pt.blocks[blockIdx]
	if blk.spec.Begin != begin || blk.spec.Length != len(data) {
		return nil, nil, false
	}

	// Collect any other peers holding duplicate requests for CANCEL fan-out
	for otherPeer := range blk.requesters {
		if otherPeer != fromPeerID {
			cancelPeers = append(cancelPeers, otherPeer)
		}
	}
	blk.requesters = make(map[string]struct{})

	if blk.downloaded {
		return nil, cancelPeers, false
	}

	if pt.buffer == nil {
		pt.buffer = make([]byte, pt.size)
	}
	copy(pt.buffer[begin:begin+len(data)], data)
	blk.downloaded = true
	pt.completedCount++
	if pt.inFlightCount > 0 {
		pt.inFlightCount--
	}

	if pt.completedCount >= len(pt.blocks) {
		pt.verifying = true
		fullBuf := pt.buffer
		pt.buffer = nil
		return fullBuf, cancelPeers, true
	}

	return nil, cancelPeers, true
}

// MarkPieceVerified marks a piece as permanently verified on disk.
func (p *HybridPiecePicker) MarkPieceVerified(pieceIndex int) (allComplete bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if pieceIndex < 0 || pieceIndex >= len(p.pieces) {
		return p.isAllWantedCompleteLocked()
	}
	pt := p.pieces[pieceIndex]
	pt.verifying = false
	if !pt.verified {
		pt.verified = true
		p.completedCount++
	}
	p.checkEndgameLocked()
	return p.isAllWantedCompleteLocked()
}

// MarkPieceCorrupt resets a piece whose SHA-1/SHA-256 hash failed verification so it will be re-downloaded.
func (p *HybridPiecePicker) MarkPieceCorrupt(pieceIndex int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if pieceIndex < 0 || pieceIndex >= len(p.pieces) {
		return
	}
	pt := p.pieces[pieceIndex]
	pt.verified = false
	pt.verifying = false
	pt.completedCount = 0
	pt.inFlightCount = 0
	pt.buffer = nil
	for i := range pt.blocks {
		pt.blocks[i].downloaded = false
		pt.blocks[i].requesters = make(map[string]struct{})
	}
	p.telemetry.SetPieceStatus(pieceIndex, PieceMissing)
}

func (p *HybridPiecePicker) checkEndgameLocked() {
	if len(p.pieces) == 0 {
		return
	}
	remainingPieces := len(p.pieces) - p.completedCount
	if remainingPieces == 0 {
		p.endgameActive = false
		p.telemetry.endgameActive.Store(false)
		return
	}
	if remainingPieces <= p.endgameThreshold {
		p.endgameActive = true
		p.telemetry.endgameActive.Store(true)
		return
	}
	// Also enter endgame if every remaining unverified block has at least one active requester
	allRequested := true
	for _, pt := range p.pieces {
		if pt.verified || pt.verifying {
			continue
		}
		for _, blk := range pt.blocks {
			if !blk.downloaded && len(blk.requesters) == 0 {
				allRequested = false
				break
			}
		}
		if !allRequested {
			break
		}
	}
	p.endgameActive = allRequested
	p.telemetry.endgameActive.Store(allRequested)
}

// BitfieldBytes returns the packed BEP 3 bitfield byte slice of verified pieces for sending to peers.
func (p *HybridPiecePicker) BitfieldBytes() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()

	numPieces := len(p.pieces)
	byteLen := (numPieces + 7) / 8
	bf := make([]byte, byteLen)
	for i, pt := range p.pieces {
		if pt.verified {
			bf[i/8] |= 1 << (7 - uint(i%8))
		}
	}
	return bf
}

// VerifiedSlice returns a boolean slice indicating which pieces are verified on disk.
func (p *HybridPiecePicker) VerifiedSlice() []bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]bool, len(p.pieces))
	for i, pt := range p.pieces {
		out[i] = pt.verified
	}
	return out
}

func (p *HybridPiecePicker) isAllWantedCompleteLocked() bool {
	if len(p.pieces) == 0 {
		return false
	}
	wanted := 0
	for _, pt := range p.pieces {
		if pt.priority == PrioritySkip {
			continue
		}
		wanted++
		if !pt.verified {
			return false
		}
	}
	return wanted > 0
}

// IsComplete returns true when every non-skipped piece in the torrent is verified.
func (p *HybridPiecePicker) IsComplete() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.isAllWantedCompleteLocked()
}
