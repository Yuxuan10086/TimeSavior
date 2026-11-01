package config

import (
	"reflect"
	"testing"
)

func TestSyncWatchRulesMergesLegacy(t *testing.T) {
	cfg := &Config{
		WatchWhitelistRules: []WatchWhitelistRule{
			{Site: "youtube", Kind: "channel", Keyword: "Volka English", Mode: "contains"},
		},
		WatchTitleWhitelist: []string{"雅思", "  "},
	}
	cfg.SyncWatchRules(map[string][]string{
		"youtube": {"@VolkaEnglish", "Volka English"},
	})
	rules := cfg.WatchRules()
	var hasTitle, channelCount int
	var channelKeyword, channelMode string
	for _, r := range rules {
		if r.Kind == "title" && r.Keyword == "雅思" && r.Site == "all" && r.Mode == "contains" {
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
	if channelKeyword != "Volka English" {
		t.Fatalf("prefer display name, got %q", channelKeyword)
	}
	if channelMode != "exact" {
		t.Fatalf("channel mode must be exact, got %q", channelMode)
	}
}

func TestMatchWatchMetaTitleAndChannel(t *testing.T) {
	cfg := &Config{
		WatchWhitelistRules: []WatchWhitelistRule{
			{Site: "all", Kind: "title", Keyword: "雅思", Mode: "contains"},
			{Site: "youtube", Kind: "channel", Keyword: "Volka English", Mode: "exact"},
			{Site: "youtube", Kind: "channel", Keyword: "@VolkaEnglish", Mode: "exact"},
		},
	}
	cfg.SyncWatchRules(nil)

	if hit, label := cfg.MatchWatchMeta("youtube", "雅思口语 Part2", "Someone"); !hit || label != "title:雅思" {
		t.Fatalf("title: hit=%v label=%q", hit, label)
	}
	if hit, label := cfg.MatchWatchMeta("youtube", "fun video", "Volka English"); !hit || label != "Volka English" {
		t.Fatalf("channel exact: hit=%v label=%q", hit, label)
	}
	if hit, _ := cfg.MatchWatchMeta("bilibili", "fun", "Volka English"); hit {
		t.Fatal("youtube-only channel must not match bilibili")
	}
	if hit, label := cfg.MatchWatchMeta("youtube", "x", "@VolkaEnglish"); !hit || label != "Volka English" {
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
			{Site: "youtube", Kind: "channel", Keyword: "Volka English", Mode: "exact"},
		},
	}
	cfg.SyncWatchRules(nil)
	hay := `{"author":"Volka English","channelId":"UCxxx"}`
	if hit, _ := cfg.MatchWatchHay("youtube", hay); !hit {
		t.Fatal("expected channel name hit")
	}
	idOnly := `{"channelId":"UCxxx","videoId":"abc"}`
	if hit, _ := cfg.MatchWatchHay("youtube", idOnly); hit {
		t.Fatal("channel/video ids must not match")
	}
	handleHay := `https://www.youtube.com/@VolkaEnglish/videos`
	if hit, _ := cfg.MatchWatchHay("youtube", handleHay); !hit {
		t.Fatal("exact channel should still match /@handle in hay")
	}
}

func TestWatchRulesDisplayOrderStable(t *testing.T) {
	cfg := &Config{
		WatchWhitelistRules: []WatchWhitelistRule{
			{Site: "all", Kind: "title", Keyword: "雅思", Mode: "contains"},
			{Site: "youtube", Kind: "channel", Keyword: "Volka English", Mode: "exact"},
		},
	}
	cfg.SyncWatchRules(nil)
	a := cfg.WatchRules()
	b := cfg.WatchRules()
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("unstable: %#v vs %#v", a, b)
	}
}
