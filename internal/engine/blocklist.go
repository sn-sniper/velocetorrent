package engine

import (
	"bufio"
	"encoding/binary"
	"io"
	"math/bits"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

type radixNode struct {
	children [2]*radixNode
	terminal bool
}

// IPBlocklist stores blocked IPv4 and IPv6 CIDR prefixes in an in-memory binary Radix Tree
// to drop malicious or poisoned peers at the socket level before the BitTorrent handshake.
type IPBlocklist struct {
	mu          sync.RWMutex
	rootV4      *radixNode
	rootV6      *radixNode
	prefixCount int
	blockedHits atomic.Int64
}

// NewIPBlocklist creates an empty radix tree blocklist.
func NewIPBlocklist() *IPBlocklist {
	return &IPBlocklist{
		rootV4: &radixNode{},
		rootV6: &radixNode{},
	}
}

// AddCIDR inserts a parsed *net.IPNet prefix into the radix tree.
func (b *IPBlocklist) AddCIDR(ipNet *net.IPNet) {
	if ipNet == nil {
		return
	}
	ones, totalBits := ipNet.Mask.Size()
	b.mu.Lock()
	defer b.mu.Unlock()

	if ip4 := ipNet.IP.To4(); ip4 != nil && totalBits == 32 {
		if insertPrefix(b.rootV4, ip4, ones) {
			b.prefixCount++
		}
		return
	}
	if ip16 := ipNet.IP.To16(); ip16 != nil {
		if insertPrefix(b.rootV6, ip16, ones) {
			b.prefixCount++
		}
	}
}

func insertPrefix(root *radixNode, ipBytes []byte, prefixLen int) bool {
	curr := root
	for i := 0; i < prefixLen; i++ {
		if curr.terminal {
			return false // Already covered by a shorter aggregate prefix
		}
		byteIdx := i / 8
		bitIdx := uint(7 - (i % 8))
		bit := (ipBytes[byteIdx] >> bitIdx) & 1
		if curr.children[bit] == nil {
			curr.children[bit] = &radixNode{}
		}
		curr = curr.children[bit]
	}
	if curr.terminal {
		return false
	}
	curr.terminal = true
	curr.children[0] = nil
	curr.children[1] = nil
	return true
}

// AddIPRange decomposes an inclusive IPv4 [startIP, endIP] range (PeerGuardian / I-Blocklist format)
// into exact minimal CIDR prefixes and inserts them into the radix tree.
func (b *IPBlocklist) AddIPRange(startIP, endIP net.IP) int {
	s4 := startIP.To4()
	e4 := endIP.To4()
	if s4 == nil || e4 == nil {
		return 0
	}
	start := binary.BigEndian.Uint32(s4)
	end := binary.BigEndian.Uint32(e4)
	if start > end {
		return 0
	}

	added := 0
	for start <= end {
		// Maximum alignment of `start` based on trailing zeros
		tz := 32
		if start != 0 {
			tz = bits.TrailingZeros32(start)
		}
		// Maximum block size that does not exceed `end`
		remaining := uint64(end) - uint64(start) + 1
		maxBit := 63 - bits.LeadingZeros64(remaining)
		if maxBit < tz {
			tz = maxBit
		}
		prefixLen := 32 - tz
		var ipBuf [4]byte
		binary.BigEndian.PutUint32(ipBuf[:], start)
		b.AddCIDR(&net.IPNet{
			IP:   net.IPv4(ipBuf[0], ipBuf[1], ipBuf[2], ipBuf[3]),
			Mask: net.CIDRMask(prefixLen, 32),
		})
		added++

		blockSize := uint64(1) << uint(tz)
		if uint64(start)+blockSize > 0xFFFFFFFF {
			break
		}
		start += uint32(blockSize)
	}
	return added
}

// LoadFromReader parses CIDR and PeerGuardian ("Label:1.2.3.0-1.2.3.255") blocklist lines.
func (b *IPBlocklist) LoadFromReader(r io.Reader) (int, error) {
	scanner := bufio.NewScanner(r)
	loaded := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		if b.ParseAndAddRule(line) {
			loaded++
		}
	}
	return loaded, scanner.Err()
}

// LoadFromFile loads a blocklist file from disk.
func (b *IPBlocklist) LoadFromFile(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	return b.LoadFromReader(f)
}

// ParseAndAddRule parses a single CIDR, IP, or PeerGuardian range rule and adds it to the radix tree.
func (b *IPBlocklist) ParseAndAddRule(rule string) bool {
	rule = strings.TrimSpace(rule)
	if rule == "" {
		return false
	}

	// Check PeerGuardian format: "Description:192.0.2.0-192.0.2.255"
	if lastColon := strings.LastIndex(rule, ":"); lastColon >= 0 && strings.Contains(rule[lastColon+1:], "-") {
		rule = strings.TrimSpace(rule[lastColon+1:])
	}

	// Check range format: "192.0.2.0-192.0.2.255"
	if dashIdx := strings.Index(rule, "-"); dashIdx > 0 {
		startStr := strings.TrimSpace(rule[:dashIdx])
		endStr := strings.TrimSpace(rule[dashIdx+1:])
		sIP := net.ParseIP(startStr)
		eIP := net.ParseIP(endStr)
		if sIP != nil && eIP != nil {
			return b.AddIPRange(sIP, eIP) > 0
		}
	}

	// Check CIDR format: "192.0.2.0/24"
	if _, ipNet, err := net.ParseCIDR(rule); err == nil && ipNet != nil {
		b.AddCIDR(ipNet)
		return true
	}

	// Check single IP format: "192.0.2.1"
	if ip := net.ParseIP(rule); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			b.AddCIDR(&net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)})
		} else {
			b.AddCIDR(&net.IPNet{IP: ip.To16(), Mask: net.CIDRMask(128, 128)})
		}
		return true
	}

	return false
}

// IsBlockedAddr extracts the IP from a "host:port" or IP string and checks if it matches the radix tree.
func (b *IPBlocklist) IsBlockedAddr(addr string) bool {
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	return b.IsBlockedIP(ip)
}

// IsBlockedIP returns true in O(W) time (max 32 steps for IPv4) if ip matches any prefix in the radix tree.
func (b *IPBlocklist) IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	b.mu.RLock()
	var matched bool
	if ip4 := ip.To4(); ip4 != nil {
		matched = matchPrefix(b.rootV4, ip4, 32)
	} else if ip16 := ip.To16(); ip16 != nil {
		matched = matchPrefix(b.rootV6, ip16, 128)
	}
	b.mu.RUnlock()

	if matched {
		b.blockedHits.Add(1)
	}
	return matched
}

func matchPrefix(root *radixNode, ipBytes []byte, maxBits int) bool {
	curr := root
	for i := 0; i < maxBits && curr != nil; i++ {
		if curr.terminal {
			return true
		}
		byteIdx := i / 8
		bitIdx := uint(7 - (i % 8))
		bit := (ipBytes[byteIdx] >> bitIdx) & 1
		curr = curr.children[bit]
	}
	return curr != nil && curr.terminal
}

// PrefixCount returns the number of active CIDR prefixes stored in the radix tree.
func (b *IPBlocklist) PrefixCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.prefixCount
}

// BlockedHits returns the total number of peer connections dropped by the blocklist.
func (b *IPBlocklist) BlockedHits() int64 {
	return b.blockedHits.Load()
}
