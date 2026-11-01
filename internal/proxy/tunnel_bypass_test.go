package proxy

import "testing"

func TestTunnelOnlyHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"api2.cursor.sh:443", true},
		{"api2.cursor.sh", true},
		{"www.cursor.com", true},
		{"marketplace.cursorapi.com", true},
		{"authenticator.cursor.sh", true},
		{"www.bilibili.com:443", false},
		{"evilcursor.sh", false},
		{"", false},
	}
	for _, c := range cases {
		if got := tunnelOnlyHost(c.host); got != c.want {
			t.Fatalf("tunnelOnlyHost(%q)=%v want %v", c.host, got, c.want)
		}
	}
}
