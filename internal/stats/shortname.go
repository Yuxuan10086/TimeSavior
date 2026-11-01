package stats

import (
	"sync"

	"timesavior/internal/titleshort"
)

var (
	shortMu       sync.RWMutex
	shortener     *titleshort.Shortener
	shortNotifyMu sync.Mutex
	shortNotify   func()
)

// SetTitleShortener wires optional LLM title compression for child rows.
func SetTitleShortener(s *titleshort.Shortener) {
	shortMu.Lock()
	shortener = s
	shortMu.Unlock()
}

// SetShortNameNotify registers a callback after async short names land (SSE).
func SetShortNameNotify(fn func()) {
	shortNotifyMu.Lock()
	shortNotify = fn
	shortNotifyMu.Unlock()
}

// BumpAfterShortName invalidates cache and notifies listeners.
func BumpAfterShortName() {
	InvalidateCache()
	shortNotifyMu.Lock()
	fn := shortNotify
	shortNotifyMu.Unlock()
	if fn != nil {
		fn()
	}
}

func applyShortName(row *AppRow) {
	if row == nil {
		return
	}
	src := row.Bucket
	if src == "" {
		return
	}
	shortMu.RLock()
	s := shortener
	shortMu.RUnlock()
	if s == nil {
		return
	}
	display, full := s.Resolve(src)
	if full == "" {
		return
	}
	row.ShortName = display
	row.FullTitle = full
}
