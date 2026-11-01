package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Path: filepath.Join(dir, "t.db"), KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	until := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	snap := RuntimeSnapshot{
		Unlock: UnlockQuota{
			DayKey: "2026-07-25", DayCount: 1,
			WeekKey: "2026-07-21", WeekCount: 2,
			TempUnlockUntil: until,
		},
		Sites: []SiteRuntime{{
			Site: "youtube", FullBlockUntil: until,
			WatchStarted: until.Add(-30 * time.Minute),
			LastPlayAt:   until.Add(-1 * time.Minute),
		}},
	}
	if err := db.SaveRuntime(snap); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if got.Unlock.DayCount != 1 || got.Unlock.WeekCount != 2 {
		t.Fatalf("quota=%+v", got.Unlock)
	}
	if len(got.Sites) != 1 || got.Sites[0].Site != "youtube" {
		t.Fatalf("sites=%+v", got.Sites)
	}
	if got.Sites[0].FullBlockUntil.IsZero() {
		t.Fatal("full block lost")
	}
}

func TestPlaySegmentsAggregate(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Path: filepath.Join(dir, "t.db")})
	if err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	db.NotePlay("youtube", base)
	db.NotePlay("youtube", base.Add(6*time.Second))
	db.NotePlay("youtube", base.Add(12*time.Second))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(Options{Path: filepath.Join(dir, "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	dur, _, n := db2.PlaySiteDurations("2026-07-25")
	if n < 1 {
		t.Fatal("expected segment")
	}
	if dur["youtube"] < 10 || dur["youtube"] > 30 {
		t.Fatalf("dur=%v", dur["youtube"])
	}
}

func TestFocusAndEvents(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Path: filepath.Join(dir, "t.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	if err := db.InsertFocus(FocusRec{
		StartedAt: now.Add(-time.Minute), EndedAt: now,
		DurationSec: 60, Bucket: "code", Group: "app", Title: "vim", Exe: "nvim.exe",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertEvent("app_start", map[string]any{"listen": "x"}); err != nil {
		t.Fatal(err)
	}
	day := dayKey(now)
	if db.CountFocusDay(day) != 1 {
		t.Fatal("focus count")
	}
	ev := db.RecentEvents(5)
	if len(ev) != 1 || ev[0]["kind"] != "app_start" {
		t.Fatalf("events=%v", ev)
	}
}

func TestRollupAndPruneKeepsDaily(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Path: filepath.Join(dir, "t.db"), KeepDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 25, 15, 0, 0, 0, time.Local)
	old := now.AddDate(0, 0, -5) // outside raw window
	mid := now.AddDate(0, 0, -2) // inside raw window
	oldDay := dayKey(old)
	midDay := dayKey(mid)

	if err := db.InsertFocus(FocusRec{
		StartedAt: old, EndedAt: old.Add(time.Minute),
		DurationSec: 60, Bucket: "code", Group: "app", Title: "old",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertFocus(FocusRec{
		StartedAt: mid, EndedAt: mid.Add(2 * time.Minute),
		DurationSec: 120, Bucket: "browser", Group: "browser", Title: "mid",
	}); err != nil {
		t.Fatal(err)
	}
	db.NotePlay("youtube", old)
	db.NotePlay("youtube", old.Add(30*time.Second))
	db.NotePlay("bilibili", mid)
	db.NotePlay("bilibili", mid.Add(20*time.Second))

	db.RollupClosedDays(now)
	if !db.HasDailyDay(oldDay) || !db.HasDailyDay(midDay) {
		t.Fatalf("rollup missing: old=%v mid=%v", db.HasDailyDay(oldDay), db.HasDailyDay(midDay))
	}
	apps := db.SumDailyAppStats(oldDay, midDay)
	var codeSec, browserSec float64
	for _, a := range apps {
		switch a.Bucket {
		case "code":
			codeSec = a.DurationSec
		case "browser":
			browserSec = a.DurationSec
		}
	}
	if codeSec < 59 || browserSec < 119 {
		t.Fatalf("daily apps code=%v browser=%v", codeSec, browserSec)
	}
	plays := db.SumDailyPlayStats(oldDay, midDay)
	bySite := map[string]float64{}
	for _, p := range plays {
		bySite[p.Site] = p.DurationSec
	}
	if bySite["youtube"] < 1 || bySite["bilibili"] < 1 {
		t.Fatalf("daily play=%v", bySite)
	}

	db.Prune(now)
	if db.CountFocusDay(oldDay) != 0 {
		t.Fatal("old raw focus should be pruned")
	}
	if db.CountFocusDay(midDay) == 0 {
		t.Fatal("mid raw focus should remain")
	}
	// Daily totals for pruned day must survive.
	apps2 := db.SumDailyAppStats(oldDay, oldDay)
	if len(apps2) == 0 || apps2[0].DurationSec < 59 {
		t.Fatalf("daily survived prune? %#v", apps2)
	}
}

func TestRollupIdempotent(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Path: filepath.Join(dir, "t.db"), KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	day := time.Date(2026, 7, 20, 10, 0, 0, 0, time.Local)
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.Local)
	if err := db.InsertFocus(FocusRec{
		StartedAt: day, EndedAt: day.Add(time.Minute),
		DurationSec: 60, Bucket: "code", Group: "app",
	}); err != nil {
		t.Fatal(err)
	}
	dk := dayKey(day)
	if err := db.RollupDay(dk, now); err != nil {
		t.Fatal(err)
	}
	if err := db.RollupDay(dk, now); err != nil {
		t.Fatal(err)
	}
	apps := db.SumDailyAppStats(dk, dk)
	if len(apps) != 1 || apps[0].Sessions != 1 {
		t.Fatalf("idempotent rollup failed: %#v", apps)
	}
}

func TestLLMDialogInsertRecentAndPrune(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Path: filepath.Join(dir, "t.db"), KeepDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	allow := true
	old := time.Date(2026, 7, 10, 12, 0, 0, 0, time.Local)
	mid := time.Date(2026, 7, 23, 12, 0, 0, 0, time.Local)
	now := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)

	fullReq := "[system]\n系统提示\n\n[user]\n站点：youtube\napi_key=sk-ABCDEFGHIJKLMNOP"
	if err := db.InsertLLMDialog(LLMDialogInput{
		Day: dayKey(old), Site: "youtube", Kind: LLMKindWatchJudge, CostCNY: 0.001, Allow: &allow,
		Reason: "old", RequestRaw: fullReq,
		ResponseRaw: `{"reason":"ok","allow":true}`, VideosJSON: `[{"title":"a"}]`, At: old,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertLLMDialog(LLMDialogInput{
		Day: dayKey(mid), Site: "bilibili", Kind: LLMKindWatchJudge, CostCNY: 0.002, Allow: &allow,
		Reason: "mid", RequestRaw: "[system]\ns\n\n[user]\n清单", ResponseRaw: `{"reason":"mid","allow":true}`,
		VideosJSON: `[{"title":"b","author":"up"}]`, At: mid,
	}); err != nil {
		t.Fatal(err)
	}
	block := false
	if err := db.InsertLLMDialog(LLMDialogInput{
		Day: dayKey(now), Site: "douyin", Kind: LLMKindWatchJudge, CostCNY: 0.003, Allow: &block,
		Reason: "刷屏", RequestRaw: "[system]\ns\n\n[user]\n清单2", ResponseRaw: `{"reason":"刷屏","allow":false}`,
		VideosJSON: `[]`, At: now,
	}); err != nil {
		t.Fatal(err)
	}
	titleReq := "[system]\n短名系统\n\n[user]\n限≤23字（按字符计，含标点）。标题：很长的标题……"
	if err := db.InsertLLMDialog(LLMDialogInput{
		Day: dayKey(now), Site: "title_short", Kind: LLMKindTitleShort, CostCNY: 0.0001,
		Reason: "短名：测试短名", RequestRaw: titleReq, ResponseRaw: "测试短名",
		VideosJSON: `[]`, At: now.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.RecentLLMDialogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("want 4 got %d", len(rows))
	}
	if rows[0].Kind != LLMKindTitleShort || rows[0].ResponseRaw != "测试短名" {
		t.Fatalf("newest title_short=%+v", rows[0])
	}
	if rows[0].RequestRaw != titleReq || rows[0].PromptSummary != titleReq {
		t.Fatalf("title request fields=%q / %q", rows[0].RequestRaw, rows[0].PromptSummary)
	}
	if rows[1].Site != "douyin" || rows[1].Kind != LLMKindWatchJudge || rows[1].Allow == nil || *rows[1].Allow {
		t.Fatalf("watch judge=%+v", rows[1])
	}
	if strings.Contains(rows[3].RequestRaw, "sk-ABCDEF") {
		t.Fatalf("api key leaked into dialog: %q", rows[3].RequestRaw)
	}
	if !strings.Contains(rows[3].RequestRaw, "[REDACTED]") {
		t.Fatalf("expected redaction: %q", rows[3].RequestRaw)
	}
	if !strings.Contains(rows[3].RequestRaw, "[system]") || !strings.Contains(rows[3].RequestRaw, "[user]") {
		t.Fatalf("expected full request transcript: %q", rows[3].RequestRaw)
	}

	// Soft cap: keep nearly 64KB; only truncate beyond MaxLLMDialogTextBytes.
	big := strings.Repeat("中", (MaxLLMDialogTextBytes/3)+100)
	if err := db.InsertLLMDialog(LLMDialogInput{
		Day: dayKey(now), Site: "youtube", Kind: LLMKindWatchJudge,
		RequestRaw: big, ResponseRaw: big, At: now.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	rowsBig, err := db.RecentLLMDialogs(1)
	if err != nil || len(rowsBig) != 1 {
		t.Fatalf("big dialog: %v %#v", err, rowsBig)
	}
	if !strings.Contains(rowsBig[0].RequestRaw, "上限64KB") {
		t.Fatalf("expected 64KB truncate note, got len=%d", len(rowsBig[0].RequestRaw))
	}
	if len(rowsBig[0].RequestRaw) < MaxLLMDialogTextBytes {
		t.Fatalf("truncated too aggressive: %d", len(rowsBig[0].RequestRaw))
	}

	db.Prune(now)
	rows, err = db.RecentLLMDialogs(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("after prune want 4 got %d", len(rows))
	}
	for _, r := range rows {
		if r.Day == dayKey(old) {
			t.Fatal("old dialog should be pruned")
		}
	}
}

func TestTitleShortMap(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(Options{Path: filepath.Join(dir, "t.db"), KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	src := "英语真题在线 - 大学英语六级真题，英语六级，PDF下载 | 英语真题在线"
	if _, ok := db.LookupTitleShort(src); ok {
		t.Fatal("unexpected hit")
	}
	if err := db.UpsertTitleShort(src, "六级真题在线"); err != nil {
		t.Fatal(err)
	}
	got, ok := db.LookupTitleShort(src)
	if !ok || got != "六级真题在线" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	over := strings.Repeat("超", titleShortMaxRunes+1)
	if err := db.UpsertTitleShort(src, over); err == nil {
		t.Fatal("expected reject overlong short name")
	}
}
