package engine

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TrackerAnnounceResult holds peer addresses and swarm counts returned by an HTTP or UDP tracker.
type TrackerAnnounceResult struct {
	Peers    []string
	Seeders  int
	Leechers int
	Interval time.Duration
}

// AnnounceTracker queries either an HTTP/HTTPS or UDP tracker for active swarm peers.
func AnnounceTracker(trackerURL string, infoHash [20]byte, peerID [20]byte, listenPort int, uploaded, downloaded, left int64) (*TrackerAnnounceResult, error) {
	lower := strings.ToLower(strings.TrimSpace(trackerURL))
	switch {
	case strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://"):
		return announceHTTPTracker(trackerURL, infoHash, peerID, listenPort, uploaded, downloaded, left)
	case strings.HasPrefix(lower, "udp://"):
		return announceUDPTracker(trackerURL, infoHash, peerID, listenPort, uploaded, downloaded, left)
	default:
		return nil, fmt.Errorf("unsupported tracker scheme in %q", trackerURL)
	}
}

func announceHTTPTracker(trackerURL string, infoHash [20]byte, peerID [20]byte, listenPort int, uploaded, downloaded, left int64) (*TrackerAnnounceResult, error) {
	u, err := url.Parse(trackerURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("info_hash", string(infoHash[:]))
	q.Set("peer_id", string(peerID[:]))
	q.Set("port", strconv.Itoa(listenPort))
	q.Set("uploaded", strconv.FormatInt(uploaded, 10))
	q.Set("downloaded", strconv.FormatInt(downloaded, 10))
	q.Set("left", strconv.FormatInt(left, 10))
	q.Set("compact", "1")
	q.Set("numwant", "100")
	u.RawQuery = q.Encode()

	client := &http.Client{Timeout: 8 * time.Second}
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "VeloceTorrent/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2*1024*1024))
	if err != nil {
		return nil, err
	}

	decoded, _, err := DecodeBencode(body)
	if err != nil {
		return nil, err
	}
	dict, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("invalid tracker response dictionary")
	}
	if failReason, hasFail := dict["failure reason"].(string); hasFail {
		return nil, fmt.Errorf("tracker failure: %s", failReason)
	}

	res := &TrackerAnnounceResult{
		Interval: 15 * time.Minute,
	}
	if iv, ok := dict["interval"].(int64); ok && iv > 0 {
		res.Interval = time.Duration(iv) * time.Second
	}
	if complete, ok := dict["complete"].(int64); ok {
		res.Seeders = int(complete)
	}
	if incomplete, ok := dict["incomplete"].(int64); ok {
		res.Leechers = int(incomplete)
	}

	// Parse compact 6-byte peers or dictionary list peers
	switch peersVal := dict["peers"].(type) {
	case string:
		raw := []byte(peersVal)
		for i := 0; i+6 <= len(raw); i += 6 {
			ip := net.IP(raw[i : i+4])
			port := binary.BigEndian.Uint16(raw[i+4 : i+6])
			if port > 0 {
				res.Peers = append(res.Peers, fmt.Sprintf("%s:%d", ip.String(), port))
			}
		}
	case []any:
		for _, item := range peersVal {
			if pMap, ok := item.(map[string]any); ok {
				ipStr, _ := pMap["ip"].(string)
				portVal, _ := pMap["port"].(int64)
				if ipStr != "" && portVal > 0 {
					res.Peers = append(res.Peers, net.JoinHostPort(ipStr, strconv.FormatInt(portVal, 10)))
				}
			}
		}
	}

	return res, nil
}

func announceUDPTracker(trackerURL string, infoHash [20]byte, peerID [20]byte, listenPort int, uploaded, downloaded, left int64) (*TrackerAnnounceResult, error) {
	u, err := url.Parse(trackerURL)
	if err != nil {
		return nil, err
	}
	hostPort := u.Host
	if !strings.Contains(hostPort, ":") {
		hostPort += ":80"
	}

	raddr, err := net.ResolveUDPAddr("udp", hostPort)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	TuneUDPSocketBuffers(conn)
	_ = conn.SetDeadline(time.Now().Add(6 * time.Second))

	// Step 1: BEP 15 Connect Request
	txID := rand.Uint32()
	var connReq [16]byte
	binary.BigEndian.PutUint64(connReq[0:8], 0x41727101980) // Protocol magic constant
	binary.BigEndian.PutUint32(connReq[8:12], 0)            // Action 0 = Connect
	binary.BigEndian.PutUint32(connReq[12:16], txID)

	if _, err := conn.Write(connReq[:]); err != nil {
		return nil, err
	}

	var connResp [16]byte
	if _, err := io.ReadFull(conn, connResp[:]); err != nil {
		return nil, err
	}
	if binary.BigEndian.Uint32(connResp[0:4]) != 0 || binary.BigEndian.Uint32(connResp[4:8]) != txID {
		return nil, errors.New("UDP tracker connect transaction mismatch")
	}
	connID := binary.BigEndian.Uint64(connResp[8:16])

	// Step 2: BEP 15 Announce Request (98 bytes)
	txID2 := rand.Uint32()
	var annReq [98]byte
	binary.BigEndian.PutUint64(annReq[0:8], connID)
	binary.BigEndian.PutUint32(annReq[8:12], 1) // Action 1 = Announce
	binary.BigEndian.PutUint32(annReq[12:16], txID2)
	copy(annReq[16:36], infoHash[:])
	copy(annReq[36:56], peerID[:])
	binary.BigEndian.PutUint64(annReq[56:64], uint64(downloaded))
	binary.BigEndian.PutUint64(annReq[64:72], uint64(left))
	binary.BigEndian.PutUint64(annReq[72:80], uint64(uploaded))
	binary.BigEndian.PutUint32(annReq[80:84], 0) // Event 0 = none
	binary.BigEndian.PutUint32(annReq[84:88], 0) // IP default
	binary.BigEndian.PutUint32(annReq[88:92], rand.Uint32())
	binary.BigEndian.PutUint32(annReq[92:96], 100) // num_want = 100
	binary.BigEndian.PutUint16(annReq[96:98], uint16(listenPort))

	if _, err := conn.Write(annReq[:]); err != nil {
		return nil, err
	}

	respBuf := make([]byte, 2048)
	n, err := conn.Read(respBuf)
	if err != nil {
		return nil, err
	}
	if n < 20 {
		return nil, errors.New("short UDP tracker announce response")
	}
	if binary.BigEndian.Uint32(respBuf[0:4]) != 1 || binary.BigEndian.Uint32(respBuf[4:8]) != txID2 {
		return nil, errors.New("UDP tracker announce transaction mismatch")
	}

	interval := binary.BigEndian.Uint32(respBuf[8:12])
	leechers := binary.BigEndian.Uint32(respBuf[12:16])
	seeders := binary.BigEndian.Uint32(respBuf[16:20])

	res := &TrackerAnnounceResult{
		Seeders:  int(seeders),
		Leechers: int(leechers),
		Interval: time.Duration(interval) * time.Second,
	}

	peersRaw := respBuf[20:n]
	for i := 0; i+6 <= len(peersRaw); i += 6 {
		ip := net.IP(peersRaw[i : i+4])
		port := binary.BigEndian.Uint16(peersRaw[i+4 : i+6])
		if port > 0 {
			res.Peers = append(res.Peers, fmt.Sprintf("%s:%d", ip.String(), port))
		}
	}
	return res, nil
}
