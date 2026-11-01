//go:build windows

package ui

import (
	"os/exec"
	"syscall"
)

func openURL(url string) {
	if url == "" {
		url = "http://127.0.0.1:18889/"
	}
	// rundll32 avoids a visible cmd.exe flash that "cmd /c start" often causes.
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	_ = cmd.Start()
}
