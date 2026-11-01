package proxy

import (
	"net"
	"strings"
)

// tunnelOnlySuffixes: CONNECT is accepted as a raw TCP tunnel (no MITM).
// Cursor's gRPC/HTTP2 stack resets under goproxy MITM even when CA is trusted.
var tunnelOnlySuffixes = []string{
	"cursor.sh",
	"cursor.com",
	"cursorapi.com",
}

func tunnelOnlyHost(hostport string) bool {
	host := strings.TrimSpace(hostport)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	if host == "" {
		return false
	}
	for _, suf := range tunnelOnlySuffixes {
		if host == suf || strings.HasSuffix(host, "."+suf) {
			return true
		}
	}
	return false
}
