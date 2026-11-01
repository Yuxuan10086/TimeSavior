package policy

import (
	"path/filepath"
	"testing"
	"time"

	"timesavior/internal/config"
	"timesavior/internal/rules"
	"timesavior/internal/store"
)

func TestRuntimePersistsAcrossEngines(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: filepath.Join(dir, "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	rs := &rules.Store{}
	cfg := &config.Config{
		TempUnlockDailyLimit:  2,
		TempUnlockWeeklyLimit: 5,
		TempUnlockMinutes:     15,
		DayStartHour:          4,
	}
	e1 := New(cfg, rs)
	e1.AttachStore(db)
	// Use "now" close to wall clock so AttachStore → LoadPersisted(time.Now())
	// does not roll the policy day and clear full blocks.
	now := time.Now().Truncate(time.Second)
	e1.mu.Lock()
	st := e1.getSite("youtube")
	st.fullBlockUntil = now.Add(2 * time.Hour)
	st.watchStarted = now.Add(-20 * time.Minute)
	st.lastPlayAt = now.Add(-time.Minute)
	e1.tempUnlockCount = 1
	e1.tempUnlockWeekCount = 2
	e1.policyDay = PolicyDayKey(now, 4)
	e1.tempUnlockWeekKey = CalendarWeekKey(now)
	e1.tempUnlockUntil = now.Add(10 * time.Minute)
	e1.mu.Unlock()
	e1.FlushPersist()

	e2 := New(cfg, rs)
	e2.AttachStore(db)
	active, until := e2.FullBlockActive("youtube", now)
	if !active {
		t.Fatal("full block not restored")
	}
	if until.IsZero() {
		t.Fatal("until zero")
	}
	st2 := e2.Status(now)
	if st2["temp_unlock_used"] != 1 {
		t.Fatalf("daily used=%v", st2["temp_unlock_used"])
	}
	if st2["temp_unlock_week_used"] != 2 {
		t.Fatalf("week used=%v", st2["temp_unlock_week_used"])
	}
	if st2["temp_unlock_active"] != true {
		t.Fatalf("temp unlock not active: %v", st2["temp_unlock_active"])
	}
	sites, _ := st2["sites"].(map[string]any)
	yt, _ := sites["youtube"].(map[string]any)
	if yt["watch_minutes"].(int) < 18 {
		t.Fatalf("watch minutes not restored: %v", yt["watch_minutes"])
	}
}
