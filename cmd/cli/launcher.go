package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OpenBrowserURL opens the given URL in the user's default web browser across Windows, macOS, and Linux.
func OpenBrowserURL(targetURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
	case "darwin":
		cmd = exec.Command("open", targetURL)
	default:
		cmd = exec.Command("xdg-open", targetURL)
	}
	return cmd.Start()
}

// InstallSystemIntegration performs 1-command native OS installation across Windows, macOS, and Linux.
func InstallSystemIntegration(ipcAddr string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot resolve executable path: %w", err)
	}
	absExe, _ := filepath.Abs(exePath)
	exeDir := filepath.Dir(absExe)

	switch runtime.GOOS {
	case "windows":
		return installWindowsIntegration(absExe, exeDir, ipcAddr)
	case "darwin":
		return installMacOSIntegration(absExe, exeDir, ipcAddr)
	default:
		return installLinuxIntegration(absExe, exeDir, ipcAddr)
	}
}

func installWindowsIntegration(vtExe, exeDir, ipcAddr string) error {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		home, _ := os.UserHomeDir()
		localAppData = filepath.Join(home, "AppData", "Local")
	}
	installDir := filepath.Join(localAppData, "Programs", "VeloceTorrent")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return err
	}

	dstVt := filepath.Join(installDir, "vt.exe")
	dstVtd := filepath.Join(installDir, "vtd.exe")
	_ = copyExecutableIfDifferent(vtExe, dstVt)
	srcVtd := filepath.Join(exeDir, "vtd.exe")
	if _, err := os.Stat(srcVtd); err == nil {
		_ = copyExecutableIfDifferent(srcVtd, dstVtd)
	} else {
		_ = copyExecutableIfDifferent(vtExe, dstVtd)
	}

	// 1. Register Windows Startup background daemon (HKCU\Software\Microsoft\Windows\CurrentVersion\Run)
	daemonCmd := fmt.Sprintf(`"%s" --addr %s`, dstVtd, ipcAddr)
	_ = exec.Command("reg", "add", `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`,
		"/v", "VeloceTorrentDaemon", "/d", daemonCmd, "/f").Run()

	// 2. Register magnet: URI handler
	_ = registerDesktopAndBrowserHandlers()

	// 3. Create Desktop 1-click Web UI & TUI shortcuts
	if home, err := os.UserHomeDir(); err == nil {
		desktopDir := filepath.Join(home, "Desktop")
		if info, stErr := os.Stat(desktopDir); stErr == nil && info.IsDir() {
			urlShortcut := filepath.Join(desktopDir, "VeloceTorrent Web UI.url")
			urlContent := fmt.Sprintf("[InternetShortcut]\r\nURL=http://%s\r\n", ipcAddr)
			_ = os.WriteFile(urlShortcut, []byte(urlContent), 0o644)

			batShortcut := filepath.Join(desktopDir, "VeloceTorrent TUI.bat")
			batContent := fmt.Sprintf("@echo off\r\n\"%s\" open\r\n\"%s\" ui\r\n", dstVt, dstVt)
			_ = os.WriteFile(batShortcut, []byte(batContent), 0o644)
		}
	}

	// 4. Add installDir to User PATH if not already present
	userPathOut, _ := exec.Command("powershell", "-NoProfile", "-Command",
		`[Environment]::GetEnvironmentVariable('Path', 'User')`).CombinedOutput()
	currentUserPath := strings.TrimSpace(string(userPathOut))
	if !strings.Contains(strings.ToLower(currentUserPath), strings.ToLower(installDir)) {
		newPath := currentUserPath
		if newPath != "" && !strings.HasSuffix(newPath, ";") {
			newPath += ";"
		}
		newPath += installDir
		_ = exec.Command("powershell", "-NoProfile", "-Command",
			fmt.Sprintf(`[Environment]::SetEnvironmentVariable('Path', '%s', 'User')`, strings.ReplaceAll(newPath, "'", "''"))).Run()
	}

	fmt.Fprintln(os.Stdout, "WINDOWS INSTALLATION COMPLETE")
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 80))
	fmt.Fprintf(os.Stdout, "INSTALLED BINARIES : %s\n", installDir)
	fmt.Fprintf(os.Stdout, "AUTO-START ON BOOT : Enabled (HKCU\\...\\Run\\VeloceTorrentDaemon)\n")
	fmt.Fprintf(os.Stdout, "DESKTOP SHORTCUTS  : 'VeloceTorrent Web UI.url' & 'VeloceTorrent TUI.bat'\n")
	fmt.Fprintf(os.Stdout, "MAGNET HANDLER     : Registered (magnet:? links open directly in VeloceTorrent)\n")
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 80))
	return nil
}

func installMacOSIntegration(vtExe, exeDir, ipcAddr string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	binDir := filepath.Join(home, ".local", "bin")
	_ = os.MkdirAll(binDir, 0o755)

	dstVt := filepath.Join(binDir, "vt")
	dstVtd := filepath.Join(binDir, "vtd")
	_ = copyExecutableIfDifferent(vtExe, dstVt)
	_ = os.Chmod(dstVt, 0o755)

	srcVtd := filepath.Join(exeDir, "vtd")
	if _, err := os.Stat(srcVtd); err == nil {
		_ = copyExecutableIfDifferent(srcVtd, dstVtd)
	} else {
		_ = copyExecutableIfDifferent(vtExe, dstVtd)
	}
	_ = os.Chmod(dstVtd, 0o755)

	// 1. Install macOS LaunchAgent (~/Library/LaunchAgents/com.velocetorrent.daemon.plist)
	launchAgentsDir := filepath.Join(home, "Library", "LaunchAgents")
	_ = os.MkdirAll(launchAgentsDir, 0o755)
	plistPath := filepath.Join(launchAgentsDir, "com.velocetorrent.daemon.plist")
	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.velocetorrent.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>--addr</string>
        <string>%s</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
</dict>
</plist>
`, dstVtd, ipcAddr)
	_ = os.WriteFile(plistPath, []byte(plistContent), 0o644)
	_ = exec.Command("launchctl", "unload", plistPath).Run()
	_ = exec.Command("launchctl", "load", "-w", plistPath).Run()

	// 2. Create ~/Applications/VeloceTorrent.app bundle for Spotlight & Finder launching
	appBundleDir := filepath.Join(home, "Applications", "VeloceTorrent.app", "Contents", "MacOS")
	_ = os.MkdirAll(appBundleDir, 0o755)
	launcherScript := filepath.Join(appBundleDir, "VeloceTorrent")
	scriptBody := fmt.Sprintf("#!/usr/bin/env bash\n\"%s\" open\n", dstVt)
	_ = os.WriteFile(launcherScript, []byte(scriptBody), 0o755)

	fmt.Fprintln(os.Stdout, "MACOS INSTALLATION COMPLETE")
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 80))
	fmt.Fprintf(os.Stdout, "INSTALLED BINARIES : %s\n", binDir)
	fmt.Fprintf(os.Stdout, "LAUNCHD AGENT      : %s (Loaded)\n", plistPath)
	fmt.Fprintf(os.Stdout, "APP BUNDLE         : ~/Applications/VeloceTorrent.app\n")
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 80))
	return nil
}

func installLinuxIntegration(vtExe, exeDir, ipcAddr string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	binDir := filepath.Join(home, ".local", "bin")
	_ = os.MkdirAll(binDir, 0o755)

	dstVt := filepath.Join(binDir, "vt")
	dstVtd := filepath.Join(binDir, "vtd")
	_ = copyExecutableIfDifferent(vtExe, dstVt)
	_ = os.Chmod(dstVt, 0o755)

	srcVtd := filepath.Join(exeDir, "vtd")
	if _, err := os.Stat(srcVtd); err == nil {
		_ = copyExecutableIfDifferent(srcVtd, dstVtd)
	} else {
		_ = copyExecutableIfDifferent(vtExe, dstVtd)
	}
	_ = os.Chmod(dstVtd, 0o755)

	// Install systemd --user unit
	systemdDir := filepath.Join(home, ".config", "systemd", "user")
	_ = os.MkdirAll(systemdDir, 0o755)
	unitPath := filepath.Join(systemdDir, "velocetorrent.service")
	unitContent := fmt.Sprintf(`[Unit]
Description=VeloceTorrent High-Throughput BitTorrent Engine
After=network-online.target

[Service]
Type=simple
ExecStart=%s --addr %s
KillSignal=SIGTERM
Restart=on-failure

[Install]
WantedBy=default.target
`, dstVtd, ipcAddr)
	_ = os.WriteFile(unitPath, []byte(unitContent), 0o644)
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	_ = exec.Command("systemctl", "--user", "enable", "--now", "velocetorrent.service").Run()

	_ = registerDesktopAndBrowserHandlers()

	fmt.Fprintln(os.Stdout, "LINUX INSTALLATION COMPLETE")
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 80))
	fmt.Fprintf(os.Stdout, "INSTALLED BINARIES : %s\n", binDir)
	fmt.Fprintf(os.Stdout, "SYSTEMD SERVICE    : %s (Enabled)\n", unitPath)
	fmt.Fprintln(os.Stdout, strings.Repeat("-", 80))
	return nil
}

func copyExecutableIfDifferent(src, dst string) error {
	if strings.EqualFold(src, dst) {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
