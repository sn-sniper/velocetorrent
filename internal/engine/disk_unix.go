//go:build !windows

package engine

import (
	"os"
	"path/filepath"
	"syscall"
)

// GetDiskSpace queries available free bytes and total volume bytes for the given directory path on Unix systems.
func GetDiskSpace(path string) (freeBytes uint64, totalBytes uint64, err error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	probe := absPath
	for {
		if info, statErr := os.Stat(probe); statErr == nil && info.IsDir() {
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		probe = parent
	}

	var stat syscall.Statfs_t
	if err := syscall.Statfs(probe, &stat); err != nil {
		return 0, 0, err
	}
	bsize := uint64(stat.Bsize)
	return uint64(stat.Bavail) * bsize, uint64(stat.Blocks) * bsize, nil
}

// PreallocateFileExtent reserves the full file extent on disk before piece writes begin.
func PreallocateFileExtent(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	return f.Truncate(size)
}
