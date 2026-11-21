package engine

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OnCompleteEvent is the payload delivered to lifecycle scripts and webhooks upon torrent completion.
type OnCompleteEvent struct {
	Event       string `json:"event"`
	TorrentID   string `json:"torrentId"`
	InfoHash    string `json:"infoHash"`
	Name        string `json:"name"`
	OutputDir   string `json:"outputDir"`
	TotalSize   int64  `json:"totalSize"`
	CompletedAt int64  `json:"completedAt"`
}

// LifecycleHookManager manages post-download `on_complete` scripts, webhooks, and desktop notifications.
type LifecycleHookManager struct {
	mu            sync.RWMutex
	onCompleteCmd string
	webhookURL    string
	desktopNotify bool
	lastStatus    string
	firedCount    int64
}

// NewLifecycleHookManager creates a hook manager initialized from config or environment variables.
func NewLifecycleHookManager(cmdStr string, webhookURL string) *LifecycleHookManager {
	if strings.TrimSpace(cmdStr) == "" {
		cmdStr = os.Getenv("VT_ON_COMPLETE_CMD")
	}
	if strings.TrimSpace(webhookURL) == "" {
		webhookURL = os.Getenv("VT_ON_COMPLETE_WEBHOOK")
	}
	return &LifecycleHookManager{
		onCompleteCmd: strings.TrimSpace(cmdStr),
		webhookURL:    strings.TrimSpace(webhookURL),
		desktopNotify: os.Getenv("VT_DESKTOP_NOTIFY") == "1",
	}
}

// Configure updates the active post-download command and webhook URL at runtime.
func (h *LifecycleHookManager) Configure(cmdStr string, webhookURL string, desktopNotify bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onCompleteCmd = strings.TrimSpace(cmdStr)
	h.webhookURL = strings.TrimSpace(webhookURL)
	h.desktopNotify = desktopNotify
}

// ConfigSnapshot returns the currently configured lifecycle hook settings.
func (h *LifecycleHookManager) ConfigSnapshot() (cmdStr string, webhookURL string, desktopNotify bool, firedCount int64, lastStatus string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.onCompleteCmd, h.webhookURL, h.desktopNotify, h.firedCount, h.lastStatus
}

// FireOnComplete asynchronously executes any configured script, webhook, or desktop alert for a completed torrent.
func (h *LifecycleHookManager) FireOnComplete(sess *TorrentSession) {
	if sess == nil || sess.meta == nil {
		return
	}
	h.mu.RLock()
	cmdStr := h.onCompleteCmd
	webhook := h.webhookURL
	notify := h.desktopNotify
	h.mu.RUnlock()

	if cmdStr == "" && webhook == "" && !notify {
		return
	}

	ev := OnCompleteEvent{
		Event:       "torrent.completed",
		TorrentID:   sess.ID(),
		InfoHash:    sess.meta.InfoHashHex,
		Name:        sess.meta.Name,
		OutputDir:   sess.OutputDir(),
		TotalSize:   sess.meta.TotalSize,
		CompletedAt: time.Now().UnixMilli(),
	}

	go h.executeHooks(ev, cmdStr, webhook, notify)
}

func (h *LifecycleHookManager) executeHooks(ev OnCompleteEvent, cmdStr, webhook string, notify bool) {
	var statusParts []string

	if cmdStr != "" {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.Command("powershell", "-NoProfile", "-Command", cmdStr)
		} else {
			cmd = exec.Command("sh", "-c", cmdStr)
		}
		cmd.Env = append(os.Environ(),
			"VT_TORRENT_ID="+ev.TorrentID,
			"VT_INFO_HASH="+ev.InfoHash,
			"VT_TORRENT_NAME="+ev.Name,
			"VT_OUTPUT_DIR="+ev.OutputDir,
			"VT_TOTAL_SIZE="+strconv.FormatInt(ev.TotalSize, 10),
			"VT_COMPLETED_AT="+strconv.FormatInt(ev.CompletedAt, 10),
		)
		if err := cmd.Run(); err != nil {
			statusParts = append(statusParts, fmt.Sprintf("cmd_err:%v", err))
		} else {
			statusParts = append(statusParts, "cmd:ok")
		}
	}

	if webhook != "" {
		body, _ := json.Marshal(ev)
		client := &http.Client{Timeout: 8 * time.Second}
		req, err := http.NewRequest(http.MethodPost, webhook, bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("User-Agent", "VeloceTorrent-Webhook/1.0")
			resp, doErr := client.Do(req)
			if doErr != nil {
				statusParts = append(statusParts, fmt.Sprintf("webhook_err:%v", doErr))
			} else {
				_ = resp.Body.Close()
				statusParts = append(statusParts, fmt.Sprintf("webhook:HTTP_%d", resp.StatusCode))
			}
		}
	}

	if notify && runtime.GOOS == "linux" {
		_ = exec.Command("notify-send", "VeloceTorrent Complete", fmt.Sprintf("%s finished in %s", ev.Name, ev.OutputDir)).Run()
		statusParts = append(statusParts, "notify:ok")
	}

	h.mu.Lock()
	h.firedCount++
	h.lastStatus = strings.Join(statusParts, " | ")
	h.mu.Unlock()
}
