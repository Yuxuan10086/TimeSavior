package policy

import (
	"testing"
	"time"

	"timesavior/internal/config"
	"timesavior/internal/rules"
)

func testWhitelistCfg() *config.Config {
	cfg := &config.Config{
		UnknownDefault: "allow",
		DayStartHour:   4,
		WatchWhitelistRules: []config.WatchWhitelistRule{
			{Site: "youtube", Kind: "channel", Keyword: "Example Channel", Mode: "exact"},
		},
	}
	return cfg
}

func TestWatchWhitelistSkipsTimer(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(testWhitelistCfg(), store)
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)

	// Simulate player JSON for Example Channel
	body := []byte(`{"videoDetails":{"videoId":"abc","author":"Example Channel","channelId":"UCxxx"}}`)
	e.ObserveResponse("www.youtube.com", "/youtubei/v1/player", body, base)

	var last Result
	for i := 0; i <= 130; i++ {
		now := base.Add(time.Duration(i) * 10 * time.Second)
		last = e.Evaluate("rr1---sn-test.googlevideo.com", "/videoplayback", now)
		if last.Reason == ReasonFullBlock {
			t.Fatalf("whitelisted playback must not full_block at step %d", i)
		}
	}
	st := e.Status(base.Add(25 * time.Minute))["sites"].(map[string]any)["youtube"].(map[string]any)
	if active, _ := st["full_block_active"].(bool); active {
		t.Fatal("youtube should not be full-blocked under whitelist")
	}
	if exempt, _ := st["watch_exempt"].(bool); !exempt {
		t.Fatal("expected watch_exempt while whitelist active")
	}

	// Switch to a non-whitelist video → scoring resumes; without LLM, FullBlock at 20m
	other := []byte(`{"videoDetails":{"videoId":"zzz","author":"Some Entertainment","channelId":"UCyyy"}}`)
	switchAt := base.Add(26 * time.Minute)
	e.ObserveResponse("www.youtube.com", "/youtubei/v1/player", other, switchAt)
	for i := 0; i <= 130; i++ {
		now := switchAt.Add(time.Duration(i) * 10 * time.Second)
		last = e.Evaluate("rr1---sn-test.googlevideo.com", "/videoplayback", now)
	}
	if last.Reason != ReasonFullBlock {
		t.Fatalf("non-whitelist watch should full_block, got %s", last.Reason)
	}
}

func TestWatchWhitelistURLHandle(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(testWhitelistCfg(), store)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	_ = e.EvaluateMeta(RequestMeta{
		Host:    "www.youtube.com",
		Path:    "/@ExampleChannel/videos",
		Referer: "https://www.youtube.com/",
	}, now)
	st := e.Status(now)["sites"].(map[string]any)["youtube"].(map[string]any)
	if exempt, _ := st["watch_exempt"].(bool); !exempt {
		t.Fatal("channel handle URL should enable watch_exempt")
	}
}

func TestWatchWhitelistIgnoresChannelIDs(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(testWhitelistCfg(), store)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	body := []byte(`{"videoDetails":{"videoId":"abc","author":"Other","channelId":"UCxxx"}}`)
	e.ObserveResponse("www.youtube.com", "/youtubei/v1/player", body, now)
	st := e.Status(now)["sites"].(map[string]any)["youtube"].(map[string]any)
	if exempt, _ := st["watch_exempt"].(bool); exempt {
		t.Fatal("channel_id alone must not exempt")
	}
}
