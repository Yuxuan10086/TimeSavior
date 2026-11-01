package config

import (
	"reflect"
	"testing"
)

func TestSyncWatchRulesMergesLegacy(t *testing.T) {
	cfg := &Config{
		WatchWhitelistRules: []WatchWhitelistRule{
			{Site: "youtube", Kind: "channel", Keyword: "Example Channel", Mode: "contains"},
		},
		WatchTitleWhitelist: []string{"英语学习", "  "},
	}
	cfg.SyncWatchRules(map[string][]string{
		"youtube": {"@ExampleChannel", "Example Channel"},
	})
	rules := cfg.WatchRules()
	var hasTitle, channelCount int
	var channelKeyword, channelMode string
	for _, r := range rules {
		if r.Kind == "title" && r.Keyword == "英语学习" && r.Site == "all" && r.Mode == "contains" {
			hasTitle++
		}
		if r.Kind == "channel" && r.Site == "youtube" {
			channelCount++
			channelKeyword = r.Keyword
			channelMode = r.Mode
		}
	}
	if hasTitle != 1 {
		t.Fatalf("expected one title rule, got %#v", rules)
	}
	if channelCount != 1 {
		t.Fatalf("expected one collapsed channel rule, got %#v", rules)
	}
	if channelKeyword != "Example Channel" {
		t.Fatalf("prefer display name, got %q", channelKeyword)
	}
	if channelMode != "exact" {
		t.Fatalf("channel mode must be exact, got %q", channelMode)
	}
}

func TestMatchWatchMetaTitleAndChannel(t *testing.T) {
	cfg := &Config{
		WatchWhitelistRules: []WatchWhitelistRule{
			{Site: "all", Kind: "title", Keyword: "英语学习", Mode: "contains"},
			{Site: "youtube", Kind: "channel", Keyword: "Example Channel", Mode: "exact"},
			{Site: "youtube", Kind: "channel", Keyword: "@ExampleChannel", Mode: "exact"},
		},
	}
	cfg.SyncWatchRules(nil)

	if hit, label := cfg.MatchWatchMeta("youtube", "英语学习口语 Part2", "Someone"); !hit || label != "title:英语学习" {
		t.Fatalf("title: hit=%v label=%q", hit, label)
	}
	if hit, label := cfg.MatchWatchMeta("youtube", "fun video", "Example Channel"); !hit || label != "Example Channel" {
		t.Fatalf("channel exact: hit=%v label=%q", hit, label)
	}
	if hit, _ := cfg.MatchWatchMeta("bilibili", "fun", "Example Channel"); hit {
		t.Fatal("youtube-only channel must not match bilibili")
	}
	if hit, label := cfg.MatchWatchMeta("youtube", "x", "@ExampleChannel"); !hit || label != "Example Channel" {
		t.Fatalf("handle must match collapsed display rule: hit=%v label=%q", hit, label)
	}
	// Only one channel row after collapse.
	n := 0
	for _, r := range cfg.WatchRules() {
		if r.Kind == "channel" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected 1 channel rule after collapse, got %#v", cfg.WatchRules())
	}
}

func TestMatchWatchHayIgnoresIDs(t *testing.T) {
	cfg := &Config{
		WatchWhitelistRules: []WatchWhitelistRule{
			{Site: "youtube", Kind: "channel", Keyword: "Example Channel", Mode: "exact"},
		},
	}
	cfg.SyncWatchRules(nil)
	hay := `{"author":"Example Channel","channelId":"UCxxx"}`
	if hit, _ := cfg.MatchWatchHay("youtube", hay); !hit {
		t.Fatal("expected channel name hit")
	}
	idOnly := `{"channelId":"UCxxx","videoId":"abc"}`
	if hit, _ := cfg.MatchWatchHay("youtube", idOnly); hit {
		t.Fatal("channel/video ids must not match")
	}
	handleHay := `https://www.youtube.com/@ExampleChannel/videos`
	if hit, _ := cfg.MatchWatchHay("youtube", handleHay); !hit {
		t.Fatal("exact channel should still match /@handle in hay")
	}
}

func TestWatchRulesDisplayOrderStable(t *testing.T) {
	cfg := &Config{
		WatchWhitelistRules: []WatchWhitelistRule{
			{Site: "all", Kind: "title", Keyword: "英语学习", Mode: "contains"},
			{Site: "youtube", Kind: "channel", Keyword: "Example Channel", Mode: "exact"},
		},
	}
	cfg.SyncWatchRules(nil)
	a := cfg.WatchRules()
	b := cfg.WatchRules()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("unstable: %#v vs %#v", a, b)
	}
}
