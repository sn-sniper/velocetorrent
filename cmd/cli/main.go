package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"velocetorrent/internal/engine"
	"velocetorrent/internal/ipc"
	"velocetorrent/internal/mcp"
	"velocetorrent/internal/tui"
	"velocetorrent/internal/tui/components"
)

func main() {
	args := os.Args[1:]
	ipcAddr := os.Getenv("VT_IPC_ADDR")
	if ipcAddr == "" {
		ipcAddr = ipc.DefaultTCPAddr
	}
	sockPath := os.Getenv("VT_SOCKET_PATH")
	if sockPath == "" {
		sockPath = ipc.DefaultUnixSocketPath()
	}

	if len(args) == 0 {
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		if err := tui.Run(client, ipcAddr); err != nil {
			exitWithError(err)
		}
		return
	}

	subcmd := strings.ToLower(args[0])
	switch subcmd {
	case "add":
		if err := handleAddCommand(args[1:], ipcAddr, sockPath); err != nil {
			exitWithError(err)
		}

	case "ui", "tui", "attach":
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		if err := tui.Run(client, ipcAddr); err != nil {
			exitWithError(err)
		}

	case "ls", "list", "status":
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		if err := printTabularStatus(client); err != nil {
			exitWithError(err)
		}

	case "pause":
		if len(args) < 2 {
			exitWithError(errors.New("missing torrent ID or prefix\nUsage: vt pause <id>"))
		}
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		snap, err := client.Pause(args[1])
		if err != nil {
			exitWithError(err)
		}
		fmt.Fprintf(os.Stdout, "PAUSED   InfoHash=%s  Name=%q  Status=%s\n", snap.InfoHash, snap.Name, snap.Status)

	case "resume":
		if len(args) < 2 {
			exitWithError(errors.New("missing torrent ID or prefix\nUsage: vt resume <id>"))
		}
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		snap, err := client.Resume(args[1])
		if err != nil {
			exitWithError(err)
		}
		fmt.Fprintf(os.Stdout, "RESUMED  InfoHash=%s  Name=%q  Status=%s\n", snap.InfoHash, snap.Name, snap.Status)

	case "rm", "remove", "delete":
		if len(args) < 2 {
			exitWithError(errors.New("missing torrent ID or prefix\nUsage: vt rm <id> [--delete-data]"))
		}
		deleteData := len(args) > 2 && (args[2] == "--delete-data" || args[2] == "-d")
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		if err := client.DeleteTorrent(args[1], deleteData); err != nil {
			exitWithError(err)
		}
		fmt.Fprintf(os.Stdout, "REMOVED  ID=%s  DeleteData=%v\n", args[1], deleteData)

	case "dir":
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		if len(args) < 2 {
			snap, err := client.FetchState()
			if err != nil {
				exitWithError(err)
			}
			fmt.Fprintf(os.Stdout, "DEFAULT DIR: %s\n", snap.DefaultOutputDir)
			for _, d := range snap.SavedDirs {
				fmt.Fprintf(os.Stdout, "  SAVED: %s\n", d)
			}
			return
		}
		res, err := client.SetDefaultDir(args[1])
		if err != nil {
			exitWithError(err)
		}
		fmt.Fprintf(os.Stdout, "DEFAULT DIR -> %s\n", res.DefaultOutputDir)

	case "priority":
		if len(args) < 4 {
			exitWithError(errors.New("usage: vt priority <torrent-id> <file-index> <skip|normal|high>"))
		}
		fileIdx, err := strconv.Atoi(args[2])
		if err != nil {
			exitWithError(fmt.Errorf("invalid file index %q: %w", args[2], err))
		}
		prio := engine.FilePriority(strings.ToLower(strings.TrimSpace(args[3])))
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		snap, err := client.SetFilePriority(args[1], fileIdx, prio)
		if err != nil {
			exitWithError(err)
		}
		fmt.Fprintf(os.Stdout, "FILE PRIORITY UPDATED  Torrent=%q  FileIndex=%d  Priority=%s\n", snap.Name, fileIdx, prio)

	case "throttle":
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		mode := "toggle"
		if len(args) >= 2 {
			mode = strings.ToLower(strings.TrimSpace(args[1]))
		}
		var snap *engine.ThrottleSnapshot
		var err error
		switch mode {
		case "uncapped", "max", "off":
			snap, err = client.SetThrottle(engine.ProfileUncapped, false, 0, 0)
		case "background", "bg", "low":
			snap, err = client.SetThrottle(engine.ProfileBackground, false, 0, 0)
		default:
			snap, err = client.SetThrottle("", true, 0, 0)
		}
		if err != nil {
			exitWithError(err)
		}
		fmt.Fprintf(os.Stdout, "THROTTLE PROFILE -> %s (MaxDown=%s MaxUp=%s)\n",
			snap.Profile, strings.TrimSpace(components.FormatRate(snap.MaxDownBytesPs)), strings.TrimSpace(components.FormatRate(snap.MaxUpBytesPs)))

	case "completion":
		if err := handleCompletionCommand(args[1:]); err != nil {
			exitWithError(err)
		}

	case "__complete":
		_ = handleDynamicComplete(args[1:], ipcAddr, sockPath)

	case "mcp":
		if err := ensureDaemonRunning(ipcAddr, sockPath, ""); err != nil {
			exitWithError(err)
		}
		client := ipc.NewClient(ipcAddr, sockPath)
		srv := mcp.NewServer(client)
		if err := srv.ServeStdio(os.Stdin, os.Stdout); err != nil {
			exitWithError(err)
		}

	case "bench", "benchmark":
		if err := runThroughputBenchmark(args[1:]); err != nil {
			exitWithError(err)
		}

	case "register-handlers":
		if err := registerDesktopAndBrowserHandlers(); err != nil {
			exitWithError(err)
		}

	case "daemon":
		if err := runForegroundDaemon(args[1:], ipcAddr, sockPath); err != nil {
			exitWithError(err)
		}

	case "help", "-h", "--help":
		printUsage()

	default:
		// If user passed a .torrent file or magnet:? directly (e.g. from browser/desktop handler `vt <magnet>`), route to `vt add`
		if strings.HasPrefix(strings.ToLower(args[0]), "magnet:?") || strings.HasSuffix(strings.ToLower(args[0]), ".torrent") {
			if err := handleAddCommand(args, ipcAddr, sockPath); err != nil {
				exitWithError(err)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "Unknown command %q\n\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func handleAddCommand(args []string, ipcAddr string, sockPath string) error {
	var (
		sources    []string
		outputDir  string
		detach     bool
		sequential bool
		stream     bool
	)

	// Parse flags in any position (`vt add <file|magnet> --dir <path> --stream --detach`)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--dir" || arg == "-d":
			if i+1 >= len(args) {
				return errors.New("missing path argument after --dir")
			}
			outputDir = args[i+1]
			i++
		case strings.HasPrefix(arg, "--dir="):
			outputDir = strings.TrimPrefix(arg, "--dir=")
		case arg == "--detach" || arg == "-detach":
			detach = true
		case arg == "--seq" || arg == "--sequential":
			sequential = true
		case arg == "--stream" || arg == "-stream":
			stream = true
			sequential = true
		case strings.HasPrefix(arg, "-"):
			return fmt.Errorf("unknown flag %q", arg)
		default:
			sources = append(sources, arg)
		}
	}

	if len(sources) == 0 {
		return errors.New("missing <file.torrent|magnet> argument\nUsage: vt add <file.torrent|magnet> [--dir /path/to/output] [--stream] [--detach]")
	}

	if err := ensureDaemonRunning(ipcAddr, sockPath, outputDir); err != nil {
		return err
	}

	client := ipc.NewClient(ipcAddr, sockPath)
	for _, src := range sources {
		snap, err := client.AddTorrentWithOptions(src, outputDir, sequential, stream)
		if err != nil {
			return err
		}
		if detach {
			fmt.Fprintf(os.Stdout, "QUEUED  InfoHash=%s  Name=%q  Size=%s  Dir=%s  Stream=%v  Status=%s\n",
				snap.InfoHash, snap.Name, strings.TrimSpace(components.FormatBytes(snap.TotalSize)), snap.OutputDir, snap.StreamMode, snap.Status)
		}
	}

	if detach {
		return nil
	}

	return tui.Run(client, ipcAddr)
}

// ensureDaemonRunning checks if the background daemon is reachable over IPC;
// if not, it automatically forks `vtd` (or `vt daemon` fallback) detached in the background.
func ensureDaemonRunning(ipcAddr string, sockPath string, defaultDir string) error {
	client := ipc.NewClient(ipcAddr, sockPath)
	if client.Ping() {
		return nil
	}

	daemonBin, daemonArgs, err := resolveDaemonCommand(ipcAddr, sockPath, defaultDir)
	if err != nil {
		return err
	}

	cmd := exec.Command(daemonBin, daemonArgs...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to fork background daemon (%s): %w", daemonBin, err)
	}
	_ = cmd.Process.Release()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if client.Ping() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for VeloceTorrent daemon on %s", ipcAddr)
}

func resolveDaemonCommand(ipcAddr string, sockPath string, defaultDir string) (string, []string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("cannot resolve executable path: %w", err)
	}
	exeDir := filepath.Dir(exePath)

	// Prefer dedicated `vtd` / `vtd.exe` binary if present alongside `vt` or in PATH
	candidates := []string{
		filepath.Join(exeDir, "vtd.exe"),
		filepath.Join(exeDir, "vtd"),
		filepath.Join(exeDir, "velocetorrent-daemon.exe"),
	}
	for _, c := range candidates {
		if info, statErr := os.Stat(c); statErr == nil && !info.IsDir() {
			args := []string{"--addr", ipcAddr, "--socket", sockPath}
			if defaultDir != "" {
				args = append(args, "--dir", defaultDir)
			}
			return c, args, nil
		}
	}
	if pathVtd, lookErr := exec.LookPath("vtd"); lookErr == nil {
		args := []string{"--addr", ipcAddr, "--socket", sockPath}
		if defaultDir != "" {
			args = append(args, "--dir", defaultDir)
		}
		return pathVtd, args, nil
	}

	// Fallback to self-forking `vt daemon`
	args := []string{"daemon", "--addr", ipcAddr, "--socket", sockPath}
	if defaultDir != "" {
		args = append(args, "--dir", defaultDir)
	}
	return exePath, args, nil
}

func runForegroundDaemon(args []string, ipcAddr string, sockPath string) error {
	var outDir string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--addr":
			if i+1 < len(args) {
				ipcAddr = args[i+1]
				i++
			}
		case "--socket":
			if i+1 < len(args) {
				sockPath = args[i+1]
				i++
			}
		case "--dir":
			if i+1 < len(args) {
				outDir = args[i+1]
				i++
			}
		}
	}

	eng, err := engine.NewEngine(engine.Config{
		DefaultOutputDir: outDir,
		ListenAddr:       "0.0.0.0:0",
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	srv := ipc.NewServer(eng, ipcAddr, sockPath)
	if err := srv.Start(); err != nil {
		return err
	}
	defer srv.Close()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	return nil
}

// runThroughputBenchmark executes Phase 4 Item 1: end-to-end TCP BitTorrent wire + SHA-1 worker pool + sparse NVMe/SSD pre-allocation benchmark.
func runThroughputBenchmark(args []string) error {
	sizeMB := int64(32)
	for i := 0; i < len(args); i++ {
		if (args[i] == "--size-mb" || args[i] == "-s") && i+1 < len(args) {
			if v, err := strconv.ParseInt(args[i+1], 10, 64); err == nil && v > 0 {
				sizeMB = v
			}
			i++
		}
	}

	tmpDir, err := os.MkdirTemp("", "vt-bench-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)

	eng, err := engine.NewEngine(engine.Config{
		DefaultOutputDir: tmpDir,
		ListenAddr:       "127.0.0.1:0",
	})
	if err != nil {
		return err
	}
	defer eng.Close()

	totalBytes := sizeMB * 1024 * 1024
	fmt.Fprintf(os.Stdout, "VELOCETORRENT // PHASE 4 THROUGHPUT & DISK I/O BENCHMARK\n")
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 88))
	fmt.Fprintf(os.Stdout, "PAYLOAD SIZE : %d MiB (%d bytes)\n", sizeMB, totalBytes)
	fmt.Fprintf(os.Stdout, "HASH WORKERS : %d threads (SHA-1 Piece Verification Pool)\n", runtime.NumCPU())
	fmt.Fprintf(os.Stdout, "STORAGE MODE : Pre-Allocated Sparse Extent + Sequential Batch Flush\n")
	fmt.Fprintf(os.Stdout, "PIPELINE     : Adaptive BDP (Max %d x 16 KiB In-Flight Blocks)\n", engine.MaxPipelineDepth)
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 88))

	start := time.Now()
	sess, _, err := eng.CreateSyntheticSwarmTorrent("ubuntu-26.04-live-server-amd64.iso", totalBytes, tmpDir, false)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		snap := sess.Snapshot(false)
		if snap.Status == engine.StatusSeeding && snap.CompletedPieces == snap.PieceCount {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	elapsed := time.Since(start)
	snap := sess.Snapshot(true)
	if snap.Status != engine.StatusSeeding {
		return fmt.Errorf("benchmark did not complete within deadline (status=%s, %d/%d pieces)",
			snap.Status, snap.CompletedPieces, snap.PieceCount)
	}

	mbPerSec := float64(totalBytes) / (1024.0 * 1024.0) / elapsed.Seconds()
	gbps := (float64(totalBytes) * 8.0) / 1e9 / elapsed.Seconds()

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	fmt.Fprintf(os.Stdout, "STATUS       : COMPLETED & VERIFIED (%d/%d pieces)\n", snap.CompletedPieces, snap.PieceCount)
	fmt.Fprintf(os.Stdout, "WALL TIME    : %d ms\n", elapsed.Milliseconds())
	fmt.Fprintf(os.Stdout, "THROUGHPUT   : %.2f MB/s (%.2f Gbps)\n", mbPerSec, gbps)
	fmt.Fprintf(os.Stdout, "HEAP IN USE  : %.2f MiB\n", float64(mem.HeapAlloc)/(1024.0*1024.0))
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 88))
	return nil
}

// registerDesktopAndBrowserHandlers implements Phase 4 Item 3: registering `vt` as the default handler for magnet: and .torrent.
func registerDesktopAndBrowserHandlers() error {
	exePath, err := os.Executable()
	if err != nil {
		return err
	}
	absExe, _ := filepath.Abs(exePath)

	if runtime.GOOS == "windows" {
		// Register magnet: URI protocol handler under HKCU\Software\Classes\magnet (no admin elevation required)
		cmds := [][]string{
			{"reg", "add", `HKCU\Software\Classes\magnet`, "/ve", "/d", "URL:BitTorrent Magnet Protocol", "/f"},
			{"reg", "add", `HKCU\Software\Classes\magnet`, "/v", "URL Protocol", "/d", "", "/f"},
			{"reg", "add", `HKCU\Software\Classes\magnet\shell\open\command`, "/ve", "/d", fmt.Sprintf(`"%s" add "%%1"`, absExe), "/f"},
		}
		for _, c := range cmds {
			if out, err := exec.Command(c[0], c[1:]...).CombinedOutput(); err != nil {
				return fmt.Errorf("registry update failed (%v): %s", err, string(out))
			}
		}
		fmt.Fprintf(os.Stdout, "REGISTERED magnet: URI handler -> %q add \"%%1\"\n", absExe)
		return nil
	}

	// Linux / FreeDesktop XDG registration
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	appsDir := filepath.Join(home, ".local", "share", "applications")
	if err := os.MkdirAll(appsDir, 0o755); err != nil {
		return err
	}
	desktopFile := filepath.Join(appsDir, "velocetorrent.desktop")
	desktopContent := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=VeloceTorrent
Comment=High-Throughput BitTorrent Engine & Terminal UI
Exec=%s add %%U
Terminal=true
Categories=Network;FileTransfer;P2P;
MimeType=application/x-bittorrent;x-scheme-handler/magnet;
`, absExe)

	if err := os.WriteFile(desktopFile, []byte(desktopContent), 0o644); err != nil {
		return err
	}

	_ = exec.Command("xdg-mime", "default", "velocetorrent.desktop", "x-scheme-handler/magnet").Run()
	_ = exec.Command("xdg-mime", "default", "velocetorrent.desktop", "application/x-bittorrent").Run()
	fmt.Fprintf(os.Stdout, "REGISTERED %s via xdg-mime for x-scheme-handler/magnet and application/x-bittorrent\n", desktopFile)
	return nil
}

func printTabularStatus(client *ipc.Client) error {
	snap, err := client.FetchState()
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "DAEMON ONLINE // UPTIME: %ds // DOWN: %s // UP: %s // IOPS: %d // DIR: %s\n",
		snap.UptimeSeconds,
		strings.TrimSpace(components.FormatRate(snap.GlobalDownBytesPs)),
		strings.TrimSpace(components.FormatRate(snap.GlobalUpBytesPs)),
		snap.GlobalIOPS,
		snap.DefaultOutputDir,
	)
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 104))
	fmt.Fprintf(os.Stdout, "%-28s %-12s %8s %11s %11s %11s %8s %7s\n",
		"NAME", "STATE", "PROGRESS", "SIZE", "DOWN", "UP", "ETA", "SWARM")
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 104))
	if len(snap.Torrents) == 0 {
		fmt.Fprintln(os.Stdout, "NO ACTIVE TORRENTS.")
		return nil
	}
	for _, t := range snap.Torrents {
		fmt.Fprintf(os.Stdout, "%-28s %-12s %7.1f%% %11s %11s %11s %8s %3d/%-3d\n",
			components.PadRight(t.Name, 28),
			string(t.Status),
			t.Progress,
			components.FormatBytes(t.TotalSize),
			components.FormatRate(t.DownBytesPs),
			components.FormatRate(t.UpBytesPs),
			components.FormatETA(t.ETASeconds),
			t.ActivePeers,
			t.TotalSwarmPeers,
		)
	}
	return nil
}

func printUsage() {
	fmt.Fprintln(os.Stdout, `VeloceTorrent CLI & Terminal UI (vt)

USAGE:
  vt                                                Start daemon (vtd) and attach 60fps TUI
  vt ui                                             Attach 60fps TUI to running daemon
  vt add <file.torrent|magnet> [--dir <path>]       Queue download and attach interactive TUI
  vt add <file.torrent|magnet> --detach             Queue download in headless mode and exit immediately
  vt ls | vt status                                 Print tabular telemetry snapshot
  vt pause <id>                                     Pause transfer by info_hash, prefix, or name
  vt resume <id>                                    Resume transfer by info_hash, prefix, or name
  vt rm <id> [--delete-data]                        Remove transfer from daemon
  vt bench [--size-mb 32]                           Run Phase 4 throughput & NVMe/SSD I/O benchmark
  vt register-handlers                              Register vt for magnet: URIs and .torrent MIME types
  vt daemon [--addr 127.0.0.1:7878] [--dir <path>]  Run headless engine daemon in foreground`)
}

func exitWithError(err error) {
	fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
	os.Exit(1)
}
