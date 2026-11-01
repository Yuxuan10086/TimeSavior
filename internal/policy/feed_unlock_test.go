package policy

import (
	"testing"
	"time"

	"timesavior/internal/config"
	"timesavior/internal/rules"
)

func feedUnlockCfg() *config.Config {
	return &config.Config{
		UnknownDefault:        "allow",
		DayStartHour:          4,
		TempUnlockEnabled:     true,
		TempUnlockMinutes:     30,
		TempUnlockWindowStart: "21:30",
		TempUnlockDailyLimit:  1,
		TempUnlockWeeklyLimit: 1,
	}
}

func TestInFeedUnlockWindow(t *testing.T) {
	e := New(feedUnlockCfg(), &rules.Store{})
	loc := time.Local

	// 21:29 → outside
	if e.InFeedUnlockWindow(time.Date(2026, 7, 24, 21, 29, 0, 0, loc)) {
		t.Fatal("21:29 should be outside window")
	}
	// 21:30 → inside
	if !e.InFeedUnlockWindow(time.Date(2026, 7, 24, 21, 30, 0, 0, loc)) {
		t.Fatal("21:30 should be inside window")
	}
	// 23:00 → inside
	if !e.InFeedUnlockWindow(time.Date(2026, 7, 24, 23, 0, 0, 0, loc)) {
		t.Fatal("23:00 should be inside window")
	}
	// next calendar 03:59 → still same policy day window
	if !e.InFeedUnlockWindow(time.Date(2026, 7, 25, 3, 59, 0, 0, loc)) {
		t.Fatal("03:59 should be inside previous policy-day window")
	}
	// 04:00 → new policy day, before 21:30
	if e.InFeedUnlockWindow(time.Date(2026, 7, 25, 4, 0, 0, 0, loc)) {
		t.Fatal("04:00 should be outside window")
	}
	// 12:00 → outside
	if e.InFeedUnlockWindow(time.Date(2026, 7, 25, 12, 0, 0, 0, loc)) {
		t.Fatal("12:00 should be outside window")
	}
}

func TestFeedUnlockStartsOnFirstFeedInWindow(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(feedUnlockCfg(), store)
	now := time.Date(2026, 7, 24, 22, 0, 0, 0, time.Local)

	res := e.Evaluate("www.bilibili.com", "/x/web-interface/wbi/index", now)
	if res.Decision != Allow || res.Reason != ReasonTempUnlock {
		t.Fatalf("first feed should unlock, got %s %s", res.Decision, res.Reason)
	}
	st := e.Status(now)
	if st["temp_unlock_active"] != true {
		t.Fatal("unlock should be active")
	}
	if st["temp_unlock_started"] != true {
		t.Fatal("started flag missing")
	}
	until, _ := st["temp_unlock_until"].(string)
	if until == "" {
		t.Fatal("missing until")
	}
	// Content/play should also be allowed during unlock
	res2 := e.Evaluate("www.bilibili.com", "/video/BV1xx411c7mD", now.Add(time.Minute))
	if res2.Decision != Allow {
		t.Fatalf("during unlock content should allow, got %s", res2.Decision)
	}
}

func TestFeedUnlockDoesNotStartOutsideWindow(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(feedUnlockCfg(), store)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.Local) // before 21:30

	res := e.Evaluate("www.bilibili.com", "/x/web-interface/wbi/index", now)
	if res.Decision != Block || res.Reason != ReasonFeedBlocked {
		t.Fatalf("outside window feed should soft-block, got %s %s", res.Decision, res.Reason)
	}
	st := e.Status(now)
	if st["temp_unlock_started"] == true || st["temp_unlock_active"] == true {
		t.Fatal("should not start outside window")
	}
}

func TestFeedUnlockNoCarryOverNextDay(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(feedUnlockCfg(), store)
	// Never open feed on day 1; roll to day 2 and open — still only today's 30m.
	day1 := time.Date(2026, 7, 24, 22, 0, 0, 0, time.Local)
	_ = e.Status(day1) // establish policy day without starting
	if e.Status(day1)["temp_unlock_started"] == true {
		t.Fatal("should not start without feed")
	}

	day2Morning := time.Date(2026, 7, 25, 5, 0, 0, 0, time.Local) // after 04:00, before 21:30
	res := e.Evaluate("www.youtube.com", "/", day2Morning)
	if res.Reason != ReasonFeedBlocked {
		t.Fatalf("before window on new day: want feed_blocked, got %s", res.Reason)
	}
	if e.Status(day2Morning)["temp_unlock_started"] == true {
		t.Fatal("unused day1 must not carry into day2")
	}

	day2Open := time.Date(2026, 7, 25, 22, 0, 0, 0, time.Local)
	res = e.Evaluate("www.youtube.com", "/", day2Open)
	if res.Reason != ReasonTempUnlock {
		t.Fatalf("day2 first feed should start unlock, got %s", res.Reason)
	}
}

func TestActiveTempUnlockSurvivesPolicyDayRoll(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(feedUnlockCfg(), store)
	start := time.Date(2026, 8, 3, 12, 0, 0, 0, time.Local)
	until := time.Date(2026, 8, 5, 4, 0, 0, 0, time.Local)
	_ = e.Status(start)
	e.mu.Lock()
	e.tempUnlockCount = 1
	e.tempUnlockStartedAt = start
	e.tempUnlockUntil = until
	e.mu.Unlock()

	nextMorning := time.Date(2026, 8, 4, 5, 0, 0, 0, time.Local)
	st := e.Status(nextMorning)
	if st["temp_unlock_active"] != true {
		t.Fatal("multi-day unlock must survive 04:00 roll")
	}
	res := e.Evaluate("www.bilibili.com", "/", nextMorning)
	if res.Reason != ReasonTempUnlock {
		t.Fatalf("want temp_unlock after roll, got %s", res.Reason)
	}
}

func TestFeedUnlockExpiresThenSoftBlock(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(feedUnlockCfg(), store)
	start := time.Date(2026, 7, 24, 22, 0, 0, 0, time.Local)
	_ = e.Evaluate("www.zhihu.com", "/hot", start)
	after := start.Add(31 * time.Minute)
	res := e.Evaluate("www.zhihu.com", "/hot", after)
	if res.Decision != Block || res.Reason != ReasonFeedBlocked {
		t.Fatalf("after expiry want feed_blocked, got %s %s", res.Decision, res.Reason)
	}
	// Second start same day must not re-grant
	res2 := e.Evaluate("www.bilibili.com", "/", after)
	if res2.Reason == ReasonTempUnlock {
		t.Fatal("must not restart unlock same policy day")
	}
}

func TestFeedUnlockCancelKeepsUsed(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(feedUnlockCfg(), store)
	now := time.Date(2026, 7, 24, 22, 0, 0, 0, time.Local)
	_ = e.Evaluate("www.youtube.com", "/", now)
	view := e.CancelTempUnlock(now.Add(time.Minute))
	if view["temp_unlock_active"] == true {
		t.Fatal("cancel should end unlock")
	}
	res := e.Evaluate("www.youtube.com", "/", now.Add(2*time.Minute))
	if res.Reason == ReasonTempUnlock {
		t.Fatal("cancel must not refund today's quota")
	}
}
