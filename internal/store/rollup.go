package store

import (
	"database/sql"
	"path/filepath"
	"strings"
	"time"

	"timesavior/internal/watchmeta"
)

// DailyAppRow is one bucket's rolled-up focus totals for a day.
type DailyAppRow struct {
	Day         string
	Bucket      string
	Group       string
	DurationSec float64
	Sessions    int
	LastTitle   string
}

// DailyAppDetailRow is one child under an aggregate bucket for a day.
// ChildKey is a storage key; Exe+LastTitle are used by stats to form the UI label.
type DailyAppDetailRow struct {
	Day         string
	Bucket      string
	ChildKey    string
	Group       string
	Exe         string
	DurationSec float64
	Sessions    int
	LastTitle   string
}

// DailyPlayRow is play duration for a day at site + author/title grain.
type DailyPlayRow struct {
	Day         string
	Site        string
	Author      string
	Title       string
	URL         string
	DurationSec float64
	Segments    int
	Exempt      bool
	LastSeen    string
}

// RollupClosedDays aggregates raw facts for all closed calendar days (before today).
// Idempotent: re-upserts from raw while raw rows still exist.
func (d *DB) RollupClosedDays(now time.Time) {
	if d == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	today := dayKey(now)
	d.mu.Lock()
	defer d.mu.Unlock()

	minDay := d.minRawDayLocked()
	if minDay == "" {
		return
	}
	start, ok := parseDay(minDay)
	if !ok {
		return
	}
	end, ok := parseDay(today)
	if !ok {
		return
	}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		_ = d.rollupDayLocked(dayKey(day), now)
	}
}

// RollupDay aggregates one closed calendar day from raw into daily_* tables.
func (d *DB) RollupDay(day string, now time.Time) error {
	if d == nil || day == "" {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	if day >= dayKey(now) {
		return nil // never finalize today from partial raw
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rollupDayLocked(day, now)
}

// maybeRollupYesterdayLocked rolls up yesterday once if not yet marked done.
// Caller must hold d.mu. Maintenance/Prune re-aggregates while raw still exists.
func (d *DB) maybeRollupYesterdayLocked(now time.Time) {
	y := dayKey(now.AddDate(0, 0, -1))
	var done int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM daily_rollup_done WHERE day = ?`, y).Scan(&done)
	if done > 0 {
		return
	}
	_ = d.rollupDayLocked(y, now)
}

func (d *DB) rollupDayLocked(day string, now time.Time) error {
	if day == "" || day >= dayKey(now) {
		return nil
	}
	var focusN, playN, watchN int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM focus_sessions WHERE day = ?`, day).Scan(&focusN)
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM play_segments WHERE day = ?`, day).Scan(&playN)
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM watch_meta WHERE substr(last_seen, 1, 10) = ?`, day).Scan(&watchN)
	var done int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM daily_rollup_done WHERE day = ?`, day).Scan(&done)
	if focusN == 0 && playN == 0 && watchN == 0 {
		if done == 0 {
			_, _ = d.sql.Exec(
				`INSERT OR REPLACE INTO daily_rollup_done(day, rolled_at) VALUES (?, ?)`,
				day, formatTS(now),
			)
		}
		return nil
	}

	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := d.rollupAppLocked(tx, day); err != nil {
		return err
	}
	if err := d.rollupPlayLocked(tx, day); err != nil {
		return err
	}
	if err := d.rollupWatchLocked(tx, day); err != nil {
		return err
	}

	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO daily_rollup_done(day, rolled_at) VALUES (?, ?)`,
		day, formatTS(now),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func isLockScreenRollup(exe, title string) bool {
	exe = strings.ToLower(filepath.Base(strings.TrimSpace(exe)))
	if exe == "lockapp.exe" || exe == "lockapp" {
		return true
	}
	t := strings.ToLower(title)
	return strings.Contains(t, "锁屏") || strings.Contains(t, "lock screen")
}

func isAggregateGroup(group, bucket string) bool {
	if group == "system" || strings.HasPrefix(group, "browser_") {
		return true
	}
	switch bucket {
	case "系统应用", "浏览器-视频", "浏览器-AI", "浏览器-资讯":
		return true
	}
	return false
}

func (d *DB) rollupAppLocked(tx *sql.Tx, day string) error {
	if _, err := tx.Exec(`DELETE FROM daily_app_stats WHERE day = ?`, day); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM daily_app_detail WHERE day = ?`, day); err != nil {
		return err
	}
	rows, err := tx.Query(`
		SELECT bucket, group_name, exe, title, duration_sec
		FROM focus_sessions WHERE day = ?`, day)
	if err != nil {
		return err
	}
	defer rows.Close()

	type agg struct {
		group, title string
		dur          float64
		n            int
	}
	type childAgg struct {
		group, exe, title string
		dur               float64
		n                 int
	}
	parents := map[string]*agg{}
	children := map[string]map[string]*childAgg{} // bucket → child_key

	for rows.Next() {
		var bucket, group, exe, title string
		var dur float64
		if err := rows.Scan(&bucket, &group, &exe, &title, &dur); err != nil {
			continue
		}
		if isLockScreenRollup(exe, title) {
			continue
		}
		if bucket == "" {
			bucket = "unknown"
		}
		if group == "" {
			group = "app"
		}
		p := parents[bucket]
		if p == nil {
			p = &agg{group: group}
			parents[bucket] = p
		}
		p.dur += dur
		p.n++
		if title != "" {
			p.title = title
		}
		if !isAggregateGroup(group, bucket) || dur <= 0 {
			continue
		}
		key := strings.ToLower(filepath.Base(strings.TrimSpace(exe))) + "\x00" + strings.TrimSpace(title)
		m := children[bucket]
		if m == nil {
			m = map[string]*childAgg{}
			children[bucket] = m
		}
		c := m[key]
		if c == nil {
			c = &childAgg{group: group, exe: exe}
			m[key] = c
		}
		c.dur += dur
		c.n++
		if title != "" {
			c.title = title
		}
		if exe != "" {
			c.exe = exe
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for bucket, p := range parents {
		if p.dur <= 0 && p.n <= 0 {
			continue
		}
		if _, err := tx.Exec(`
			INSERT INTO daily_app_stats(day, bucket, group_name, duration_sec, sessions, last_title)
			VALUES (?,?,?,?,?,?)`,
			day, bucket, p.group, p.dur, p.n, p.title); err != nil {
			return err
		}
	}
	for bucket, m := range children {
		for key, c := range m {
			if c.dur <= 0 {
				continue
			}
			if _, err := tx.Exec(`
				INSERT INTO daily_app_detail(day, bucket, child_key, group_name, exe, duration_sec, sessions, last_title)
				VALUES (?,?,?,?,?,?,?,?)`,
				day, bucket, key, c.group, c.exe, c.dur, c.n, c.title); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *DB) rollupPlayLocked(tx *sql.Tx, day string) error {
	if _, err := tx.Exec(`DELETE FROM daily_play_stats WHERE day = ?`, day); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM daily_play_spans WHERE day = ?`, day); err != nil {
		return err
	}

	segRows, err := tx.Query(`
		SELECT site, start_at, end_at, duration_sec FROM play_segments
		WHERE day = ? AND site != '' ORDER BY start_at`, day)
	if err != nil {
		return err
	}
	type span struct {
		site       string
		start, end string
		dur        float64
	}
	var spans []span
	siteDur := map[string]float64{}
	siteSegN := map[string]int{}
	minWatch := d.minWatchSec
	for segRows.Next() {
		var s span
		if err := segRows.Scan(&s.site, &s.start, &s.end, &s.dur); err != nil {
			continue
		}
		if minWatch > 0 && s.dur+1e-9 < minWatch {
			continue
		}
		spans = append(spans, s)
		siteDur[s.site] += s.dur
		siteSegN[s.site]++
	}
	segRows.Close()
	if err := segRows.Err(); err != nil {
		return err
	}
	for _, s := range spans {
		if _, err := tx.Exec(`
			INSERT INTO daily_play_spans(day, site, start_at, end_at, duration_sec)
			VALUES (?,?,?,?,?)`, day, s.site, s.start, s.end, s.dur); err != nil {
			return err
		}
	}

	labels := d.collectRollupWatchLabels(tx, day)
	bySite := map[string][]WatchLabel{}
	for _, l := range labels {
		if l.Site == "" {
			continue
		}
		bySite[l.Site] = append(bySite[l.Site], l)
	}

	type playKey struct{ site, author, title string }
	type playAgg struct {
		url, last string
		dur       float64
		exempt    bool
		segN      int
	}
	out := map[playKey]*playAgg{}

	credit := func(site, author, title, url, last string, dur float64, exempt bool, segN int) {
		if site == "" || dur <= 0 {
			return
		}
		author = watchmeta.CleanAuthor(author)
		title = watchmeta.CleanVideoTitle(site, title)
		if author == "" && title == "" {
			author = "未识别"
		}
		k := playKey{site: site, author: author, title: title}
		a := out[k]
		if a == nil {
			a = &playAgg{}
			out[k] = a
		}
		a.dur += dur
		if segN > a.segN {
			a.segN = segN
		}
		if exempt {
			a.exempt = true
		}
		if url != "" {
			a.url = url
		}
		if last >= a.last {
			a.last = last
		}
	}

	for site, dur := range siteDur {
		labs := bySite[site]
		if len(labs) == 0 {
			credit(site, "未识别", "", "", "", dur, false, siteSegN[site])
			continue
		}
		type wLab struct {
			l WatchLabel
			w int
		}
		var weighted []wLab
		totalW := 0
		for _, l := range labs {
			w := 1
			if l.Author != "" {
				w += 3
			}
			if l.Title != "" {
				w += 1
			}
			if l.Source != "" && l.Source != "focus_title" {
				w += 2
			}
			weighted = append(weighted, wLab{l: l, w: w})
			totalW += w
		}
		if totalW <= 0 {
			credit(site, "未识别", "", "", "", dur, false, siteSegN[site])
			continue
		}
		var assigned float64
		for i, wl := range weighted {
			share := dur * float64(wl.w) / float64(totalW)
			if i == len(weighted)-1 {
				share = dur - assigned
			}
			assigned += share
			name := wl.l.Author
			if name == "" {
				name = wl.l.Title
			}
			if name == "" {
				name = "未识别"
			}
			credit(site, name, wl.l.Title, wl.l.URL, wl.l.TS, share, wl.l.Exempt, siteSegN[site])
		}
	}

	for k, a := range out {
		if minWatch > 0 && a.dur+1e-9 < minWatch {
			continue
		}
		ex := 0
		if a.exempt {
			ex = 1
		}
		if _, err := tx.Exec(`
			INSERT INTO daily_play_stats(day, site, author, title, url, duration_sec, segments, watch_exempt, last_seen)
			VALUES (?,?,?,?,?,?,?,?,?)`,
			day, k.site, k.author, k.title, a.url, a.dur, a.segN, ex, a.last); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) collectRollupWatchLabels(tx *sql.Tx, day string) []WatchLabel {
	var out []WatchLabel
	rows, err := tx.Query(`
		SELECT site, title, author, url, source, watch_exempt, exempt_label, last_seen
		FROM watch_meta
		WHERE substr(last_seen, 1, 10) = ? AND site != ''
		ORDER BY last_seen ASC`, day)
	if err == nil {
		for rows.Next() {
			var site, title, author, url, source, elabel, ts string
			var ex int
			if err := rows.Scan(&site, &title, &author, &url, &source, &ex, &elabel, &ts); err != nil {
				continue
			}
			title = watchmeta.CleanVideoTitle(site, title)
			author = watchmeta.CleanAuthor(author)
			if author == "" {
				author = watchmeta.AuthorFromExemptLabel(elabel)
			}
			out = append(out, WatchLabel{
				Site: site, Title: title, Author: author, URL: url, TS: ts,
				Source: source, ExemptLabel: elabel, Exempt: ex != 0,
			})
		}
		rows.Close()
	}
	frows, err := tx.Query(`
		SELECT group_name, bucket, title, started_at FROM focus_sessions WHERE day = ?`, day)
	if err != nil {
		return out
	}
	defer frows.Close()
	for frows.Next() {
		var group, bucket, title, started string
		if err := frows.Scan(&group, &bucket, &title, &started); err != nil {
			continue
		}
		if group != "browser_video" && bucket != "浏览器-视频" {
			continue
		}
		meta := watchmeta.FromWindowTitle(title)
		if meta == nil || !meta.Useful() {
			continue
		}
		out = append(out, WatchLabel{
			Site: meta.Site, Title: meta.Title, Author: meta.Author,
			URL: meta.URL, Source: "focus_title", TS: started,
		})
	}
	return out
}

func (d *DB) rollupWatchLocked(tx *sql.Tx, day string) error {
	if _, err := tx.Exec(`DELETE FROM daily_watch_stats WHERE day = ?`, day); err != nil {
		return err
	}
	labels := d.collectRollupWatchLabels(tx, day)
	seen := map[string]bool{}
	for _, l := range labels {
		if l.Site == "" {
			continue
		}
		key := l.Site + "\x00" + l.Author + "\x00" + l.Title + "\x00" + l.TS
		if seen[key] {
			continue
		}
		seen[key] = true
		ex := 0
		if l.Exempt {
			ex = 1
		}
		if _, err := tx.Exec(`
			INSERT INTO daily_watch_stats(day, site, title, author, url, source, watch_exempt, last_seen)
			VALUES (?,?,?,?,?,?,?,?)`,
			day, l.Site, l.Title, l.Author, l.URL, l.Source, ex, l.TS); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) minRawDayLocked() string {
	var a, b, c string
	_ = d.sql.QueryRow(`SELECT MIN(day) FROM focus_sessions`).Scan(&a)
	_ = d.sql.QueryRow(`SELECT MIN(day) FROM play_segments`).Scan(&b)
	_ = d.sql.QueryRow(`SELECT MIN(substr(last_seen, 1, 10)) FROM watch_meta`).Scan(&c)
	min := ""
	for _, x := range []string{a, b, c} {
		if x == "" {
			continue
		}
		if min == "" || x < min {
			min = x
		}
	}
	return min
}

// HasDailyDay reports whether a closed day has been rolled up.
func (d *DB) HasDailyDay(day string) bool {
	if d == nil || day == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM daily_rollup_done WHERE day = ?`, day).Scan(&n)
	return n > 0
}

// SumDailyAppStats sums daily_app_stats over [fromDay, toDay] inclusive.
func (d *DB) SumDailyAppStats(fromDay, toDay string) []DailyAppRow {
	if d == nil || fromDay == "" || toDay == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT bucket, MAX(group_name), SUM(duration_sec), SUM(sessions),
		       COALESCE(MAX(last_title), '')
		FROM daily_app_stats
		WHERE day >= ? AND day <= ?
		GROUP BY bucket`, fromDay, toDay)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []DailyAppRow
	for rows.Next() {
		var r DailyAppRow
		if err := rows.Scan(&r.Bucket, &r.Group, &r.DurationSec, &r.Sessions, &r.LastTitle); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// SumDailyAppDetail sums child keys under aggregate buckets for [fromDay, toDay].
func (d *DB) SumDailyAppDetail(fromDay, toDay string) []DailyAppDetailRow {
	if d == nil || fromDay == "" || toDay == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT bucket, child_key, MAX(group_name), COALESCE(MAX(exe), ''),
		       SUM(duration_sec), SUM(sessions), COALESCE(MAX(last_title), '')
		FROM daily_app_detail
		WHERE day >= ? AND day <= ?
		GROUP BY bucket, child_key`, fromDay, toDay)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []DailyAppDetailRow
	for rows.Next() {
		var r DailyAppDetailRow
		if err := rows.Scan(&r.Bucket, &r.ChildKey, &r.Group, &r.Exe, &r.DurationSec, &r.Sessions, &r.LastTitle); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// CountDailyAppDetailDay returns how many detail rows exist for a day.
func (d *DB) CountDailyAppDetailDay(day string) int {
	if d == nil || day == "" {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM daily_app_detail WHERE day = ?`, day).Scan(&n)
	return n
}

// SumDailyPlayStats sums play duration by site over [fromDay, toDay] inclusive.
func (d *DB) SumDailyPlayStats(fromDay, toDay string) []DailyPlayRow {
	if d == nil || fromDay == "" || toDay == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT site, SUM(duration_sec), MAX(segments)
		FROM daily_play_stats
		WHERE day >= ? AND day <= ?
		GROUP BY site`, fromDay, toDay)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []DailyPlayRow
	for rows.Next() {
		var r DailyPlayRow
		if err := rows.Scan(&r.Site, &r.DurationSec, &r.Segments); err != nil {
			continue
		}
		out = append(out, r)
	}
	return out
}

// SumDailyPlayByAuthor sums play duration at author/title grain over [fromDay, toDay].
func (d *DB) SumDailyPlayByAuthor(fromDay, toDay string) []DailyPlayRow {
	if d == nil || fromDay == "" || toDay == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT site, author, title, COALESCE(MAX(url), ''), SUM(duration_sec),
		       MAX(segments), MAX(watch_exempt), COALESCE(MAX(last_seen), '')
		FROM daily_play_stats
		WHERE day >= ? AND day <= ?
		GROUP BY site, author, title`, fromDay, toDay)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []DailyPlayRow
	for rows.Next() {
		var r DailyPlayRow
		var ex int
		if err := rows.Scan(&r.Site, &r.Author, &r.Title, &r.URL, &r.DurationSec, &r.Segments, &ex, &r.LastSeen); err != nil {
			continue
		}
		r.Exempt = ex != 0
		r.Author = watchmeta.CleanAuthor(r.Author)
		r.Title = watchmeta.CleanVideoTitle(r.Site, r.Title)
		out = append(out, r)
	}
	return out
}

// ListDailyPlaySpans returns rolled-up play spans for a day (after raw prune).
func (d *DB) ListDailyPlaySpans(day string) []PlaySeg {
	if d == nil || day == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT site, start_at, end_at, duration_sec FROM daily_play_spans
		WHERE day = ? ORDER BY start_at`, day)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []PlaySeg
	for rows.Next() {
		var site, start, end string
		var dur float64
		if err := rows.Scan(&site, &start, &end, &dur); err != nil {
			continue
		}
		out = append(out, PlaySeg{
			Site: site, Start: parseTS(start), End: parseTS(end), DurationSec: dur,
		})
	}
	return out
}

// CountDailyPlaySpansDay returns span rows for a rolled day.
func (d *DB) CountDailyPlaySpansDay(day string) int {
	if d == nil || day == "" {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM daily_play_spans WHERE day = ?`, day).Scan(&n)
	return n
}

// DailyWatchLabels returns best-per-site labels from daily_watch_stats for [fromDay, toDay].
func (d *DB) DailyWatchLabels(fromDay, toDay string) (map[string]WatchLabel, int) {
	entries, n := d.DailyWatchEntries(fromDay, toDay)
	out := map[string]WatchLabel{}
	for _, cand := range entries {
		site := cand.Site
		if site == "" {
			continue
		}
		cur := out[site]
		if betterWatchLabel(cand, cur) {
			exExempt := cur.Exempt || cand.Exempt
			cand.Exempt = exExempt
			out[site] = cand
		} else if cand.Exempt {
			cur.Exempt = true
			out[site] = cur
		}
	}
	return out, n
}

// DailyWatchEntries returns all rolled watch labels (multi author/title) for [fromDay, toDay].
func (d *DB) DailyWatchEntries(fromDay, toDay string) ([]WatchLabel, int) {
	if d == nil {
		return nil, 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT site, title, author, url, COALESCE(source, ''), watch_exempt, last_seen
		FROM daily_watch_stats
		WHERE day >= ? AND day <= ?
		ORDER BY last_seen ASC`, fromDay, toDay)
	if err != nil {
		return nil, 0
	}
	defer rows.Close()
	var out []WatchLabel
	for rows.Next() {
		var site, title, author, url, source, ts string
		var ex int
		if err := rows.Scan(&site, &title, &author, &url, &source, &ex, &ts); err != nil {
			continue
		}
		title = watchmeta.CleanVideoTitle(site, title)
		author = watchmeta.CleanAuthor(author)
		out = append(out, WatchLabel{
			Site: site, Title: title, Author: author, URL: url, TS: ts,
			Source: source, Exempt: ex != 0,
		})
	}
	return out, len(out)
}

// ListMissingRollupDays returns closed days in [fromDay, toDay] that lack rollup
// but still have raw rows (caller may RollupDay).
func (d *DB) ListMissingRollupDays(fromDay, toDay, today string) []string {
	if d == nil || fromDay == "" || toDay == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	from, ok1 := parseDay(fromDay)
	to, ok2 := parseDay(toDay)
	if !ok1 || !ok2 || to.Before(from) {
		return nil
	}
	var missing []string
	for day := from; !day.After(to); day = day.AddDate(0, 0, 1) {
		dk := dayKey(day)
		if dk >= today {
			continue
		}
		var done int
		_ = d.sql.QueryRow(`SELECT COUNT(*) FROM daily_rollup_done WHERE day = ?`, dk).Scan(&done)
		if done > 0 {
			continue
		}
		var focusN, playN, watchN int
		_ = d.sql.QueryRow(`SELECT COUNT(*) FROM focus_sessions WHERE day = ?`, dk).Scan(&focusN)
		_ = d.sql.QueryRow(`SELECT COUNT(*) FROM play_segments WHERE day = ?`, dk).Scan(&playN)
		_ = d.sql.QueryRow(`SELECT COUNT(*) FROM watch_meta WHERE substr(last_seen, 1, 10) = ?`, dk).Scan(&watchN)
		if focusN > 0 || playN > 0 || watchN > 0 {
			missing = append(missing, dk)
		}
	}
	return missing
}
