package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"timesavior/internal/focus"
	"timesavior/internal/store"
	"timesavior/internal/watchmeta"
)

const playIdleGap = 10 * time.Minute
const playPulsePad = 5 * time.Second // matches audit.Play throttle
const maxRangeDays = 366
const defaultMinWatchSec = 15.0

// minWatchSec drops isolated play sessions / attributed works shorter than this.
// Long-session CDN fragments stay merged via playIdleGap before this check.
var minWatchSec = defaultMinWatchSec

// SetMinWatchSec configures the minimum counted watch duration (seconds).
// Values <= 0 disable the filter.
func SetMinWatchSec(sec int) {
	if sec <= 0 {
		minWatchSec = 0
		return
	}
	minWatchSec = float64(sec)
}

func effectiveMinWatchSec() float64 {
	if minWatchSec < 0 {
		return 0
	}
	return minWatchSec
}

// AppRow is aggregated focus time for one peer-level bucket.
// Aggregate buckets (browser_*, system) may include Children with finer keys.
type AppRow struct {
	Bucket      string   `json:"bucket"`
	Group       string   `json:"group"`
	DurationSec float64  `json:"duration_sec"`
	Sessions    int      `json:"sessions"`
	SharePct    float64  `json:"share_pct"`
	LastTitle   string   `json:"last_title,omitempty"`
	ShortName   string   `json:"short_name,omitempty"` // LLM/cache short label for UI
	FullTitle   string   `json:"full_title,omitempty"` // original when ShortName set
	Children    []AppRow `json:"children,omitempty"`
}

// VideoRow aggregates play-traffic duration by author (site is secondary).
type VideoRow struct {
	Author      string  `json:"author"`
	Site        string  `json:"site,omitempty"`
	Title       string  `json:"title,omitempty"`
	URL         string  `json:"url,omitempty"`
	DurationSec float64 `json:"duration_sec"`
	SharePct    float64 `json:"share_pct"`
	Exempt      bool    `json:"watch_exempt,omitempty"`
	LastSeen    string  `json:"last_seen,omitempty"`
}

// AuthorRow aggregates watch duration by author when metadata exists.
type AuthorRow struct {
	Site        string  `json:"site,omitempty"`
	Author      string  `json:"author"`
	DurationSec float64 `json:"duration_sec"`
	Title       string  `json:"title,omitempty"`
}

// Report is the stats payload for UI/API.
type Report struct {
	GeneratedAt     string             `json:"generated_at"`
	Days            int                `json:"days"`
	FromDay         string             `json:"from_day"`
	ToDay           string             `json:"to_day"`
	AppTotalSec     float64            `json:"app_total_sec"`
	Apps            []AppRow           `json:"apps"`
	Videos          []VideoRow         `json:"videos"` // by author
	Authors         []AuthorRow        `json:"authors"`
	VideoBySite     map[string]float64 `json:"video_by_site"`   // seconds (secondary)
	VideoByAuthor   map[string]float64 `json:"video_by_author"` // seconds (primary cards)
	VideoTotalSec   float64            `json:"video_total_sec"`
	PlayPulses      int                `json:"play_pulses"`
	FocusSessions   int                `json:"focus_sessions"`
	WatchMetaEvents int                `json:"watch_meta_events"`
}

// Query selects a calendar-day range for Build.
// Prefer From/To (YYYY-MM-DD). If unset, Days means last N calendar days including today.
type Query struct {
	Days int
	From string // inclusive YYYY-MM-DD
	To   string // inclusive YYYY-MM-DD
}

// Build aggregates stats for the last `days` calendar days (local).
// Prefers daily_* rollups for closed days + raw for today; falls back to JSONL.
func Build(logDir string, days int, openFocus map[string]any, db *store.DB) Report {
	return BuildQuery(logDir, Query{Days: days}, openFocus, db)
}

// BuildQuery aggregates stats for an explicit or relative day range.
func BuildQuery(logDir string, q Query, openFocus map[string]any, db *store.DB) Report {
	now := time.Now()
	fromDay, toDay, days := resolveRange(q, now)

	// Backfill any closed days that still have raw but no rollup.
	if db != nil {
		today := now.Format("2006-01-02")
		for _, day := range db.ListMissingRollupDays(fromDay, toDay, today) {
			_ = db.RollupDay(day, now)
		}
	}

	appMap := map[string]*AppRow{}
	childMaps := map[string]map[string]*AppRow{} // bucket → detail key → row
	var focusCount int
	var appTotal float64

	resolveClass := func(rec map[string]any) (bucket, group, exe, title string) {
		bucket, _ = rec["bucket"].(string)
		group, _ = rec["group"].(string)
		exe, _ = rec["exe"].(string)
		title, _ = rec["title"].(string)
		if bucket == "" || group == "" {
			c := focus.Classify(exe, title)
			if bucket == "" {
				bucket = c.Bucket
			}
			if group == "" {
				group = c.Group
			}
		}
		// Correct historical proxy→video mislabels when title has no matching site hint.
		if (group == "browser_video" || bucket == focus.BucketBrowserVideo) &&
			title != "" && !focus.HasVideoTitleHint(title) {
			c := focus.Classify(exe, title)
			if c.Group != "browser_video" {
				bucket, group = c.Bucket, c.Group
			}
		}
		// Correct title→AI / no-domain AI: browser_ai only valid with proxy class_source.
		src, _ := rec["class_source"].(string)
		if (group == "browser_ai" || bucket == focus.BucketBrowserAI) && title != "" &&
			!focus.ClassSourceHasAIDomain(src) {
			c := focus.Classify(exe, title)
			if c.Group != "browser_ai" {
				bucket, group = c.Bucket, c.Group
			}
		}
		if group == "" {
			group = "app"
		}
		return bucket, group, exe, title
	}

	adjustParent := func(bucket, group string, dur float64, sessionsDelta int, title string) {
		if bucket == "" || (dur == 0 && sessionsDelta == 0) {
			return
		}
		row := appMap[bucket]
		if row == nil {
			if dur <= 0 {
				return
			}
			row = &AppRow{Bucket: bucket, Group: group}
			appMap[bucket] = row
		}
		row.DurationSec += dur
		if row.DurationSec < 0 {
			row.DurationSec = 0
		}
		row.Sessions += sessionsDelta
		if row.Sessions < 0 {
			row.Sessions = 0
		}
		if title != "" && dur > 0 {
			row.LastTitle = title
		}
	}

	addChild := func(bucket, group, exe, title string, dur float64) {
		if !focus.IsAggregateBucket(group, bucket) || dur <= 0 {
			return
		}
		key := focus.DetailLabel(group, exe, title)
		if key == "" || key == "锁屏" {
			if key == "锁屏" {
				return
			}
			key = "其它"
		}
		m := childMaps[bucket]
		if m == nil {
			m = map[string]*AppRow{}
			childMaps[bucket] = m
		}
		row := m[key]
		if row == nil {
			row = &AppRow{Bucket: key, Group: group}
			m[key] = row
		}
		row.DurationSec += dur
		row.Sessions++
		if title != "" {
			row.LastTitle = title
		}
	}

	addFocus := func(rec map[string]any) {
		bucket, group, exe, title := resolveClass(rec)
		if focus.IsLockScreen(exe, title) {
			return
		}
		focusCount++
		dur, _ := asFloat(rec["duration_sec"])
		row := appMap[bucket]
		if row == nil {
			row = &AppRow{Bucket: bucket, Group: group}
			appMap[bucket] = row
		}
		row.DurationSec += dur
		row.Sessions++
		if title != "" {
			row.LastTitle = title
		}
		appTotal += dur
		addChild(bucket, group, exe, title, dur)
	}

	addDailyApp := func(r store.DailyAppRow) {
		focusCount += r.Sessions
		bucket := r.Bucket
		if bucket == "" {
			bucket = "unknown"
		}
		group := r.Group
		if group == "" {
			group = "app"
		}
		row := appMap[bucket]
		if row == nil {
			row = &AppRow{Bucket: bucket, Group: group}
			appMap[bucket] = row
		}
		row.DurationSec += r.DurationSec
		row.Sessions += r.Sessions
		if r.LastTitle != "" {
			row.LastTitle = r.LastTitle
		}
		appTotal += r.DurationSec
	}

	addDailyChild := func(r store.DailyAppDetailRow) {
		if r.DurationSec <= 0 {
			return
		}
		g := r.Group
		if g == "" {
			g = "app"
		}
		label := focus.DetailLabel(g, r.Exe, r.LastTitle)
		if label == "" || label == "锁屏" || focus.IsLockScreen(r.Exe, r.LastTitle) {
			if label == "锁屏" || focus.IsLockScreen(r.Exe, r.LastTitle) {
				return
			}
			label = "其它"
		}
		m := childMaps[r.Bucket]
		if m == nil {
			m = map[string]*AppRow{}
			childMaps[r.Bucket] = m
		}
		row := m[label]
		if row == nil {
			row = &AppRow{Bucket: label, Group: g}
			m[label] = row
		}
		row.DurationSec += r.DurationSec
		row.Sessions += r.Sessions
		if r.LastTitle != "" {
			row.LastTitle = r.LastTitle
		}
	}

	// Children for rolled-up days: re-scan raw focus (if still present) without
	// double-counting parent totals already taken from daily_app_stats.
	// Also moves duration when resolveClass corrects a stale browser_video label,
	// and subtracts lock-screen time from system totals.
	addChildFromRaw := func(rec map[string]any) {
		storedBucket, _ := rec["bucket"].(string)
		exe, _ := rec["exe"].(string)
		title, _ := rec["title"].(string)
		dur, _ := asFloat(rec["duration_sec"])
		if focus.IsLockScreen(exe, title) {
			b := storedBucket
			if b == "" {
				b = focus.BucketSystemApp
			}
			adjustParent(b, "system", -dur, -1, "")
			appTotal -= dur
			if appTotal < 0 {
				appTotal = 0
			}
			focusCount--
			if focusCount < 0 {
				focusCount = 0
			}
			return
		}
		bucket, group, exe, title := resolveClass(rec)
		if storedBucket != "" && bucket != storedBucket && dur > 0 {
			adjustParent(storedBucket, "", -dur, -1, "")
			adjustParent(bucket, group, dur, 1, title)
		}
		addChild(bucket, group, exe, title, dur)
	}

	today := now.Format("2006-01-02")
	fromT, _ := time.ParseInLocation("2006-01-02", fromDay, time.Local)
	toT, _ := time.ParseInLocation("2006-01-02", toDay, time.Local)

	// Split: closed days → daily_*; today (if in range) → raw/JSONL.
	histFrom, histTo := "", ""
	includeToday := !toT.Before(fromT) && fromDay <= today && toDay >= today
	if fromDay < today {
		histFrom = fromDay
		histTo = toDay
		if histTo >= today {
			y := now.AddDate(0, 0, -1).Format("2006-01-02")
			if y < histFrom {
				histFrom = ""
				histTo = ""
			} else {
				histTo = y
			}
		}
	}

	if db != nil && histFrom != "" {
		for _, r := range db.SumDailyAppStats(histFrom, histTo) {
			addDailyApp(r)
		}
		// Days in range that lack rollup and still have raw (or only JSONL).
		// Rolled-up days: children from raw while present, else daily_app_detail.
		for day := fromT; !day.After(toT); day = day.AddDate(0, 0, 1) {
			dk := day.Format("2006-01-02")
			if dk >= today {
				continue
			}
			if dk < histFrom || dk > histTo {
				continue
			}
			if db.HasDailyDay(dk) {
				if db.CountFocusDay(dk) > 0 {
					for _, rec := range db.FocusDayRows(dk) {
						addChildFromRaw(rec)
					}
				} else if recs := readJSONL(filepath.Join(logDir, "focus-"+dk+".jsonl")); len(recs) > 0 {
					for _, rec := range recs {
						addChildFromRaw(rec)
					}
				} else {
					for _, r := range db.SumDailyAppDetail(dk, dk) {
						addDailyChild(r)
					}
				}
				continue
			}
			if db.CountFocusDay(dk) > 0 {
				for _, rec := range db.FocusDayRows(dk) {
					addFocus(rec)
				}
			} else {
				for _, rec := range readJSONL(filepath.Join(logDir, "focus-"+dk+".jsonl")) {
					addFocus(rec)
				}
			}
		}
	} else if histFrom != "" {
		for day := fromT; !day.After(toT); day = day.AddDate(0, 0, 1) {
			dk := day.Format("2006-01-02")
			if dk >= today {
				continue
			}
			for _, rec := range readJSONL(filepath.Join(logDir, "focus-"+dk+".jsonl")) {
				addFocus(rec)
			}
		}
	}

	if includeToday {
		var recs []map[string]any
		if db != nil && db.CountFocusDay(today) > 0 {
			recs = db.FocusDayRows(today)
		} else {
			recs = readJSONL(filepath.Join(logDir, "focus-"+today+".jsonl"))
		}
		for _, rec := range recs {
			addFocus(rec)
		}
	}
	if openFocus != nil && includeToday {
		exe, _ := openFocus["exe"].(string)
		title, _ := openFocus["title"].(string)
		if !focus.IsLockScreen(exe, title) {
			addFocus(openFocus)
		}
	}

	apps := make([]AppRow, 0, len(appMap))
	for _, r := range appMap {
		if r.DurationSec <= 0 && r.Sessions <= 0 {
			continue
		}
		if appTotal > 0 {
			r.SharePct = r.DurationSec / appTotal * 100
		}
		if m := childMaps[r.Bucket]; len(m) > 0 {
			kids := make([]AppRow, 0, len(m))
			for _, c := range m {
				if c.Bucket == "锁屏" || focus.IsLockScreen("", c.Bucket) {
					continue
				}
				if r.DurationSec > 0 {
					c.SharePct = c.DurationSec / r.DurationSec * 100
				}
				applyShortName(c)
				kids = append(kids, *c)
			}
			sort.Slice(kids, func(i, j int) bool {
				if kids[i].DurationSec == kids[j].DurationSec {
					return kids[i].Bucket < kids[j].Bucket
				}
				return kids[i].DurationSec > kids[j].DurationSec
			})
			if len(kids) > 40 {
				kids = kids[:40]
			}
			// Skip trivial single self-row that only mirrors the parent label.
			if !(len(kids) == 1 && kids[0].Bucket == r.Bucket) {
				r.Children = kids
			}
		}
		apps = append(apps, *r)
	}
	sort.Slice(apps, func(i, j int) bool {
		if apps[i].DurationSec == apps[j].DurationSec {
			return apps[i].Bucket < apps[j].Bucket
		}
		return apps[i].DurationSec > apps[j].DurationSec
	})

	// --- play traffic duration ---
	// Always union SQLite segments + play/request JSONL for a day. Exclusive
	// "DB if any row else JSONL" hid morning request pulses after a mid-day empty DB.
	siteDur := map[string]float64{}
	siteLast := map[string]string{}
	var playPulses int

	if db != nil && histFrom != "" {
		for _, r := range db.SumDailyPlayStats(histFrom, histTo) {
			siteDur[r.Site] += r.DurationSec
			playPulses += r.Segments
		}
		for day := fromT; !day.After(toT); day = day.AddDate(0, 0, 1) {
			dk := day.Format("2006-01-02")
			if dk >= today || dk < histFrom || dk > histTo || db.HasDailyDay(dk) {
				continue
			}
			addPlayDay(logDir, dk, db, siteDur, siteLast, &playPulses)
		}
	} else if histFrom != "" {
		for day := fromT; !day.After(toT); day = day.AddDate(0, 0, 1) {
			dk := day.Format("2006-01-02")
			if dk >= today {
				continue
			}
			addPlayDay(logDir, dk, nil, siteDur, siteLast, &playPulses)
		}
	}

	if includeToday {
		addPlayDay(logDir, today, db, siteDur, siteLast, &playPulses)
	}

	// Metadata for author/title aggregation.
	// Prefer raw watch_meta even on rolled-up days (daily_watch_stats keeps only
	// one label/site/day and often loses authors). Fall back to daily_* / JSONL.
	var metaEntries []store.WatchLabel
	var watchMetaEvents int
	rawMetaDays := map[string]bool{}
	if db != nil {
		for day := fromT; !day.After(toT); day = day.AddDate(0, 0, 1) {
			dk := day.Format("2006-01-02")
			entries, n := db.WatchMetaEntries(dk, dk)
			if n == 0 {
				continue
			}
			metaEntries = append(metaEntries, entries...)
			watchMetaEvents += n
			rawMetaDays[dk] = true
		}
		if histFrom != "" {
			for day := fromT; !day.After(toT); day = day.AddDate(0, 0, 1) {
				dk := day.Format("2006-01-02")
				if dk >= today || rawMetaDays[dk] || !db.HasDailyDay(dk) {
					continue
				}
				entries, n := db.DailyWatchEntries(dk, dk)
				watchMetaEvents += n
				metaEntries = append(metaEntries, entries...)
			}
		}
	}
	if watchMetaEvents == 0 {
		for day := fromT; !day.After(toT); day = day.AddDate(0, 0, 1) {
			dk := day.Format("2006-01-02")
			for _, rec := range readJSONL(filepath.Join(logDir, "watch-"+dk+".jsonl")) {
				watchMetaEvents++
				site, _ := rec["site"].(string)
				if site == "" {
					continue
				}
				title, _ := rec["title"].(string)
				author, _ := rec["author"].(string)
				url, _ := rec["url"].(string)
				source, _ := rec["source"].(string)
				exempt, _ := rec["watch_exempt"].(bool)
				elabel, _ := rec["exempt_label"].(string)
				ts, _ := rec["ts"].(string)
				title = watchmeta.CleanVideoTitle(site, title)
				author = watchmeta.CleanAuthor(author)
				if author == "" {
					author = watchmeta.AuthorFromExemptLabel(elabel)
				}
				metaEntries = append(metaEntries, store.WatchLabel{
					Site: site, Title: title, Author: author, URL: url, TS: ts,
					Source: source, ExemptLabel: elabel, Exempt: exempt,
				})
			}
		}
	}
	// Focus-title timeline (same source as 浏览器-视频 children). Keep even for
	// rolled days while raw focus rows remain — needed to split long play segments.
	for day := fromT; !day.After(toT); day = day.AddDate(0, 0, 1) {
		dk := day.Format("2006-01-02")
		var recs []map[string]any
		if db != nil && db.CountFocusDay(dk) > 0 {
			recs = db.FocusDayRows(dk)
		} else {
			recs = readJSONL(filepath.Join(logDir, "focus-"+dk+".jsonl"))
		}
		for _, rec := range recs {
			group, _ := rec["group"].(string)
			title, _ := rec["title"].(string)
			if group == "" {
				exe, _ := rec["exe"].(string)
				group = focus.Classify(exe, title).Group
			}
			if group != "browser_video" {
				continue
			}
			meta := watchmeta.FromWindowTitle(title)
			if meta == nil || !meta.Useful() {
				continue
			}
			ts, _ := rec["started_at"].(string)
			if ts == "" {
				ts, _ = rec["ts"].(string)
			}
			metaEntries = append(metaEntries, store.WatchLabel{
				Site: meta.Site, Title: meta.Title, Author: meta.Author,
				URL: meta.URL, Source: "focus_title", TS: ts,
			})
		}
	}

	// Timed play spans for attributing duration to author/title (not site blob).
	// Prefer raw play_segments; fall back to daily_play_spans after prune.
	// Isolated sessions below min_watch_sec are dropped after merge (not CDN fragments).
	var playSpans []store.PlaySeg
	for day := fromT; !day.After(toT); day = day.AddDate(0, 0, 1) {
		dk := day.Format("2006-01-02")
		pulses := collectPlayPulses(logDir, dk, db)
		if len(pulses) > 0 {
			playSpans = append(playSpans, filterShortPlaySegs(pulsesToSegments(pulses))...)
			continue
		}
		if db != nil && db.HasDailyDay(dk) {
			if spans := db.ListDailyPlaySpans(dk); len(spans) > 0 {
				playSpans = append(playSpans, filterShortPlaySegs(spans)...)
			}
		}
	}

	videos, authors, byAuthor := aggregateVideosByAuthor(siteDur, siteLast, metaEntries, playSpans)
	// If timeline attribution yielded nothing useful but daily_play_stats has
	// author/title rows (rollup), use those directly.
	if len(videos) == 0 || onlyUnknownVideos(videos) {
		if db != nil && histFrom != "" {
			if rows := db.SumDailyPlayByAuthor(histFrom, histTo); len(rows) > 0 {
				videos2, authors2, byAuthor2 := videosFromDailyPlay(rows, siteDur)
				if len(videos2) > 0 && !onlyUnknownVideos(videos2) {
					videos, authors, byAuthor = videos2, authors2, byAuthor2
				} else if len(videos) == 0 && len(videos2) > 0 {
					videos, authors, byAuthor = videos2, authors2, byAuthor2
				}
			}
		}
	}
	// Drop attributed works below min_watch_sec; resync site totals (apps untouched).
	videos, authors, byAuthor, siteDur = applyMinWatchFilter(videos, authors, byAuthor, siteDur)
	var videoTotal float64
	for _, sec := range siteDur {
		videoTotal += sec
	}
	for i := range videos {
		if videoTotal > 0 {
			videos[i].SharePct = videos[i].DurationSec / videoTotal * 100
		}
	}

	return Report{
		GeneratedAt:     now.Format("2006-01-02 15:04:05"),
		Days:            days,
		FromDay:         fromDay,
		ToDay:           toDay,
		AppTotalSec:     appTotal,
		Apps:            apps,
		Videos:          videos,
		Authors:         authors,
		VideoBySite:     siteDur,
		VideoByAuthor:   byAuthor,
		VideoTotalSec:   videoTotal,
		PlayPulses:      playPulses,
		FocusSessions:   focusCount,
		WatchMetaEvents: watchMetaEvents,
	}
}

func onlyUnknownVideos(videos []VideoRow) bool {
	if len(videos) == 0 {
		return true
	}
	for _, v := range videos {
		name := strings.TrimSpace(v.Author)
		if name != "" && name != "未识别" {
			return false
		}
	}
	return true
}

func applyMinWatchFilter(
	videos []VideoRow,
	authors []AuthorRow,
	byAuthor map[string]float64,
	siteDur map[string]float64,
) ([]VideoRow, []AuthorRow, map[string]float64, map[string]float64) {
	minSec := effectiveMinWatchSec()
	if minSec <= 0 {
		return videos, authors, byAuthor, siteDur
	}
	out := make([]VideoRow, 0, len(videos))
	for _, v := range videos {
		if v.DurationSec+1e-9 < minSec {
			continue
		}
		out = append(out, v)
	}
	siteDur2 := map[string]float64{}
	byAuthor2 := map[string]float64{}
	for _, v := range out {
		if v.Site != "" {
			siteDur2[v.Site] += v.DurationSec
		}
		byAuthor2[v.Author] += v.DurationSec
	}
	// If attribution produced no rows, still strip short site-only totals.
	if len(out) == 0 && len(siteDur) > 0 {
		for site, sec := range siteDur {
			if sec+1e-9 >= minSec {
				siteDur2[site] = sec
			}
		}
	}
	authors2 := make([]AuthorRow, 0, len(out))
	for _, v := range out {
		authors2 = append(authors2, AuthorRow{
			Site: v.Site, Author: v.Author, DurationSec: v.DurationSec, Title: v.Title,
		})
	}
	return out, authors2, byAuthor2, siteDur2
}

func videosFromDailyPlay(rows []store.DailyPlayRow, siteDur map[string]float64) ([]VideoRow, []AuthorRow, map[string]float64) {
	byKey := map[string]*VideoRow{}
	byAuthor := map[string]float64{}
	minSec := effectiveMinWatchSec()
	for _, r := range rows {
		if r.DurationSec <= 0.05 {
			continue
		}
		if minSec > 0 && r.DurationSec+1e-9 < minSec {
			continue
		}
		name := watchmeta.CleanAuthor(r.Author)
		title := watchmeta.CleanVideoTitle(r.Site, r.Title)
		if name == "" {
			name = strings.TrimSpace(title)
		}
		if name == "" {
			name = "未识别"
		}
		key := r.Site + "\x00" + authorKey(name) + "\x00" + authorKey(title)
		row := byKey[key]
		if row == nil {
			row = &VideoRow{
				Author: name, Site: r.Site, Title: title, URL: r.URL,
				Exempt: r.Exempt, LastSeen: r.LastSeen,
			}
			byKey[key] = row
		}
		row.DurationSec += r.DurationSec
		if r.LastSeen > row.LastSeen {
			row.LastSeen = r.LastSeen
		}
		if r.Exempt {
			row.Exempt = true
		}
		if r.URL != "" {
			row.URL = r.URL
		}
		byAuthor[name] += r.DurationSec
	}
	videos := make([]VideoRow, 0, len(byKey))
	for _, r := range byKey {
		videos = append(videos, *r)
	}
	sort.Slice(videos, func(i, j int) bool {
		if videos[i].DurationSec == videos[j].DurationSec {
			return videos[i].Author < videos[j].Author
		}
		return videos[i].DurationSec > videos[j].DurationSec
	})
	if len(videos) > 50 {
		videos = videos[:50]
	}
	authors := make([]AuthorRow, 0, len(videos))
	for _, v := range videos {
		authors = append(authors, AuthorRow{
			Site: v.Site, Author: v.Author, DurationSec: v.DurationSec, Title: v.Title,
		})
	}
	_ = siteDur
	return videos, authors, byAuthor
}

func aggregateVideosByAuthor(
	siteDur map[string]float64,
	siteLast map[string]string,
	metas []store.WatchLabel,
	spans []store.PlaySeg,
) ([]VideoRow, []AuthorRow, map[string]float64) {
	type agg struct {
		author   string
		site     string
		siteSecs map[string]float64
		title    string
		url      string
		lastSeen string
		exempt   bool
		dur      float64
		titleSc  int
	}
	byKey := map[string]*agg{}

	// title → best author (from API meta) to enrich focus_title rows.
	titleAuthor := map[string]string{}
	for _, m := range metas {
		title := watchmeta.CleanVideoTitle(m.Site, m.Title)
		author := watchmeta.CleanAuthor(m.Author)
		if author == "" {
			author = watchmeta.AuthorFromExemptLabel(m.ExemptLabel)
		}
		if title == "" || author == "" {
			continue
		}
		tk := authorKey(title)
		titleAuthor[tk] = preferAuthorName(titleAuthor[tk], author)
	}

	type siteKey struct{ site, key string }
	weight := map[siteKey]int{}
	display := map[string]string{} // agg key → display name (author or work title)

	ensure := func(key, author, site string) *agg {
		a := byKey[key]
		if a == nil {
			a = &agg{author: author, site: site, siteSecs: map[string]float64{}}
			byKey[key] = a
		}
		return a
	}

	touchMeta := func(m store.WatchLabel) (key string, ok bool) {
		author := watchmeta.CleanAuthor(m.Author)
		if author == "" {
			author = watchmeta.AuthorFromExemptLabel(m.ExemptLabel)
		}
		title := watchmeta.CleanVideoTitle(m.Site, m.Title)
		if author == "" && title != "" {
			if a := titleAuthor[authorKey(title)]; a != "" {
				author = a
			}
		}
		if author == "" && title == "" && m.URL == "" && !m.Exempt {
			return "", false
		}
		key, name := videoAggKey(author, title, m.Site)
		if key == "" {
			return "", false
		}
		display[key] = preferAuthorName(display[key], name)
		a := ensure(key, name, m.Site)
		if author != "" {
			a.author = preferAuthorName(a.author, author)
		} else if a.author == "" {
			a.author = name
		}
		if m.Site != "" {
			if a.site == "" {
				a.site = m.Site
			}
			weight[siteKey{m.Site, key}]++
		}
		if m.Exempt {
			a.exempt = true
		}
		if m.TS >= a.lastSeen {
			a.lastSeen = m.TS
		}
		sc := metaTitleScore(title, m.Source)
		if sc > a.titleSc || (sc == a.titleSc && m.TS >= a.lastSeen && title != "") {
			if title != "" {
				a.title = title
				a.titleSc = sc
			}
			if m.URL != "" {
				a.url = m.URL
			}
		} else if a.url == "" && m.URL != "" {
			a.url = m.URL
		}
		return key, true
	}
	for _, m := range metas {
		_, _ = touchMeta(m)
	}

	credit := func(key, site string, sec float64, at time.Time) {
		if key == "" || sec <= 0 {
			return
		}
		a := ensure(key, display[key], site)
		if a.author == "" {
			a.author = display[key]
		}
		if site != "" && a.site == "" {
			a.site = site
		}
		a.dur += sec
		if site != "" {
			a.siteSecs[site] += sec
		}
		if ts := at.Format(time.RFC3339); ts > a.lastSeen {
			a.lastSeen = ts
		}
	}

	unknownKey := func(site string) string {
		if site == "" {
			return "u:"
		}
		return "u:" + site
	}

	// Prefer timed play spans: split each segment across the label timeline.
	credited := map[string]float64{} // site → seconds attributed via spans
	if len(spans) > 0 {
		eventsBySite := buildSiteLabelEvents(metas, titleAuthor)
		for _, seg := range spans {
			if seg.Site == "" || seg.DurationSec <= 0 {
				continue
			}
			start, end := seg.Start, seg.End
			if start.IsZero() {
				continue
			}
			if end.IsZero() || end.Before(start) {
				end = start.Add(time.Duration(seg.DurationSec * float64(time.Second)))
			}
			parts := splitSegByLabels(seg.Site, start, end, seg.DurationSec, eventsBySite[seg.Site])
			for _, p := range parts {
				key := p.key
				if key == "" {
					key = unknownKey(seg.Site)
					display[key] = "未识别"
				}
				credit(key, seg.Site, p.sec, p.at)
				credited[seg.Site] += p.sec
			}
		}
	}

	// Align per-site totals with siteDur (rollup / pulse merge source of truth).
	for site, dur := range siteDur {
		got := credited[site]
		switch {
		case dur <= 0:
			continue
		case got <= 0:
			// No timed spans: weight-split across author/title keys on this site.
			var keys []string
			var totalW int
			for sa, w := range weight {
				if sa.site == site && w > 0 && !strings.HasPrefix(sa.key, "u:") {
					keys = append(keys, sa.key)
					totalW += w
				}
			}
			if totalW == 0 || len(keys) == 0 {
				key := unknownKey(site)
				display[key] = "未识别"
				credit(key, site, dur, time.Time{})
				if ts := siteLast[site]; ts != "" {
					if a := byKey[key]; a != nil && ts > a.lastSeen {
						a.lastSeen = ts
					}
				}
				continue
			}
			for _, key := range keys {
				w := weight[siteKey{site, key}]
				share := dur * float64(w) / float64(totalW)
				credit(key, site, share, time.Time{})
				if ts := siteLast[site]; ts != "" {
					if a := byKey[key]; a != nil && ts > a.lastSeen {
						a.lastSeen = ts
					}
				}
			}
		case got+0.5 < dur:
			// Residual play time with no label coverage → 未识别 only.
			key := unknownKey(site)
			display[key] = "未识别"
			credit(key, site, dur-got, time.Time{})
		case got > dur+0.5:
			// Scale down this site's rows so shares match siteDur.
			scale := dur / got
			for _, a := range byKey {
				if sec := a.siteSecs[site]; sec > 0 {
					delta := sec - sec*scale
					a.siteSecs[site] = sec * scale
					a.dur -= delta
				}
			}
		}
	}

	videos := make([]VideoRow, 0, len(byKey))
	byAuthor := map[string]float64{}
	for key, a := range byKey {
		if a.dur <= 0.05 {
			continue
		}
		site := a.site
		best := 0.0
		for s, sec := range a.siteSecs {
			if sec > best {
				best = sec
				site = s
			}
		}
		name := watchmeta.CleanAuthor(a.author)
		if disp := watchmeta.CleanAuthor(display[key]); disp != "" {
			name = preferAuthorName(name, disp)
		}
		if name == "" {
			name = "未识别"
		}
		row := VideoRow{
			Author: name, Site: site, Title: a.title, URL: a.url,
			DurationSec: a.dur, Exempt: a.exempt, LastSeen: a.lastSeen,
		}
		videos = append(videos, row)
		byAuthor[name] += a.dur
	}
	sort.Slice(videos, func(i, j int) bool {
		if videos[i].DurationSec == videos[j].DurationSec {
			return videos[i].Author < videos[j].Author
		}
		return videos[i].DurationSec > videos[j].DurationSec
	})
	if len(videos) > 50 {
		videos = videos[:50]
	}

	authors := make([]AuthorRow, 0, len(videos))
	for _, v := range videos {
		authors = append(authors, AuthorRow{
			Site: v.Site, Author: v.Author, DurationSec: v.DurationSec, Title: v.Title,
		})
	}
	return videos, authors, byAuthor
}

// videoAggKey returns aggregation key + display name: author if known, else work title.
func videoAggKey(author, title, site string) (key, display string) {
	if k := authorKey(author); k != "" {
		return "a:" + k, author
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return "", ""
	}
	tk := authorKey(title)
	if tk == "" {
		tk = strings.ToLower(title)
	}
	if site == "" {
		return "t:" + tk, title
	}
	return "t:" + site + ":" + tk, title
}

type labelEvent struct {
	at           time.Time
	key          string
	author       string
	title        string
	url          string
	source       string
	exempt       bool
	exemptLabel  string
}

func buildSiteLabelEvents(metas []store.WatchLabel, titleAuthor map[string]string) map[string][]labelEvent {
	out := map[string][]labelEvent{}
	for _, m := range metas {
		if m.Site == "" {
			continue
		}
		author := watchmeta.CleanAuthor(m.Author)
		if author == "" {
			author = watchmeta.AuthorFromExemptLabel(m.ExemptLabel)
		}
		title := watchmeta.CleanVideoTitle(m.Site, m.Title)
		if author == "" && title != "" {
			if a := titleAuthor[authorKey(title)]; a != "" {
				author = a
			}
		}
		key, _ := videoAggKey(author, title, m.Site)
		if key == "" {
			continue
		}
		at := parseStatsTS(m.TS)
		out[m.Site] = append(out[m.Site], labelEvent{
			at: at, key: key, author: author, title: title, url: m.URL,
			source: m.Source, exempt: m.Exempt, exemptLabel: m.ExemptLabel,
		})
	}
	for site := range out {
		ev := out[site]
		sort.SliceStable(ev, func(i, j int) bool {
			if ev[i].at.Equal(ev[j].at) {
				return metaTitleScore(ev[i].title, ev[i].source) < metaTitleScore(ev[j].title, ev[j].source)
			}
			return ev[i].at.Before(ev[j].at)
		})
		out[site] = ev
	}
	return out
}

type segPart struct {
	key string
	sec float64
	at  time.Time
}

// splitSegByLabels attributes a play segment to the nearest preceding label on that site.
func splitSegByLabels(site string, start, end time.Time, totalSec float64, events []labelEvent) []segPart {
	if totalSec <= 0 || !end.After(start) {
		if totalSec <= 0 {
			return nil
		}
		key := activeLabelKey(events, start)
		return []segPart{{key: key, sec: totalSec, at: start}}
	}
	// Build ordered cut points: segment start/end + label timestamps inside (start, end].
	cuts := []time.Time{start}
	for _, ev := range events {
		if ev.at.IsZero() {
			continue
		}
		if ev.at.After(start) && !ev.at.After(end) {
			cuts = append(cuts, ev.at)
		}
	}
	cuts = append(cuts, end)
	// Dedupe cuts.
	uniq := cuts[:0]
	var prev time.Time
	for i, c := range cuts {
		if i > 0 && (c.Equal(prev) || c.Before(prev)) {
			continue
		}
		uniq = append(uniq, c)
		prev = c
	}
	cuts = uniq
	if len(cuts) < 2 {
		key := activeLabelKey(events, start)
		return []segPart{{key: key, sec: totalSec, at: start}}
	}

	wall := end.Sub(start).Seconds()
	if wall <= 0 {
		key := activeLabelKey(events, start)
		return []segPart{{key: key, sec: totalSec, at: start}}
	}

	var parts []segPart
	var assigned float64
	for i := 0; i < len(cuts)-1; i++ {
		a, b := cuts[i], cuts[i+1]
		if !b.After(a) {
			continue
		}
		frac := b.Sub(a).Seconds() / wall
		sec := totalSec * frac
		key := activeLabelKey(events, a)
		parts = append(parts, segPart{key: key, sec: sec, at: a})
		assigned += sec
	}
	if len(parts) == 0 {
		return []segPart{{key: activeLabelKey(events, start), sec: totalSec, at: start}}
	}
	// Fix float drift on last part.
	if drift := totalSec - assigned; drift != 0 {
		parts[len(parts)-1].sec += drift
	}
	_ = site
	return parts
}

func activeLabelKey(events []labelEvent, at time.Time) string {
	var best labelEvent
	var found bool
	var untimed labelEvent
	var hasUntimed bool
	for _, ev := range events {
		if ev.at.IsZero() {
			if !hasUntimed || labelRicher(ev, untimed) {
				untimed = ev
				hasUntimed = true
			}
			continue
		}
		if ev.at.After(at) {
			break
		}
		if !found || ev.at.After(best.at) || (ev.at.Equal(best.at) && labelRicher(ev, best)) {
			best = ev
			found = true
		}
	}
	if found {
		return best.key
	}
	// No prior label: nearest upcoming event (play often starts seconds before meta).
	for _, ev := range events {
		if ev.at.IsZero() || ev.at.Before(at) {
			continue
		}
		return ev.key
	}
	if hasUntimed {
		return untimed.key
	}
	return ""
}

func labelRicher(a, b labelEvent) bool {
	as := metaTitleScore(a.title, a.source) + boolScore(a.author != "")*3
	bs := metaTitleScore(b.title, b.source) + boolScore(b.author != "")*3
	return as > bs
}

func boolScore(ok bool) int {
	if ok {
		return 1
	}
	return 0
}

func parseStatsTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

func authorKey(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "@")
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "　", "")
	return s
}

func preferAuthorName(cur, cand string) string {
	cand = strings.TrimSpace(cand)
	if cand == "" {
		return cur
	}
	if cur == "" {
		return cand
	}
	// Prefer plain display name over @handle.
	cAt := strings.HasPrefix(cur, "@")
	nAt := strings.HasPrefix(cand, "@")
	if cAt && !nAt {
		return cand
	}
	if !cAt && nAt {
		return cur
	}
	return cur
}

func metaTitleScore(title, source string) int {
	if title == "" || watchmeta.IsJunkTitle(title) {
		return 0
	}
	s := 2
	if source != "" && source != "focus_title" {
		s += 5
	}
	return s
}

func resolveRange(q Query, now time.Time) (fromDay, toDay string, days int) {
	today := now.Format("2006-01-02")
	if q.From != "" || q.To != "" {
		fromDay = q.From
		toDay = q.To
		if toDay == "" {
			toDay = today
		}
		if fromDay == "" {
			fromDay = toDay
		}
		fromT, err1 := time.ParseInLocation("2006-01-02", fromDay, time.Local)
		toT, err2 := time.ParseInLocation("2006-01-02", toDay, time.Local)
		if err1 != nil || err2 != nil {
			return resolveRange(Query{Days: q.Days}, now)
		}
		if toT.Before(fromT) {
			fromT, toT = toT, fromT
			fromDay, toDay = fromT.Format("2006-01-02"), toT.Format("2006-01-02")
		}
		days = int(toT.Sub(fromT).Hours()/24) + 1
		if days > maxRangeDays {
			fromT = toT.AddDate(0, 0, -(maxRangeDays - 1))
			fromDay = fromT.Format("2006-01-02")
			days = maxRangeDays
		}
		return fromDay, toDay, days
	}
	days = q.Days
	if days <= 0 {
		days = 1
	}
	if days > maxRangeDays {
		days = maxRangeDays
	}
	toDay = today
	fromDay = now.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	return fromDay, toDay, days
}

func addPlayJSONL(logDir, day string, siteDur map[string]float64, siteLast map[string]string, playPulses *int) {
	addPlayDay(logDir, day, nil, siteDur, siteLast, playPulses)
}

// addPlayDay unions SQLite segment pulses with play/request JSONL for one day.
func addPlayDay(logDir, day string, db *store.DB, siteDur map[string]float64, siteLast map[string]string, playPulses *int) {
	pulses := collectPlayPulses(logDir, day, db)
	*playPulses += len(pulses)
	for site, sec := range mergePlayDuration(pulses) {
		siteDur[site] += sec
	}
	for _, p := range pulses {
		if p.Site == "" {
			continue
		}
		ts := p.At.Format(time.RFC3339)
		if ts > siteLast[p.Site] {
			siteLast[p.Site] = ts
		}
	}
}

func collectPlayPulses(logDir, day string, db *store.DB) []playPulse {
	pulses := loadPlayPulses(filepath.Join(logDir, "play-"+day+".jsonl"))
	pulses = unionPlayPulses(pulses, loadPlayFromRequests(filepath.Join(logDir, "request-"+day+".jsonl")))
	if db != nil {
		pulses = unionPlayPulses(pulses, pulsesFromDBSegments(db.ListPlaySegments(day)))
	}
	return pulses
}

func pulsesFromDBSegments(segs []store.PlaySeg) []playPulse {
	var out []playPulse
	for _, seg := range segs {
		if seg.Site == "" || seg.Start.IsZero() {
			continue
		}
		end := seg.End
		if end.IsZero() || end.Before(seg.Start) {
			end = seg.Start
		}
		for t := seg.Start; !t.After(end); t = t.Add(5 * time.Second) {
			out = append(out, playPulse{Site: seg.Site, At: t})
		}
		if end.After(seg.Start) {
			out = append(out, playPulse{Site: seg.Site, At: end})
		}
	}
	return out
}

// BackfillPlayDB rebuilds play_segments for recent days from the union of
// existing DB segments + play/request JSONL (idempotent).
func BackfillPlayDB(db *store.DB, logDir string, days int) int {
	if db == nil || logDir == "" {
		return 0
	}
	if days <= 0 {
		days = 1
	}
	if days > maxRangeDays {
		days = maxRangeDays
	}
	now := time.Now()
	n := 0
	for i := 0; i < days; i++ {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		pulses := collectPlayPulses(logDir, day, db)
		if len(pulses) == 0 {
			continue
		}
		segs := filterShortPlaySegs(pulsesToSegments(pulses))
		if err := db.ReplacePlayDay(day, segs); err != nil {
			continue
		}
		n++
	}
	return n
}

func pulsesToSegments(pulses []playPulse) []store.PlaySeg {
	if len(pulses) == 0 {
		return nil
	}
	sorted := append([]playPulse(nil), pulses...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	type curSeg struct {
		site        string
		start, last time.Time
	}
	var cur *curSeg
	var out []store.PlaySeg
	flush := func() {
		if cur == nil {
			return
		}
		dur := cur.last.Sub(cur.start) + playPulsePad
		if dur < playPulsePad {
			dur = playPulsePad
		}
		out = append(out, store.PlaySeg{
			Site: cur.site, Start: cur.start, End: cur.last, DurationSec: dur.Seconds(),
		})
		cur = nil
	}
	for _, p := range sorted {
		if p.Site == "" || p.At.IsZero() {
			continue
		}
		if cur == nil || cur.site != p.Site || p.At.Sub(cur.last) > playIdleGap {
			flush()
			cur = &curSeg{site: p.Site, start: p.At, last: p.At}
			continue
		}
		cur.last = p.At
	}
	flush()
	return out
}

// filterShortPlaySegs drops isolated merged sessions below min_watch_sec.
// Call only after idle-gap merge so mid-session CDN shards are not discarded.
func filterShortPlaySegs(segs []store.PlaySeg) []store.PlaySeg {
	minSec := effectiveMinWatchSec()
	if minSec <= 0 || len(segs) == 0 {
		return segs
	}
	out := make([]store.PlaySeg, 0, len(segs))
	for _, s := range segs {
		if s.DurationSec+1e-9 < minSec {
			continue
		}
		out = append(out, s)
	}
	return out
}

type playPulse struct {
	Site string
	At   time.Time
}

func loadPlayPulses(path string) []playPulse {
	var out []playPulse
	for _, rec := range readJSONL(path) {
		site, _ := rec["site"].(string)
		ts, _ := rec["ts"].(string)
		at, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			at, err = time.Parse(time.RFC3339, ts)
		}
		if err != nil || site == "" {
			continue
		}
		out = append(out, playPulse{Site: site, At: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func loadPlayFromRequests(path string) []playPulse {
	var out []playPulse
	for _, rec := range readJSONL(path) {
		cat, _ := rec["category"].(string)
		if cat != "play" {
			continue
		}
		site, _ := rec["site"].(string)
		ts, _ := rec["ts"].(string)
		at, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			at, err = time.Parse(time.RFC3339, ts)
		}
		if err != nil || site == "" {
			continue
		}
		out = append(out, playPulse{Site: site, At: at})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// unionPlayPulses merges pulse sources with 5s site buckets (matches audit throttle).
func unionPlayPulses(a, b []playPulse) []playPulse {
	type key struct {
		site string
		slot int64
	}
	seen := map[key]struct{}{}
	out := make([]playPulse, 0, len(a)+len(b))
	add := func(p playPulse) {
		if p.Site == "" || p.At.IsZero() {
			return
		}
		k := key{site: p.Site, slot: p.At.Unix() / 5}
		if _, ok := seen[k]; ok {
			return
		}
		seen[k] = struct{}{}
		out = append(out, p)
	}
	for _, p := range a {
		add(p)
	}
	for _, p := range b {
		add(p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// mergePlayDuration aggregates play pulses into per-site watch seconds.
// Pulses are processed on one timeline: a site change ends the previous segment.
// That prevents (1) attributing one span to the wrong site and (2) double-counting
// overlapping CDN noise from multiple sites as full wall-clock for each.
// Isolated merged sessions shorter than min_watch_sec are discarded.
func mergePlayDuration(pulses []playPulse) map[string]float64 {
	out := map[string]float64{}
	for _, seg := range filterShortPlaySegs(pulsesToSegments(pulses)) {
		out[seg.Site] += seg.DurationSec
	}
	return out
}

func readJSONL(path string) []map[string]any {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	out := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
