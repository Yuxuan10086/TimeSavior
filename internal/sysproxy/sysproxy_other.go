//go:build !windows

package sysproxy

import "fmt"

type Snapshot struct {
	ProxyEnable   int
	ProxyServer   string
	ProxyOverride string
}

func Get() (Snapshot, error) { return Snapshot{}, fmt.Errorf("sysproxy only supported on windows") }
func Set(host string, port int) error {
	return fmt.Errorf("sysproxy only supported on windows")
}
func Restore(s Snapshot) error { return fmt.Errorf("sysproxy only supported on windows") }
func Ensure(host string, port int) (bool, string, error) {
	return false, "", fmt.Errorf("sysproxy only supported on windows")
}
func LooksLikeClash(proxyServer string, clashPorts []int) bool { return false }
