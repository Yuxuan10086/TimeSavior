// Package live provides a Server-Sent Events hub for real-time admin UI updates.
package live

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Event kinds pushed to browsers.
const (
	KindStatus = "status"  // policy / site / unlock window
	KindStats  = "stats"   // focus / play facts changed
	KindUnlock = "unlock"  // challenge phase / timers
	KindLLMLog = "llm_log" // new LLM judge dialog
	KindPing   = "ping"    // heartbeat (keep-alive only)
)

// Hub fans out SSE events to connected admin pages.
type Hub struct {
	mu      sync.Mutex
	clients map[chan event]struct{}

	debounceMu sync.Mutex
	pending    map[string]*time.Timer
}

type event struct {
	Kind string
	Data []byte
}

// New returns an empty hub.
func New() *Hub {
	return &Hub{
		clients: make(map[chan event]struct{}),
		pending: make(map[string]*time.Timer),
	}
}

// Publish sends kind+payload to all clients immediately.
// data may be nil (empty object).
func (h *Hub) Publish(kind string, data any) {
	if h == nil {
		return
	}
	payload, err := json.Marshal(data)
	if err != nil || payload == nil {
		payload = []byte("{}")
	}
	ev := event{Kind: kind, Data: payload}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.clients {
		select {
		case ch <- ev:
		default:
			// Slow client: drop rather than block writers.
		}
	}
}

// PublishDebounced coalesces rapid publishes of the same kind.
func (h *Hub) PublishDebounced(kind string, data any, wait time.Duration) {
	if h == nil {
		return
	}
	if wait <= 0 {
		wait = 400 * time.Millisecond
	}
	h.debounceMu.Lock()
	defer h.debounceMu.Unlock()
	if t, ok := h.pending[kind]; ok {
		t.Stop()
	}
	h.pending[kind] = time.AfterFunc(wait, func() {
		h.debounceMu.Lock()
		delete(h.pending, kind)
		h.debounceMu.Unlock()
		h.Publish(kind, data)
	})
}

// ClientCount returns connected SSE clients (for tests).
func (h *Hub) ClientCount() int {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// ServeHTTP handles GET /__timesavior/events as text/event-stream.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil {
		http.Error(w, "live hub unavailable", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch := make(chan event, 16)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.clients, ch)
		h.mu.Unlock()
		close(ch)
	}()

	// Immediate hello so clients know the stream is live.
	writeSSE(w, flusher, KindPing, []byte(`{"hello":true}`))

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			writeSSE(w, flusher, KindPing, []byte(`{}`))
		case ev, ok := <-ch:
			if !ok {
				return
			}
			writeSSE(w, flusher, ev.Kind, ev.Data)
		}
	}
}

func writeSSE(w http.ResponseWriter, flusher http.Flusher, kind string, data []byte) {
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, data)
	flusher.Flush()
}
