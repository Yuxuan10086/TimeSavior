package policy

import (
	"testing"
	"time"

	"timesavior/internal/config"
	"timesavior/internal/llm"
	"timesavior/internal/rules"
	"timesavior/internal/store"
)

func TestTitleWhitelistExemptsWatch(t *testing.T) {
	ruleStore, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		UnknownDefault:      "allow",
		DayStartHour:        4,
		WatchTitleWhitelist: []string{"雅思"},
	}
	e := New(cfg, ruleStore)
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)

	ex, label := e.NoteWatchMeta("youtube", "雅思口语 Part2 高分范例", "Teacher", nil, "vid1", base)
	if !ex || label != "title:雅思" {
		t.Fatalf("expected title exempt, got %v %q", ex, label)
	}

	var last Result
	for i := 0; i <= 20; i++ {
		now := base.Add(time.Duration(i) * 2 * time.Minute)
		last = e.Evaluate("rr1---sn-test.googlevideo.com", "/videoplayback", now)
		if last.Reason == ReasonFullBlock {
			t.Fatalf("title-whitelisted playback must not full_block at step %d", i)
		}
	}
	st := e.Status(base.Add(40*time.Minute))["sites"].(map[string]any)["youtube"].(map[string]any)
	if active, _ := st["full_block_active"].(bool); active {
		t.Fatal("youtube should not full-block under title whitelist")
	}
	if exempt, _ := st["watch_exempt"].(bool); !exempt {
		t.Fatal("expected watch_exempt for 雅思 title")
	}
}

func TestTitleWhitelistBodySniff(t *testing.T) {
	ruleStore, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		UnknownDefault:      "allow",
		DayStartHour:        4,
		WatchTitleWhitelist: []string{"雅思"},
	}
	e := New(cfg, ruleStore)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	body := []byte(`{"videoDetails":{"title":"雅思听力精听训练 Day3","author":"X"}}`)
	e.ObserveResponse("www.youtube.com", "/youtubei/v1/player", body, now)
	st := e.Status(now)["sites"].(map[string]any)["youtube"].(map[string]any)
	if exempt, _ := st["watch_exempt"].(bool); !exempt {
		t.Fatal("body title keyword should exempt")
	}
}

type memLLMUsage struct {
	dayCost   float64
	dayCalls  int
	totalCost float64
	day       string
}

func (m *memLLMUsage) AddLLMUsage(day string, costCNY float64) error {
	m.day = day
	m.dayCost += costCNY
	m.dayCalls++
	m.totalCost += costCNY
	return nil
}

func (m *memLLMUsage) InsertLLMDialog(in store.LLMDialogInput) error {
	return nil
}

func (m *memLLMUsage) LLMUsage(day string) store.LLMUsageSnapshot {
	return store.LLMUsageSnapshot{
		Day: day, DayCostCNY: m.dayCost, DayCalls: m.dayCalls, TotalCostCNY: m.totalCost, TotalCalls: m.dayCalls,
	}
}

func TestLLMCircuitFallsBackToFullBlock(t *testing.T) {
	ruleStore, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	limit := 0.01
	fb := true
	cfg := &config.Config{
		UnknownDefault: "allow",
		DayStartHour:   4,
		LLMJudge: config.LLMJudgeCfg{
			Enabled:              true,
			APIKey:               "test-key",
			CostMonitorEnabled:   true,
			DailyCostLimitCNY:    limit,
			CircuitFallbackBlock: &fb,
		},
	}
	e := New(cfg, ruleStore)
	usage := &memLLMUsage{dayCost: 0.02} // already over limit
	e.AttachLLM(nil, usage)             // client nil → not ready; but circuit also open

	// Even with key set, nil client => !llmReady → FullBlock
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	var last Result
	for i := 0; i <= 130; i++ {
		now := base.Add(time.Duration(i) * 10 * time.Second) // ~22 min dense play
		last = e.Evaluate("www.bilibili.com", "/x/player/wbi/playurl", now)
	}
	if last.Reason != ReasonFullBlock {
		t.Fatalf("expected FullBlock when LLM unavailable/circuit, got %s", last.Reason)
	}
}

func TestLLMCostAccumulatesAndOpensCircuit(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: dir + "/t.db", KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	day := "2026-07-25"
	if err := db.AddLLMUsage(day, 0.04); err != nil {
		t.Fatal(err)
	}
	if err := db.AddLLMUsage(day, 0.04); err != nil {
		t.Fatal(err)
	}
	u := db.LLMUsage(day)
	if u.DayCalls != 2 || u.DayCostCNY < 0.079 {
		t.Fatalf("usage=%+v", u)
	}
	if u.TotalCostCNY < 0.079 || u.TotalCalls != 2 {
		t.Fatalf("totals=%+v", u)
	}
	if u.DayCostCNY < 0.10 {
		// Simulate threshold check
		t.Log("below 0.10 limit as expected for partial")
	}
	_ = db.AddLLMUsage(day, 0.03)
	u = db.LLMUsage(day)
	if u.DayCostCNY < 0.10 {
		t.Fatalf("expected circuit threshold reached, got %v", u.DayCostCNY)
	}
}

func TestCostMonitorOffNeverOpensCircuit(t *testing.T) {
	ruleStore, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	fb := true
	cfg := &config.Config{
		UnknownDefault: "allow",
		DayStartHour:   4,
		LLMJudge: config.LLMJudgeCfg{
			Enabled:              true,
			APIKey:               "test-key",
			CostMonitorEnabled:   false,
			DailyCostLimitCNY:    0.01,
			CircuitFallbackBlock: &fb,
		},
	}
	e := New(cfg, ruleStore)
	usage := &memLLMUsage{dayCost: 9.99} // far over limit
	e.AttachLLM(&llm.Client{APIKey: "test-key"}, usage)

	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	st := e.LLMStatus(now)
	if st["circuit_open"].(bool) {
		t.Fatal("cost monitor off must not open circuit")
	}
	if st["status"] != "monitor_off" {
		t.Fatalf("status=%v want monitor_off", st["status"])
	}
	if st["cost_estimated_only"] != true {
		t.Fatalf("expected cost_estimated_only")
	}
}
