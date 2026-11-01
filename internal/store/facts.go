package store

import (
	"encoding/json"
	"time"

	"timesavior/internal/watchmeta"
)

const playIdleGap = 10 * time.Minute
const playPulsePad = 5 * time.Second
const playThrottle = 5 * time.Second

// FocusRec is one foreground-window session.
type FocusRec struct {
	StartedAt   time.Time
	EndedAt     time.Time
	DurationSec float64
	Bucket      string
	Group       string
	Title       string
	Exe         string
	ExePath     string
	PID         int64
	HWND        string
	IsBrowser   bool
	ClassSource string
}

// InsertFocus writes a completed focus session.
func (d *DB) InsertFocus(rec FocusRec) error {
	if d == nil {
		return nil
	}
	if rec.EndedAt.IsZero() {
		rec.EndedAt = time.Now()
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = rec.EndedAt
	}
	if rec.DurationSec <= 0 {
		rec.DurationSec = rec.EndedAt.Sub(rec.StartedAt).Seconds()
	}
	day := dayKey(rec.StartedAt)
	br := 0
	if rec.IsBrowser {
		br = 1
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(`
		INSERT INTO focus_sessions(
		  day, started_at, ended_at, duration_sec, bucket, group_name,
		  title, exe, exe_path, pid, hwnd, is_browser, class_source
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		day, formatTS(rec.StartedAt), formatTS(rec.EndedAt), rec.DurationSec,
		rec.Bucket, rec.Group, rec.Title, rec.Exe, rec.ExePath,
		rec.PID, rec.HWND, br, rec.ClassSource,
	)
	if err == nil {
		d.maybeRollupYesterdayLocked(rec.EndedAt)
		d.notifyFactsLocked()
	}
	return err
}

// NotePlay extends or starts an aggregated play segment for site (throttled).
// Only one site segment stays open: a pulse for site B closes site A's open segment
// so overlapping CDN traffic is not double-counted as full wall-clock for each site.
func (d *DB) NotePlay(site string, now time.Time) {
	if d == nil || site == "" {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if last, ok := d.lastPlay[site]; ok && now.Sub(last) < playThrottle {
		return
	}
	d.lastPlay[site] = now

	today := dayKey(now)
	// Close other sites (single open site) and any segment that crossed midnight.
	toFlush := make([]*openPlaySeg, 0, len(d.openPlay))
	for other, otherSeg := range d.openPlay {
		if other != site || dayKey(otherSeg.start) != today {
			toFlush = append(toFlush, otherSeg)
		}
	}
	for _, otherSeg := range toFlush {
		d.flushSegLocked(otherSeg, otherSeg.last)
	}

	seg, ok := d.openPlay[site]
	if !ok || now.Sub(seg.last) > playIdleGap {
		if ok {
			d.flushSegLocked(seg, seg.last)
		}
		seg = &openPlaySeg{site: site, start: now, last: now}
		d.openPlay[site] = seg
		d.upsertOpenSegLocked(seg)
		d.maybeRollupYesterdayLocked(now)
		d.notifyFactsLocked()
		return
	}
	seg.last = now
	d.upsertOpenSegLocked(seg)
	d.notifyFactsLocked()
}

func (d *DB) upsertOpenSegLocked(seg *openPlaySeg) {
	dur := seg.last.Sub(seg.start) + playPulsePad
	if dur < playPulsePad {
		dur = playPulsePad
	}
	day := dayKey(seg.start)
	if seg.rowID == 0 {
		res, err := d.sql.Exec(`
			INSERT INTO play_segments(day, site, start_at, end_at, duration_sec)
			VALUES (?,?,?,?,?)`,
			day, seg.site, formatTS(seg.start), formatTS(seg.last), dur.Seconds(),
		)
		if err != nil {
			return
		}
		id, _ := res.LastInsertId()
		seg.rowID = id
		return
	}
	_, _ = d.sql.Exec(`
		UPDATE play_segments SET end_at = ?, duration_sec = ? WHERE id = ?`,
		formatTS(seg.last), dur.Seconds(), seg.rowID,
	)
}

func (d *DB) flushSegLocked(seg *openPlaySeg, end time.Time) {
	if seg == nil {
		return
	}
	seg.last = end
	dur := seg.last.Sub(seg.start) + playPulsePad
	if dur < playPulsePad {
		dur = playPulsePad
	}
	// Isolated short sessions (prefetch): drop on close so they never roll up.
	if d.minWatchSec > 0 && dur.Seconds()+1e-9 < d.minWatchSec {
		if seg.rowID != 0 {
			_, _ = d.sql.Exec(`DELETE FROM play_segments WHERE id = ?`, seg.rowID)
		}
		delete(d.openPlay, seg.site)
		return
	}
	d.upsertOpenSegLocked(seg)
	delete(d.openPlay, seg.site)
}

func (d *DB) flushOpenPlayLocked(_ time.Time) {
	segs := make([]*openPlaySeg, 0, len(d.openPlay))
	for _, seg := range d.openPlay {
		segs = append(segs, seg)
	}
	for _, seg := range segs {
		// Keep end at last observed play pulse (+ pad applied in upsert).
		d.flushSegLocked(seg, seg.last)
	}
}

// WatchRec is video metadata (upserted by dedupe key).
type WatchRec struct {
	DedupeKey   string
	Site        string
	VideoID     string
	Title       string
	Author      string
	Tags        []string
	URL         string
	Source      string
	WatchExempt bool
	ExemptLabel string
	SeenAt      time.Time
}

// UpsertWatch inserts or refreshes watch metadata.
func (d *DB) UpsertWatch(rec WatchRec) error {
	if d == nil || rec.DedupeKey == "" {
		return nil
	}
	if rec.SeenAt.IsZero() {
		rec.SeenAt = time.Now()
	}
	// Drop homepage / window-chrome junk so it cannot overwrite a real title.
	rec.Title = watchmeta.CleanVideoTitle(rec.Site, rec.Title)
	rec.Author = watchmeta.CleanAuthor(rec.Author)
	if rec.Author == "" {
		rec.Author = watchmeta.AuthorFromExemptLabel(rec.ExemptLabel)
	}
	tags, _ := json.Marshal(rec.Tags)
	if tags == nil {
		tags = []byte("[]")
	}
	ex := 0
	if rec.WatchExempt {
		ex = 1
	}
	ts := formatTS(rec.SeenAt)
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(`
		INSERT INTO watch_meta(
		  dedupe_key, site, video_id, title, author, tags_json, url, source,
		  watch_exempt, exempt_label, first_seen, last_seen
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(dedupe_key) DO UPDATE SET
		  title = CASE
		    WHEN excluded.title != '' AND (
		      watch_meta.title = '' OR
		      excluded.source != 'focus_title' OR
		      watch_meta.source = 'focus_title'
		    ) THEN excluded.title
		    ELSE watch_meta.title
		  END,
		  author = CASE WHEN excluded.author != '' THEN excluded.author ELSE watch_meta.author END,
		  tags_json = CASE WHEN excluded.tags_json != '[]' THEN excluded.tags_json ELSE watch_meta.tags_json END,
		  url = CASE WHEN excluded.url != '' THEN excluded.url ELSE watch_meta.url END,
		  source = CASE
		    WHEN excluded.source != '' AND excluded.source != 'focus_title' THEN excluded.source
		    WHEN watch_meta.source = '' OR watch_meta.source = 'focus_title' THEN excluded.source
		    ELSE watch_meta.source
		  END,
		  watch_exempt = watch_meta.watch_exempt OR excluded.watch_exempt,
		  exempt_label = CASE WHEN excluded.exempt_label != '' THEN excluded.exempt_label ELSE watch_meta.exempt_label END,
		  last_seen = excluded.last_seen`,
		rec.DedupeKey, rec.Site, rec.VideoID, rec.Title, rec.Author, string(tags),
		rec.URL, rec.Source, ex, rec.ExemptLabel, ts, ts,
	)
	return err
}

// InsertEvent writes a lifecycle event.
func (d *DB) InsertEvent(kind string, fields map[string]any) error {
	if d == nil || kind == "" {
		return nil
	}
	now := time.Now()
	if fields == nil {
		fields = map[string]any{}
	}
	if _, ok := fields["ts"]; !ok {
		fields["ts"] = formatTS(now)
	}
	fields["kind"] = kind
	payload, _ := json.Marshal(fields)
	if payload == nil {
		payload = []byte("{}")
	}
	ts, _ := fields["ts"].(string)
	if ts == "" {
		ts = formatTS(now)
	}
	day := dayKey(parseTS(ts))
	if day == "" {
		day = dayKey(now)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(
		`INSERT INTO events(ts, day, kind, payload_json) VALUES (?,?,?,?)`,
		ts, day, kind, string(payload),
	)
	return err
}

// RecentEvents returns up to limit newest events (newest last, like audit).
func (d *DB) RecentEvents(limit int) []map[string]any {
	if d == nil || limit <= 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT payload_json FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var rev []map[string]any
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(raw), &m) == nil {
			rev = append(rev, m)
		}
	}
	// reverse to chronological
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}

// FocusDayRows returns focus sessions for a calendar day (local date string).
func (d *DB) FocusDayRows(day string) []map[string]any {
	if d == nil || day == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT started_at, ended_at, duration_sec, bucket, group_name, title, exe,
		       exe_path, pid, hwnd, is_browser, class_source
		FROM focus_sessions WHERE day = ?`, day)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var started, ended, bucket, group, title, exe, exePath, hwnd, src string
		var dur float64
		var pid int64
		var br int
		if err := rows.Scan(&started, &ended, &dur, &bucket, &group, &title, &exe, &exePath, &pid, &hwnd, &br, &src); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"started_at":   started,
			"ended_at":     ended,
			"duration_sec": dur,
			"bucket":       bucket,
			"group":        group,
			"title":        title,
			"exe":          exe,
			"exe_path":     exePath,
			"pid":          pid,
			"hwnd":         hwnd,
			"is_browser":   br != 0,
			"class_source": src,
		})
	}
	return out
}

// PlaySeg is one stored play segment row.
type PlaySeg struct {
	Site        string
	Start       time.Time
	End         time.Time
	DurationSec float64
}

// ListPlaySegments returns raw play segments for a calendar day.
func (d *DB) ListPlaySegments(day string) []PlaySeg {
	if d == nil || day == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT site, start_at, end_at, duration_sec FROM play_segments WHERE day = ? ORDER BY start_at`, day)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []PlaySeg
	for rows.Next() {
		var site, startS, endS string
		var dur float64
		if err := rows.Scan(&site, &startS, &endS, &dur); err != nil {
			continue
		}
		out = append(out, PlaySeg{
			Site: site, Start: parseTS(startS), End: parseTS(endS), DurationSec: dur,
		})
	}
	return out
}

// ReplacePlayDay replaces all play_segments for day with segs (used for JSONL backfill).
// Flushes in-memory open segments first so row IDs stay consistent.
func (d *DB) ReplacePlayDay(day string, segs []PlaySeg) error {
	if d == nil || day == "" {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flushOpenPlayLocked(time.Now())
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM play_segments WHERE day = ?`, day); err != nil {
		return err
	}
	for _, seg := range segs {
		if seg.Site == "" || seg.Start.IsZero() {
			continue
		}
		end := seg.End
		if end.IsZero() {
			end = seg.Start
		}
		dur := seg.DurationSec
		if dur <= 0 {
			dur = end.Sub(seg.Start).Seconds() + playPulsePad.Seconds()
			if dur < playPulsePad.Seconds() {
				dur = playPulsePad.Seconds()
			}
		}
		if _, err := tx.Exec(`
			INSERT INTO play_segments(day, site, start_at, end_at, duration_sec)
			VALUES (?,?,?,?,?)`,
			day, seg.Site, formatTS(seg.Start), formatTS(end), dur,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PlaySiteDurations returns site → seconds for a calendar day (from segments).
func (d *DB) PlaySiteDurations(day string) (map[string]float64, map[string]string, int) {
	out := map[string]float64{}
	last := map[string]string{}
	if d == nil || day == "" {
		return out, last, 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT site, duration_sec, end_at FROM play_segments WHERE day = ?`, day)
	if err != nil {
		return out, last, 0
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var site, endAt string
		var dur float64
		if err := rows.Scan(&site, &dur, &endAt); err != nil {
			continue
		}
		n++
		out[site] += dur
		if endAt > last[site] {
			last[site] = endAt
		}
	}
	return out, last, n
}

// WatchMetaForRange returns latest labels per site for days in [fromDay, toDay].
// Prefers API-sniffed meta (non-focus_title) and rows with author over dirty focus titles.
func (d *DB) WatchMetaForRange(fromDay, toDay string) (map[string]WatchLabel, int) {
	entries, n := d.WatchMetaEntries(fromDay, toDay)
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

// WatchLabel is a display label from watch_meta (site-level or per-row).
type WatchLabel struct {
	Site, Title, Author, URL, TS, Source, ExemptLabel string
	Exempt                                            bool
}

// WatchMetaEntries returns all watch_meta rows in [fromDay, toDay] (not collapsed by site).
func (d *DB) WatchMetaEntries(fromDay, toDay string) ([]WatchLabel, int) {
	if d == nil {
		return nil, 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT site, title, author, url, source, watch_exempt, exempt_label, last_seen
		FROM watch_meta
		WHERE substr(last_seen, 1, 10) >= ? AND substr(last_seen, 1, 10) <= ?
		ORDER BY last_seen ASC`, fromDay, toDay)
	if err != nil {
		return nil, 0
	}
	defer rows.Close()
	var out []WatchLabel
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
	return out, len(out)
}

// betterWatchLabel prefers real API meta / author-bearing rows over focus_title fallbacks.
func betterWatchLabel(cand, cur WatchLabel) bool {
	if cur.Title == "" && cur.Author == "" && cur.URL == "" {
		return cand.Title != "" || cand.Author != "" || cand.URL != ""
	}
	cq, oq := watchLabelScore(cand), watchLabelScore(cur)
	if cq != oq {
		return cq > oq
	}
	return cand.TS >= cur.TS && (cand.Title != "" || cand.Author != "")
}

func watchLabelScore(l WatchLabel) int {
	s := 0
	if l.Title != "" {
		s += 2
	}
	if l.Author != "" {
		s += 4
	}
	if l.URL != "" {
		s += 1
	}
	if l.Source != "" && l.Source != "focus_title" {
		s += 5
	}
	return s
}

// CountFocusDays returns how many focus rows exist for day.
func (d *DB) CountFocusDay(day string) int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM focus_sessions WHERE day = ?`, day).Scan(&n)
	return n
}

// CountPlayDay returns how many play segments exist for day.
func (d *DB) CountPlayDay(day string) int {
	if d == nil {
		return 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var n int
	_ = d.sql.QueryRow(`SELECT COUNT(*) FROM play_segments WHERE day = ?`, day).Scan(&n)
	return n
}
