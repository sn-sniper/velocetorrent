package ipc

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"velocetorrent/internal/engine"
)

const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// TelemetryBroadcaster broadcasts 100ms high-frequency telemetry snapshots to all connected WebSocket and SSE clients.
type TelemetryBroadcaster struct {
	eng      *engine.Engine
	interval time.Duration

	mu        sync.RWMutex
	wsClients map[*wsClient]struct{}
	sseChans  map[chan []byte]struct{}

	closed  atomic.Bool
	closeCh chan struct{}
	wg      sync.WaitGroup
}

type wsClient struct {
	conn    net.Conn
	writeMu sync.Mutex
	sendCh  chan []byte
	closed  atomic.Bool
}

// NewTelemetryBroadcaster creates a 100ms telemetry broadcaster attached to the BitTorrent Engine.
func NewTelemetryBroadcaster(eng *engine.Engine, tickInterval time.Duration) *TelemetryBroadcaster {
	if tickInterval <= 0 {
		tickInterval = 100 * time.Millisecond
	}
	b := &TelemetryBroadcaster{
		eng:       eng,
		interval:  tickInterval,
		wsClients: make(map[*wsClient]struct{}),
		sseChans:  make(map[chan []byte]struct{}),
		closeCh:   make(chan struct{}),
	}
	b.wg.Add(1)
	go b.broadcastLoop()
	return b
}

func (b *TelemetryBroadcaster) broadcastLoop() {
	defer b.wg.Done()
	ticker := time.NewTicker(b.interval)
	defer ticker.Stop()

	for {
		select {
		case <-b.closeCh:
			return
		case <-ticker.C:
			b.mu.RLock()
			clientCount := len(b.wsClients) + len(b.sseChans)
			b.mu.RUnlock()
			if clientCount == 0 {
				continue
			}

			snap := b.eng.Snapshot(true)
			payload, err := json.Marshal(snap)
			if err != nil {
				continue
			}

			b.mu.RLock()
			for c := range b.wsClients {
				select {
				case c.sendCh <- payload:
				default:
					// Drop stale frame if slow client is backpressured so we never block the 100ms tick
				}
			}
			for ch := range b.sseChans {
				select {
				case ch <- payload:
				default:
				}
			}
			b.mu.RUnlock()
		}
	}
}

// BroadcastImmediate pushes an immediate state update to all connected clients (e.g. right after adding/pausing a torrent).
func (b *TelemetryBroadcaster) BroadcastImmediate() {
	snap := b.eng.Snapshot(true)
	payload, err := json.Marshal(snap)
	if err != nil {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for c := range b.wsClients {
		select {
		case c.sendCh <- payload:
		default:
		}
	}
	for ch := range b.sseChans {
		select {
		case ch <- payload:
		default:
		}
	}
}

// HandleWebSocket upgrades an HTTP/1.1 request to an RFC 6455 WebSocket stream and pushes 100ms telemetry frames.
func (b *TelemetryBroadcaster) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "Expected WebSocket Upgrade", http.StatusBadRequest)
		return
	}
	wsKey := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if wsKey == "" {
		http.Error(w, "Missing Sec-WebSocket-Key", http.StatusBadRequest)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "WebSocket hijacking not supported", http.StatusInternalServerError)
		return
	}

	conn, rw, err := hj.Hijack()
	if err != nil {
		return
	}

	acceptHash := sha1.Sum([]byte(wsKey + wsGUID))
	acceptVal := base64.StdEncoding.EncodeToString(acceptHash[:])

	respLines := []string{
		"HTTP/1.1 101 Switching Protocols",
		"Upgrade: websocket",
		"Connection: Upgrade",
		"Sec-WebSocket-Accept: " + acceptVal,
		"",
		"",
	}
	if _, err := rw.WriteString(strings.Join(respLines, "\r\n")); err != nil {
		_ = conn.Close()
		return
	}
	if err := rw.Flush(); err != nil {
		_ = conn.Close()
		return
	}

	client := &wsClient{
		conn:   conn,
		sendCh: make(chan []byte, 8),
	}

	b.mu.Lock()
	b.wsClients[client] = struct{}{}
	b.mu.Unlock()

	// Send initial telemetry frame immediately on connect
	initialSnap := b.eng.Snapshot(true)
	if initBytes, err := json.Marshal(initialSnap); err == nil {
		client.sendCh <- initBytes
	}

	go b.wsWriteLoop(client)
	b.wsReadLoop(client, rw.Reader)
}

func (b *TelemetryBroadcaster) wsWriteLoop(c *wsClient) {
	defer b.removeWSClient(c)
	for {
		select {
		case <-b.closeCh:
			return
		case msg, ok := <-c.sendCh:
			if !ok {
				return
			}
			if err := c.writeFrame(0x1, msg); err != nil { // 0x1 = UTF-8 Text Frame
				return
			}
		}
	}
}

func (b *TelemetryBroadcaster) wsReadLoop(c *wsClient, br *bufio.Reader) {
	defer b.removeWSClient(c)
	for !c.closed.Load() && !b.closed.Load() {
		_ = c.conn.SetReadDeadline(time.Now().Add(90 * time.Second))
		header := make([]byte, 2)
		if _, err := io.ReadFull(br, header); err != nil {
			return
		}
		opcode := header[0] & 0x0f
		masked := (header[1] & 0x80) != 0
		payloadLen := int64(header[1] & 0x7f)

		switch payloadLen {
		case 126:
			var ext [2]byte
			if _, err := io.ReadFull(br, ext[:]); err != nil {
				return
			}
			payloadLen = int64(binary.BigEndian.Uint16(ext[:]))
		case 127:
			var ext [8]byte
			if _, err := io.ReadFull(br, ext[:]); err != nil {
				return
			}
			payloadLen = int64(binary.BigEndian.Uint64(ext[:]))
		}

		if payloadLen > 1024*1024 {
			return
		}

		var maskKey [4]byte
		if masked {
			if _, err := io.ReadFull(br, maskKey[:]); err != nil {
				return
			}
		}

		payload := make([]byte, payloadLen)
		if payloadLen > 0 {
			if _, err := io.ReadFull(br, payload); err != nil {
				return
			}
			if masked {
				for i := range payload {
					payload[i] ^= maskKey[i%4]
				}
			}
		}

		switch opcode {
		case 0x8: // Close frame
			_ = c.writeFrame(0x8, nil)
			return
		case 0x9: // Ping frame -> reply with Pong (0xA)
			_ = c.writeFrame(0xA, payload)
		}
	}
}

func (c *wsClient) writeFrame(opcode byte, payload []byte) error {
	if c.closed.Load() {
		return errors.New("ws client closed")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	length := len(payload)
	var header []byte
	if length < 126 {
		header = []byte{0x80 | opcode, byte(length)}
	} else if length <= 65535 {
		header = make([]byte, 4)
		header[0] = 0x80 | opcode
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:4], uint16(length))
	} else {
		header = make([]byte, 10)
		header[0] = 0x80 | opcode
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:10], uint64(length))
	}

	_ = c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	if length > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func (b *TelemetryBroadcaster) removeWSClient(c *wsClient) {
	if !c.closed.CompareAndSwap(false, true) {
		return
	}
	b.mu.Lock()
	delete(b.wsClients, c)
	b.mu.Unlock()
	_ = c.conn.Close()
}

// HandleSSE streams 100ms telemetry snapshots over Server-Sent Events (text/event-stream).
func (b *TelemetryBroadcaster) HandleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := make(chan []byte, 8)
	b.mu.Lock()
	b.sseChans[ch] = struct{}{}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		delete(b.sseChans, ch)
		b.mu.Unlock()
	}()

	// Push initial state immediately
	initSnap := b.eng.Snapshot(true)
	if initBytes, err := json.Marshal(initSnap); err == nil {
		fmt.Fprintf(w, "data: %s\n\n", initBytes)
		flusher.Flush()
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.closeCh:
			return
		case msg := <-ch:
			if _, err := fmt.Fprintf(w, "data: %s\n\n", msg); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// Close shuts down the broadcaster and disconnects all active stream clients.
func (b *TelemetryBroadcaster) Close() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	close(b.closeCh)
	b.wg.Wait()

	b.mu.Lock()
	for c := range b.wsClients {
		_ = c.conn.Close()
	}
	b.wsClients = make(map[*wsClient]struct{})
	b.sseChans = make(map[chan []byte]struct{})
	b.mu.Unlock()
}
