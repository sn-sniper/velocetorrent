package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const defaultIPCAddr = "127.0.0.1:7878"

func main() {
	noLaunch := flag.Bool("no-launch", false, "Install binaries and start vtd without opening interactive TUI window")
	addr := flag.String("addr", defaultIPCAddr, "Local IPC daemon bind address")
	flag.Parse()

	homeDir, err := os.UserHomeDir()
	if err != nil || homeDir == "" {
		homeDir = os.Getenv("USERPROFILE")
	}
	if homeDir == "" {
		fatalf("unable to resolve user home directory")
	}

	desktopDir := filepath.Join(homeDir, "Desktop")
	installDir := filepath.Join(desktopDir, "VeloceTorrent")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		fatalf("failed to create install directory %s: %v", installDir, err)
	}

	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	vtdTarget := filepath.Join(installDir, "vtd"+ext)
	vtTarget := filepath.Join(installDir, "vt"+ext)

	fmt.Printf("[VELOCETORRENT SETUP] Installing VeloceTorrent to %s\n", installDir)

	repoRoot := detectRepoRoot()
	builtFromSource := false

	if repoRoot != "" {
		ensureWebDist(repoRoot)
		if goBin := findGoBinary(); goBin != "" {
			fmt.Printf("[VELOCETORRENT SETUP] Compiling stripped vtd%s and vt%s from source (%s)...\n", ext, ext, repoRoot)
			// Stop any existing target vtd process if locked on Windows
			_ = buildGoBinary(goBin, repoRoot, "./cmd/daemon", vtdTarget)
			errVT := buildGoBinary(goBin, repoRoot, "./cmd/cli", vtTarget)
			if fileExists(vtdTarget) && errVT == nil && fileExists(vtTarget) {
				builtFromSource = true
			}
		}
	}

	if !builtFromSource {
		srcVtd, srcVt := findPrebuiltBinaries(repoRoot, ext)
		if srcVtd == "" || srcVt == "" {
			fatalf("could not locate Go compiler or prebuilt vtd%s / vt%s binaries", ext, ext)
		}
		fmt.Printf("[VELOCETORRENT SETUP] Copying prebuilt binaries into %s...\n", installDir)
		_ = copyFileExecutable(srcVtd, vtdTarget)
		if err := copyFileExecutable(srcVt, vtTarget); err != nil {
			fatalf("failed to install vt binary: %v", err)
		}
	}

	// Create Desktop shortcut & launcher helper on Windows
	if runtime.GOOS == "windows" {
		createWindowsDesktopShortcut(desktopDir, installDir, vtTarget, *addr)
	}

	// Spawn vtd silently in background if not already listening
	if !isDaemonHealthy(*addr) {
		fmt.Printf("[VELOCETORRENT SETUP] Starting background daemon vtd on %s...\n", *addr)
		if err := spawnSilentDaemon(vtdTarget, *addr); err != nil {
			fmt.Fprintf(os.Stderr, "[WARN] background daemon spawn returned: %v\n", err)
		}
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			if isDaemonHealthy(*addr) {
				break
			}
			time.Sleep(80 * time.Millisecond)
		}
	} else {
		fmt.Printf("[VELOCETORRENT SETUP] Background daemon already online at http://%s\n", *addr)
	}

	fmt.Printf("[VELOCETORRENT SETUP] Ready! Binaries: %s | Web UI: http://%s\n", installDir, *addr)

	if *noLaunch {
		return
	}

	if err := launchInteractiveTUI(vtTarget, *addr); err != nil {
		fmt.Fprintf(os.Stderr, "[WARN] Failed to launch interactive terminal window: %v\n", err)
	}
}

func detectRepoRoot() string {
	candidates := []string{}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd, filepath.Dir(cwd), filepath.Dir(filepath.Dir(cwd)))
	}
	if exe, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exe)
		candidates = append(candidates, exeDir, filepath.Dir(exeDir), filepath.Dir(filepath.Dir(exeDir)))
	}
	for _, dir := range candidates {
		if fileExists(filepath.Join(dir, "go.mod")) && fileExists(filepath.Join(dir, "cmd", "daemon", "main.go")) {
			return dir
		}
	}
	return ""
}

func ensureWebDist(repoRoot string) {
	distIndex := filepath.Join(repoRoot, "web", "dist", "index.html")
	if fileExists(distIndex) {
		return
	}
	pkgJSON := filepath.Join(repoRoot, "web", "package.json")
	if !fileExists(pkgJSON) {
		return
	}
	npmBin, err := exec.LookPath("npm")
	if err != nil {
		return
	}
	fmt.Println("[VELOCETORRENT SETUP] Building embedded Web UI (web/dist)...")
	installCmd := exec.Command(npmBin, "install", "--no-audit", "--no-fund")
	installCmd.Dir = filepath.Join(repoRoot, "web")
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	_ = installCmd.Run()

	buildCmd := exec.Command(npmBin, "run", "build")
	buildCmd.Dir = filepath.Join(repoRoot, "web")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	_ = buildCmd.Run()
}

func findGoBinary() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	winGo := `C:\Program Files\Go\bin\go.exe`
	if fileExists(winGo) {
		return winGo
	}
	return ""
}

func buildGoBinary(goBin, repoRoot, pkgPath, outPath string) error {
	cmd := exec.Command(goBin, "build", "-trimpath", "-ldflags=-s -w", "-o", outPath, pkgPath)
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func findPrebuiltBinaries(repoRoot, ext string) (string, string) {
	dirs := []string{}
	if cwd, err := os.Getwd(); err == nil {
		dirs = append(dirs, cwd)
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if repoRoot != "" {
		dirs = append(dirs, repoRoot)
	}
	for _, d := range dirs {
		vtd := filepath.Join(d, "vtd"+ext)
		vt := filepath.Join(d, "vt"+ext)
		if fileExists(vtd) && fileExists(vt) {
			return vtd, vt
		}
	}
	return "", ""
}

func copyFileExecutable(src, dst string) error {
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func isDaemonHealthy(addr string) bool {
	client := &http.Client{Timeout: 450 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/api/health")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func createWindowsDesktopShortcut(desktopDir, installDir, vtTarget, addr string) {
	batPath := filepath.Join(installDir, "Start-VeloceTorrent.bat")
	batContent := fmt.Sprintf("@echo off\r\ncd /d \"%s\"\r\n\"%s\" ui --addr %s\r\n", installDir, vtTarget, addr)
	_ = os.WriteFile(batPath, []byte(batContent), 0o755)

	lnkPath := filepath.Join(desktopDir, "VeloceTorrent.lnk")
	psScript := fmt.Sprintf(
		`$ws = New-Object -ComObject WScript.Shell; $s = $ws.CreateShortcut('%s'); $s.TargetPath = '%s'; $s.Arguments = 'ui --addr %s'; $s.WorkingDirectory = '%s'; $s.Description = 'VeloceTorrent High-Throughput BitTorrent Client'; $s.Save()`,
		lnkPath, vtTarget, addr, installDir,
	)
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", psScript)
	configureSilentProc(cmd)
	_ = cmd.Run()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[VELOCETORRENT SETUP ERROR] "+format+"\n", args...)
	os.Exit(1)
}
