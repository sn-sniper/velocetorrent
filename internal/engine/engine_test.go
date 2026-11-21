package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCorruptBencodeErrorBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"Empty", []byte("")},
		{"NotADict", []byte("i42e")},
		{"UnterminatedDict", []byte("d4:infod6:lengthi100eee")},
		{"MissingInfo", []byte("d8:announce15:http://test.come")},
		{"InvalidPiecesLength", []byte("d4:infod6:lengthi1024e4:name8:test.bin12:piece lengthi1024e6:pieces5:shortee")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseTorrentBytes(tc.payload)
			if err == nil {
				t.Fatalf("expected error for corrupt payload %s, got nil", tc.name)
			}
			if !errors.Is(err, ErrCorruptBencode) {
				t.Fatalf("expected ErrCorruptBencode, got %v", err)
			}
		})
	}
}

func TestParseMagnetURI(t *testing.T) {
	uri := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=archlinux-2026.iso&xl=1048576&tr=udp%3A%2F%2Ftracker.opentrackr.org%3A1337%2Fannounce"
	meta, err := ParseMagnetURI(uri)
	if err != nil {
		t.Fatalf("unexpected error parsing magnet URI: %v", err)
	}
	if meta.InfoHashHex != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("unexpected infoHashHex: %s", meta.InfoHashHex)
	}
	if meta.Name != "archlinux-2026.iso" {
		t.Fatalf("unexpected name: %s", meta.Name)
	}
	if meta.TotalSize != 1048576 {
		t.Fatalf("unexpected totalSize: %d", meta.TotalSize)
	}
	if len(meta.Trackers) != 1 {
		t.Fatalf("expected 1 tracker, got %d", len(meta.Trackers))
	}
}

func TestAdaptiveBDPAndHybridPiecePicker(t *testing.T) {
	// Verify BDP scaling from slow peer to 1 Gbps fiber peer
	slowDepth := ComputeAdaptiveBDPDepth(50*1024, 20.0)
	if slowDepth != MinPipelineDepth {
		t.Fatalf("expected MinPipelineDepth (%d) for slow peer, got %d", MinPipelineDepth, slowDepth)
	}
	fastDepth := ComputeAdaptiveBDPDepth(120*1024*1024, 25.0) // 120 MB/s @ 25ms RTT
	if fastDepth != MaxPipelineDepth {
		t.Fatalf("expected MaxPipelineDepth (%d) for 120 MB/s peer, got %d", MaxPipelineDepth, fastDepth)
	}

	payload := bytes.Repeat([]byte("VELOCETORRENT_BLOCK_"), 65536/20*4) // 4 pieces of 64 KiB
	_, meta, err := BuildSingleFileTorrent("picker-test.bin", payload, 65536, nil)
	if err != nil {
		t.Fatalf("failed to build torrent: %v", err)
	}

	tel := NewAtomicTorrentTelemetry(meta.NumPieces())
	picker := NewHybridPiecePicker(meta, tel, make([]bool, meta.NumPieces()))

	// Peer 1 has all pieces; Peer 2 only has piece 2 (making piece 1 & 3 rarer than piece 2)
	picker.ApplyPeerBitfield([]bool{true, true, true, true})
	picker.ApplyPeerBitfield([]bool{false, false, true, false})

	blocks := picker.NextBlocks("peer-1", []bool{true, true, true, true}, 2)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	// Piece 0 is boundary piece, so it should be picked first
	if blocks[0].PieceIndex != 0 {
		t.Fatalf("expected boundary piece 0 first, got %d", blocks[0].PieceIndex)
	}
}

func TestPreallocatedStorageAndEndToEndSwarmTransfer(t *testing.T) {
	tmpDir := t.TempDir()
	eng, err := NewEngine(Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer eng.Close()

	// Create a 512 KiB synthetic swarm torrent (8 pieces x 64 KiB = 32 blocks of 16 KiB)
	sess, _, err := eng.CreateSyntheticSwarmTorrent("ubuntu-minimal.iso", 512*1024, tmpDir, false)
	if err != nil {
		t.Fatalf("failed to create synthetic swarm torrent: %v", err)
	}

	// Wait for TCP loopback peer transfer + parallel SHA-1 verification + sequential disk flush to complete
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		snap := sess.Snapshot(true)
		if snap.Status == StatusSeeding && snap.CompletedPieces == snap.PieceCount {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	finalSnap := sess.Snapshot(true)
	if finalSnap.Status != StatusSeeding {
		t.Fatalf("expected torrent to reach SEEDING status, got %s (completed %d/%d pieces)",
			finalSnap.Status, finalSnap.CompletedPieces, finalSnap.PieceCount)
	}
	if finalSnap.VerifiedBytes != 512*1024 {
		t.Fatalf("expected VerifiedBytes = 524288, got %d", finalSnap.VerifiedBytes)
	}

	// Verify file exists on disk with exact size
	targetFile := filepath.Join(tmpDir, "ubuntu-minimal.iso")
	info, err := os.Stat(targetFile)
	if err != nil {
		t.Fatalf("expected downloaded file on disk at %s: %v", targetFile, err)
	}
	if info.Size() != 512*1024 {
		t.Fatalf("expected file size 524288, got %d", info.Size())
	}

	// Test live directory relocation
	newDir := filepath.Join(tmpDir, "relocated-output")
	if err := eng.RelocateTorrent(sess.ID(), newDir); err != nil {
		t.Fatalf("failed to relocate torrent: %v", err)
	}
	relocatedFile := filepath.Join(newDir, "ubuntu-minimal.iso")
	if _, err := os.Stat(relocatedFile); err != nil {
		t.Fatalf("expected relocated file at %s: %v", relocatedFile, err)
	}
}

func TestFastResumeStateDBAndInstantMtimeVerification(t *testing.T) {
	tmpDir := t.TempDir()
	stateDB := filepath.Join(tmpDir, "state.db")

	// 1. Start Engine #1, complete a multi-piece torrent, and close cleanly (flushing state.db)
	eng1, err := NewEngine(Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
		StateDBPath:      stateDB,
	})
	if err != nil {
		t.Fatalf("failed to start engine #1: %v", err)
	}

	sess1, _, err := eng1.CreateSyntheticSwarmTorrent("fast-resume-kernel.tar.zst", 256*1024, tmpDir, false)
	if err != nil {
		t.Fatalf("failed to create synthetic torrent: %v", err)
	}
	id := sess1.ID()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if sess1.Snapshot(false).Status == StatusSeeding {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err := eng1.Close(); err != nil {
		t.Fatalf("failed to close engine #1: %v", err)
	}

	if _, err := os.Stat(stateDB); err != nil {
		t.Fatalf("expected state.db to exist at %s: %v", stateDB, err)
	}

	// 2. Boot Engine #2 with the same state.db and verify instant mtime/size fast-resume
	bootStart := time.Now()
	eng2, err := NewEngine(Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
		StateDBPath:      stateDB,
	})
	if err != nil {
		t.Fatalf("failed to start engine #2: %v", err)
	}
	defer eng2.Close()
	bootElapsed := time.Since(bootStart)

	restored, ok := eng2.GetSession(id[:8]) // Also tests prefix lookup!
	if !ok {
		t.Fatalf("expected torrent %s to be restored from state.db", id)
	}
	snap := restored.Snapshot(true)
	if snap.Status != StatusSeeding || snap.Progress != 100.0 || snap.CompletedPieces != snap.PieceCount {
		t.Fatalf("expected restored torrent to be 100%% SEEDING via fast-resume, got status=%s progress=%.1f%% (%d/%d)",
			snap.Status, snap.Progress, snap.CompletedPieces, snap.PieceCount)
	}
	if bootElapsed > 2*time.Second {
		t.Fatalf("expected sub-second instant startup verification, took %v", bootElapsed)
	}
}

func TestIPBlocklistRadixTreeAndLifecycleWebhook(t *testing.T) {
	// 1. Verify CIDR and PeerGuardian range parsing in Radix Tree
	bl := NewIPBlocklist()
	loaded, err := bl.LoadFromReader(strings.NewReader(`
# Comment line
PoisonedSwarmRange:198.51.100.0-198.51.100.255
203.0.113.0/24
192.0.2.99
`))
	if err != nil || loaded != 3 {
		t.Fatalf("expected 3 rules loaded, got %d (err=%v)", loaded, err)
	}
	if !bl.IsBlockedAddr("198.51.100.42:6881") {
		t.Fatalf("expected 198.51.100.42:6881 to be blocked by PeerGuardian range")
	}
	if !bl.IsBlockedAddr("203.0.113.200:51413") {
		t.Fatalf("expected 203.0.113.200:51413 to be blocked by CIDR")
	}
	if !bl.IsBlockedAddr("192.0.2.99:9000") {
		t.Fatalf("expected 192.0.2.99:9000 to be blocked by single IP")
	}
	if bl.IsBlockedAddr("127.0.0.1:6881") {
		t.Fatalf("expected 127.0.0.1:6881 NOT to be blocked")
	}
	if bl.BlockedHits() != 3 {
		t.Fatalf("expected 3 blocked hits, got %d", bl.BlockedHits())
	}

	// 2. Verify Post-Download Lifecycle Webhook (on_complete)
	webhookCh := make(chan OnCompleteEvent, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev OnCompleteEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err == nil {
			select {
			case webhookCh <- ev:
			default:
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	tmpDir := t.TempDir()
	eng, err := NewEngine(Config{
		DefaultOutputDir:  tmpDir,
		ListenAddr:        "127.0.0.1:0",
		OnCompleteWebhook: ts.URL,
	})
	if err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer eng.Close()

	_, _, err = eng.CreateSyntheticSwarmTorrent("webhook-test.iso", 128*1024, tmpDir, false)
	if err != nil {
		t.Fatalf("failed to create synthetic torrent: %v", err)
	}

	select {
	case ev := <-webhookCh:
		if ev.Event != "torrent.completed" || ev.Name != "webhook-test.iso" || ev.TotalSize != 128*1024 {
			t.Fatalf("unexpected webhook payload: %+v", ev)
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("timed out waiting for on_complete lifecycle webhook")
	}
}
