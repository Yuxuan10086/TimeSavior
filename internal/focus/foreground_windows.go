//go:build windows

package focus

import (
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modUser32                       = windows.NewLazySystemDLL("user32.dll")
	modKernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGetForegroundWindow         = modUser32.NewProc("GetForegroundWindow")
	procGetWindowTextW              = modUser32.NewProc("GetWindowTextW")
	procGetWindowTextLengthW        = modUser32.NewProc("GetWindowTextLengthW")
	procGetWindowThreadProcessId    = modUser32.NewProc("GetWindowThreadProcessId")
	procQueryFullProcessImageNameW  = modKernel32.NewProc("QueryFullProcessImageNameW")
)

// Foreground returns the currently active top-level window info.
func Foreground() (Info, error) {
	hwnd, _, _ := procGetForegroundWindow.Call()
	if hwnd == 0 {
		return Info{}, nil
	}
	info := Info{HWND: hwnd}

	var pid uint32
	procGetWindowThreadProcessId.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	info.PID = pid
	info.Title = windowTitle(hwnd)
	if pid != 0 {
		info.ExePath, info.ExeName = processImage(pid)
	}
	return info, nil
}

func windowTitle(hwnd uintptr) string {
	length, _, _ := procGetWindowTextLengthW.Call(hwnd)
	if length == 0 {
		return ""
	}
	buf := make([]uint16, length+1)
	n, _, _ := procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	if n == 0 {
		return ""
	}
	return windows.UTF16ToString(buf[:n])
}

func processImage(pid uint32) (path, name string) {
	const processQueryLimitedInformation = 0x1000
	h, err := windows.OpenProcess(processQueryLimitedInformation, false, pid)
	if err != nil {
		return "", ""
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	r, _, _ := procQueryFullProcessImageNameW.Call(
		uintptr(h),
		0,
		uintptr(unsafe.Pointer(&buf[0])),
		uintptr(unsafe.Pointer(&size)),
	)
	if r == 0 {
		return "", ""
	}
	path = windows.UTF16ToString(buf[:size])
	name = filepath.Base(path)
	// Normalize for nicer logs
	name = strings.ToLower(name)
	return path, name
}
