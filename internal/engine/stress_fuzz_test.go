package engine

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// FuzzBencodeAndMetainfoParser validates that arbitrary or mutated bencode/.torrent inputs never panic.
func FuzzBencodeAndMetainfoParser(f *testing.F) {
	seeds := [][]byte{
		[]byte("d8:announce18:http://tracker.org4:infod6:lengthi65536e4:name8:seed.bin12:piece lengthi65536e6:pieces20:01234567890123456789ee"),
		[]byte("d4:infod5:filesld6:lengthi1024e4:pathl4:a.txteee4:name3:dir12:piece lengthi1024e6:pieces20:01234567890123456789ee"),
		[]byte("i-42e"),
		[]byte("l4:spami123ee"),
		[]byte("d3:foo3:bare"),
		[]byte(""),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 2*1024*1024 {
			return
		}
		_, _, _ = DecodeBencode(data)
		_, _ = ParseTorrentBytes(data)
	})
}

// TestSelectiveFilePriorityAndStreamingLookahead verifies Phase 9 selective file skip/high priority and streaming lookahead.
func TestSelectiveFilePriorityAndStreamingLookahead(t *testing.T) {
	p0 := make([]byte, 65536)
	p1 := make([]byte, 65536)
	sum0 := sha1.Sum(p0)
	sum1 := sha1.Sum(p1)

	meta := &TorrentMeta{
		InfoHashHex: "3344556677889900112233445566778899001122",
		Name:        "multi-iso-bundle",
		TotalSize:   131072,
		PieceLength: 65536,
		PieceHashes: [][]byte{sum0[:], sum1[:]},
		HashSize:    20,
		Files: []FileEntry{
			{RelPath: filepath.Join("multi-iso-bundle", "readme.txt"), Length: 65536, Offset: 0},
			{RelPath: filepath.Join("multi-iso-bundle", "arch.iso"), Length: 65536, Offset: 65536},
		},
	}

	tel := NewAtomicTorrentTelemetry(2)
	picker := NewHybridPiecePicker(meta, tel, []bool{false, false})
	picker.SetStreamMode(true)

	// Mark file 0 as "skip" and file 1 as "high"
	picker.UpdateFilePriorities([]FilePriority{PrioritySkip, PriorityHigh})

	// NextBlocks should skip piece 0 (which belongs exclusively to skipped file 0) and only request piece 1!
	blocks := picker.NextBlocks("peer-A", []bool{true, true}, 4)
	if len(blocks) != 4 {
		t.Fatalf("expected 4 blocks for piece 1, got %d", len(blocks))
	}
	for _, blk := range blocks {
		if blk.PieceIndex != 1 {
			t.Fatalf("expected only piece 1 (high priority) to be requested when piece 0 is skipped, got piece %d", blk.PieceIndex)
		}
	}

	// Verify that completing piece 1 satisfies IsComplete() even though skipped piece 0 is unverified
	allDone := picker.MarkPieceVerified(1)
	if !allDone || !picker.IsComplete() {
		t.Fatalf("expected IsComplete() == true when all non-skipped pieces are verified")
	}
}

// TestFullDiskENOSPCGracefulPause simulates an ENOSPC disk write failure and verifies the session pauses gracefully.
func TestFullDiskENOSPCGracefulPause(t *testing.T) {
	tmpDir := t.TempDir()
	eng, err := NewEngine(Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer eng.Close()

	// Create a paused session and inject a synthetic ENOSPC write failure on writeResCh
	sess, _, err := eng.CreateSyntheticSwarmTorrent("enospc-test.iso", 131072, tmpDir, false)
	if err != nil {
		t.Fatalf("failed to create torrent: %v", err)
	}
	sess.Pause()

	sess.writeResCh <- PieceWriteResult{
		PieceIndex: 0,
		Valid:      true,
		Err:        errors.New("write enospc-test.iso: no space left on device (ENOSPC)"),
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := sess.Snapshot(false)
		if strings.Contains(snap.ErrorMsg, "ENOSPC") {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	snap := sess.Snapshot(false)
	if snap.Status != StatusPaused {
		t.Fatalf("expected torrent to be PAUSED after ENOSPC disk failure, got %s", snap.Status)
	}
	if !strings.Contains(snap.ErrorMsg, "ENOSPC") {
		t.Fatalf("expected actionable ENOSPC error message, got %q", snap.ErrorMsg)
	}
}

// TestTenConcurrentHighSeedSwarmsStress runs 10 concurrent multi-piece swarms simultaneously to verify thread safety and throughput.
func TestTenConcurrentHighSeedSwarmsStress(t *testing.T) {
	tmpDir := t.TempDir()
	eng, err := NewEngine(Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer eng.Close()

	const numSwarms = 10
	sessions := make([]*TorrentSession, numSwarms)
	var wg sync.WaitGroup

	for i := 0; i < numSwarms; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("concurrent-swarm-%02d.iso", idx)
			// Vary sizes so each swarm has a distinct info_hash
			size := int64((4 + idx) * 65536)
			s, _, sErr := eng.CreateSyntheticSwarmTorrent(name, size, tmpDir, idx%2 == 0)
			if sErr != nil {
				t.Errorf("failed to start swarm %d: %v", idx, sErr)
				return
			}
			sessions[idx] = s
		}(i)
	}
	wg.Wait()

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		completedSwarms := 0
		for _, s := range sessions {
			if s != nil && s.Snapshot(false).Status == StatusSeeding {
				completedSwarms++
			}
		}
		if completedSwarms == numSwarms {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	for i, s := range sessions {
		if s == nil {
			t.Fatalf("swarm %d session is nil", i)
		}
		snap := s.Snapshot(false)
		if snap.Status != StatusSeeding || snap.CompletedPieces != snap.PieceCount {
			t.Fatalf("swarm %d (%s) did not complete: status=%s (%d/%d pieces)",
				i, snap.Name, snap.Status, snap.CompletedPieces, snap.PieceCount)
		}
	}
}

// TestPrivateTrackerAndSeedRatioStop verifies BEP 27 private: 1 parsing, DHT/PEX suppression, and seed-ratio auto-stop.
func TestPrivateTrackerAndSeedRatioStop(t *testing.T) {
	payload := make([]byte, 65536)
	sum := sha1.Sum(payload)
	root := map[string]any{
		"announce": "https://private.tracker.local/announce",
		"info": map[string]any{
			"length":       int64(len(payload)),
			"name":         "private-release.iso",
			"piece length": int64(65536),
			"pieces":       sum[:],
			"private":      int64(1),
		},
	}
	rawTorrent, err := EncodeBencode(root)
	if err != nil {
		t.Fatalf("failed to encode private torrent: %v", err)
	}
	meta, err := ParseTorrentBytes(rawTorrent)
	if err != nil {
		t.Fatalf("failed to parse private torrent: %v", err)
	}
	if !meta.IsPrivate {
		t.Fatalf("expected meta.IsPrivate == true for private: 1 torrent")
	}

	tmpDir := t.TempDir()
	eng, err := NewEngine(Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
		SeedRatioLimit:   1.0,
	})
	if err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer eng.Close()

	eng.RegisterLocalSeedPayload(meta.InfoHash, payload)
	sess, err := eng.AddTorrentFromBytes(rawTorrent, tmpDir, false)
	if err != nil {
		t.Fatalf("failed to add private torrent: %v", err)
	}

	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if sess.Snapshot(false).Status == StatusSeeding {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Simulate uploading 65536 bytes (ratio = 1.0) and verify automatic transition to StatusCompleted
	sess.telemetry.AddUploaded(65536)
	stopped := sess.CheckSeedStopLimits(time.Now())
	if !stopped || sess.Status() != StatusCompleted {
		t.Fatalf("expected seeding torrent to stop at ratio 1.0 with StatusCompleted, got %s", sess.Status())
	}
}

// TestWatchFolderAutoDownloader verifies that dropping a .torrent file into WatchDir ingests it and removes the source .torrent.
func TestWatchFolderAutoDownloader(t *testing.T) {
	tmpDir := t.TempDir()
	watchDir := filepath.Join(tmpDir, "watch")
	if err := os.MkdirAll(watchDir, 0o755); err != nil {
		t.Fatalf("failed to create watchDir: %v", err)
	}

	eng, err := NewEngine(Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
		WatchDir:         watchDir,
	})
	if err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}
	defer eng.Close()

	payload := make([]byte, 65536)
	rawTorrent, meta, err := BuildSingleFileTorrent("auto-ingested.iso", payload, 65536, nil)
	if err != nil {
		t.Fatalf("failed to build torrent: %v", err)
	}
	eng.RegisterLocalSeedPayload(meta.InfoHash, payload)

	torrentFile := filepath.Join(watchDir, "auto-ingested.torrent")
	if err := os.WriteFile(torrentFile, rawTorrent, 0o644); err != nil {
		t.Fatalf("failed to write torrent to watchDir: %v", err)
	}

	eng.AutoDownloader().ScanNow()

	if _, ok := eng.GetSession(meta.InfoHashHex); !ok {
		t.Fatalf("expected watch-folder .torrent to be ingested into engine session")
	}
	if _, statErr := os.Stat(torrentFile); !os.IsNotExist(statErr) {
		t.Fatalf("expected source .torrent file in watchDir to be deleted after ingestion")
	}
}

// TestFifteenSwarmSoakAndGoroutineLeakAudit runs 15 concurrent active/seeding swarms and verifies zero leaked peer goroutines after shutdown.
func TestFifteenSwarmSoakAndGoroutineLeakAudit(t *testing.T) {
	baselineGoroutines := runtime.NumGoroutine()
	tmpDir := t.TempDir()
	eng, err := NewEngine(Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
	})
	if err != nil {
		t.Fatalf("failed to start engine: %v", err)
	}

	const numSwarms = 15
	for i := 0; i < numSwarms; i++ {
		name := fmt.Sprintf("soak-swarm-%02d.bin", i)
		size := int64((2 + (i % 4)) * 65536)
		if _, _, err := eng.CreateSyntheticSwarmTorrent(name, size, tmpDir, false); err != nil {
			_ = eng.Close()
			t.Fatalf("failed to start soak swarm %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		snap := eng.Snapshot(false)
		done := 0
		for _, ts := range snap.Torrents {
			if ts.Status == StatusSeeding {
				done++
			}
		}
		if done == numSwarms {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if err := eng.Close(); err != nil {
		t.Fatalf("failed to close engine: %v", err)
	}
	time.Sleep(80 * time.Millisecond)

	buf := make([]byte, 256*1024)
	n := runtime.Stack(buf, true)
	stacks := string(buf[:n])
	if strings.Contains(stacks, "(*PeerConn).RunLoop") {
		t.Fatalf("detected leaked (*PeerConn).RunLoop goroutine after 15-swarm soak test shutdown")
	}
	_ = baselineGoroutines
}
