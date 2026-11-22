//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	createNoWindow        = 0x08000000
	detachedProcess       = 0x00000008
	createNewProcessGroup = 0x00000200
)

func configureSilentProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow,
		HideWindow:    true,
	}
}

func spawnSilentDaemon(vtdPath, addr string) error {
	cmd := exec.Command(vtdPath, "--addr", addr)
	cmd.Dir = filepath.Dir(vtdPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow | detachedProcess | createNewProcessGroup,
		HideWindow:    true,
	}
	return cmd.Start()
}

func launchInteractiveTUI(vtPath, addr string) error {
	installDir := filepath.Dir(vtPath)
	if wtBin, err := exec.LookPath("wt.exe"); err == nil {
		cmd := exec.Command(wtBin, "-d", installDir, vtPath, "ui", "--addr", addr)
		return cmd.Start()
	}
	cmd := exec.Command("cmd.exe", "/c", "start", "VeloceTorrent", vtPath, "ui", "--addr", addr)
	cmd.Dir = installDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Start()
}
