package focus

import (
	"strconv"
	"time"
)

// Info is the foreground window snapshot.
type Info struct {
	HWND    uintptr
	PID     uint32
	Title   string
	ExeName string
	ExePath string
}

// Key identifies a focus session (HWND is stable while the window lives).
func (i Info) Key() string {
	if i.HWND == 0 {
		return ""
	}
	return "0x" + strconv.FormatUint(uint64(i.HWND), 16)
}

// Session is one continuous activation of a window (start..end).
type Session struct {
	HWND        uintptr
	PID         uint32
	Title       string
	ExeName     string
	ExePath     string
	StartedAt   time.Time
	EndedAt     time.Time
	DurationSec float64
}
