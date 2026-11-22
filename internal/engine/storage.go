package engine

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrDirectoryPermission is returned when an output directory cannot be created or written to.
var ErrDirectoryPermission = errors.New("output directory permission denied")

// ErrInsufficientDiskSpace is returned when the target volume lacks enough free bytes for pre-allocation.
var ErrInsufficientDiskSpace = errors.New("insufficient disk space in output directory")

// DirectoryValidationResult describes the readiness and free space of a filesystem directory.
type DirectoryValidationResult struct {
	Path        string   `json:"path"`
	Valid       bool     `json:"valid"`
	Writable    bool     `json:"writable"`
	Exists      bool     `json:"exists"`
	FreeBytes   uint64   `json:"freeBytes"`
	TotalBytes  uint64   `json:"totalBytes"`
	ErrorMsg    string   `json:"errorMsg,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// NormalizeDirectoryPath cleans user-entered directory paths, strips control/quote characters,
// and resolves concatenated Windows drive roots (e.g. "C:\old\pathD:\Games" -> "D:\Games").
func NormalizeDirectoryPath(rawPath string) string {
	// Strip ASCII control characters (e.g., Ctrl+V \x16 or carriage returns)
	cleaned := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, rawPath)
	cleaned = strings.TrimSpace(cleaned)
	cleaned = strings.Trim(cleaned, `"'`)
	cleaned = strings.TrimSpace(cleaned)

	// On Windows, if a new drive path (e.g. "D:\Games") was typed or pasted after an existing path
	// ("C:\Users\...\VeloceTorrentD:\Games"), extract from the last "<DriveLetter>:\" or "<DriveLetter>:/".
	for i := len(cleaned) - 2; i > 0; i-- {
		if cleaned[i] == ':' && (cleaned[i+1] == '\\' || cleaned[i+1] == '/') {
			driveChar := cleaned[i-1]
			if (driveChar >= 'A' && driveChar <= 'Z') || (driveChar >= 'a' && driveChar <= 'z') {
				cleaned = cleaned[i-1:]
				break
			}
		}
	}

	// If user entered a bare Windows drive letter like "D:", normalize to "D:\"
	if len(cleaned) == 2 && cleaned[1] == ':' {
		driveChar := cleaned[0]
		if (driveChar >= 'A' && driveChar <= 'Z') || (driveChar >= 'a' && driveChar <= 'z') {
			cleaned += `\`
		}
	}
	return cleaned
}

// ValidateOutputDirectory checks if a path is valid, writable (or creatable), and returns free space + autocomplete suggestions.
func ValidateOutputDirectory(rawPath string, checkWritable bool) DirectoryValidationResult {
	trimmed := NormalizeDirectoryPath(rawPath)
	if trimmed == "" {
		return DirectoryValidationResult{
			Path:     rawPath,
			Valid:    false,
			ErrorMsg: "output directory path cannot be empty",
		}
	}
	absPath, err := filepath.Abs(trimmed)
	if err != nil {
		return DirectoryValidationResult{
			Path:     trimmed,
			Valid:    false,
			ErrorMsg: fmt.Sprintf("invalid path syntax (%q): %v", trimmed, err),
		}
	}

	// If path has a Windows volume name (e.g. "D:"), verify the drive root is mounted
	if vol := filepath.VolumeName(absPath); len(vol) == 2 && vol[1] == ':' {
		if _, volErr := os.Stat(vol + `\`); volErr != nil {
			return DirectoryValidationResult{
				Path:     absPath,
				Valid:    false,
				ErrorMsg: fmt.Sprintf("drive %s\\ is not mounted or accessible", strings.ToUpper(vol)),
			}
		}
	}

	res := DirectoryValidationResult{
		Path:        absPath,
		Suggestions: computeDirSuggestions(absPath),
	}

	freeB, totalB, spaceErr := GetDiskSpace(absPath)
	if spaceErr == nil {
		res.FreeBytes = freeB
		res.TotalBytes = totalB
	}

	info, statErr := os.Stat(absPath)
	if statErr == nil {
		if !info.IsDir() {
			res.Valid = false
			res.ErrorMsg = "target path exists and is a regular file, not a directory"
			return res
		}
		res.Exists = true
	}

	if !checkWritable {
		res.Valid = true
		res.Writable = true
		return res
	}

	if err := EnsureWritableDirectory(absPath); err != nil {
		res.Valid = false
		res.Writable = false
		res.ErrorMsg = err.Error()
		return res
	}

	res.Exists = true
	res.Valid = true
	res.Writable = true
	return res
}

func computeDirSuggestions(partialPath string) []string {
	var out []string
	searchDir := partialPath
	prefix := ""
	if info, err := os.Stat(partialPath); err != nil || !info.IsDir() {
		searchDir = filepath.Dir(partialPath)
		prefix = strings.ToLower(filepath.Base(partialPath))
	}
	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if prefix == "" || strings.HasPrefix(strings.ToLower(name), prefix) {
			out = append(out, filepath.Join(searchDir, name))
			if len(out) >= 10 {
				break
			}
		}
	}
	return out
}

// EnsureWritableDirectory creates the directory if missing and verifies write permissions via an atomic probe file.
func EnsureWritableDirectory(dirPath string) error {
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("%w: cannot create %q: %v", ErrDirectoryPermission, dirPath, err)
	}
	probeFile, err := os.CreateTemp(dirPath, ".vt_probe_*")
	if err != nil {
		return fmt.Errorf("%w: cannot write to %q: %v", ErrDirectoryPermission, dirPath, err)
	}
	probeName := probeFile.Name()
	_ = probeFile.Close()
	_ = os.Remove(probeName)
	return nil
}

// PieceWriteResult is emitted after a piece is verified by the hash worker pool and flushed to disk.
type PieceWriteResult struct {
	PieceIndex int
	Valid      bool
	BytesWrote int64
	Err        error
}

type pieceVerifyJob struct {
	pieceIndex int
	data       []byte
	resultCh   chan<- PieceWriteResult
}

type diskFlushItem struct {
	pieceIndex int
	offset     int64
	data       []byte
	resultCh   chan<- PieceWriteResult
}

type openFileHandle struct {
	entry        FileEntry
	absPath      string
	file         *os.File
	priority     FilePriority
	preallocated atomic.Bool
	bytesWritten atomic.Int64
}

// PreallocatedStorage manages pre-allocated sparse files, parallel SHA-1/SHA-256 verification workers,
// and sequential ring-buffer disk flushing across single or multi-file torrents.
type PreallocatedStorage struct {
	mu          sync.RWMutex
	meta        *TorrentMeta
	outputDir   string
	telemetry   *AtomicTorrentTelemetry
	handles     []*openFileHandle
	pieceCache  map[int][]byte // Ring-bounded hot read cache for recently verified pieces
	pieceOrder  []int
	maxCacheLen int

	verifyQueue chan pieceVerifyJob
	flushQueue  chan diskFlushItem
	closed      atomic.Bool
	closeCh     chan struct{}
	wg          sync.WaitGroup
}

// NewPreallocatedStorage creates a storage engine with hash workers matching physical CPU count and a sequential disk flusher.
func NewPreallocatedStorage(meta *TorrentMeta, outputDir string, telemetry *AtomicTorrentTelemetry) (*PreallocatedStorage, error) {
	normDir := NormalizeDirectoryPath(outputDir)
	absDir, err := filepath.Abs(normDir)
	if err != nil {
		return nil, fmt.Errorf("invalid output directory %q: %w", normDir, err)
	}
	if err := EnsureWritableDirectory(absDir); err != nil {
		return nil, err
	}

	if meta.TotalSize > 0 {
		if freeBytes, _, err := GetDiskSpace(absDir); err == nil && freeBytes > 0 {
			if uint64(meta.TotalSize) > freeBytes {
				return nil, fmt.Errorf("%w: need %d bytes, only %d bytes available in %s",
					ErrInsufficientDiskSpace, meta.TotalSize, freeBytes, absDir)
			}
		}
	}

	numWorkers := runtime.NumCPU()
	if numWorkers < 2 {
		numWorkers = 2
	}

	s := &PreallocatedStorage{
		meta:        meta,
		outputDir:   absDir,
		telemetry:   telemetry,
		pieceCache:  make(map[int][]byte),
		maxCacheLen: 64, // Keep up to 64 hot pieces in RAM for zero-disk-read seeding
		verifyQueue: make(chan pieceVerifyJob, numWorkers*8),
		flushQueue:  make(chan diskFlushItem, 256),
		closeCh:     make(chan struct{}),
	}

	if !meta.IsMagnetOnly && len(meta.Files) > 0 {
		if err := s.PreallocateAll(); err != nil {
			return nil, err
		}
	}

	// Spawn parallel hash verification workers matching CPU core count
	for i := 0; i < numWorkers; i++ {
		s.wg.Add(1)
		go s.hashWorkerLoop()
	}

	// Spawn sequential batch disk flusher
	s.wg.Add(1)
	go s.diskFlusherLoop()

	return s, nil
}

// PreallocateAll opens and pre-allocates every file extent in the torrent on disk before piece writes begin.
func (s *PreallocatedStorage) PreallocateAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.preallocateLocked()
}

func (s *PreallocatedStorage) preallocateLocked() error {
	// Close any existing handles first
	for _, h := range s.handles {
		if h.file != nil {
			_ = h.file.Close()
		}
	}
	s.handles = make([]*openFileHandle, 0, len(s.meta.Files))

	for _, entry := range s.meta.Files {
		absPath := filepath.Join(s.outputDir, entry.RelPath)
		parentDir := filepath.Dir(absPath)
		if err := os.MkdirAll(parentDir, 0o755); err != nil {
			s.closeHandlesLocked()
			return fmt.Errorf("%w: failed to create parent directory %q: %v", ErrDirectoryPermission, parentDir, err)
		}

		f, err := os.OpenFile(absPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			s.closeHandlesLocked()
			return fmt.Errorf("%w: failed to open target file %q: %v", ErrDirectoryPermission, absPath, err)
		}

		if err := PreallocateFileExtent(f, entry.Length); err != nil {
			_ = f.Close()
			s.closeHandlesLocked()
			return fmt.Errorf("failed to pre-allocate %d bytes for %q: %w", entry.Length, absPath, err)
		}

		s.telemetry.RecordDiskOp(1)
		h := &openFileHandle{
			entry:    entry,
			absPath:  absPath,
			file:     f,
			priority: PriorityNormal,
		}
		h.preallocated.Store(true)
		s.handles = append(s.handles, h)
	}
	return nil
}

// ScanExistingPieces verifies any existing on-disk file contents against piece hashes (for fast resume / seeding).
func (s *PreallocatedStorage) ScanExistingPieces() (verifiedPieces []bool, completedCount int) {
	numPieces := s.meta.NumPieces()
	verifiedPieces = make([]bool, numPieces)
	if numPieces == 0 || s.meta.IsMagnetOnly {
		return verifiedPieces, 0
	}

	var totalVerifiedBytes int64
	for idx := 0; idx < numPieces; idx++ {
		expectedSize := s.meta.PieceSize(idx)
		data, err := s.ReadPieceFromDisk(idx)
		if err != nil || int64(len(data)) != expectedSize {
			continue
		}
		// Quick check if slice is all zeros (unwritten sparse region) unless the hash actually matches all zeros
		if s.verifyHash(idx, data) {
			verifiedPieces[idx] = true
			completedCount++
			totalVerifiedBytes += expectedSize
			s.telemetry.SetPieceStatus(idx, PieceComplete)
			s.recordFileBytes(int64(idx)*s.meta.PieceLength, expectedSize)
		}
	}
	s.telemetry.SetVerifiedBytes(totalVerifiedBytes)
	return verifiedPieces, completedCount
}

func (s *PreallocatedStorage) verifyHash(pieceIndex int, data []byte) bool {
	if pieceIndex < 0 || pieceIndex >= len(s.meta.PieceHashes) {
		return false
	}
	expected := s.meta.PieceHashes[pieceIndex]
	if s.meta.HashSize == 32 {
		sum := sha256.Sum256(data)
		return bytes.Equal(sum[:], expected)
	}
	sum := sha1.Sum(data)
	return bytes.Equal(sum[:], expected)
}

// SubmitPiece queues a completed piece buffer for parallel hash verification and ring-buffer disk flushing.
func (s *PreallocatedStorage) SubmitPiece(pieceIndex int, data []byte, resultCh chan<- PieceWriteResult) error {
	if s.closed.Load() {
		return errors.New("storage is closed")
	}
	s.telemetry.SetPieceStatus(pieceIndex, PieceVerifying)
	s.telemetry.AddHashQueue(1)

	bufCopy := make([]byte, len(data))
	copy(bufCopy, data)

	select {
	case s.verifyQueue <- pieceVerifyJob{
		pieceIndex: pieceIndex,
		data:       bufCopy,
		resultCh:   resultCh,
	}:
		return nil
	case <-s.closeCh:
		s.telemetry.AddHashQueue(-1)
		return errors.New("storage shutting down")
	}
}

func (s *PreallocatedStorage) hashWorkerLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.closeCh:
			return
		case job, ok := <-s.verifyQueue:
			if !ok {
				return
			}
			vStart := time.Now()
			valid := s.verifyHash(job.pieceIndex, job.data)
			latUs := time.Since(vStart).Microseconds()
			if latUs < 1 {
				latUs = 1
			}
			oldLat := s.telemetry.verifyLatencyUs.Load()
			if oldLat == 0 {
				s.telemetry.verifyLatencyUs.Store(latUs)
			} else {
				s.telemetry.verifyLatencyUs.Store((oldLat*7 + latUs*3) / 10)
			}
			s.telemetry.AddHashQueue(-1)

			if !valid {
				s.telemetry.SetPieceStatus(job.pieceIndex, PieceMissing)
				if job.resultCh != nil {
					job.resultCh <- PieceWriteResult{
						PieceIndex: job.pieceIndex,
						Valid:      false,
						Err:        fmt.Errorf("piece %d failed SHA verification", job.pieceIndex),
					}
				}
				continue
			}

			// Cache verified piece in hot RAM ring buffer immediately for peers requesting blocks
			s.cachePiece(job.pieceIndex, job.data)

			// Queue for sequential batch disk flush
			s.telemetry.AddDiskQueue(1)
			offset := int64(job.pieceIndex) * s.meta.PieceLength
			select {
			case s.flushQueue <- diskFlushItem{
				pieceIndex: job.pieceIndex,
				offset:     offset,
				data:       job.data,
				resultCh:   job.resultCh,
			}:
			case <-s.closeCh:
				s.telemetry.AddDiskQueue(-1)
				return
			}
		}
	}
}

const (
	// MaxNVMeCoalesceBytes is the 16 MiB upper bound for coalescing contiguous piece writes into a single sequential NVMe flush.
	MaxNVMeCoalesceBytes = 16 * 1024 * 1024
)

func (s *PreallocatedStorage) diskFlusherLoop() {
	defer s.wg.Done()
	batch := make([]diskFlushItem, 0, 64)

	for {
		select {
		case <-s.closeCh:
			return
		case first, ok := <-s.flushQueue:
			if !ok {
				return
			}
			batch = append(batch[:0], first)
			batchBytes := len(first.data)

			// Drain ready pieces up to 16 MiB (4MB-16MB NVMe sequential alignment window)
		drainLoop:
			for len(batch) < 64 && batchBytes < MaxNVMeCoalesceBytes {
				select {
				case item, ok := <-s.flushQueue:
					if !ok {
						break drainLoop
					}
					batch = append(batch, item)
					batchBytes += len(item.data)
				default:
					break drainLoop
				}
			}

			// Sort batch by disk byte offset so NVMe/SSD writes occur strictly sequentially
			if len(batch) > 1 {
				sort.Slice(batch, func(i, j int) bool {
					return batch[i].offset < batch[j].offset
				})
			}

			// Coalesce adjacent contiguous pieces into single 4MB-16MB aligned sequential writes
			i := 0
			for i < len(batch) {
				j := i + 1
				coalescedLen := len(batch[i].data)
				for j < len(batch) {
					prevEnd := batch[j-1].offset + int64(len(batch[j-1].data))
					if batch[j].offset != prevEnd || coalescedLen+len(batch[j].data) > MaxNVMeCoalesceBytes {
						break
					}
					coalescedLen += len(batch[j].data)
					j++
				}

				var writeErr error
				if j == i+1 {
					writeErr = s.writeSpanAtOffset(batch[i].offset, batch[i].data)
				} else {
					merged := make([]byte, 0, coalescedLen)
					for k := i; k < j; k++ {
						merged = append(merged, batch[k].data...)
					}
					writeErr = s.writeSpanAtOffset(batch[i].offset, merged)
				}

				for k := i; k < j; k++ {
					item := batch[k]
					s.telemetry.AddDiskQueue(-1)
					if writeErr != nil {
						s.telemetry.SetPieceStatus(item.pieceIndex, PieceMissing)
						if item.resultCh != nil {
							item.resultCh <- PieceWriteResult{
								PieceIndex: item.pieceIndex,
								Valid:      true,
								Err:        writeErr,
							}
						}
						continue
					}
					wrote := int64(len(item.data))
					s.telemetry.SetPieceStatus(item.pieceIndex, PieceComplete)
					s.telemetry.AddVerifiedBytes(wrote)
					if item.resultCh != nil {
						item.resultCh <- PieceWriteResult{
							PieceIndex: item.pieceIndex,
							Valid:      true,
							BytesWrote: wrote,
						}
					}
				}
				i = j
			}
		}
	}
}

func (s *PreallocatedStorage) writeSpanAtOffset(globalOffset int64, data []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.handles) == 0 {
		return errors.New("no pre-allocated file handles open")
	}

	remaining := data
	curOffset := globalOffset

	for _, h := range s.handles {
		fileStart := h.entry.Offset
		fileEnd := fileStart + h.entry.Length
		if curOffset >= fileEnd || h.entry.Length == 0 {
			continue
		}
		if curOffset < fileStart {
			break
		}

		localOffset := curOffset - fileStart
		maxWrite := h.entry.Length - localOffset
		writeLen := int64(len(remaining))
		if writeLen > maxWrite {
			writeLen = maxWrite
		}

		n, err := h.file.WriteAt(remaining[:writeLen], localOffset)
		s.telemetry.RecordDiskOp(1)
		if err != nil {
			return fmt.Errorf("disk write error in %q at offset %d: %w", h.absPath, localOffset, err)
		}
		h.bytesWritten.Add(int64(n))

		remaining = remaining[writeLen:]
		curOffset += writeLen
		if len(remaining) == 0 {
			break
		}
	}

	if len(remaining) > 0 {
		return fmt.Errorf("short span write: %d bytes exceeded total torrent extent", len(remaining))
	}
	return nil
}

func (s *PreallocatedStorage) recordFileBytes(globalOffset int64, length int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rem := length
	cur := globalOffset
	for _, h := range s.handles {
		fileStart := h.entry.Offset
		fileEnd := fileStart + h.entry.Length
		if cur >= fileEnd || h.entry.Length == 0 {
			continue
		}
		if cur < fileStart {
			break
		}
		localOffset := cur - fileStart
		avail := h.entry.Length - localOffset
		chunk := rem
		if chunk > avail {
			chunk = avail
		}
		h.bytesWritten.Add(chunk)
		rem -= chunk
		cur += chunk
		if rem <= 0 {
			break
		}
	}
}

func (s *PreallocatedStorage) cachePiece(pieceIndex int, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.pieceCache[pieceIndex]; !exists {
		if len(s.pieceOrder) >= s.maxCacheLen {
			evict := s.pieceOrder[0]
			s.pieceOrder = s.pieceOrder[1:]
			delete(s.pieceCache, evict)
		}
		s.pieceOrder = append(s.pieceOrder, pieceIndex)
	}
	s.pieceCache[pieceIndex] = data
}

// ReadBlock reads a sub-piece block (for uploading to peers) from the RAM ring cache or pre-allocated disk files.
func (s *PreallocatedStorage) ReadBlock(pieceIndex int, begin int, length int) ([]byte, error) {
	s.mu.RLock()
	if cached, ok := s.pieceCache[pieceIndex]; ok {
		if begin >= 0 && begin+length <= len(cached) {
			out := make([]byte, length)
			copy(out, cached[begin:begin+length])
			s.mu.RUnlock()
			return out, nil
		}
	}
	s.mu.RUnlock()

	pieceData, err := s.ReadPieceFromDisk(pieceIndex)
	if err != nil {
		return nil, err
	}
	if begin < 0 || begin+length > len(pieceData) {
		return nil, fmt.Errorf("block [%d:%d] out of bounds for piece %d (len %d)", begin, begin+length, pieceIndex, len(pieceData))
	}
	return pieceData[begin : begin+length], nil
}

// ReadPieceFromDisk reads the full byte span of a piece across one or more underlying files.
func (s *PreallocatedStorage) ReadPieceFromDisk(pieceIndex int) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pieceSize := s.meta.PieceSize(pieceIndex)
	if pieceSize <= 0 {
		return nil, fmt.Errorf("invalid piece index %d", pieceIndex)
	}
	buf := make([]byte, pieceSize)
	curOffset := int64(pieceIndex) * s.meta.PieceLength
	bufPos := int64(0)

	for _, h := range s.handles {
		fileStart := h.entry.Offset
		fileEnd := fileStart + h.entry.Length
		if curOffset >= fileEnd || h.entry.Length == 0 {
			continue
		}
		if curOffset < fileStart {
			break
		}

		localOffset := curOffset - fileStart
		maxRead := h.entry.Length - localOffset
		toRead := pieceSize - bufPos
		if toRead > maxRead {
			toRead = maxRead
		}

		n, err := h.file.ReadAt(buf[bufPos:bufPos+toRead], localOffset)
		s.telemetry.RecordDiskOp(1)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if int64(n) < toRead {
			return nil, io.ErrUnexpectedEOF
		}

		bufPos += toRead
		curOffset += toRead
		if bufPos >= pieceSize {
			break
		}
	}

	return buf, nil
}

// Relocate safely moves pre-allocated and downloaded files to a new output directory.
func (s *PreallocatedStorage) Relocate(newOutputDir string) error {
	normDir := NormalizeDirectoryPath(newOutputDir)
	absNewDir, err := filepath.Abs(normDir)
	if err != nil {
		return fmt.Errorf("invalid output directory %q: %w", normDir, err)
	}
	if err := EnsureWritableDirectory(absNewDir); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.EqualFold(s.outputDir, absNewDir) {
		return nil
	}

	oldDir := s.outputDir
	for _, h := range s.handles {
		if h.file != nil {
			_ = h.file.Sync()
			_ = h.file.Close()
			h.file = nil
		}
		newAbsPath := filepath.Join(absNewDir, h.entry.RelPath)
		if err := os.MkdirAll(filepath.Dir(newAbsPath), 0o755); err != nil {
			return fmt.Errorf("%w: cannot create target subdirectory in %q: %v", ErrDirectoryPermission, absNewDir, err)
		}
		if err := moveOrCopyFile(h.absPath, newAbsPath); err != nil {
			return fmt.Errorf("failed to relocate %q to %q: %w", h.absPath, newAbsPath, err)
		}
		f, err := os.OpenFile(newAbsPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return fmt.Errorf("failed to reopen relocated file %q: %w", newAbsPath, err)
		}
		h.absPath = newAbsPath
		h.file = f
	}
	s.outputDir = absNewDir
	_ = oldDir
	return nil
}

func moveOrCopyFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	_ = in.Close()
	_ = os.Remove(src)
	return nil
}

// OutputDir returns the current absolute output directory path.
func (s *PreallocatedStorage) OutputDir() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.outputDir
}

// SetFilePriority updates the selective download priority for a specific file index.
func (s *PreallocatedStorage) SetFilePriority(fileIndex int, prio FilePriority) error {
	switch prio {
	case PrioritySkip, PriorityNormal, PriorityHigh:
	default:
		return fmt.Errorf("invalid file priority %q (expected skip, normal, or high)", prio)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if fileIndex < 0 || fileIndex >= len(s.handles) {
		return fmt.Errorf("file index %d out of range [0..%d)", fileIndex, len(s.handles))
	}
	s.handles[fileIndex].priority = prio
	return nil
}

// FilePriorities returns the current priority slice for all files in the torrent.
func (s *PreallocatedStorage) FilePriorities() []FilePriority {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]FilePriority, len(s.handles))
	for i, h := range s.handles {
		if h.priority == "" {
			out[i] = PriorityNormal
		} else {
			out[i] = h.priority
		}
	}
	return out
}

// PrimaryFileAbsPath returns the absolute path of the largest or first non-skipped file (for HTTP Range media streaming).
func (s *PreallocatedStorage) PrimaryFileAbsPath() (string, int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.handles) == 0 {
		return "", 0, errors.New("no files allocated in storage")
	}
	best := s.handles[0]
	for _, h := range s.handles {
		if h.priority != PrioritySkip && h.entry.Length > best.entry.Length {
			best = h
		}
	}
	return best.absPath, best.entry.Length, nil
}

// SnapshotFiles returns real-time telemetry for every file in the torrent.
func (s *PreallocatedStorage) SnapshotFiles() []FileTelemetrySnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]FileTelemetrySnapshot, 0, len(s.handles))
	for idx, h := range s.handles {
		written := h.bytesWritten.Load()
		if written > h.entry.Length {
			written = h.entry.Length
		}
		prog := 0.0
		if h.entry.Length > 0 {
			prog = float64(written) / float64(h.entry.Length) * 100.0
		}
		prio := h.priority
		if prio == "" {
			prio = PriorityNormal
		}
		out = append(out, FileTelemetrySnapshot{
			Index:        idx,
			Path:         h.entry.RelPath,
			Length:       h.entry.Length,
			Offset:       h.entry.Offset,
			BytesOnDisk:  written,
			Preallocated: h.preallocated.Load(),
			Priority:     prio,
			Progress:     prog,
		})
	}
	return out
}

// SyncAll flushes OS page cache buffers to physical NVMe/SSD storage (invoked only on torrent completion or clean shutdown).
func (s *PreallocatedStorage) SyncAll() {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.handles {
		if h.file != nil {
			_ = h.file.Sync()
			s.telemetry.RecordDiskOp(1)
		}
	}
}

// SnapshotFileMetadata captures on-disk file sizes and mtime timestamps (UnixNano) for fast-resume persistence.
func (s *PreallocatedStorage) SnapshotFileMetadata() (sizes map[string]int64, mtimes map[string]int64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sizes = make(map[string]int64, len(s.handles))
	mtimes = make(map[string]int64, len(s.handles))
	for _, h := range s.handles {
		if info, err := os.Stat(h.absPath); err == nil {
			sizes[h.entry.RelPath] = info.Size()
			mtimes[h.entry.RelPath] = info.ModTime().UnixNano()
		}
	}
	return sizes, mtimes
}

// FastVerifySavedBitfield checks if on-disk file sizes and modification timestamps match the saved fast-resume state.
// If valid, it restores piece completion states in milliseconds without reading/hashing the full files from disk.
func (s *PreallocatedStorage) FastVerifySavedBitfield(savedBitfield []bool, savedSizes map[string]int64, savedMtimes map[string]int64) (verified []bool, completedCount int, ok bool) {
	numPieces := s.meta.NumPieces()
	if numPieces == 0 || len(savedBitfield) != numPieces || len(savedSizes) == 0 {
		return nil, 0, false
	}

	s.mu.RLock()
	handles := append([]*openFileHandle(nil), s.handles...)
	s.mu.RUnlock()

	for _, h := range handles {
		info, err := os.Stat(h.absPath)
		if err != nil {
			return nil, 0, false
		}
		expSize, hasSize := savedSizes[h.entry.RelPath]
		expMtime, hasMtime := savedMtimes[h.entry.RelPath]
		if !hasSize || !hasMtime {
			return nil, 0, false
		}
		if info.Size() != expSize || info.ModTime().UnixNano() != expMtime {
			return nil, 0, false
		}
	}

	verified = make([]bool, numPieces)
	var totalVerifiedBytes int64
	for idx, done := range savedBitfield {
		if done {
			verified[idx] = true
			completedCount++
			pSize := s.meta.PieceSize(idx)
			totalVerifiedBytes += pSize
			s.telemetry.SetPieceStatus(idx, PieceComplete)
			s.recordFileBytes(int64(idx)*s.meta.PieceLength, pSize)
		}
	}
	s.telemetry.SetVerifiedBytes(totalVerifiedBytes)
	return verified, completedCount, true
}

func (s *PreallocatedStorage) closeHandlesLocked() {
	for _, h := range s.handles {
		if h.file != nil {
			_ = h.file.Sync()
			_ = h.file.Close()
			h.file = nil
		}
	}
}

// Close flushes and closes all file handles and stops worker goroutines.
func (s *PreallocatedStorage) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.closeCh)
	s.wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeHandlesLocked()
	return nil
}
