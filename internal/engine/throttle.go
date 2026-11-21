package engine

import (
	"sync"
	"sync/atomic"
	"time"
)

// ThrottleProfile defines a named bandwidth scheduler mode.
type ThrottleProfile string

const (
	ProfileUncapped   ThrottleProfile = "UNCAPPED"
	ProfileBackground ThrottleProfile = "BACKGROUND"
	ProfileCustom     ThrottleProfile = "CUSTOM"

	// DefaultBackgroundDownBps caps background download rate at 2 MiB/s to prevent SSH/VoIP bufferbloat.
	DefaultBackgroundDownBps int64 = 2 * 1024 * 1024
	// DefaultBackgroundUpBps caps background upload rate at 512 KiB/s.
	DefaultBackgroundUpBps int64 = 512 * 1024
)

// ThrottleSnapshot describes the current bandwidth limiter state in telemetry snapshots.
type ThrottleSnapshot struct {
	Profile        ThrottleProfile `json:"profile"`
	MaxDownBytesPs int64           `json:"maxDownBytesPs"` // 0 = uncapped
	MaxUpBytesPs   int64           `json:"maxUpBytesPs"`   // 0 = uncapped
}

// BandwidthThrottler implements a low-overhead token-bucket rate limiter for global download and upload streams.
type BandwidthThrottler struct {
	mu             sync.RWMutex
	profile        ThrottleProfile
	maxDownBytesPs atomic.Int64
	maxUpBytesPs   atomic.Int64

	downTokens     atomic.Int64
	upTokens       atomic.Int64
	lastRefillNs   atomic.Int64
}

// NewBandwidthThrottler creates a bandwidth throttler defaulting to UNCAPPED line-rate saturation.
func NewBandwidthThrottler() *BandwidthThrottler {
	bt := &BandwidthThrottler{
		profile: ProfileUncapped,
	}
	bt.lastRefillNs.Store(time.Now().UnixNano())
	return bt
}

// SetProfile switches between UNCAPPED, BACKGROUND, or CUSTOM bandwidth limits.
func (b *BandwidthThrottler) SetProfile(profile ThrottleProfile, customDownBps, customUpBps int64) ThrottleSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()

	switch profile {
	case ProfileBackground:
		b.profile = ProfileBackground
		down := customDownBps
		if down <= 0 {
			down = DefaultBackgroundDownBps
		}
		up := customUpBps
		if up <= 0 {
			up = DefaultBackgroundUpBps
		}
		b.maxDownBytesPs.Store(down)
		b.maxUpBytesPs.Store(up)
		b.downTokens.Store(down)
		b.upTokens.Store(up)

	case ProfileCustom:
		b.profile = ProfileCustom
		b.maxDownBytesPs.Store(customDownBps)
		b.maxUpBytesPs.Store(customUpBps)
		b.downTokens.Store(customDownBps)
		b.upTokens.Store(customUpBps)

	default:
		b.profile = ProfileUncapped
		b.maxDownBytesPs.Store(0)
		b.maxUpBytesPs.Store(0)
	}

	return ThrottleSnapshot{
		Profile:        b.profile,
		MaxDownBytesPs: b.maxDownBytesPs.Load(),
		MaxUpBytesPs:   b.maxUpBytesPs.Load(),
	}
}

// ToggleBackground flips between UNCAPPED and BACKGROUND modes.
func (b *BandwidthThrottler) ToggleBackground() ThrottleSnapshot {
	b.mu.RLock()
	cur := b.profile
	b.mu.RUnlock()
	if cur == ProfileUncapped {
		return b.SetProfile(ProfileBackground, DefaultBackgroundDownBps, DefaultBackgroundUpBps)
	}
	return b.SetProfile(ProfileUncapped, 0, 0)
}

// Snapshot returns the current ThrottleSnapshot.
func (b *BandwidthThrottler) Snapshot() ThrottleSnapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return ThrottleSnapshot{
		Profile:        b.profile,
		MaxDownBytesPs: b.maxDownBytesPs.Load(),
		MaxUpBytesPs:   b.maxUpBytesPs.Load(),
	}
}

// WaitDownload paces download block reception when a non-zero download cap is active.
func (b *BandwidthThrottler) WaitDownload(bytesCount int64) {
	maxDown := b.maxDownBytesPs.Load()
	if maxDown <= 0 || bytesCount <= 0 {
		return
	}
	b.consumeTokens(&b.downTokens, maxDown, bytesCount)
}

// WaitUpload paces upload block transmission when a non-zero upload cap is active.
func (b *BandwidthThrottler) WaitUpload(bytesCount int64) {
	maxUp := b.maxUpBytesPs.Load()
	if maxUp <= 0 || bytesCount <= 0 {
		return
	}
	b.consumeTokens(&b.upTokens, maxUp, bytesCount)
}

func (b *BandwidthThrottler) consumeTokens(bucket *atomic.Int64, ratePerSec int64, needed int64) {
	nowNs := time.Now().UnixNano()
	prevNs := b.lastRefillNs.Swap(nowNs)
	if nowNs > prevNs {
		elapsedSec := float64(nowNs-prevNs) / 1e9
		added := int64(elapsedSec * float64(ratePerSec))
		if added > 0 {
			cur := bucket.Add(added)
			if cur > ratePerSec {
				bucket.Store(ratePerSec)
			}
		}
	}
	rem := bucket.Add(-needed)
	if rem < 0 {
		deficit := -rem
		sleepSec := float64(deficit) / float64(ratePerSec)
		if sleepSec > 0.25 {
			sleepSec = 0.25
		}
		if sleepSec > 0.002 {
			time.Sleep(time.Duration(sleepSec * float64(time.Second)))
		}
	}
}
