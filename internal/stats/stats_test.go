package stats

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"timesavior/internal/store"
)

func restoreMinWatch(t *testing.T) {
	t.Helper()
	prev := int(minWatchSec)
	t.Cleanup(func() { SetMinWatchSec(prev) })
}

func TestMergePlayDuration(t *testing.T) {
	restoreMinWatch(t)
	SetMinWatchSec(15)
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	pulses := []playPulse{
		{Site: "youtube", At: base},
		{Site: "youtube", At: base.Add(30 * time.Second)},
		{Site: "youtube", At: base.Add(60 * time.Second)},
		// idle gap → new short segment (prefetch) — dropped
		{Site: "youtube", At: base.Add(20 * time.Minute)},
		{Site: "bilibili", At: base.Add(21 * time.Minute)},
	}
	got := mergePlayDuration(pulses)
	// first segment: 60s + 5s pad = 65; isolated shorts discarded
	if got["youtube"] < 60 || got["youtube"] > 90 {
		t.Fatalf("youtube dur=%v", got["youtube"])
	}
	if got["bilibili"] != 0 {
		t.Fatalf("short bilibili prefetch must be dropped, got %v", got["bilibili"])
	}
}

// Interleaved CDN pulses must not each inherit the full wall-clock span.
func TestMergePlayDurationNoCrossSite(t *testing.T) {
	restoreMinWatch(t)
	SetMinWatchSec(0) // measure merge math without short-session filter
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	var pulses []playPulse
	for i := 0; i < 120; i++ {
		site := "youtube"
		if i%2 == 1 {
			site = "bilibili"
		}
		pulses = append(pulses, playPulse{Site: site, At: base.Add(time.Duration(i*5) * time.Second)})
	}
	got := mergePlayDuration(pulses)
	yt, bi := got["youtube"], got["bilibili"]
	// Site switches end segments → ~60 pulses * 5s pad each ≈ 300s.
	if yt < 250 || yt > 350 {
		t.Fatalf("youtube want ~300s, got %v", yt)
	}
	if bi < 250 || bi > 350 {
		t.Fatalf("bilibili want ~300s, got %v", bi)
	}
	if yt > 400 || bi > 400 {
		t.Fatalf("wall-clock double-count suspected: yt=%v bi=%v", yt, bi)
	}
}

func TestMergePlayDurationSeparateSessions(t *testing.T) {
	base := time.Date(2026, 7, 25, 8, 0, 0, 0, time.Local)
	var pulses []playPulse
	for m := 0; m <= 40; m += 5 {
		pulses = append(pulses, playPulse{Site: "bilibili", At: base.Add(time.Duration(m) * time.Minute)})
	}
	yt0 := base.Add(2 * time.Hour)
	for m := 0; m <= 10; m += 5 {
		pulses = append(pulses, playPulse{Site: "youtube", At: yt0.Add(time.Duration(m) * time.Minute)})
	}
	got := mergePlayDuration(pulses)
	if got["bilibili"] < 40*60 || got["bilibili"] > 41*60 {
		t.Fatalf("bilibili=%v", got["bilibili"])
	}
	if got["youtube"] < 10*60 || got["youtube"] > 11*60 {
		t.Fatalf("youtube=%v", got["youtube"])
	}
	if absF(got["bilibili"]-got["youtube"]) < 20*60 {
		t.Fatalf("sites must differ substantially: %v", got)
	}
}

// Regression: a buggy shared-duration map would force equal site totals.
func TestMergePlayDurationUnequalSites(t *testing.T) {
	base := time.Date(2026, 7, 25, 9, 0, 0, 0, time.Local)
	var pulses []playPulse
	for m := 0; m <= 41; m += 5 {
		pulses = append(pulses, playPulse{Site: "bilibili", At: base.Add(time.Duration(m) * time.Minute)})
	}
	yt0 := base.Add(2 * time.Hour)
	for m := 0; m <= 3; m++ {
		pulses = append(pulses, playPulse{Site: "youtube", At: yt0.Add(time.Duration(m) * time.Minute)})
	}
	got := mergePlayDuration(pulses)
	bi := got["bilibili"]
	yt := got["youtube"]
	if absF(bi-yt) < 30*60 {
		t.Fatalf("expected clearly different durations, bi=%v yt=%v", bi, yt)
	}
	if bi < 40*60 || bi > 42*60 {
		t.Fatalf("bilibili=%v", bi)
	}
	if yt < 3*60 || yt > 4*60 {
		t.Fatalf("youtube=%v", yt)
	}
}

func TestUnionPlayPulsesDedup(t *testing.T) {
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	a := []playPulse{{Site: "youtube", At: base}}
	b := []playPulse{
		{Site: "youtube", At: base.Add(2 * time.Second)}, // same 5s bucket
		{Site: "bilibili", At: base.Add(10 * time.Second)},
	}
	got := unionPlayPulses(a, b)
	if len(got) != 2 {
		t.Fatalf("want 2 after dedup, got %d %#v", len(got), got)
	}
}

func TestBuildUnionsRequestWhenPlayPartial(t *testing.T) {
	restoreMinWatch(t)
	SetMinWatchSec(15)
	dir := t.TempDir()
	day := time.Now().Format("2006-01-02")
	play := filepath.Join(dir, "play-"+day+".jsonl")
	req := filepath.Join(dir, "request-"+day+".jsonl")
	base := time.Now().Add(-2 * time.Hour).Truncate(time.Second)

	// youtube: real watch ≥15s; single prefetch pulse would be dropped
	var playBody string
	for _, d := range []time.Duration{0, 10 * time.Second, 20 * time.Second} {
		playBody += `{"kind":"play_pulse","site":"youtube","ts":"` + base.Add(d).Format(time.RFC3339Nano) + `"}` + "\n"
	}
	mustWrite(t, play, playBody)
	mustWrite(t, req,
		`{"category":"play","site":"bilibili","ts":"`+base.Add(time.Minute).Format(time.RFC3339Nano)+`"}`+"\n"+
			`{"category":"play","site":"bilibili","ts":"`+base.Add(11*time.Minute).Format(time.RFC3339Nano)+`"}`+"\n",
	)
	rep := Build(dir, 1, nil, nil)
	if rep.VideoBySite["bilibili"] < 10*60 {
		t.Fatalf("bilibili missing/short from request union: %v", rep.VideoBySite)
	}
	if rep.VideoBySite["youtube"] < 15 {
		t.Fatalf("youtube missing from play file: %v", rep.VideoBySite)
	}
	if absF(rep.VideoBySite["bilibili"]-rep.VideoBySite["youtube"]) < 5*60 {
		t.Fatalf("union must keep site durations distinct: %v", rep.VideoBySite)
	}
}

func TestShortPlaySessionsDropped(t *testing.T) {
	restoreMinWatch(t)
	SetMinWatchSec(15)
	dir := t.TempDir()
	day := time.Now().Format("2006-01-02")
	base := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
	play := filepath.Join(dir, "play-"+day+".jsonl")
	// Isolated 5s prefetch on youtube; 40s real watch on bilibili
	mustWrite(t, play,
		`{"kind":"play_pulse","site":"youtube","ts":"`+base.Format(time.RFC3339Nano)+`"}`+"\n"+
			`{"kind":"play_pulse","site":"bilibili","ts":"`+base.Add(time.Minute).Format(time.RFC3339Nano)+`"}`+"\n"+
			`{"kind":"play_pulse","site":"bilibili","ts":"`+base.Add(time.Minute+40*time.Second).Format(time.RFC3339Nano)+`"}`+"\n",
	)
	rep := Build(dir, 1, nil, nil)
	if rep.VideoBySite["youtube"] != 0 {
		t.Fatalf("youtube prefetch must be dropped: %v", rep.VideoBySite)
	}
	if rep.VideoBySite["bilibili"] < 35 {
		t.Fatalf("bilibili real watch kept: %v", rep.VideoBySite)
	}
	// App focus path untouched
	if rep.AppTotalSec != 0 || len(rep.Apps) != 0 {
		t.Fatalf("apps must stay empty without focus data: total=%v apps=%v", rep.AppTotalSec, rep.Apps)
	}
}

func TestLongSessionFragmentsNotDropped(t *testing.T) {
	restoreMinWatch(t)
	SetMinWatchSec(15)
	base := time.Date(2026, 7, 25, 12, 0, 0, 0, time.Local)
	var pulses []playPulse
	// One continuous session with 5s CDN shards — must keep full span.
	for i := 0; i <= 24; i++ {
		pulses = append(pulses, playPulse{Site: "youtube", At: base.Add(time.Duration(i*5) * time.Second)})
	}
	got := mergePlayDuration(pulses)
	if got["youtube"] < 120 || got["youtube"] > 140 {
		t.Fatalf("long session shards must merge, got %v", got["youtube"])
	}
}

// Mid-day empty DB must not hide morning request play pulses.
func TestBuildMergesDBAfternoonWithRequestMorning(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: filepath.Join(dir, "t.db"), KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	day := now.Format("2006-01-02")
	morning := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, time.Local)
	afternoon := time.Date(now.Year(), now.Month(), now.Day(), 14, 0, 0, 0, time.Local)

	req := filepath.Join(dir, "request-"+day+".jsonl")
	var body string
	for m := 0; m <= 40; m += 5 {
		ts := morning.Add(time.Duration(m) * time.Minute)
		body += `{"category":"play","site":"bilibili","ts":"` + ts.Format(time.RFC3339Nano) + `"}` + "\n"
	}
	mustWrite(t, req, body)

	db.NotePlay("youtube", afternoon)
	db.NotePlay("youtube", afternoon.Add(30*time.Second))

	rep := Build(dir, 1, nil, db)
	if rep.VideoBySite["bilibili"] < 35*60 {
		t.Fatalf("morning bilibili hidden: %v", rep.VideoBySite)
	}
	if rep.VideoBySite["youtube"] < 20 {
		t.Fatalf("afternoon youtube missing: %v", rep.VideoBySite)
	}

	if n := BackfillPlayDB(db, dir, 1); n < 1 {
		t.Fatalf("backfill days=%d", n)
	}
	dur, _, _ := db.PlaySiteDurations(day)
	if dur["bilibili"] < 35*60 {
		t.Fatalf("backfill did not persist bilibili: %v", dur)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func TestAggregateVideosByAuthor(t *testing.T) {
	siteDur := map[string]float64{"youtube": 100, "bilibili": 40}
	siteLast := map[string]string{"youtube": "2026-07-25T12:00:00+08:00"}
	metas := []store.WatchLabel{
		{Site: "youtube", Title: "最热门", Author: "", ExemptLabel: "Volka English", Exempt: true, Source: "youtube_player", TS: "2026-07-25T12:00:00+08:00"},
		{Site: "youtube", Title: "Learn English Fast", Author: "Volka English 白名单", URL: "https://www.youtube.com/watch?v=abc", Source: "youtube_player", TS: "2026-07-25T11:00:00+08:00"},
		{Site: "bilibili", Title: "具身智能-哔哩哔哩_bilibili - Tabbit", Author: "", Source: "focus_title", TS: "2026-07-25T10:00:00+08:00"},
	}
	videos, _, byAuth := aggregateVideosByAuthor(siteDur, siteLast, metas, nil)
	if len(videos) < 1 {
		t.Fatalf("expected author rows, got %#v", videos)
	}
	var volka *VideoRow
	var jushen *VideoRow
	for i := range videos {
		if videos[i].Author == "Volka English" {
			volka = &videos[i]
		}
		if videos[i].Author == "具身智能" {
			jushen = &videos[i]
		}
	}
	if volka == nil {
		t.Fatalf("missing Volka English row: %#v", videos)
	}
	if strings.Contains(volka.Author, "白名单") || strings.Contains(strings.ToLower(volka.Author), "whitelist") {
		t.Fatalf("author must not carry whitelist chrome: %q", volka.Author)
	}
	if !volka.Exempt {
		t.Fatal("whitelist hit must set watch_exempt separately, not glue into author")
	}
	if volka.Title != "Learn English Fast" {
		t.Fatalf("dirty title leaked: %q", volka.Title)
	}
	if volka.DurationSec < 99 {
		t.Fatalf("youtube duration not attributed: %v", volka.DurationSec)
	}
	if byAuth["Volka English"] < 99 {
		t.Fatalf("video_by_author: %v", byAuth)
	}
	if jushen == nil {
		t.Fatalf("title-only bilibili should split by work title, got %#v", videos)
	}
	if jushen.DurationSec < 39 {
		t.Fatalf("bilibili title duration: %v", jushen.DurationSec)
	}
	// Main list must not be site-primary duplicates named after sites.
	for _, v := range videos {
		if v.Author == "youtube" || v.Author == "YouTube" || v.Author == "bilibili" {
			t.Fatalf("site used as author: %#v", v)
		}
		if v.Author == "未识别" && v.DurationSec > 1 {
			t.Fatalf("unexpected 未识别 blob: %#v", v)
		}
	}
}

func TestAggregateVideosTimelineSplit(t *testing.T) {
	loc := time.FixedZone("CST", 8*3600)
	start := time.Date(2026, 7, 25, 19, 57, 31, 0, loc)
	end := time.Date(2026, 7, 25, 20, 24, 46, 0, loc)
	dur := end.Sub(start).Seconds()
	siteDur := map[string]float64{"bilibili": dur}
	metas := []store.WatchLabel{
		{Site: "bilibili", Title: "人民的名义05", Author: "弥先生碎碎念", Source: "bilibili_view", TS: start.Add(23 * time.Second).Format(time.RFC3339)},
		{Site: "bilibili", Title: "人民的名义05", Author: "", Source: "focus_title", TS: start.Add(12 * time.Second).Format(time.RFC3339)},
		{Site: "bilibili", Title: "无人区惊魂", Author: "鸟鸟影视", Source: "bilibili_html", TS: time.Date(2026, 7, 25, 20, 2, 56, 0, loc).Format(time.RFC3339)},
		{Site: "bilibili", Title: "浙江第一悬案", Author: "我是江无情", Source: "bilibili_view", TS: time.Date(2026, 7, 25, 20, 13, 18, 0, loc).Format(time.RFC3339)},
		{Site: "bilibili", Title: "郑州续面恶人论", Author: "我是江无情", Source: "bilibili_view", TS: time.Date(2026, 7, 25, 20, 23, 44, 0, loc).Format(time.RFC3339)},
		{Site: "bilibili", Title: "郑州续面恶人论", Author: "", Source: "focus_title", TS: time.Date(2026, 7, 25, 20, 24, 47, 0, loc).Format(time.RFC3339)},
	}
	spans := []store.PlaySeg{{Site: "bilibili", Start: start, End: end, DurationSec: dur}}
	videos, _, byAuth := aggregateVideosByAuthor(siteDur, nil, metas, spans)
	if byAuth["弥先生碎碎念"] < 60 {
		t.Fatalf("弥先生碎碎念 too small: %v videos=%#v", byAuth, videos)
	}
	if byAuth["鸟鸟影视"] < 60 {
		t.Fatalf("鸟鸟影视 too small: %v", byAuth)
	}
	if byAuth["我是江无情"] < 60 {
		t.Fatalf("我是江无情 too small: %v", byAuth)
	}
	var unknown float64
	for _, v := range videos {
		if v.Author == "未识别" {
			unknown += v.DurationSec
		}
	}
	if unknown > dur*0.05 {
		t.Fatalf("未识别 ate too much: %v / %v; %#v", unknown, dur, videos)
	}
	sum := 0.0
	for _, v := range videos {
		sum += v.DurationSec
	}
	if absF(sum-dur) > 1 {
		t.Fatalf("duration sum %v != site %v", sum, dur)
	}
}

func TestBuildUsesDailyRollup(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: filepath.Join(dir, "t.db"), KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	yesterday := now.AddDate(0, 0, -1)
	yDay := yesterday.Format("2006-01-02")
	if err := db.InsertFocus(store.FocusRec{
		StartedAt: yesterday, EndedAt: yesterday.Add(5 * time.Minute),
		DurationSec: 300, Bucket: "code", Group: "app", Title: "nvim",
	}); err != nil {
		t.Fatal(err)
	}
	db.NotePlay("youtube", yesterday)
	db.NotePlay("youtube", yesterday.Add(40*time.Second))
	db.RollupClosedDays(now)

	// Prune would keep yesterday (within 7d); simulate history-only path by querying days=2.
	rep := Build(dir, 2, nil, db)
	if rep.FromDay == "" || rep.ToDay == "" {
		t.Fatalf("range unset: %+v", rep)
	}
	var code float64
	for _, a := range rep.Apps {
		if a.Bucket == "code" {
			code = a.DurationSec
		}
	}
	if code < 299 {
		t.Fatalf("expected daily code focus, got apps=%v focus=%d", rep.Apps, rep.FocusSessions)
	}
	if rep.VideoBySite["youtube"] < 1 {
		t.Fatalf("expected youtube from daily play: %v", rep.VideoBySite)
	}

	// from/to should work and include the rolled day.
	rep2 := BuildQuery(dir, Query{From: yDay, To: yDay}, nil, db)
	if rep2.Days != 1 || rep2.FromDay != yDay {
		t.Fatalf("from/to report: %+v", rep2)
	}
	if rep2.AppTotalSec < 299 {
		t.Fatalf("from/to app total=%v", rep2.AppTotalSec)
	}
}

func TestBuildRollupPreservesChildrenAndAuthors(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: filepath.Join(dir, "t.db"), KeepDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.Local)
	old := now.AddDate(0, 0, -5) // outside raw keep window
	oldDay := old.Format("2006-01-02")
	start := old.Add(1 * time.Hour)

	recs := []store.FocusRec{
		{
			StartedAt: start, EndedAt: start.Add(10 * time.Minute), DurationSec: 600,
			Bucket: "系统应用", Group: "system", Title: "", Exe: "explorer.exe",
		},
		{
			StartedAt: start.Add(10 * time.Minute), EndedAt: start.Add(15 * time.Minute), DurationSec: 300,
			Bucket: "系统应用", Group: "system", Title: "", Exe: "WindowsTerminal.exe",
		},
		{
			StartedAt: start.Add(15 * time.Minute), EndedAt: start.Add(20 * time.Minute), DurationSec: 300,
			Bucket: "浏览器-AI", Group: "browser_ai",
			Title: "ChatGPT - Microsoft Edge", Exe: "msedge.exe",
			ClassSource: "proxy:chatgpt.com",
		},
	}
	for _, r := range recs {
		if err := db.InsertFocus(r); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertWatch(store.WatchRec{
		DedupeKey: "bili-a", Site: "bilibili", Title: "作品A", Author: "作者甲",
		URL: "https://bilibili.com/1", Source: "api", SeenAt: start.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertWatch(store.WatchRec{
		DedupeKey: "bili-b", Site: "bilibili", Title: "作品B", Author: "作者乙",
		URL: "https://bilibili.com/2", Source: "api", SeenAt: start.Add(5 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	db.NotePlay("bilibili", start)
	db.NotePlay("bilibili", start.Add(30*time.Second))
	db.NotePlay("bilibili", start.Add(4*time.Minute))
	db.NotePlay("bilibili", start.Add(4*time.Minute+20*time.Second))

	db.RollupClosedDays(now)
	if !db.HasDailyDay(oldDay) {
		t.Fatal("rollup missing")
	}
	if db.CountDailyAppDetailDay(oldDay) < 2 {
		t.Fatalf("expected app detail rows, got %d", db.CountDailyAppDetailDay(oldDay))
	}
	if n, _ := db.DailyWatchEntries(oldDay, oldDay); len(n) < 2 {
		t.Fatalf("expected multi watch labels, got %d", len(n))
	}

	db.Prune(now)
	if db.CountFocusDay(oldDay) != 0 {
		t.Fatal("raw focus should be pruned")
	}

	rep := BuildQuery(dir, Query{From: oldDay, To: oldDay}, nil, db)
	var sys, ai *AppRow
	for i := range rep.Apps {
		switch rep.Apps[i].Bucket {
		case "系统应用":
			sys = &rep.Apps[i]
		case "浏览器-AI":
			ai = &rep.Apps[i]
		}
	}
	if sys == nil || len(sys.Children) < 2 {
		t.Fatalf("system children after prune: %+v", sys)
	}
	names := map[string]float64{}
	for _, c := range sys.Children {
		names[c.Bucket] = c.DurationSec
	}
	if names["资源管理器"] < 599 || names["Windows Terminal"] < 299 {
		t.Fatalf("detail durations after prune: %v", names)
	}
	if ai == nil || len(ai.Children) < 1 {
		t.Fatalf("ai children after prune: %+v", ai)
	}
	if onlyUnknownVideos(rep.Videos) {
		t.Fatalf("authors should survive prune: %#v", rep.Videos)
	}
	authors := map[string]bool{}
	for _, v := range rep.Videos {
		authors[v.Author] = true
	}
	if !authors["作者甲"] && !authors["作者乙"] {
		t.Fatalf("want 作者甲/乙 in videos: %#v", rep.Videos)
	}
}


func TestBuildAppChildren(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: filepath.Join(dir, "t.db"), KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	start := now.Add(-30 * time.Minute)
	recs := []store.FocusRec{
		{
			StartedAt: start, EndedAt: start.Add(10 * time.Minute), DurationSec: 600,
			Bucket: "系统应用", Group: "system", Title: "", Exe: "explorer.exe",
		},
		{
			StartedAt: start.Add(10 * time.Minute), EndedAt: start.Add(15 * time.Minute), DurationSec: 300,
			Bucket: "系统应用", Group: "system", Title: "", Exe: "WindowsTerminal.exe",
		},
		{
			StartedAt: start.Add(15 * time.Minute), EndedAt: start.Add(20 * time.Minute), DurationSec: 300,
			Bucket: "浏览器-AI", Group: "browser_ai",
			Title: "ChatGPT - Microsoft Edge", Exe: "msedge.exe",
			ClassSource: "proxy:chatgpt.com",
		},
		{
			StartedAt: start.Add(20 * time.Minute), EndedAt: start.Add(25 * time.Minute), DurationSec: 300,
			Bucket: "浏览器-AI", Group: "browser_ai",
			Title: "Claude - Google Chrome", Exe: "chrome.exe",
			ClassSource: "proxy:claude.ai",
		},
		{
			StartedAt: start.Add(25 * time.Minute), EndedAt: start.Add(28 * time.Minute), DurationSec: 180,
			Bucket: "Cursor", Group: "app", Title: "main.go", Exe: "Cursor.exe",
		},
	}
	for _, r := range recs {
		if err := db.InsertFocus(r); err != nil {
			t.Fatal(err)
		}
	}

	rep := Build(dir, 1, nil, db)
	var sys, ai, cursor *AppRow
	for i := range rep.Apps {
		switch rep.Apps[i].Bucket {
		case "系统应用":
			sys = &rep.Apps[i]
		case "浏览器-AI":
			ai = &rep.Apps[i]
		case "Cursor":
			cursor = &rep.Apps[i]
		}
	}
	if sys == nil || len(sys.Children) < 2 {
		t.Fatalf("system children: %+v", sys)
	}
	names := map[string]float64{}
	for _, c := range sys.Children {
		names[c.Bucket] = c.DurationSec
	}
	if names["资源管理器"] < 599 || names["Windows Terminal"] < 299 {
		t.Fatalf("system detail durations: %v", names)
	}
	if ai == nil || len(ai.Children) < 2 {
		t.Fatalf("browser AI children: %+v", ai)
	}
	aiNames := map[string]bool{}
	for _, c := range ai.Children {
		aiNames[c.Bucket] = true
	}
	if !aiNames["ChatGPT"] || !aiNames["Claude"] {
		t.Fatalf("want ChatGPT/Claude children, got %v", aiNames)
	}
	if cursor != nil && len(cursor.Children) > 0 {
		t.Fatalf("plain app should not expand: %+v", cursor.Children)
	}
}

func TestBuildSkipsLockScreen(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: filepath.Join(dir, "t.db"), KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	start := now.Add(-10 * time.Minute)
	recs := []store.FocusRec{
		{
			StartedAt: start, EndedAt: start.Add(2 * time.Minute), DurationSec: 120,
			Bucket: "系统应用", Group: "system", Title: "Windows 默认锁屏界面", Exe: "LockApp.exe",
		},
		{
			StartedAt: start.Add(2 * time.Minute), EndedAt: start.Add(5 * time.Minute), DurationSec: 180,
			Bucket: "系统应用", Group: "system", Title: "", Exe: "explorer.exe",
		},
	}
	for _, r := range recs {
		if err := db.InsertFocus(r); err != nil {
			t.Fatal(err)
		}
	}
	rep := Build(dir, 1, nil, db)
	var sys *AppRow
	for i := range rep.Apps {
		if rep.Apps[i].Bucket == "系统应用" {
			sys = &rep.Apps[i]
		}
	}
	if sys == nil {
		t.Fatal("missing system app")
	}
	if sys.DurationSec < 179 || sys.DurationSec > 181 {
		t.Fatalf("lock must be excluded, dur=%v", sys.DurationSec)
	}
	for _, c := range sys.Children {
		if c.Bucket == "锁屏" {
			t.Fatalf("lock child visible: %+v", c)
		}
	}
}

func TestBuildReclassifiesStaleVideoTitle(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: filepath.Join(dir, "t.db"), KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	start := now.Add(-10 * time.Minute)
	title := "英语真题在线 - 大学英语六级真题，英语六级，PDF下载 | 英语真题在线 - Microsoft Edge"
	if err := db.InsertFocus(store.FocusRec{
		StartedAt: start, EndedAt: start.Add(2 * time.Minute), DurationSec: 120,
		Bucket: "浏览器-视频", Group: "browser_video", Title: title, Exe: "msedge.exe",
	}); err != nil {
		t.Fatal(err)
	}
	rep := Build(dir, 1, nil, db)
	var video, news *AppRow
	for i := range rep.Apps {
		switch rep.Apps[i].Bucket {
		case "浏览器-视频":
			video = &rep.Apps[i]
		case "浏览器-资讯":
			news = &rep.Apps[i]
		}
	}
	if video != nil && video.DurationSec > 0 {
		t.Fatalf("CET must not stay under video: %+v", video)
	}
	if news == nil || news.DurationSec < 119 {
		t.Fatalf("CET should land in news: %+v", news)
	}
	if len(news.Children) == 0 {
		t.Fatal("expected news child")
	}
}

func TestBuildReclassifiesTitleOnlyAI(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(store.Options{Path: filepath.Join(dir, "t.db"), KeepDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	now := time.Now()
	start := now.Add(-10 * time.Minute)
	// Historical title→AI mislabel (Zhihu post about ChatGPT), no proxy class_source.
	if err := db.InsertFocus(store.FocusRec{
		StartedAt: start, EndedAt: start.Add(2 * time.Minute), DurationSec: 120,
		Bucket: "浏览器-AI", Group: "browser_ai",
		Title: "如何看待 ChatGPT - 知乎 - Microsoft Edge", Exe: "msedge.exe",
		ClassSource: "title",
	}); err != nil {
		t.Fatal(err)
	}
	rep := Build(dir, 1, nil, db)
	var ai, news *AppRow
	for i := range rep.Apps {
		switch rep.Apps[i].Bucket {
		case "浏览器-AI":
			ai = &rep.Apps[i]
		case "浏览器-资讯":
			news = &rep.Apps[i]
		}
	}
	if ai != nil && ai.DurationSec > 0 {
		t.Fatalf("zhihu+ChatGPT must not stay under AI: %+v", ai)
	}
	if news == nil || news.DurationSec < 119 {
		t.Fatalf("zhihu+ChatGPT should land in news: %+v", news)
	}
}

