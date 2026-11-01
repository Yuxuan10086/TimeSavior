//go:build windows

package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	createNoWindow = 0x08000000
)

// ExeRunning reports whether a process with the given executable base name is alive.
func ExeRunning(exeName string) bool {
	want := strings.ToLower(exeName)
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(snap)

	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	err = windows.Process32First(snap, &pe)
	if err != nil {
		return false
	}
	for {
		name := strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))
		if name == want {
			return true
		}
		err = windows.Process32Next(snap, &pe)
		if err != nil {
			break
		}
	}
	return false
}

// StartDetached launches exe with args without a console window.
// Avoid DETACHED_PROCESS: it can break the Windows tray icon click/menu session.
func StartDetached(exe string, args ...string) error {
	cmd := exec.Command(exe, args...)
	cmd.Dir = filepath.Dir(exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// EnsurePeer keeps peerExe running (restart if missing). Blocks forever.
func EnsurePeer(peerExe string, args []string, every time.Duration) {
	if every <= 0 {
		every = time.Second
	}
	name := filepath.Base(peerExe)
	for {
		if !ExeRunning(name) {
			_ = StartDetached(peerExe, args...)
		}
		time.Sleep(every)
	}
}

// SelfDir returns the directory of the current executable.
func SelfDir() string {
	self, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(self)
}
