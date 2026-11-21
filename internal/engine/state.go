package engine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var stateDBMagic = [5]byte{'V', 'T', 'D', 'B', 0x01}

// DefaultStateDBPath returns ~/.config/velocetorrent/state.db.
func DefaultStateDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "velocetorrent", "state.db")
	}
	return filepath.Join(home, ".config", "velocetorrent", "state.db")
}

// PersistedTorrentState stores all metadata, bitfields, file mtime stamps, and peer caches for fast-resume.
type PersistedTorrentState struct {
	InfoHashHex      string           `json:"infoHashHex"`
	InfoHash         [20]byte         `json:"infoHash"`
	Name             string           `json:"name"`
	TotalSize        int64            `json:"totalSize"`
	PieceLength      int64            `json:"pieceLength"`
	PieceHashes      [][]byte         `json:"pieceHashes"`
	HashSize         int              `json:"hashSize"`
	Files            []FileEntry      `json:"files"`
	Trackers         []string         `json:"trackers"`
	WebSeeds         []string         `json:"webSeeds"`
	PeerHints        []string         `json:"peerHints"`
	RawInfoBytes     []byte           `json:"rawInfoBytes"`
	IsMagnetOnly     bool             `json:"isMagnetOnly"`
	OutputDir        string           `json:"outputDir"`
	Paused           bool             `json:"paused"`
	Sequential       bool             `json:"sequential"`
	AddedAt          int64            `json:"addedAt"`
	CompletedAt      int64            `json:"completedAt"`
	Downloaded       int64            `json:"downloaded"`
	Uploaded         int64            `json:"uploaded"`
	VerifiedBitfield []bool           `json:"verifiedBitfield"`
	KnownPeers       []string         `json:"knownPeers"`
	FileSizes        map[string]int64 `json:"fileSizes"`
	FileMtimes       map[string]int64 `json:"fileMtimes"`
}

// PersistedEngineState represents the full transactional snapshot saved in state.db.
type PersistedEngineState struct {
	Version           int                     `json:"version"`
	UpdatedAtMs       int64                   `json:"updatedAtMs"`
	DefaultOutputDir  string                  `json:"defaultOutputDir"`
	SavedDirs         []string                `json:"savedDirs"`
	OnCompleteCmd     string                  `json:"onCompleteCmd,omitempty"`
	OnCompleteWebhook string                  `json:"onCompleteWebhook,omitempty"`
	Torrents          []PersistedTorrentState `json:"torrents"`
}

// StateStore manages crash-safe atomic persistence of engine state with a 5-second debounce timer.
type StateStore struct {
	path    string
	eng     *Engine
	writeMu sync.Mutex
	dirty   atomic.Bool
	closed  atomic.Bool
	closeCh chan struct{}
	wg      sync.WaitGroup
}

// NewStateStore initializes the fast-resume store at dbPath and starts the 5s debounce writer loop.
func NewStateStore(dbPath string, eng *Engine) (*StateStore, error) {
	if dbPath == "" {
		dbPath = DefaultStateDBPath()
	}
	absPath, err := filepath.Abs(dbPath)
	if err == nil {
		dbPath = absPath
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("cannot create state.db directory: %w", err)
	}

	st := &StateStore{
		path:    dbPath,
		eng:     eng,
		closeCh: make(chan struct{}),
	}
	st.wg.Add(1)
	go st.debounceLoop()
	return st, nil
}

// Path returns the filesystem path of state.db.
func (s *StateStore) Path() string {
	return s.path
}

// MarkDirty flags the session state as modified so the 5-second debounce timer flushes it to disk.
func (s *StateStore) MarkDirty() {
	s.dirty.Store(true)
}

func (s *StateStore) debounceLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-s.closeCh:
			return
		case <-ticker.C:
			if s.dirty.CompareAndSwap(true, false) {
				_ = s.FlushNow()
			}
		}
	}
}

// FlushNow captures the current engine state and commits it atomically to state.db with CRC32 integrity framing.
func (s *StateStore) FlushNow() error {
	if s.eng == nil {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	state := s.eng.buildPersistedState()
	jsonBytes, err := json.Marshal(state)
	if err != nil {
		return err
	}

	checksum := crc32.ChecksumIEEE(jsonBytes)
	var buf bytes.Buffer
	buf.Write(stateDBMagic[:])
	var crcBuf [4]byte
	binary.BigEndian.PutUint32(crcBuf[:], checksum)
	buf.Write(crcBuf[:])
	buf.Write(jsonBytes)

	tmpPath := s.path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		_ = f.Close()
		return err
	}
	_ = f.Sync()
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, s.path)
}

// Load reads and verifies state.db from disk. Returns nil if state.db does not exist yet.
func (s *StateStore) Load() (*PersistedEngineState, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(raw) < 9 {
		return nil, errors.New("corrupt state.db: file too short")
	}
	if !bytes.Equal(raw[:5], stateDBMagic[:]) {
		return nil, errors.New("corrupt state.db: invalid header magic")
	}
	expectedCRC := binary.BigEndian.Uint32(raw[5:9])
	payload := raw[9:]
	if crc32.ChecksumIEEE(payload) != expectedCRC {
		return nil, errors.New("corrupt state.db: CRC32 checksum mismatch")
	}

	var state PersistedEngineState
	if err := json.Unmarshal(payload, &state); err != nil {
		return nil, err
	}
	return &state, nil
}

// Close stops the debounce timer and performs a final synchronous flush of state.db.
func (s *StateStore) Close() error {
	if !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	close(s.closeCh)
	s.wg.Wait()
	return s.FlushNow()
}
