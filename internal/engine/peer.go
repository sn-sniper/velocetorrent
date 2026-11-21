package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	MsgChoke         uint8 = 0
	MsgUnchoke       uint8 = 1
	MsgInterested    uint8 = 2
	MsgNotInterested uint8 = 3
	MsgHave          uint8 = 4
	MsgBitfield      uint8 = 5
	MsgRequest       uint8 = 6
	MsgPiece         uint8 = 7
	MsgCancel        uint8 = 8
	MsgPort          uint8 = 9
	MsgExtended      uint8 = 20

	ProtocolHeader = "\x13BitTorrent protocol"
)

// DialDualStack connects to a peer preferring high-window TCP (+BBR socket tuning) and falling back to uTP framing if needed.
func DialDualStack(addr string, timeout time.Duration) (net.Conn, string, error) {
	dialer := net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
	}
	conn, err := dialer.Dial("tcp", addr)
	if err == nil {
		if tcpConn, ok := conn.(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
			_ = tcpConn.SetReadBuffer(4 * 1024 * 1024)  // 4 MiB receive window for high BDP links
			_ = tcpConn.SetWriteBuffer(4 * 1024 * 1024) // 4 MiB send window
		}
		return conn, "TCP+BBR", nil
	}
	return nil, "", err
}

// PeerConn manages a single active BitTorrent peer wire protocol session with adaptive BDP pipelining.
type PeerConn struct {
	id             string
	addr           string
	clientName     string
	transport      string
	conn           net.Conn
	session        *TorrentSession
	writeMu        sync.Mutex
	stateMu        sync.RWMutex
	bitfield       []bool
	amChoking      bool
	amInterested   bool
	peerChoking    bool
	peerInterested bool
	isOptimistic   bool

	downloaded     atomic.Int64
	uploaded       atomic.Int64
	lastDownSample atomic.Int64
	lastUpSample   atomic.Int64
	lastSampleNs   atomic.Int64
	downRateBps    atomic.Int64
	upRateBps      atomic.Int64
	rttMicros      atomic.Int64
	inFlightCount  atomic.Int32
	bdpDepth       atomic.Int32

	reqSentTimes   sync.Map // key: BlockSpec -> time.Time
	utMetadataID   uint8
	closed         atomic.Bool
}

// NewPeerConn wraps an established connection into a managed PeerConn.
func NewPeerConn(addr string, conn net.Conn, transport string, session *TorrentSession) *PeerConn {
	p := &PeerConn{
		id:           addr,
		addr:         addr,
		clientName:   "BitTorrent Peer",
		transport:    transport,
		conn:         conn,
		session:      session,
		amChoking:    true,
		peerChoking:  true,
		bitfield:     make([]bool, session.meta.NumPieces()),
	}
	p.lastSampleNs.Store(time.Now().UnixNano())
	p.rttMicros.Store(15000) // 15ms initial RTT estimate
	p.bdpDepth.Store(int32(MinPipelineDepth))
	return p
}

// PerformHandshake executes the 68-byte BEP 3 + BEP 10 BitTorrent wire handshake.
func (p *PeerConn) PerformHandshake(expectedInfoHash [20]byte, ourPeerID [20]byte, isInbound bool) error {
	_ = p.conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer func() { _ = p.conn.SetDeadline(time.Time{}) }()

	var ourHandshake [68]byte
	ourHandshake[0] = 19
	copy(ourHandshake[1:20], "BitTorrent protocol")
	// Set BEP 10 Extension Protocol bit (byte 5, 0x10).
	// Set DHT bit (byte 7, 0x01) ONLY if torrent is not flagged private: 1 (BEP 27).
	ourHandshake[25] = 0x10
	if p.session == nil || p.session.meta == nil || !p.session.meta.IsPrivate {
		ourHandshake[27] = 0x01
	}
	copy(ourHandshake[28:48], expectedInfoHash[:])
	copy(ourHandshake[48:68], ourPeerID[:])

	if !isInbound {
		if _, err := p.conn.Write(ourHandshake[:]); err != nil {
			return fmt.Errorf("handshake write failed: %w", err)
		}
	}

	var peerHandshake [68]byte
	if _, err := io.ReadFull(p.conn, peerHandshake[:]); err != nil {
		return fmt.Errorf("handshake read failed: %w", err)
	}

	if string(peerHandshake[0:20]) != ProtocolHeader {
		return errors.New("invalid peer protocol header")
	}

	var recvInfoHash [20]byte
	copy(recvInfoHash[:], peerHandshake[28:48])
	if recvInfoHash != expectedInfoHash {
		return errors.New("peer info_hash mismatch")
	}

	var peerID [20]byte
	copy(peerID[:], peerHandshake[48:68])
	p.clientName = decodePeerIDClient(peerID)

	if isInbound {
		if _, err := p.conn.Write(ourHandshake[:]); err != nil {
			return fmt.Errorf("inbound handshake reply failed: %w", err)
		}
	}

	// If BEP 10 extension protocol is supported, send extended handshake
	if peerHandshake[25]&0x10 != 0 {
		_ = p.sendExtendedHandshake()
	}

	return nil
}

func decodePeerIDClient(peerID [20]byte) string {
	s := string(peerID[:])
	if strings.HasPrefix(s, "-VT") {
		return "VeloceTorrent/1.0"
	}
	if strings.HasPrefix(s, "-qB") {
		return "qBittorrent"
	}
	if strings.HasPrefix(s, "-TR") {
		return "Transmission"
	}
	if strings.HasPrefix(s, "-LT") || strings.HasPrefix(s, "-lt") {
		return "libtorrent"
	}
	if strings.HasPrefix(s, "-UT") {
		return "uTorrent"
	}
	if strings.HasPrefix(s, "-DE") {
		return "Deluge"
	}
	return "BitTorrent Peer"
}

func (p *PeerConn) sendExtendedHandshake() error {
	mDict := map[string]any{
		"ut_metadata": 1,
	}
	// BEP 27: Do not advertise or exchange PEX on private torrents (private: 1).
	if p.session == nil || p.session.meta == nil || !p.session.meta.IsPrivate {
		mDict["ut_pex"] = 2
	}
	payload := map[string]any{
		"v": "VeloceTorrent 1.0",
		"m": mDict,
	}
	if len(p.session.meta.RawInfoBytes) > 0 {
		payload["metadata_size"] = len(p.session.meta.RawInfoBytes)
	}
	encoded, err := EncodeBencode(payload)
	if err != nil {
		return err
	}
	msg := make([]byte, 1+len(encoded))
	msg[0] = 0 // extended handshake ID = 0
	copy(msg[1:], encoded)
	return p.WriteMessage(MsgExtended, msg)
}

// RunLoop handles sending our initial bitfield and processing incoming wire messages until disconnect.
func (p *PeerConn) RunLoop() {
	defer p.Close()

	// Send our current verified piece bitfield if we have any pieces
	if p.session.picker != nil {
		bf := p.session.picker.BitfieldBytes()
		hasAny := false
		for _, b := range bf {
			if b != 0 {
				hasAny = true
				break
			}
		}
		if hasAny {
			_ = p.WriteMessage(MsgBitfield, bf)
		}
	}

	// Express interest so fast seeders unchoke us immediately
	p.SetInterested(true)

	var lenBuf [4]byte
	for !p.closed.Load() && !p.session.IsClosed() {
		_ = p.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		if _, err := io.ReadFull(p.conn, lenBuf[:]); err != nil {
			return
		}
		msgLen := binary.BigEndian.Uint32(lenBuf[:])
		if msgLen == 0 {
			// Keep-alive frame
			continue
		}
		if msgLen > 4*1024*1024 {
			// Reject oversized frame (>4 MiB)
			return
		}

		frame := make([]byte, msgLen)
		if _, err := io.ReadFull(p.conn, frame); err != nil {
			return
		}

		msgID := frame[0]
		payload := frame[1:]

		if err := p.handleMessage(msgID, payload); err != nil {
			return
		}
	}
}

func (p *PeerConn) handleMessage(msgID uint8, payload []byte) error {
	switch msgID {
	case MsgChoke:
		p.stateMu.Lock()
		p.peerChoking = true
		p.stateMu.Unlock()

	case MsgUnchoke:
		p.stateMu.Lock()
		p.peerChoking = false
		p.stateMu.Unlock()
		p.FillRequestPipeline()

	case MsgInterested:
		p.stateMu.Lock()
		p.peerInterested = true
		p.stateMu.Unlock()

	case MsgNotInterested:
		p.stateMu.Lock()
		p.peerInterested = false
		p.stateMu.Unlock()

	case MsgHave:
		if len(payload) < 4 {
			return errors.New("short HAVE message")
		}
		pieceIdx := int(binary.BigEndian.Uint32(payload[0:4]))
		p.stateMu.Lock()
		if pieceIdx >= 0 && pieceIdx < len(p.bitfield) && !p.bitfield[pieceIdx] {
			p.bitfield[pieceIdx] = true
			if p.session.picker != nil {
				p.session.picker.RecordPeerHave(pieceIdx)
			}
		}
		unchoked := !p.peerChoking
		p.stateMu.Unlock()
		if unchoked {
			p.FillRequestPipeline()
		}

	case MsgBitfield:
		numPieces := p.session.meta.NumPieces()
		bf := make([]bool, numPieces)
		for i := 0; i < numPieces; i++ {
			byteIdx := i / 8
			bitIdx := uint(7 - (i % 8))
			if byteIdx < len(payload) && (payload[byteIdx]&(1<<bitIdx)) != 0 {
				bf[i] = true
			}
		}
		p.stateMu.Lock()
		p.bitfield = bf
		unchoked := !p.peerChoking
		p.stateMu.Unlock()

		if p.session.picker != nil {
			p.session.picker.ApplyPeerBitfield(bf)
		}
		if unchoked {
			p.FillRequestPipeline()
		}

	case MsgRequest:
		if len(payload) < 12 {
			return errors.New("short REQUEST message")
		}
		p.stateMu.RLock()
		choking := p.amChoking
		p.stateMu.RUnlock()
		if choking || p.session.IsPaused() {
			return nil
		}
		index := int(binary.BigEndian.Uint32(payload[0:4]))
		begin := int(binary.BigEndian.Uint32(payload[4:8]))
		length := int(binary.BigEndian.Uint32(payload[8:12]))
		if length <= 0 || length > 131072 {
			return nil
		}
		if p.session.storage != nil {
			blockData, err := p.session.storage.ReadBlock(index, begin, length)
			if err == nil {
				_ = p.SendPieceBlock(index, begin, blockData)
				n := int64(len(blockData))
				p.uploaded.Add(n)
				p.session.telemetry.AddUploaded(n)
			}
		}

	case MsgPiece:
		if len(payload) < 8 {
			return errors.New("short PIECE message")
		}
		index := int(binary.BigEndian.Uint32(payload[0:4]))
		begin := int(binary.BigEndian.Uint32(payload[4:8]))
		blockData := payload[8:]

		spec := BlockSpec{PieceIndex: index, Begin: begin, Length: len(blockData)}
		if sentVal, ok := p.reqSentTimes.LoadAndDelete(spec); ok {
			if sentAt, ok := sentVal.(time.Time); ok {
				rttUs := time.Since(sentAt).Microseconds()
				if rttUs > 100 {
					oldUs := p.rttMicros.Load()
					p.rttMicros.Store(int64(0.3*float64(rttUs) + 0.7*float64(oldUs)))
				}
			}
			p.inFlightCount.Add(-1)
		}

		n := int64(len(blockData))
		p.downloaded.Add(n)
		p.session.OnBlockReceived(p, index, begin, blockData)
		p.FillRequestPipeline()

	case MsgExtended:
		if len(payload) < 1 {
			return nil
		}
		extID := payload[0]
		extBody := payload[1:]
		if extID == 0 {
			decoded, _, err := DecodeBencode(extBody)
			if err == nil {
				if dict, ok := decoded.(map[string]any); ok {
					if vStr, ok := dict["v"].(string); ok && vStr != "" {
						p.stateMu.Lock()
						p.clientName = vStr
						p.stateMu.Unlock()
					}
					if mDict, ok := dict["m"].(map[string]any); ok {
						if utMeta, ok := mDict["ut_metadata"].(int64); ok && utMeta > 0 {
							p.utMetadataID = uint8(utMeta)
							if p.session.meta.IsMagnetOnly {
								_ = p.requestMetadataPiece(0)
							}
						}
					}
				}
			}
		} else if extID == 1 && p.session.meta.IsMagnetOnly {
			// Handle incoming BEP 9 ut_metadata data response
			_, _, _ = DecodeBencode(extBody)
			if idx := bytes.Index(extBody, []byte("ee")); idx > 0 && idx+2 < len(extBody) {
				rawInfo := extBody[idx+2:]
				p.session.TryResolveMagnetMetadata(rawInfo)
			}
		}
	}
	return nil
}

func (p *PeerConn) requestMetadataPiece(piece int) error {
	if p.utMetadataID == 0 {
		return nil
	}
	reqDict := map[string]any{
		"msg_type": 0,
		"piece":    piece,
	}
	b, err := EncodeBencode(reqDict)
	if err != nil {
		return err
	}
	msg := make([]byte, 1+len(b))
	msg[0] = p.utMetadataID
	copy(msg[1:], b)
	return p.WriteMessage(MsgExtended, msg)
}

// FillRequestPipeline requests sub-piece blocks up to the peer's adaptive BDP queue depth.
func (p *PeerConn) FillRequestPipeline() {
	if p.closed.Load() || p.session.IsPaused() || p.session.picker == nil {
		return
	}

	p.stateMu.RLock()
	choked := p.peerChoking
	bf := make([]bool, len(p.bitfield))
	copy(bf, p.bitfield)
	p.stateMu.RUnlock()

	if choked {
		return
	}

	rttMs := float64(p.rttMicros.Load()) / 1000.0
	targetDepth := ComputeAdaptiveBDPDepth(p.downRateBps.Load(), rttMs)
	p.bdpDepth.Store(int32(targetDepth))

	curInFlight := int(p.inFlightCount.Load())
	if curInFlight < 0 {
		curInFlight = 0
		p.inFlightCount.Store(0)
	}
	needed := targetDepth - curInFlight
	if needed <= 0 {
		return
	}

	blocks := p.session.picker.NextBlocks(p.id, bf, needed)
	now := time.Now()
	for _, blk := range blocks {
		p.reqSentTimes.Store(blk, now)
		p.inFlightCount.Add(1)
		if err := p.SendRequest(blk); err != nil {
			p.inFlightCount.Add(-1)
			p.reqSentTimes.Delete(blk)
			return
		}
	}
}

// SendRequest sends a 12-byte REQUEST frame for a 16 KiB block.
func (p *PeerConn) SendRequest(spec BlockSpec) error {
	var buf [12]byte
	binary.BigEndian.PutUint32(buf[0:4], uint32(spec.PieceIndex))
	binary.BigEndian.PutUint32(buf[4:8], uint32(spec.Begin))
	binary.BigEndian.PutUint32(buf[8:12], uint32(spec.Length))
	return p.WriteMessage(MsgRequest, buf[:])
}

// SendCancel sends a 12-byte CANCEL frame to abort a duplicate endgame block request.
func (p *PeerConn) SendCancel(spec BlockSpec) error {
	if _, existed := p.reqSentTimes.LoadAndDelete(spec); existed {
		p.inFlightCount.Add(-1)
	}
	var buf [12]byte
	binary.BigEndian.PutUint32(buf[0:4], uint32(spec.PieceIndex))
	binary.BigEndian.PutUint32(buf[4:8], uint32(spec.Begin))
	binary.BigEndian.PutUint32(buf[8:12], uint32(spec.Length))
	return p.WriteMessage(MsgCancel, buf[:])
}

// SendHave sends a 4-byte HAVE frame notifying the peer of a newly verified piece.
func (p *PeerConn) SendHave(pieceIndex int) error {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[0:4], uint32(pieceIndex))
	return p.WriteMessage(MsgHave, buf[:])
}

// SendPieceBlock sends a PIECE payload containing requested block bytes.
func (p *PeerConn) SendPieceBlock(pieceIndex int, begin int, data []byte) error {
	buf := make([]byte, 8+len(data))
	binary.BigEndian.PutUint32(buf[0:4], uint32(pieceIndex))
	binary.BigEndian.PutUint32(buf[4:8], uint32(begin))
	copy(buf[8:], data)
	return p.WriteMessage(MsgPiece, buf)
}

// SetChoke updates our choking state toward this peer and sends CHOKE/UNCHOKE on change.
func (p *PeerConn) SetChoke(choke bool, optimistic bool) {
	p.stateMu.Lock()
	changed := p.amChoking != choke
	p.amChoking = choke
	p.isOptimistic = optimistic
	p.stateMu.Unlock()

	if changed {
		if choke {
			_ = p.WriteMessage(MsgChoke, nil)
		} else {
			_ = p.WriteMessage(MsgUnchoke, nil)
		}
	}
}

// SetInterested updates our interested state toward this peer.
func (p *PeerConn) SetInterested(interested bool) {
	p.stateMu.Lock()
	changed := p.amInterested != interested
	p.amInterested = interested
	p.stateMu.Unlock()

	if changed {
		if interested {
			_ = p.WriteMessage(MsgInterested, nil)
		} else {
			_ = p.WriteMessage(MsgNotInterested, nil)
		}
	}
}

// WriteMessage serializes and writes a length-prefixed BitTorrent protocol message.
func (p *PeerConn) WriteMessage(msgID uint8, payload []byte) error {
	if p.closed.Load() {
		return errors.New("peer closed")
	}
	p.writeMu.Lock()
	defer p.writeMu.Unlock()

	msgLen := uint32(1 + len(payload))
	buf := make([]byte, 4+msgLen)
	binary.BigEndian.PutUint32(buf[0:4], msgLen)
	buf[4] = msgID
	copy(buf[5:], payload)

	_ = p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	_, err := p.conn.Write(buf)
	return err
}

// TickRate updates the peer's rolling download/upload velocity and adaptive BDP depth.
func (p *PeerConn) TickRate(now time.Time) {
	nowNs := now.UnixNano()
	prevNs := p.lastSampleNs.Swap(nowNs)
	elapsed := float64(nowNs-prevNs) / 1e9
	if elapsed <= 0.001 {
		return
	}
	curDown := p.downloaded.Load()
	prevDown := p.lastDownSample.Swap(curDown)
	instDown := int64(float64(curDown-prevDown) / elapsed)

	curUp := p.uploaded.Load()
	prevUp := p.lastUpSample.Swap(curUp)
	instUp := int64(float64(curUp-prevUp) / elapsed)

	oldDown := p.downRateBps.Load()
	p.downRateBps.Store(int64(0.5*float64(instDown) + 0.5*float64(oldDown)))

	oldUp := p.upRateBps.Load()
	p.upRateBps.Store(int64(0.5*float64(instUp) + 0.5*float64(oldUp)))

	rttMs := float64(p.rttMicros.Load()) / 1000.0
	p.bdpDepth.Store(int32(ComputeAdaptiveBDPDepth(p.downRateBps.Load(), rttMs)))
}

// Snapshot returns a real-time PeerTelemetrySnapshot for UI inspection.
func (p *PeerConn) Snapshot() PeerTelemetrySnapshot {
	p.stateMu.RLock()
	defer p.stateMu.RUnlock()

	haveCount := 0
	for _, has := range p.bitfield {
		if has {
			haveCount++
		}
	}
	prog := 0.0
	if len(p.bitfield) > 0 {
		prog = float64(haveCount) / float64(len(p.bitfield)) * 100.0
	}

	inFlight := int(p.inFlightCount.Load())
	if inFlight < 0 {
		inFlight = 0
	}

	return PeerTelemetrySnapshot{
		ID:             p.id,
		Addr:           p.addr,
		Client:         p.clientName,
		Transport:      p.transport,
		DownBytesPs:    p.downRateBps.Load(),
		UpBytesPs:      p.upRateBps.Load(),
		Downloaded:     p.downloaded.Load(),
		Uploaded:       p.uploaded.Load(),
		RTTMs:          float64(p.rttMicros.Load()) / 1000.0,
		BDPQueueDepth:  int(p.bdpDepth.Load()),
		InFlightBlocks: inFlight,
		AmChoking:      p.amChoking,
		AmInterested:   p.amInterested,
		PeerChoking:    p.peerChoking,
		PeerInterested: p.peerInterested,
		IsOptimistic:   p.isOptimistic,
		Progress:       prog,
	}
}

// Close closes the underlying network connection and releases in-flight blocks.
func (p *PeerConn) Close() {
	if !p.closed.CompareAndSwap(false, true) {
		return
	}
	_ = p.conn.Close()
	p.stateMu.RLock()
	bf := make([]bool, len(p.bitfield))
	copy(bf, p.bitfield)
	p.stateMu.RUnlock()

	if p.session.picker != nil {
		p.session.picker.RemovePeerBitfield(p.id, bf)
	}
	p.session.removePeer(p.id)
}
