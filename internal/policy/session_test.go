package policy

import (
	"testing"
	"time"

	"timesavior/internal/config"
	"timesavior/internal/rules"
)

func TestPolicyDayKeyAt4AM(t *testing.T) {
	loc := time.Local
	// 03:59 -> previous calendar day key (shifted)
	a := time.Date(2026, 7, 24, 3, 59, 0, 0, loc)
	b := time.Date(2026, 7, 24, 4, 0, 0, 0, loc)
	if PolicyDayKey(a, 4) == PolicyDayKey(b, 4) {
		t.Fatalf("03:59 and 04:00 should be different policy days")
	}
	c := time.Date(2026, 7, 24, 5, 0, 0, 0, loc)
	if PolicyDayKey(b, 4) != PolicyDayKey(c, 4) {
		t.Fatalf("04:00 and 05:00 should share policy day")
	}
}

func TestFeedDoesNotFullBlock(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	// Outside unlock window so SoftBlock stays active for this stress test.
	cfg := &config.Config{
		UnknownDefault: "allow", DayStartHour: 4,
		TempUnlockEnabled: true, TempUnlockMinutes: 30, TempUnlockWindowStart: "21:30",
		TempUnlockDailyLimit: 1,
	}
	e := New(cfg, store)
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.Local) // before 21:30
	for i := 0; i < 200; i++ {
		res := e.Evaluate("www.bilibili.com", "/x/web-interface/wbi/index", base.Add(time.Duration(i)*time.Second))
		if res.Reason == ReasonFullBlock {
			t.Fatal("feed soft-block must not trigger full_block")
		}
		if res.Decision != Block || res.Reason != ReasonFeedBlocked {
			t.Fatalf("want feed_blocked, got %s %s", res.Decision, res.Reason)
		}
	}
	// search still ok
	res := e.Evaluate("search.bilibili.com", "/all", base.Add(3*time.Hour))
	if res.Decision != Allow {
		t.Fatalf("search should allow, got %s", res.Decision)
	}
}

func TestContinuousWatchTwentyMinutesFullBlocksWithoutLLM(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{UnknownDefault: "allow", DayStartHour: 4, MinWatchSec: 15, FreeWindows: []config.FreeWindow{{Start: "21:00", End: "22:00"}}}
	e := New(cfg, store)
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.Local)

	// Dense play heartbeats (CDN-like); sparse prefetch must not confirm a session.
	var last Result
	for i := 0; i <= 130; i++ {
		now := base.Add(time.Duration(i) * 10 * time.Second)
		last = e.Evaluate("www.bilibili.com", "/x/player/wbi/playurl", now)
	}
	if last.Reason != ReasonFullBlock {
		t.Fatalf("expected full_block after ~20m watch without LLM, got %s", last.Reason)
	}
	// Until next 04:00
	until := e.Status(base.Add(22*time.Minute))["sites"].(map[string]any)["bilibili"].(map[string]any)["full_block_until"].(string)
	if until == "" {
		t.Fatal("full_block_until should be set")
	}
	// search blocked during full block
	res := e.Evaluate("search.bilibili.com", "/", base.Add(30*time.Minute))
	if res.Reason != ReasonFullBlock {
		t.Fatalf("search should full_block, got %s", res.Reason)
	}
}

func TestWatchIdleTenMinutesResets(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{UnknownDefault: "allow", DayStartHour: 4, MinWatchSec: 15, FreeWindows: []config.FreeWindow{{Start: "21:00", End: "22:00"}}}
	e := New(cfg, store)
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.Local)

	// Watch 16 minutes (under 20) with dense pulses
	for i := 0; i <= 96; i++ {
		_ = e.Evaluate("www.bilibili.com", "/x/player/wbi/playurl", base.Add(time.Duration(i)*10*time.Second))
	}
	// Idle 11 minutes then watch another 16 minutes — should NOT full block yet
	resume := base.Add(16*time.Minute + 11*time.Minute)
	var last Result
	for i := 0; i <= 96; i++ {
		last = e.Evaluate("www.bilibili.com", "/x/player/wbi/playurl", resume.Add(time.Duration(i)*10*time.Second))
	}
	if last.Reason == ReasonFullBlock {
		t.Fatal("idle >10m should reset; second 16m must not full_block alone")
	}
}

func TestShortPrefetchDoesNotAccumulateWatch(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{UnknownDefault: "allow", DayStartHour: 4, MinWatchSec: 15, FreeWindows: []config.FreeWindow{{Start: "21:00", End: "22:00"}}}
	e := New(cfg, store)
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.Local)

	// Isolated <15s bursts every 2 minutes must not confirm continuous watch.
	var last Result
	for i := 0; i < 30; i++ {
		t0 := base.Add(time.Duration(i) * 2 * time.Minute)
		last = e.Evaluate("www.bilibili.com", "/x/player/wbi/playurl", t0)
		last = e.Evaluate("www.bilibili.com", "/x/player/wbi/playurl", t0.Add(5*time.Second))
	}
	if last.Reason == ReasonFullBlock {
		t.Fatal("prefetch pulses must not full_block")
	}
	st := e.Status(base.Add(60*time.Minute))["sites"].(map[string]any)["bilibili"].(map[string]any)
	mins, _ := st["watch_minutes"].(int)
	if mins >= 1 {
		t.Fatalf("short prefetch should not accumulate watch_minutes, got %v", mins)
	}
}

func TestTextSiteNeverWatchFullBlock(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		UnknownDefault: "allow", DayStartHour: 4,
		TempUnlockEnabled: true, TempUnlockMinutes: 30, TempUnlockWindowStart: "21:30",
		TempUnlockDailyLimit: 1,
	}
	e := New(cfg, store)
	base := time.Date(2026, 7, 24, 8, 0, 0, 0, time.Local) // outside unlock window
	var last Result
	for i := 0; i <= 40; i++ {
		now := base.Add(time.Duration(i) * 2 * time.Minute)
		last = e.Evaluate("www.zhihu.com", "/api/v4/zvideos", now)
		_ = e.Evaluate("www.zhihu.com", "/question/123", now)
	}
	if last.Reason == ReasonFullBlock {
		t.Fatal("zhihu text site must not full_block from watch timer")
	}
	res := e.Evaluate("www.zhihu.com", "/search", base.Add(3*time.Hour))
	if res.Decision != Allow {
		t.Fatalf("zhihu search should allow, got %s", res.Decision)
	}
	feed := e.Evaluate("www.zhihu.com", "/", base)
	if feed.Reason != ReasonFeedBlocked {
		t.Fatalf("zhihu feed should soft-block, got %s", feed.Reason)
	}
}

func TestContentBVAllowed(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		UnknownDefault: "allow", DayStartHour: 4,
		TempUnlockEnabled: true, TempUnlockMinutes: 30, TempUnlockWindowStart: "21:30",
		TempUnlockDailyLimit: 1,
	}
	e := New(cfg, store)
	base := time.Date(2026, 7, 24, 8, 0, 0, 0, time.Local)
	res := e.Evaluate("www.bilibili.com", "/video/BV1xx411c7mD", base)
	if res.Decision != Allow {
		t.Fatalf("BV content should allow, got %s %s", res.Decision, res.Reason)
	}
}

