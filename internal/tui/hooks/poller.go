package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"velocetorrent/internal/engine"
	"velocetorrent/internal/ipc"
)

// TickMsg triggers a 100ms telemetry poll from the IPC daemon.
type TickMsg time.Time

// TelemetryUpdateMsg carries a fresh EngineTelemetrySnapshot or a connection error.
type TelemetryUpdateMsg struct {
	Snapshot *engine.EngineTelemetrySnapshot
	Err      error
}

// ActionFeedbackMsg carries the result or error of a user-initiated command (pause, dir change, add torrent).
type ActionFeedbackMsg struct {
	Info string
	Err  error
}

// KeyAction enumerates semantic TUI keyboard commands per AGENTS.md Section 5A.
type KeyAction int

const (
	ActionNone KeyAction = iota
	ActionDetach
	ActionCursorUp
	ActionCursorDown
	ActionTogglePause
	ActionOpenDirModal
	ActionInspectPeers
	ActionInspectFiles
	ActionDashboardView
	ActionOpenAddModal
	ActionSpawnSynthetic
	ActionDeleteTorrent
	ActionToggleThrottle
	ActionCycleFilePriority
	ActionOpenWebUI
)

// ResolveKeyAction maps a Bubble Tea KeyMsg to a semantic TUI KeyAction when not in text-input mode.
func ResolveKeyAction(msg tea.KeyMsg) KeyAction {
	switch msg.String() {
	case "q", "ctrl+c":
		return ActionDetach
	case "up", "k":
		return ActionCursorUp
	case "down", "j":
		return ActionCursorDown
	case " ":
		return ActionTogglePause
	case "d", "D":
		return ActionOpenDirModal
	case "p", "P":
		return ActionInspectPeers
	case "f", "F":
		return ActionInspectFiles
	case "esc", "1":
		return ActionDashboardView
	case "a", "A":
		return ActionOpenAddModal
	case "s", "S":
		return ActionSpawnSynthetic
	case "x", "delete":
		return ActionDeleteTorrent
	case "b", "B":
		return ActionToggleThrottle
	case "c", "C":
		return ActionCycleFilePriority
	case "w", "W":
		return ActionOpenWebUI
	default:
		return ActionNone
	}
}

// ScheduleNextTick schedules the next 100ms telemetry refresh tick.
func ScheduleNextTick(interval time.Duration) tea.Cmd {
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

// FetchTelemetryCmd polls the local IPC server for the current EngineTelemetrySnapshot.
func FetchTelemetryCmd(client *ipc.Client) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.FetchState()
		return TelemetryUpdateMsg{
			Snapshot: snap,
			Err:      err,
		}
	}
}

// TogglePauseCmd dispatches a pause/resume toggle to the IPC server.
func TogglePauseCmd(client *ipc.Client, torrentID string) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.TogglePause(torrentID)
		if err != nil {
			return ActionFeedbackMsg{Err: err}
		}
		return ActionFeedbackMsg{
			Info: fmt.Sprintf("TORRENT %s -> %s", snap.Name, snap.Status),
		}
	}
}

// ChangeDirCmd updates the output directory for a selected torrent (or global default if torrentID is empty).
func ChangeDirCmd(client *ipc.Client, torrentID string, newDir string) tea.Cmd {
	return func() tea.Msg {
		if torrentID != "" {
			snap, err := client.ChangeTorrentDir(torrentID, newDir)
			if err != nil {
				return ActionFeedbackMsg{Err: err}
			}
			return ActionFeedbackMsg{
				Info: fmt.Sprintf("DIR UPDATED [%s] -> %s", snap.Name, snap.OutputDir),
			}
		}
		res, err := client.SetDefaultDir(newDir)
		if err != nil {
			return ActionFeedbackMsg{Err: err}
		}
		return ActionFeedbackMsg{
			Info: fmt.Sprintf("DEFAULT DIR -> %s", res.DefaultOutputDir),
		}
	}
}

// AddTorrentCmd queues a .torrent path or magnet URI on the daemon.
func AddTorrentCmd(client *ipc.Client, source string, outputDir string) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.AddTorrent(source, outputDir, false)
		if err != nil {
			return ActionFeedbackMsg{Err: err}
		}
		return ActionFeedbackMsg{
			Info: fmt.Sprintf("QUEUED: %s (%s)", snap.Name, snap.InfoHash[:10]),
		}
	}
}

// SpawnSyntheticSwarmCmd triggers a live high-speed local TCP swarm transfer for testing/verification.
func SpawnSyntheticSwarmCmd(tcpAddr string) tea.Cmd {
	return func() tea.Msg {
		if tcpAddr == "" {
			tcpAddr = ipc.DefaultTCPAddr
		}
		payload, _ := json.Marshal(ipc.SyntheticTorrentRequest{
			Name:      fmt.Sprintf("veloce-nvme-bench-%d.iso", time.Now().Unix()%10000),
			SizeBytes: 32 * 1024 * 1024, // 32 MiB multi-piece swarm payload
		})
		resp, err := http.Post("http://"+tcpAddr+"/api/torrents/synthetic", "application/json", bytes.NewReader(payload))
		if err != nil {
			return ActionFeedbackMsg{Err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			return ActionFeedbackMsg{Err: fmt.Errorf("failed to spawn swarm (HTTP %d)", resp.StatusCode)}
		}
		var snap engine.TorrentTelemetrySnapshot
		_ = json.NewDecoder(resp.Body).Decode(&snap)
		return ActionFeedbackMsg{
			Info: fmt.Sprintf("SWARM ACTIVE: %s", snap.Name),
		}
	}
}

// DeleteTorrentCmd removes a torrent from the daemon.
func DeleteTorrentCmd(client *ipc.Client, torrentID string) tea.Cmd {
	return func() tea.Msg {
		if err := client.DeleteTorrent(torrentID, false); err != nil {
			return ActionFeedbackMsg{Err: err}
		}
		return ActionFeedbackMsg{
			Info: fmt.Sprintf("REMOVED TORRENT %s", torrentID[:10]),
		}
	}
}

// ToggleThrottleCmd flips the daemon between UNCAPPED and BACKGROUND bandwidth modes.
func ToggleThrottleCmd(client *ipc.Client) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.SetThrottle("", true, 0, 0)
		if err != nil {
			return ActionFeedbackMsg{Err: err}
		}
		return ActionFeedbackMsg{
			Info: fmt.Sprintf("BANDWIDTH PROFILE -> %s", snap.Profile),
		}
	}
}

// CycleFilePriorityCmd updates the selective priority of a file in a torrent.
func CycleFilePriorityCmd(client *ipc.Client, torrentID string, fileIndex int, nextPrio engine.FilePriority) tea.Cmd {
	return func() tea.Msg {
		snap, err := client.SetFilePriority(torrentID, fileIndex, nextPrio)
		if err != nil {
			return ActionFeedbackMsg{Err: err}
		}
		return ActionFeedbackMsg{
			Info: fmt.Sprintf("FILE #%d PRIORITY [%s] -> %s", fileIndex, snap.Name, strings.ToUpper(string(nextPrio))),
		}
	}
}

// OpenWebUICmd opens the local Web Control Plane in the user's default browser across Windows, macOS, and Linux.
func OpenWebUICmd(tcpAddr string) tea.Cmd {
	return func() tea.Msg {
		if tcpAddr == "" {
			tcpAddr = ipc.DefaultTCPAddr
		}
		tok, _, _ := ipc.LoadOrCreateAuthToken("")
		targetURL := "http://" + tcpAddr
		if tok != "" {
			targetURL += "/?token=" + tok
		}
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "windows":
			cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
		case "darwin":
			cmd = exec.Command("open", targetURL)
		default:
			cmd = exec.Command("xdg-open", targetURL)
		}
		if err := cmd.Start(); err != nil {
			return ActionFeedbackMsg{Err: err}
		}
		return ActionFeedbackMsg{
			Info: fmt.Sprintf("OPENED WEB UI IN BROWSER -> http://%s", tcpAddr),
		}
	}
}
