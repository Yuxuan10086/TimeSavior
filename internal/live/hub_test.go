package live

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPublishFansOut(t *testing.T) {
	h := New()
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("content-type: %s", ct)
	}

	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sawHello := false
		sawStatus := false
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: ping") {
				sawHello = true
				close(ready)
			}
			if strings.HasPrefix(line, "event: status") {
				sawStatus = true
				break
			}
		}
		if !sawHello || !sawStatus {
			t.Errorf("hello=%v status=%v", sawHello, sawStatus)
		}
	}()

	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for hello")
	}
	// Give subscribe a moment.
	time.Sleep(50 * time.Millisecond)
	if h.ClientCount() < 1 {
		t.Fatal("expected client")
	}
	h.Publish(KindStatus, map[string]any{"ok": true})
	time.Sleep(200 * time.Millisecond)
}

func TestPublishDebounced(t *testing.T) {
	h := New()
	got := make(chan string, 4)
	ch := make(chan event, 4)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	go func() {
		for ev := range ch {
			got <- ev.Kind
		}
	}()
	h.PublishDebounced(KindStats, map[string]any{"n": 1}, 80*time.Millisecond)
	h.PublishDebounced(KindStats, map[string]any{"n": 2}, 80*time.Millisecond)
	h.PublishDebounced(KindStats, map[string]any{"n": 3}, 80*time.Millisecond)
	select {
	case <-time.After(40 * time.Millisecond):
	case k := <-got:
		t.Fatalf("too early: %s", k)
	}
	select {
	case k := <-got:
		if k != KindStats {
			t.Fatalf("got %s", k)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("debounced publish missing")
	}
	select {
	case k := <-got:
		t.Fatalf("extra event: %s", k)
	case <-time.After(120 * time.Millisecond):
	}
}
