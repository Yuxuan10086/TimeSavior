package sysproxy

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

const (
	internetSettings = `HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings`
)

type Snapshot struct {
	ProxyEnable int
	ProxyServer string
	ProxyOverride string
}

func Get() (Snapshot, error) {
	enable, err := regGetInt("ProxyEnable")
	if err != nil {
		return Snapshot{}, err
	}
	server, _ := regGetString("ProxyServer")
	override, _ := regGetString("ProxyOverride")
	return Snapshot{
		ProxyEnable:   enable,
		ProxyServer:   server,
		ProxyOverride: override,
	}, nil
}

func Set(host string, port int) error {
	server := fmt.Sprintf("%s:%d", host, port)
	if err := regSetInt("ProxyEnable", 1); err != nil {
		return err
	}
	if err := regSetString("ProxyServer", server); err != nil {
		return err
	}
	// Bypass local and TimeSavior/Clash loopbacks to avoid loops.
	override := "localhost;127.*;10.*;192.168.*;<local>"
	if err := regSetString("ProxyOverride", override); err != nil {
		return err
	}
	return notifyProxyChanged()
}

func Restore(s Snapshot) error {
	if err := regSetInt("ProxyEnable", s.ProxyEnable); err != nil {
		return err
	}
	if err := regSetString("ProxyServer", s.ProxyServer); err != nil {
		return err
	}
	if err := regSetString("ProxyOverride", s.ProxyOverride); err != nil {
		return err
	}
	return notifyProxyChanged()
}

func Ensure(host string, port int) (rewrote bool, current string, err error) {
	want := fmt.Sprintf("%s:%d", host, port)
	s, err := Get()
	if err != nil {
		return false, "", err
	}
	current = s.ProxyServer
	if s.ProxyEnable == 1 && normalizeProxy(s.ProxyServer) == want {
		return false, current, nil
	}
	if err := Set(host, port); err != nil {
		return false, current, err
	}
	return true, current, nil
}

func LooksLikeClash(proxyServer string, clashPorts []int) bool {
	p := normalizeProxy(proxyServer)
	for _, port := range clashPorts {
		if p == fmt.Sprintf("127.0.0.1:%d", port) || p == fmt.Sprintf("localhost:%d", port) {
			return true
		}
	}
	return false
}

func normalizeProxy(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "https://")
	return s
}

func regGetInt(name string) (int, error) {
	out, err := ps(fmt.Sprintf(`(Get-ItemProperty -Path '%s' -Name %s).%s`, internetSettings, name, name))
	if err != nil {
		return 0, err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return 0, nil
	}
	var v int
	_, err = fmt.Sscanf(out, "%d", &v)
	return v, err
}

func regGetString(name string) (string, error) {
	out, err := ps(fmt.Sprintf(`(Get-ItemProperty -Path '%s' -Name %s -ErrorAction SilentlyContinue).%s`, internetSettings, name, name))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func regSetInt(name string, v int) error {
	_, err := ps(fmt.Sprintf(`Set-ItemProperty -Path '%s' -Name %s -Value %d -Type DWord`, internetSettings, name, v))
	return err
}

func regSetString(name, v string) error {
	escaped := strings.ReplaceAll(v, "'", "''")
	_, err := ps(fmt.Sprintf(`Set-ItemProperty -Path '%s' -Name %s -Value '%s' -Type String`, internetSettings, name, escaped))
	return err
}

func notifyProxyChanged() error {
	// InternetSetOption via PowerShell Add-Type is heavy; a no-op registry write usually
	// is enough for Chromium which polls settings. Also poke WinINET.
	script := `
Add-Type -TypeDefinition @"
using System;
using System.Runtime.InteropServices;
public class WinInet {
  [DllImport("wininet.dll", SetLastError=true)]
  public static extern bool InternetSetOption(IntPtr hInternet, int dwOption, IntPtr lpBuffer, int dwBufferLength);
}
"@
[void][WinInet]::InternetSetOption([IntPtr]::Zero, 39, [IntPtr]::Zero, 0)
[void][WinInet]::InternetSetOption([IntPtr]::Zero, 37, [IntPtr]::Zero, 0)
`
	_, err := ps(script)
	return err
}

func ps(script string) (string, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("powershell: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
