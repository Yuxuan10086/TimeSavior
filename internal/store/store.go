// Package store provides SQLite persistence for runtime policy state and
// queryable activity facts (focus sessions, play segments, watch meta, events).
package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"timesavior/internal/watchmeta"

	_ "modernc.org/sqlite"
)

const schemaVersion = "7"

var schemaStmts = []string{
	`PRAGMA journal_mode=WAL`,
	`PRAGMA synchronous=NORMAL`,
	`CREATE TABLE IF NOT EXISTS schema_meta (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS site_runtime (
  site TEXT PRIMARY KEY,
  full_block_until TEXT NOT NULL DEFAULT '',
  watch_started TEXT NOT NULL DEFAULT '',
  last_play_at TEXT NOT NULL DEFAULT '',
  watch_exempt INTEGER NOT NULL DEFAULT 0,
  exempt_label TEXT NOT NULL DEFAULT '',
  exempt_until TEXT NOT NULL DEFAULT ''
)`,
	`CREATE TABLE IF NOT EXISTS unlock_quota (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  day_key TEXT NOT NULL DEFAULT '',
  day_count INTEGER NOT NULL DEFAULT 0,
  week_key TEXT NOT NULL DEFAULT '',
  week_count INTEGER NOT NULL DEFAULT 0,
  temp_unlock_until TEXT NOT NULL DEFAULT '',
  temp_unlock_started_at TEXT NOT NULL DEFAULT ''
)`,
	`CREATE TABLE IF NOT EXISTS focus_sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  day TEXT NOT NULL,
  started_at TEXT NOT NULL,
  ended_at TEXT NOT NULL,
  duration_sec REAL NOT NULL,
  bucket TEXT NOT NULL DEFAULT '',
  group_name TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  exe TEXT NOT NULL DEFAULT '',
  exe_path TEXT NOT NULL DEFAULT '',
  pid INTEGER NOT NULL DEFAULT 0,
  hwnd TEXT NOT NULL DEFAULT '',
  is_browser INTEGER NOT NULL DEFAULT 0,
  class_source TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX IF NOT EXISTS idx_focus_day ON focus_sessions(day)`,
	`CREATE TABLE IF NOT EXISTS play_segments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  day TEXT NOT NULL,
  site TEXT NOT NULL,
  start_at TEXT NOT NULL,
  end_at TEXT NOT NULL,
  duration_sec REAL NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_play_day ON play_segments(day)`,
	`CREATE INDEX IF NOT EXISTS idx_play_site_day ON play_segments(site, day)`,
	`CREATE TABLE IF NOT EXISTS watch_meta (
  dedupe_key TEXT PRIMARY KEY,
  site TEXT NOT NULL,
  video_id TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  tags_json TEXT NOT NULL DEFAULT '[]',
  url TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  watch_exempt INTEGER NOT NULL DEFAULT 0,
  exempt_label TEXT NOT NULL DEFAULT '',
  first_seen TEXT NOT NULL,
  last_seen TEXT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_watch_site ON watch_meta(site)`,
	`CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT NOT NULL,
  day TEXT NOT NULL,
  kind TEXT NOT NULL,
  payload_json TEXT NOT NULL DEFAULT '{}'
)`,
	`CREATE INDEX IF NOT EXISTS idx_events_day ON events(day)`,
	`CREATE INDEX IF NOT EXISTS idx_events_kind ON events(kind)`,
	// Permanent day-grain rollups (never pruned with raw TTL).
	`CREATE TABLE IF NOT EXISTS daily_app_stats (
  day TEXT NOT NULL,
  bucket TEXT NOT NULL,
  group_name TEXT NOT NULL DEFAULT '',
  duration_sec REAL NOT NULL DEFAULT 0,
  sessions INTEGER NOT NULL DEFAULT 0,
  last_title TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (day, bucket)
)`,
	`CREATE INDEX IF NOT EXISTS idx_daily_app_day ON daily_app_stats(day)`,
	// Child rows under aggregate buckets (browser_*, system) — survives raw prune.
	// child_key is a stable storage key (exe\\0title); UI label is resolved at query time.
	`CREATE TABLE IF NOT EXISTS daily_app_detail (
  day TEXT NOT NULL,
  bucket TEXT NOT NULL,
  child_key TEXT NOT NULL,
  group_name TEXT NOT NULL DEFAULT '',
  exe TEXT NOT NULL DEFAULT '',
  duration_sec REAL NOT NULL DEFAULT 0,
  sessions INTEGER NOT NULL DEFAULT 0,
  last_title TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (day, bucket, child_key)
)`,
	`CREATE INDEX IF NOT EXISTS idx_daily_app_detail_day ON daily_app_detail(day)`,
	// Play duration by site + author/title (not site-only).
	`CREATE TABLE IF NOT EXISTS daily_play_stats (
  day TEXT NOT NULL,
  site TEXT NOT NULL,
  author TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  duration_sec REAL NOT NULL DEFAULT 0,
  segments INTEGER NOT NULL DEFAULT 0,
  watch_exempt INTEGER NOT NULL DEFAULT 0,
  last_seen TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (day, site, author, title)
)`,
	`CREATE INDEX IF NOT EXISTS idx_daily_play_day ON daily_play_stats(day)`,
	// Timed play spans for author attribution after raw prune.
	`CREATE TABLE IF NOT EXISTS daily_play_spans (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  day TEXT NOT NULL,
  site TEXT NOT NULL,
  start_at TEXT NOT NULL,
  end_at TEXT NOT NULL,
  duration_sec REAL NOT NULL DEFAULT 0
)`,
	`CREATE INDEX IF NOT EXISTS idx_daily_play_spans_day ON daily_play_spans(day)`,
	// Multi-row watch labels per day (authors/titles not collapsed to 1/site).
	`CREATE TABLE IF NOT EXISTS daily_watch_stats (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  day TEXT NOT NULL,
  site TEXT NOT NULL,
  title TEXT NOT NULL DEFAULT '',
  author TEXT NOT NULL DEFAULT '',
  url TEXT NOT NULL DEFAULT '',
  source TEXT NOT NULL DEFAULT '',
  watch_exempt INTEGER NOT NULL DEFAULT 0,
  last_seen TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX IF NOT EXISTS idx_daily_watch_day ON daily_watch_stats(day)`,
	`CREATE TABLE IF NOT EXISTS daily_rollup_done (
  day TEXT PRIMARY KEY,
  rolled_at TEXT NOT NULL
)`,
	`CREATE TABLE IF NOT EXISTS llm_usage (
  day TEXT PRIMARY KEY,
  cost_cny REAL NOT NULL DEFAULT 0,
  calls INTEGER NOT NULL DEFAULT 0
)`,
	`CREATE TABLE IF NOT EXISTS llm_totals (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  total_cost_cny REAL NOT NULL DEFAULT 0,
  total_calls INTEGER NOT NULL DEFAULT 0
)`,
	`CREATE TABLE IF NOT EXISTS llm_call_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT NOT NULL,
  day TEXT NOT NULL,
  site TEXT NOT NULL DEFAULT '',
  cost_cny REAL NOT NULL DEFAULT 0,
  allow INTEGER,
  reason TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX IF NOT EXISTS idx_llm_log_day ON llm_call_log(day)`,
	`CREATE TABLE IF NOT EXISTS llm_dialogs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts TEXT NOT NULL,
  day TEXT NOT NULL,
  site TEXT NOT NULL DEFAULT '',
  kind TEXT NOT NULL DEFAULT 'watch_judge',
  allow INTEGER,
  reason TEXT NOT NULL DEFAULT '',
  prompt_summary TEXT NOT NULL DEFAULT '',
  response_raw TEXT NOT NULL DEFAULT '',
  cost_cny REAL NOT NULL DEFAULT 0,
  videos_json TEXT NOT NULL DEFAULT '[]',
  error TEXT NOT NULL DEFAULT ''
)`,
	`CREATE INDEX IF NOT EXISTS idx_llm_dialogs_ts ON llm_dialogs(ts)`,
	`CREATE INDEX IF NOT EXISTS idx_llm_dialogs_day ON llm_dialogs(day)`,
	`CREATE TABLE IF NOT EXISTS title_shortmap (
  source_hash TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  short_name TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`,
	`CREATE INDEX IF NOT EXISTS idx_title_shortmap_updated ON title_shortmap(updated_at)`,
}

// DB is the SQLite-backed store at data/timesavior.db.
type DB struct {
	mu          sync.Mutex
	sql         *sql.DB
	path        string
	keepDays    int
	minWatchSec float64 // drop isolated play segments shorter than this on flush/rollup

	// Open play segments (site → in-progress aggregation).
	openPlay map[string]*openPlaySeg
	lastPlay map[string]time.Time // throttle NotePlay like audit (5s)

	onFactsChange func() // focus / play writes (SSE stats)

	maintStop chan struct{}
	maintOnce sync.Once
}

type openPlaySeg struct {
	site  string
	start time.Time
	last  time.Time
	rowID int64 // 0 = not yet inserted
}

// Options configures Open.
type Options struct {
	Path        string // e.g. data/timesavior.db
	KeepDays    int    // raw fact TTL in calendar days (default 7)
	MinWatchSec int    // drop isolated play < N sec on flush/rollup (default 15; 0 disables)
}

// Open creates/opens the database and migrates schema.
func Open(opt Options) (*DB, error) {
	if opt.Path == "" {
		return nil, fmt.Errorf("store: path required")
	}
	if opt.KeepDays <= 0 {
		opt.KeepDays = 7
	}
	minWatch := 15.0
	if opt.MinWatchSec > 0 {
		minWatch = float64(opt.MinWatchSec)
	} else if opt.MinWatchSec < 0 {
		minWatch = 0 // disable short-play drop
	}
	if err := os.MkdirAll(filepath.Dir(opt.Path), 0o755); err != nil {
		return nil, err
	}
	sqlDB, err := sql.Open("sqlite", opt.Path)
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1) // SQLite + simple locking
	for _, stmt := range schemaStmts {
		if _, err := sqlDB.Exec(stmt); err != nil {
			_ = sqlDB.Close()
			return nil, fmt.Errorf("store schema: %w", err)
		}
	}
	if _, err := sqlDB.Exec(
		`INSERT INTO schema_meta(key, value) VALUES ('schema_version', ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		schemaVersion,
	); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	// Ensure unlock_quota singleton row exists.
	if _, err := sqlDB.Exec(`INSERT OR IGNORE INTO unlock_quota(id) VALUES (1)`); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if _, err := sqlDB.Exec(`INSERT OR IGNORE INTO llm_totals(id) VALUES (1)`); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := migrateLLMDialogsKind(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := migrateUnlockQuotaStartedAt(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := migrateDailyRollupV7(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store migrate daily v7: %w", err)
	}
	if err := scrubDirtyAuthors(sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("store scrub authors: %w", err)
	}
	d := &DB{
		sql:         sqlDB,
		path:        opt.Path,
		keepDays:    opt.KeepDays,
		minWatchSec: minWatch,
		openPlay:    make(map[string]*openPlaySeg),
		lastPlay:    make(map[string]time.Time),
	}
	now := time.Now()
	d.RollupClosedDays(now)
	d.Prune(now)
	return d, nil
}

func (d *DB) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

func (d *DB) KeepDays() int {
	if d == nil {
		return 0
	}
	return d.keepDays
}

// SetOnFactsChange registers a callback when focus/play facts change (for SSE).
func (d *DB) SetOnFactsChange(fn func()) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.onFactsChange = fn
	d.mu.Unlock()
}

func (d *DB) notifyFactsLocked() {
	fn := d.onFactsChange
	if fn == nil {
		return
	}
	go fn()
}

func (d *DB) Close() error {
	if d == nil {
		return nil
	}
	d.StopMaintenance()
	d.mu.Lock()
	defer d.mu.Unlock()
	d.flushOpenPlayLocked(time.Now())
	return d.sql.Close()
}

// StartMaintenance runs periodic rollup + prune (default every hour).
func (d *DB) StartMaintenance(every time.Duration) {
	if d == nil {
		return
	}
	if every <= 0 {
		every = time.Hour
	}
	d.maintOnce.Do(func() {
		stop := make(chan struct{})
		d.maintStop = stop
		go func() {
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case now := <-t.C:
					d.RollupClosedDays(now)
					d.Prune(now)
				}
			}
		}()
	})
}

// StopMaintenance stops the background maintenance goroutine.
func (d *DB) StopMaintenance() {
	if d == nil {
		return
	}
	d.mu.Lock()
	ch := d.maintStop
	d.maintStop = nil
	d.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// Prune deletes raw fact rows older than keepDays (calendar days).
// Daily rollup tables are never pruned.
// Only days already marked in daily_rollup_done are deleted, so a failed/missed
// rollup cannot wipe unrecovered raw facts.
func (d *DB) Prune(now time.Time) {
	if d == nil {
		return
	}
	// Ensure closed days are rolled up before deleting raw rows.
	d.RollupClosedDays(now)

	cutoff := now.AddDate(0, 0, -d.keepDays).Format("2006-01-02")
	d.mu.Lock()
	defer d.mu.Unlock()
	_, _ = d.sql.Exec(`DELETE FROM focus_sessions WHERE day < ? AND day IN (SELECT day FROM daily_rollup_done)`, cutoff)
	_, _ = d.sql.Exec(`DELETE FROM play_segments WHERE day < ? AND day IN (SELECT day FROM daily_rollup_done)`, cutoff)
	_, _ = d.sql.Exec(`DELETE FROM events WHERE day < ? AND day IN (SELECT day FROM daily_rollup_done)`, cutoff)
	// watch_meta: drop rows whose last_seen day is older than cutoff (and rolled up).
	_, _ = d.sql.Exec(`
		DELETE FROM watch_meta
		WHERE substr(last_seen, 1, 10) < ?
		  AND substr(last_seen, 1, 10) IN (SELECT day FROM daily_rollup_done)`, cutoff)
	// LLM logs are not part of daily_* rollups; apply calendar TTL directly.
	_, _ = d.sql.Exec(`DELETE FROM llm_call_log WHERE day < ?`, cutoff)
	_, _ = d.sql.Exec(`DELETE FROM llm_dialogs WHERE day < ?`, cutoff)
}

func dayKey(t time.Time) string {
	return t.Format("2006-01-02")
}

func formatTS(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func parseTS(s string) time.Time {
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

func parseDay(s string) (time.Time, bool) {
	t, err := time.ParseInLocation("2006-01-02", s, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// migrateLLMDialogsKind adds kind/purpose column on existing DBs (CREATE IF NOT EXISTS won't alter).
func migrateLLMDialogsKind(sqlDB *sql.DB) error {
	if sqlDB == nil {
		return nil
	}
	cols, err := tableColumns(sqlDB, "llm_dialogs")
	if err != nil {
		return err
	}
	if cols["kind"] {
		return nil
	}
	_, err = sqlDB.Exec(`ALTER TABLE llm_dialogs ADD COLUMN kind TEXT NOT NULL DEFAULT 'watch_judge'`)
	return err
}

// migrateUnlockQuotaStartedAt adds temp_unlock_started_at for feed-triggered unlock persistence.
func migrateUnlockQuotaStartedAt(sqlDB *sql.DB) error {
	if sqlDB == nil {
		return nil
	}
	cols, err := tableColumns(sqlDB, "unlock_quota")
	if err != nil {
		return err
	}
	if cols["temp_unlock_started_at"] {
		return nil
	}
	_, err = sqlDB.Exec(`ALTER TABLE unlock_quota ADD COLUMN temp_unlock_started_at TEXT NOT NULL DEFAULT ''`)
	return err
}

func tableColumns(sqlDB *sql.DB, table string) (map[string]bool, error) {
	out := map[string]bool{}
	rows, err := sqlDB.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return out, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// migrateDailyRollupV7 upgrades legacy site-only daily_* tables to rich rollups
// (app children, play by author/title, multi watch labels, play spans).
func migrateDailyRollupV7(sqlDB *sql.DB) error {
	if sqlDB == nil {
		return nil
	}
	var flag string
	_ = sqlDB.QueryRow(`SELECT value FROM schema_meta WHERE key = 'daily_rollup_schema'`).Scan(&flag)
	already := flag == "7"

	upgraded := false
	playCols, err := tableColumns(sqlDB, "daily_play_stats")
	if err != nil {
		return err
	}
	if !playCols["author"] {
		if _, err := sqlDB.Exec(`ALTER TABLE daily_play_stats RENAME TO daily_play_stats_v6`); err != nil {
			return err
		}
		if _, err := sqlDB.Exec(`
			CREATE TABLE daily_play_stats (
			  day TEXT NOT NULL,
			  site TEXT NOT NULL,
			  author TEXT NOT NULL DEFAULT '',
			  title TEXT NOT NULL DEFAULT '',
			  url TEXT NOT NULL DEFAULT '',
			  duration_sec REAL NOT NULL DEFAULT 0,
			  segments INTEGER NOT NULL DEFAULT 0,
			  watch_exempt INTEGER NOT NULL DEFAULT 0,
			  last_seen TEXT NOT NULL DEFAULT '',
			  PRIMARY KEY (day, site, author, title)
			)`); err != nil {
			return err
		}
		if _, err := sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_daily_play_day ON daily_play_stats(day)`); err != nil {
			return err
		}
		if _, err := sqlDB.Exec(`
			INSERT INTO daily_play_stats(day, site, author, title, duration_sec, segments)
			SELECT day, site, '', '', duration_sec, segments FROM daily_play_stats_v6`); err != nil {
			return err
		}
		if _, err := sqlDB.Exec(`DROP TABLE daily_play_stats_v6`); err != nil {
			return err
		}
		upgraded = true
	}

	watchCols, err := tableColumns(sqlDB, "daily_watch_stats")
	if err != nil {
		return err
	}
	// Legacy PK was (day, site) without id/source — rebuild as multi-row table.
	if !watchCols["id"] || !watchCols["source"] {
		if _, err := sqlDB.Exec(`ALTER TABLE daily_watch_stats RENAME TO daily_watch_stats_v6`); err != nil {
			return err
		}
		if _, err := sqlDB.Exec(`
			CREATE TABLE daily_watch_stats (
			  id INTEGER PRIMARY KEY AUTOINCREMENT,
			  day TEXT NOT NULL,
			  site TEXT NOT NULL,
			  title TEXT NOT NULL DEFAULT '',
			  author TEXT NOT NULL DEFAULT '',
			  url TEXT NOT NULL DEFAULT '',
			  source TEXT NOT NULL DEFAULT '',
			  watch_exempt INTEGER NOT NULL DEFAULT 0,
			  last_seen TEXT NOT NULL DEFAULT ''
			)`); err != nil {
			return err
		}
		if _, err := sqlDB.Exec(`CREATE INDEX IF NOT EXISTS idx_daily_watch_day ON daily_watch_stats(day)`); err != nil {
			return err
		}
		if _, err := sqlDB.Exec(`
			INSERT INTO daily_watch_stats(day, site, title, author, url, watch_exempt, last_seen)
			SELECT day, site, title, author, url, watch_exempt, last_seen FROM daily_watch_stats_v6`); err != nil {
			return err
		}
		if _, err := sqlDB.Exec(`DROP TABLE daily_watch_stats_v6`); err != nil {
			return err
		}
		upgraded = true
	}

	if !already {
		upgraded = true
		if _, err := sqlDB.Exec(
			`INSERT OR REPLACE INTO schema_meta(key, value) VALUES ('daily_rollup_schema', '7')`,
		); err != nil {
			return err
		}
	}

	// Ensure daily_app_detail has exe column (CREATE IF NOT EXISTS won't alter).
	detailCols, err := tableColumns(sqlDB, "daily_app_detail")
	if err != nil {
		return err
	}
	if len(detailCols) > 0 && !detailCols["exe"] {
		if _, err := sqlDB.Exec(`ALTER TABLE daily_app_detail ADD COLUMN exe TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
		upgraded = true
	}

	if upgraded {
		// Re-rollup days that still have raw so rich tables fill in.
		_, _ = sqlDB.Exec(`
			DELETE FROM daily_rollup_done
			WHERE day IN (
			  SELECT day FROM focus_sessions
			  UNION SELECT day FROM play_segments
			  UNION SELECT substr(last_seen, 1, 10) FROM watch_meta
			)`)
	}
	return nil
}

// scrubDirtyAuthors strips leaked "白名单"/"whitelist" chrome from stored author fields.
func scrubDirtyAuthors(sqlDB *sql.DB) error {
	if sqlDB == nil {
		return nil
	}
	type tbl struct {
		name string
		sql  string
	}
	tables := []tbl{
		{"watch_meta", `SELECT rowid, author FROM watch_meta WHERE author LIKE '%白名单%' OR lower(author) LIKE '%whitelist%'`},
		{"daily_play_stats", `SELECT rowid, author FROM daily_play_stats WHERE author LIKE '%白名单%' OR lower(author) LIKE '%whitelist%'`},
		{"daily_watch_stats", `SELECT rowid, author FROM daily_watch_stats WHERE author LIKE '%白名单%' OR lower(author) LIKE '%whitelist%'`},
	}
	for _, t := range tables {
		rows, err := sqlDB.Query(t.sql)
		if err != nil {
			// Table may be mid-migration / absent on fresh DBs.
			continue
		}
		type pair struct {
			id     int64
			author string
		}
		var dirty []pair
		for rows.Next() {
			var id int64
			var author string
			if err := rows.Scan(&id, &author); err != nil {
				continue
			}
			clean := watchmeta.CleanAuthor(author)
			if clean == author {
				continue
			}
			dirty = append(dirty, pair{id: id, author: clean})
		}
		_ = rows.Close()
		for _, p := range dirty {
			if _, err := sqlDB.Exec(`UPDATE `+t.name+` SET author = ? WHERE rowid = ?`, p.author, p.id); err != nil {
				return err
			}
		}
	}
	return nil
}
