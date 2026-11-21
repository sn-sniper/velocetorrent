package engine

import (
	"fmt"
	"testing"
	"time"
)

func BenchmarkHighThroughputSwarmTransfer(b *testing.B) {
	const payloadSize = 8 * 1024 * 1024 // 8 MiB per iteration
	b.SetBytes(payloadSize)
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tmpDir := b.TempDir()
		eng, err := NewEngine(Config{
			DefaultOutputDir: tmpDir,
			ListenAddr:       "127.0.0.1:0",
		})
		if err != nil {
			b.Fatalf("failed to start engine: %v", err)
		}

		name := fmt.Sprintf("bench-iso-%d.bin", i)
		sess, _, err := eng.CreateSyntheticSwarmTorrent(name, payloadSize, tmpDir, false)
		if err != nil {
			_ = eng.Close()
			b.Fatalf("failed to create swarm torrent: %v", err)
		}

		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			snap := sess.Snapshot(false)
			if snap.Status == StatusSeeding && snap.CompletedPieces == snap.PieceCount {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}

		snap := sess.Snapshot(false)
		_ = eng.Close()
		if snap.Status != StatusSeeding {
			b.Fatalf("benchmark transfer incomplete: %s (%d/%d)", snap.Status, snap.CompletedPieces, snap.PieceCount)
		}
	}
}
