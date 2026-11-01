package policy

import (
	"testing"
	"time"

	"timesavior/internal/config"
	"timesavior/internal/rules"
)

func TestInFreeWindowWeekday(t *testing.T) {
	cfg := &config.Config{
		FreeWindows: []config.FreeWindow{
			{Start: "21:00", End: "21:30"},
			{Start: "21:30", End: "22:00", Weekdays: []string{"sat"}},
		},
	}
	e := New(cfg, &rules.Store{})

	// Friday 21:15 -> free
	fri := time.Date(2026, 7, 24, 21, 15, 0, 0, time.Local) // 2026-07-24 is Friday
	if fri.Weekday() != time.Friday {
		t.Fatalf("fixture weekday want Friday, got %v", fri.Weekday())
	}
	if !e.InFreeWindow(fri) {
		t.Fatal("Fri 21:15 should be free")
	}
	// Friday 21:45 -> not free
	friLate := time.Date(2026, 7, 24, 21, 45, 0, 0, time.Local)
	if e.InFreeWindow(friLate) {
		t.Fatal("Fri 21:45 should NOT be free")
	}
	// Saturday 21:45 -> free (extension)
	sat := time.Date(2026, 7, 25, 21, 45, 0, 0, time.Local)
	if sat.Weekday() != time.Saturday {
		t.Fatalf("fixture weekday want Saturday, got %v", sat.Weekday())
	}
	if !e.InFreeWindow(sat) {
		t.Fatal("Sat 21:45 should be free")
	}
	// Saturday 22:01 -> not free
	satLate := time.Date(2026, 7, 25, 22, 1, 0, 0, time.Local)
	if e.InFreeWindow(satLate) {
		t.Fatal("Sat 22:01 should NOT be free")
	}
}
