//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func configureSilentProc(_ *exec.Cmd) {}

func spawnSilentDaemon(vtdPath, addr string) error {
	cmd := exec.Command(vtdPath, "--addr", addr)
	cmd.Dir = filepath.Dir(vtdPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	return cmd.Start()
}

func launchInteractiveTUI(vtPath, addr string) error {
	cmd := exec.Command(vtPath, "ui", "--addr", addr)
	cmd.Dir = filepath.Dir(vtPath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
