package policy

import (
	"testing"
	"time"

	"timesavior/internal/config"
)

func TestClipsSinceDropsBelowMinWatch(t *testing.T) {
	cfg := &config.Config{MinWatchSec: 15}
	e := New(cfg, nil)
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.Local)

	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.getSite("youtube")
	st.sessionClips = []sessionClip{
		{Title: "预加载短片", VideoID: "short", FirstSeen: now.Add(-5 * time.Second), LastSeen: now},
		{Title: "正式观看", VideoID: "long", FirstSeen: now.Add(-90 * time.Second), LastSeen: now},
	}
	clips := e.clipsSinceLocked(st, time.Time{})
	if len(clips) != 1 {
		t.Fatalf("want 1 clip (≥15s), got %#v", clips)
	}
	if clips[0].Title != "正式观看" || clips[0].DurationSec < 15 {
		t.Fatalf("unexpected clip: %#v", clips[0])
	}
}
