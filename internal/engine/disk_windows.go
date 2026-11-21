//go:build windows

package engine

import (
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetDiskFreeExW  = kernel32.NewProc("GetDiskFreeSpaceExW")
	procDeviceIoControl = kernel32.NewProc("DeviceIoControl")
)

const fsctlSetSparse = 0x000900c4

// GetDiskSpace queries available free bytes and total volume bytes for the given directory path on Windows.
func GetDiskSpace(path string) (freeBytes uint64, totalBytes uint64, err error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	// Find existing ancestor directory so GetDiskFreeSpaceExW succeeds even before subdirs are created
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

	pathPtr, err := syscall.UTF16PtrFromString(probe)
	if err != nil {
		return 0, 0, err
	}

	var freeAvailable, totalNum, totalFree int64
	r1, _, e1 := procGetDiskFreeExW.Call(
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&freeAvailable)),
		uintptr(unsafe.Pointer(&totalNum)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r1 == 0 {
		if e1 != nil {
			return 0, 0, e1
		}
		return 0, 0, syscall.EINVAL
	}
	return uint64(freeAvailable), uint64(totalNum), nil
}

// PreallocateFileExtent marks the file sparse if supported and pre-allocates its full extent on Windows.
func PreallocateFileExtent(f *os.File, size int64) error {
	if size <= 0 {
		return nil
	}
	var bytesReturned uint32
	// Issue FSCTL_SET_SPARSE so NTFS does not zero-fill slow magnetic regions while reserving logical extent
	_, _, _ = procDeviceIoControl.Call(
		f.Fd(),
		uintptr(fsctlSetSparse),
		0,
		0,
		0,
		0,
		uintptr(unsafe.Pointer(&bytesReturned)),
		0,
	)
	return f.Truncate(size)
}
