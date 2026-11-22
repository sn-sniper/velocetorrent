package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"velocetorrent/internal/engine"
	"velocetorrent/internal/ipc"
	"velocetorrent/internal/tui/hooks"
	"velocetorrent/internal/tui/views"
)

// ViewMode enumerates the active TUI screen.
type ViewMode int

const (
	ViewDashboard ViewMode = iota
	ViewPeerInspector
	ViewFileInspector
)

// ModalMode enumerates active inline text input modals.
type ModalMode int

const (
	ModalNone ModalMode = iota
	ModalChangeDir
	ModalAddTorrent
)

// AppModel is the root Bubble Tea state model for VeloceTorrent's terminal UI.
type AppModel struct {
	client        *ipc.Client
	tcpAddr       string
	snapshot      *engine.EngineTelemetrySnapshot
	selectedIdx   int
	viewMode      ViewMode
	modalMode     ModalMode
	inputBuffer   string
	inputPristine bool
	savedDirIdx   int
	statusBanner  string
	errorBanner   string
	width         int
	height        int
}

// NewAppModel creates a new Bubble Tea model connected to the local IPC client.
func NewAppModel(client *ipc.Client, tcpAddr string) *AppModel {
	if tcpAddr == "" {
		tcpAddr = ipc.DefaultTCPAddr
	}
	return &AppModel{
		client:   client,
		tcpAddr:  tcpAddr,
		viewMode: ViewDashboard,
		width:    120,
		height:   36,
	}
}

// Init starts the 100ms telemetry polling loop and fetches the initial engine state.
func (m *AppModel) Init() tea.Cmd {
	return tea.Batch(
		hooks.FetchTelemetryCmd(m.client),
		hooks.ScheduleNextTick(100*time.Millisecond),
	)
}

// Update processes incoming telemetry snapshots, window resize events, and keyboard actions.
func (m *AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case hooks.TickMsg:
		return m, tea.Batch(
			hooks.FetchTelemetryCmd(m.client),
			hooks.ScheduleNextTick(100*time.Millisecond),
		)

	case hooks.TelemetryUpdateMsg:
		if msg.Err != nil {
			m.errorBanner = msg.Err.Error()
			return m, nil
		}
		m.snapshot = msg.Snapshot
		if m.snapshot != nil && len(m.snapshot.Torrents) > 0 {
			if m.selectedIdx >= len(m.snapshot.Torrents) {
				m.selectedIdx = len(m.snapshot.Torrents) - 1
			}
			if m.selectedIdx < 0 {
				m.selectedIdx = 0
			}
		}
		return m, nil

	case hooks.ActionFeedbackMsg:
		if msg.Err != nil {
			m.errorBanner = msg.Err.Error()
			m.statusBanner = ""
		} else {
			m.statusBanner = msg.Info
			m.errorBanner = ""
		}
		return m, hooks.FetchTelemetryCmd(m.client)

	case tea.KeyMsg:
		if m.modalMode != ModalNone {
			return m.handleModalKey(msg)
		}
		return m.handleMainKey(msg)
	}
	return m, nil
}

func (m *AppModel) handleModalKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.modalMode = ModalNone
		m.inputBuffer = ""
		m.inputPristine = false
		return m, nil

	case tea.KeyCtrlU, tea.KeyCtrlW:
		m.inputBuffer = ""
		m.inputPristine = false
		return m, nil

	case tea.KeyTab:
		if m.modalMode == ModalChangeDir && m.snapshot != nil && len(m.snapshot.SavedDirs) > 0 {
			m.savedDirIdx = (m.savedDirIdx + 1) % len(m.snapshot.SavedDirs)
			m.inputBuffer = m.snapshot.SavedDirs[m.savedDirIdx]
			m.inputPristine = false
		}
		return m, nil

	case tea.KeyBackspace, tea.KeyDelete:
		if m.inputPristine {
			m.inputBuffer = ""
			m.inputPristine = false
			return m, nil
		}
		runes := []rune(m.inputBuffer)
		if len(runes) > 0 {
			m.inputBuffer = string(runes[:len(runes)-1])
		}
		return m, nil

	case tea.KeyEnter:
		val := engine.NormalizeDirectoryPath(m.inputBuffer)
		mode := m.modalMode
		m.modalMode = ModalNone
		m.inputBuffer = ""
		m.inputPristine = false
		if val == "" {
			return m, nil
		}
		if mode == ModalChangeDir {
			targetID := m.selectedTorrentID()
			return m, hooks.ChangeDirCmd(m.client, targetID, val)
		}
		if mode == ModalAddTorrent {
			defaultDir := ""
			if m.snapshot != nil {
				defaultDir = m.snapshot.DefaultOutputDir
			}
			return m, hooks.AddTorrentCmd(m.client, val, defaultDir)
		}
		return m, nil

	default:
		if msg.Type == tea.KeySpace {
			if m.inputPristine {
				m.inputBuffer = ""
				m.inputPristine = false
			}
			m.inputBuffer += " "
		} else if len(msg.Runes) > 0 {
			var printable strings.Builder
			for _, r := range msg.Runes {
				if r >= 32 && r != 127 {
					printable.WriteRune(r)
				}
			}
			if printable.Len() > 0 {
				if m.inputPristine {
					m.inputBuffer = ""
					m.inputPristine = false
				}
				m.inputBuffer += printable.String()
			}
		}
		return m, nil
	}
}

func (m *AppModel) handleMainKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	action := hooks.ResolveKeyAction(msg)
	switch action {
	case hooks.ActionDetach:
		return m, tea.Quit

	case hooks.ActionCursorUp:
		if m.selectedIdx > 0 {
			m.selectedIdx--
		}
		return m, nil

	case hooks.ActionCursorDown:
		if m.snapshot != nil && m.selectedIdx < len(m.snapshot.Torrents)-1 {
			m.selectedIdx++
		}
		return m, nil

	case hooks.ActionTogglePause:
		id := m.selectedTorrentID()
		if id != "" {
			return m, hooks.TogglePauseCmd(m.client, id)
		}
		return m, nil

	case hooks.ActionOpenDirModal:
		m.modalMode = ModalChangeDir
		if sel := m.selectedTorrent(); sel != nil {
			m.inputBuffer = sel.OutputDir
		} else if m.snapshot != nil {
			m.inputBuffer = m.snapshot.DefaultOutputDir
		}
		m.inputPristine = true
		return m, nil

	case hooks.ActionOpenAddModal:
		m.modalMode = ModalAddTorrent
		m.inputBuffer = ""
		return m, nil

	case hooks.ActionInspectPeers:
		if m.viewMode == ViewPeerInspector {
			m.viewMode = ViewDashboard
		} else {
			m.viewMode = ViewPeerInspector
		}
		return m, nil

	case hooks.ActionInspectFiles:
		if m.viewMode == ViewFileInspector {
			m.viewMode = ViewDashboard
		} else {
			m.viewMode = ViewFileInspector
		}
		return m, nil

	case hooks.ActionDashboardView:
		m.viewMode = ViewDashboard
		m.errorBanner = ""
		return m, nil

	case hooks.ActionSpawnSynthetic:
		return m, hooks.SpawnSyntheticSwarmCmd(m.tcpAddr)

	case hooks.ActionDeleteTorrent:
		id := m.selectedTorrentID()
		if id != "" {
			return m, hooks.DeleteTorrentCmd(m.client, id)
		}
		return m, nil

	case hooks.ActionToggleThrottle:
		return m, hooks.ToggleThrottleCmd(m.client)

	case hooks.ActionOpenWebUI:
		return m, hooks.OpenWebUICmd(m.tcpAddr)

	case hooks.ActionCycleFilePriority:
		sel := m.selectedTorrent()
		if sel != nil && len(sel.Files) > 0 {
			cur := sel.Files[0].Priority
			next := engine.PriorityHigh
			switch cur {
			case engine.PriorityNormal, "":
				next = engine.PriorityHigh
			case engine.PriorityHigh:
				next = engine.PrioritySkip
			default:
				next = engine.PriorityNormal
			}
			return m, hooks.CycleFilePriorityCmd(m.client, sel.ID, 0, next)
		}
		return m, nil
	}
	return m, nil
}

func (m *AppModel) selectedTorrent() *engine.TorrentTelemetrySnapshot {
	if m.snapshot == nil || len(m.snapshot.Torrents) == 0 {
		return nil
	}
	if m.selectedIdx < 0 || m.selectedIdx >= len(m.snapshot.Torrents) {
		return &m.snapshot.Torrents[0]
	}
	return &m.snapshot.Torrents[m.selectedIdx]
}

func (m *AppModel) selectedTorrentID() string {
	sel := m.selectedTorrent()
	if sel == nil {
		return ""
	}
	return sel.ID
}

// View renders the complete 60fps terminal layout.
func (m *AppModel) View() string {
	tabLabel := "DASHBOARD"
	switch m.viewMode {
	case ViewPeerInspector:
		tabLabel = "PEER INSPECTOR"
	case ViewFileInspector:
		tabLabel = "FILE EXTENTS"
	}

	var sections []string
	sections = append(sections, views.RenderTopTelemetryBar(m.snapshot, m.width, tabLabel))
	sections = append(sections, views.RenderVelocitySection(m.snapshot, m.width))

	switch m.viewMode {
	case ViewPeerInspector:
		sections = append(sections, views.RenderPeerInspectorView(m.snapshot, m.selectedIdx, m.width))
	case ViewFileInspector:
		sections = append(sections, views.RenderFileInspectorView(m.snapshot, m.selectedIdx, m.width))
	default:
		sections = append(sections, views.RenderDashboardView(m.snapshot, m.selectedIdx, m.width, m.height))
	}

	if m.modalMode == ModalChangeDir {
		var saved []string
		if m.snapshot != nil {
			saved = m.snapshot.SavedDirs
		}
		selName := ""
		if sel := m.selectedTorrent(); sel != nil {
			selName = sel.Name
		}
		sections = append(sections, views.RenderDirInputModal(m.inputBuffer, saved, selName, m.width))
	} else if m.modalMode == ModalAddTorrent {
		defDir := ""
		if m.snapshot != nil {
			defDir = m.snapshot.DefaultOutputDir
		}
		sections = append(sections, views.RenderAddInputModal(m.inputBuffer, defDir, m.width))
	}

	sections = append(sections, views.RenderBottomControlBar(m.statusBanner, m.errorBanner, m.width))
	return strings.Join(sections, "\n")
}

// Run attaches the interactive Bubble Tea TUI to the user's terminal.
func Run(client *ipc.Client, tcpAddr string) error {
	model := NewAppModel(client, tcpAddr)
	prog := tea.NewProgram(model, tea.WithAltScreen())
	_, err := prog.Run()
	return err
}
