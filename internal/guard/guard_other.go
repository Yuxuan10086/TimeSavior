//go:build !windows

package guard

import "time"

func ExeRunning(exeName string) bool { return false }

func StartDetached(exe string, args ...string) error { return nil }

func EnsurePeer(peerExe string, args []string, every time.Duration) {}

func SelfDir() string { return "." }
