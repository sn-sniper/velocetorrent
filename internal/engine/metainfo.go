package engine

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

// FileEntry describes a single target file within a single-file or multi-file torrent.
type FileEntry struct {
	RelPath string `json:"path"`
	Length  int64  `json:"length"`
	Offset  int64  `json:"offset"`
}

// TorrentMeta holds normalized metadata parsed from a .torrent file or magnet link.
type TorrentMeta struct {
	InfoHash     [20]byte    `json:"-"`
	InfoHashV2   [32]byte    `json:"-"`
	InfoHashHex  string      `json:"infoHash"`
	Name         string      `json:"name"`
	TotalSize    int64       `json:"totalSize"`
	PieceLength  int64       `json:"pieceLength"`
	PieceHashes  [][]byte    `json:"-"`
	HashSize     int         `json:"hashSize"` // 20 for SHA-1 (v1), 32 for SHA-256 (v2)
	Files        []FileEntry `json:"files"`
	Trackers     []string    `json:"trackers"`
	WebSeeds     []string    `json:"webSeeds"`
	PeerHints    []string    `json:"peerHints"` // Explicit x.pe / peer addresses from magnet URI
	RawInfoBytes []byte      `json:"-"`
	IsMagnetOnly bool        `json:"isMagnetOnly"`
	IsPrivate    bool        `json:"isPrivate"` // BEP 27 private: 1 flag (disables DHT & PEX)
}

// NumPieces returns the total number of pieces in the torrent.
func (m *TorrentMeta) NumPieces() int {
	return len(m.PieceHashes)
}

// PieceSize returns the exact byte length of the piece at index.
func (m *TorrentMeta) PieceSize(index int) int64 {
	num := len(m.PieceHashes)
	if index < 0 || index >= num {
		return 0
	}
	if index == num-1 {
		rem := m.TotalSize % m.PieceLength
		if rem != 0 {
			return rem
		}
	}
	return m.PieceLength
}

// ParseTorrentBytes decodes and validates a .torrent file payload.
func ParseTorrentBytes(data []byte) (*TorrentMeta, error) {
	decoded, rawInfo, err := DecodeBencode(data)
	if err != nil {
		return nil, err
	}
	rootDict, ok := decoded.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: top-level bencode element must be a dictionary", ErrCorruptBencode)
	}
	infoVal, hasInfo := rootDict["info"]
	if !hasInfo || len(rawInfo) == 0 {
		return nil, fmt.Errorf("%w: missing required 'info' dictionary", ErrCorruptBencode)
	}
	infoDict, ok := infoVal.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: 'info' field must be a dictionary", ErrCorruptBencode)
	}

	meta, err := parseInfoDict(infoDict, rawInfo)
	if err != nil {
		return nil, err
	}

	// Extract trackers from announce and announce-list
	trackerSet := make(map[string]struct{})
	if ann, ok := rootDict["announce"].(string); ok && strings.TrimSpace(ann) != "" {
		ann = strings.TrimSpace(ann)
		trackerSet[ann] = struct{}{}
		meta.Trackers = append(meta.Trackers, ann)
	}
	if annList, ok := rootDict["announce-list"].([]any); ok {
		for _, tier := range annList {
			if tierList, ok := tier.([]any); ok {
				for _, tr := range tierList {
					if trStr, ok := tr.(string); ok && strings.TrimSpace(trStr) != "" {
						trStr = strings.TrimSpace(trStr)
						if _, exists := trackerSet[trStr]; !exists {
							trackerSet[trStr] = struct{}{}
							meta.Trackers = append(meta.Trackers, trStr)
						}
					}
				}
			}
		}
	}

	// Extract webseeds (url-list BEP 19)
	if urlList, ok := rootDict["url-list"].(string); ok && strings.TrimSpace(urlList) != "" {
		meta.WebSeeds = append(meta.WebSeeds, strings.TrimSpace(urlList))
	} else if urlSlice, ok := rootDict["url-list"].([]any); ok {
		for _, u := range urlSlice {
			if uStr, ok := u.(string); ok && strings.TrimSpace(uStr) != "" {
				meta.WebSeeds = append(meta.WebSeeds, strings.TrimSpace(uStr))
			}
		}
	}

	return meta, nil
}

func parseInfoDict(infoDict map[string]any, rawInfo []byte) (*TorrentMeta, error) {
	name, _ := infoDict["name"].(string)
	name = sanitizePathComponent(name)
	if name == "" {
		return nil, fmt.Errorf("%w: missing or invalid 'name' in info dictionary", ErrCorruptBencode)
	}

	pieceLength, ok := infoDict["piece length"].(int64)
	if !ok || pieceLength <= 0 {
		return nil, fmt.Errorf("%w: invalid 'piece length' in info dictionary", ErrCorruptBencode)
	}

	piecesRaw, ok := infoDict["pieces"].(string)
	hashSize := 20
	if !ok || len(piecesRaw) == 0 {
		// Check if sha256 pieces field is provided
		if pieces256, ok256 := infoDict["pieces256"].(string); ok256 && len(pieces256) > 0 {
			piecesRaw = pieces256
			hashSize = 32
		} else {
			return nil, fmt.Errorf("%w: missing 'pieces' hash array in info dictionary", ErrCorruptBencode)
		}
	}

	if len(piecesRaw)%hashSize != 0 {
		return nil, fmt.Errorf("%w: 'pieces' byte length (%d) is not a multiple of %d", ErrCorruptBencode, len(piecesRaw), hashSize)
	}

	numPieces := len(piecesRaw) / hashSize
	pieceHashes := make([][]byte, numPieces)
	rawBytes := []byte(piecesRaw)
	for i := 0; i < numPieces; i++ {
		h := make([]byte, hashSize)
		copy(h, rawBytes[i*hashSize:(i+1)*hashSize])
		pieceHashes[i] = h
	}

	var files []FileEntry
	var totalSize int64

	if filesList, hasFiles := infoDict["files"].([]any); hasFiles {
		if len(filesList) == 0 {
			return nil, fmt.Errorf("%w: empty 'files' list in multi-file torrent", ErrCorruptBencode)
		}
		var offset int64
		for idx, fItem := range filesList {
			fDict, ok := fItem.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("%w: file entry %d is not a dictionary", ErrCorruptBencode, idx)
			}
			length, ok := fDict["length"].(int64)
			if !ok || length < 0 {
				return nil, fmt.Errorf("%w: invalid file length in entry %d", ErrCorruptBencode, idx)
			}
			pathList, ok := fDict["path"].([]any)
			if !ok || len(pathList) == 0 {
				return nil, fmt.Errorf("%w: missing file path list in entry %d", ErrCorruptBencode, idx)
			}
			segments := make([]string, 0, len(pathList)+1)
			segments = append(segments, name)
			for _, seg := range pathList {
				segStr, ok := seg.(string)
				if !ok {
					return nil, fmt.Errorf("%w: non-string path segment in entry %d", ErrCorruptBencode, idx)
				}
				cleanSeg := sanitizePathComponent(segStr)
				if cleanSeg == "" {
					return nil, fmt.Errorf("%w: unsafe or empty path segment %q in entry %d", ErrCorruptBencode, segStr, idx)
				}
				segments = append(segments, cleanSeg)
			}
			relPath := filepath.Join(segments...)
			files = append(files, FileEntry{
				RelPath: relPath,
				Length:  length,
				Offset:  offset,
			})
			offset += length
			totalSize += length
		}
	} else {
		length, ok := infoDict["length"].(int64)
		if !ok || length <= 0 {
			return nil, fmt.Errorf("%w: missing or non-positive 'length' in single-file torrent", ErrCorruptBencode)
		}
		totalSize = length
		files = []FileEntry{
			{
				RelPath: name,
				Length:  length,
				Offset:  0,
			},
		}
	}

	expectedPieces := int((totalSize + pieceLength - 1) / pieceLength)
	if expectedPieces != numPieces {
		return nil, fmt.Errorf("%w: piece count mismatch (expected %d from size %d / pieceLength %d, got %d)",
			ErrCorruptBencode, expectedPieces, totalSize, pieceLength, numPieces)
	}

	v1Hash := sha1.Sum(rawInfo)
	v2Hash := sha256.Sum256(rawInfo)
	isPrivate := false
	if privVal, ok := infoDict["private"].(int64); ok && privVal == 1 {
		isPrivate = true
	}

	return &TorrentMeta{
		InfoHash:     v1Hash,
		InfoHashV2:   v2Hash,
		InfoHashHex:  hex.EncodeToString(v1Hash[:]),
		Name:         name,
		TotalSize:    totalSize,
		PieceLength:  pieceLength,
		PieceHashes:  pieceHashes,
		HashSize:     hashSize,
		Files:        files,
		RawInfoBytes: rawInfo,
		IsMagnetOnly: false,
		IsPrivate:    isPrivate,
	}, nil
}

func sanitizePathComponent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return ""
	}
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	s = strings.ReplaceAll(s, "\x00", "")
	return s
}

// ParseMagnetURI parses and validates a magnet:?xt=urn:btih:... link.
func ParseMagnetURI(uri string) (*TorrentMeta, error) {
	uri = strings.TrimSpace(uri)
	if !strings.HasPrefix(strings.ToLower(uri), "magnet:?") {
		return nil, errors.New("invalid magnet URI: must start with 'magnet:?'")
	}
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid magnet URI syntax: %w", err)
	}
	q := u.Query()
	xts := q["xt"]
	if len(xts) == 0 {
		return nil, errors.New("invalid magnet URI: missing 'xt' (exact topic) parameter")
	}

	var infoHash [20]byte
	var foundBtih bool
	for _, xt := range xts {
		lower := strings.ToLower(xt)
		if strings.HasPrefix(lower, "urn:btih:") {
			rawHash := xt[len("urn:btih:"):]
			switch len(rawHash) {
			case 40:
				b, err := hex.DecodeString(rawHash)
				if err != nil {
					return nil, fmt.Errorf("invalid 40-char hex info_hash in magnet URI: %w", err)
				}
				copy(infoHash[:], b)
				foundBtih = true
			case 32:
				b, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(rawHash))
				if err != nil {
					return nil, fmt.Errorf("invalid 32-char base32 info_hash in magnet URI: %w", err)
				}
				copy(infoHash[:], b)
				foundBtih = true
			default:
				return nil, fmt.Errorf("unsupported btih length %d (expected 40 hex or 32 base32 chars)", len(rawHash))
			}
			break
		}
	}
	if !foundBtih {
		return nil, errors.New("invalid magnet URI: no 'urn:btih:' topic found")
	}

	infoHashHex := hex.EncodeToString(infoHash[:])
	name := sanitizePathComponent(q.Get("dn"))
	if name == "" {
		name = "magnet-" + infoHashHex[:12]
	}

	var exactLength int64
	if xlStr := q.Get("xl"); xlStr != "" {
		if parsedLen, err := strconv.ParseInt(xlStr, 10, 64); err == nil && parsedLen > 0 {
			exactLength = parsedLen
		}
	}

	var trackers []string
	seenTr := make(map[string]struct{})
	for _, tr := range q["tr"] {
		tr = strings.TrimSpace(tr)
		if tr != "" {
			if _, ok := seenTr[tr]; !ok {
				seenTr[tr] = struct{}{}
				trackers = append(trackers, tr)
			}
		}
	}

	var webSeeds []string
	for _, ws := range q["ws"] {
		ws = strings.TrimSpace(ws)
		if ws != "" {
			webSeeds = append(webSeeds, ws)
		}
	}

	var peerHints []string
	for _, pe := range q["x.pe"] {
		pe = strings.TrimSpace(pe)
		if pe != "" {
			peerHints = append(peerHints, pe)
		}
	}

	// If xl (exact length) is known in the magnet link, pre-calculate synthetic piece geometry
	// until full BEP 9 ut_metadata arrives, or allow direct peer transfer when piece size is standard.
	pieceLength := int64(262144) // 256 KiB default piece size
	var files []FileEntry
	if exactLength > 0 {
		files = []FileEntry{{RelPath: name, Length: exactLength, Offset: 0}}
	}

	return &TorrentMeta{
		InfoHash:     infoHash,
		InfoHashHex:  infoHashHex,
		Name:         name,
		TotalSize:    exactLength,
		PieceLength:  pieceLength,
		HashSize:     20,
		Files:        files,
		Trackers:     trackers,
		WebSeeds:     webSeeds,
		PeerHints:    peerHints,
		IsMagnetOnly: true,
	}, nil
}

// BuildSingleFileTorrent encodes a canonical .torrent payload for a given file payload and piece length.
func BuildSingleFileTorrent(name string, content []byte, pieceLength int64, trackers []string) ([]byte, *TorrentMeta, error) {
	if pieceLength <= 0 {
		pieceLength = 262144 // 256 KiB
	}
	if len(content) == 0 {
		return nil, nil, errors.New("cannot build .torrent for empty content")
	}
	numPieces := (int64(len(content)) + pieceLength - 1) / pieceLength
	piecesBuf := make([]byte, 0, numPieces*20)
	for i := int64(0); i < numPieces; i++ {
		start := i * pieceLength
		end := start + pieceLength
		if end > int64(len(content)) {
			end = int64(len(content))
		}
		sum := sha1.Sum(content[start:end])
		piecesBuf = append(piecesBuf, sum[:]...)
	}

	infoDict := map[string]any{
		"length":       int64(len(content)),
		"name":         name,
		"piece length": pieceLength,
		"pieces":       piecesBuf,
	}
	root := map[string]any{
		"info": infoDict,
	}
	if len(trackers) > 0 {
		root["announce"] = trackers[0]
		tiers := make([]any, 0, len(trackers))
		for _, tr := range trackers {
			tiers = append(tiers, []any{tr})
		}
		root["announce-list"] = tiers
	}

	rawTorrent, err := EncodeBencode(root)
	if err != nil {
		return nil, nil, err
	}
	meta, err := ParseTorrentBytes(rawTorrent)
	if err != nil {
		return nil, nil, err
	}
	return rawTorrent, meta, nil
}
