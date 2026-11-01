//go:build !windows

package ui

import (
	"fmt"
	"os/exec"
	"runtime"
)

func openURL(url string) {
	if url == "" {
		url = "http://127.0.0.1:18889/"
	}
	var err error
	switch runtime.GOOS {
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = exec.Command("xdg-open", url).Start()
	}
	if err != nil {
		fmt.Println("open dashboard:", url)
	}
}
