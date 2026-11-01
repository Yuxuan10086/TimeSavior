package focus

import "testing"

func TestInfoKey(t *testing.T) {
	if (Info{}).Key() != "" {
		t.Fatal("zero hwnd should have empty key")
	}
	k := Info{HWND: 0xABC}.Key()
	if k != "0xabc" {
		t.Fatalf("got %q", k)
	}
}
