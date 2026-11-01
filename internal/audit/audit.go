package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"timesavior/internal/store"
	"timesavior/internal/watchmeta"
)

// Store persists app / request / event / watch logs under data/logs with daily rotation.
// When a SQLite DB is attached, focus / play / watch / events prefer the DB
// (play as aggregated segments); JSONL for those streams is skipped to save space.
type Store struct {
	mu       sync.Mutex
	dir      string
	keepDays int
	db       *store.DB

	appDay string
	app    *os.File
	reqDay string
	req    *os.File
	evtDay string
	evt    *os.File
	watDay string
	wat    *os.File
	focDay string
	foc    *os.File
	playDay string
	play    *os.File

	prevFullBlock map[string]bool
	lastWatchKey  string
	lastWatchAt   time.Time
	lastPlaySite  map[string]time.Time // throttle play pulse writes (JSONL fallback)
}

type Options struct {
	Dir      string
	KeepDays int
	DB       *store.DB // optional SQLite fact store
}

func NewStore(opt Options) (*Store, error) {
	if opt.Dir == "" {
		return nil, fmt.Errorf("log dir required")
	}
	if opt.KeepDays <= 0 {
		opt.KeepDays = 14
	}
	if err := os.MkdirAll(opt.Dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{
		dir:           opt.Dir,
		keepDays:      opt.KeepDays,
		db:            opt.DB,
		prevFullBlock: make(map[string]bool),
		lastPlaySite:  make(map[string]time.Time),
	}
	now := time.Now()
	if err := s.rotateAll(now); err != nil {
		return nil, err
	}
	s.prune(now)
	return s, nil
}

// AttachStdLog redirects the standard library logger to the persistent app log.
// Under -H=windowsgui, writing stderr often errors; MultiWriter would then skip
// the file writer if stderr is first — so the app log is the primary sink.
func (s *Store) AttachStdLog() {
	if s == nil {
		return
	}
	log.SetOutput(s.AppWriter())
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
}

func (s *Store) AppWriter() io.Writer {
	return &lockedWriter{s: s, which: "app"}
}

type lockedWriter struct {
	s     *Store
	which string
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	_ = w.s.rotateAll(time.Now())
	switch w.which {
	case "app":
		return w.s.app.Write(p)
	default:
		return 0, fmt.Errorf("unknown writer")
	}
}

func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// DB returns the attached SQLite store (may be nil).
func (s *Store) DB() *store.DB {
	if s == nil {
		return nil
	}
	return s.db
}

func (s *Store) rotateAll(now time.Time) error {
	day := now.Format("2006-01-02")
	var err error
	if s.appDay != day {
		if s.app, err = reopen(s.app, filepath.Join(s.dir, "app-"+day+".log")); err != nil {
			return err
		}
		s.appDay = day
	}
	if s.reqDay != day {
		if s.req, err = reopen(s.req, filepath.Join(s.dir, "request-"+day+".jsonl")); err != nil {
			return err
		}
		s.reqDay = day
	}
	if s.evtDay != day {
		if s.evt, err = reopen(s.evt, filepath.Join(s.dir, "event-"+day+".jsonl")); err != nil {
			return err
		}
		s.evtDay = day
	}
	if s.watDay != day {
		if s.wat, err = reopen(s.wat, filepath.Join(s.dir, "watch-"+day+".jsonl")); err != nil {
			return err
		}
		s.watDay = day
	}
	if s.focDay != day {
		if s.foc, err = reopen(s.foc, filepath.Join(s.dir, "focus-"+day+".jsonl")); err != nil {
			return err
		}
		s.focDay = day
	}
	if s.playDay != day {
		if s.play, err = reopen(s.play, filepath.Join(s.dir, "play-"+day+".jsonl")); err != nil {
			return err
		}
		s.playDay = day
	}
	return nil
}

func reopen(old *os.File, path string) (*os.File, error) {
	if old != nil {
		_ = old.Close()
	}
	return os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

func (s *Store) prune(now time.Time) {
	cutoff := now.AddDate(0, 0, -s.keepDays)
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		idx := strings.Index(name, "-")
		if idx < 0 {
			continue
		}
		rest := strings.TrimSuffix(name[idx+1:], filepath.Ext(name))
		day, err := time.Parse("2006-01-02", rest)
		if err != nil {
			continue
		}
		if day.Before(cutoff) {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

func (s *Store) writeJSON(prefix string, rec map[string]any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	_ = s.rotateAll(now)
	if _, ok := rec["ts"]; !ok {
		rec["ts"] = now.Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	var file *os.File
	switch prefix {
	case "request":
		file = s.req
	case "event":
		file = s.evt
	case "watch":
		file = s.wat
	case "focus":
		file = s.foc
	case "play":
		file = s.play
	default:
		return
	}
	_, _ = file.Write(append(b, '\n'))
}

// Request logs a proxied decision (managed traffic).
func (s *Store) Request(fields map[string]any) {
	s.writeJSON("request", fields)
}

// Event logs lifecycle / policy transitions.
func (s *Store) Event(kind string, fields map[string]any) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["kind"] = kind
	if s != nil && s.db != nil {
		_ = s.db.InsertEvent(kind, fields)
	}
	s.writeJSON("event", fields)
}

// Focus logs one foreground-window session (written only when focus leaves the window).
func (s *Store) Focus(fields map[string]any) {
	if fields == nil {
		return
	}
	fields["kind"] = "focus_session"
	if s != nil && s.db != nil {
		rec := store.FocusRec{
			Bucket:      strField(fields, "bucket"),
			Group:       strField(fields, "group"),
			Title:       strField(fields, "title"),
			Exe:         strField(fields, "exe"),
			ExePath:     strField(fields, "exe_path"),
			HWND:        strField(fields, "hwnd"),
			ClassSource: strField(fields, "class_source"),
			IsBrowser:   boolField(fields, "is_browser"),
		}
		rec.PID = int64Field(fields, "pid")
		rec.DurationSec, _ = floatField(fields, "duration_sec")
		rec.StartedAt = parseFieldTime(fields, "started_at")
		rec.EndedAt = parseFieldTime(fields, "ended_at")
		_ = s.db.InsertFocus(rec)
		return // SQLite is the source of truth; skip JSONL
	}
	s.writeJSON("focus", fields)
}

// Watch logs video metadata (title/author/tags). Dedupes identical items within 10 minutes.
func (s *Store) Watch(fields map[string]any) {
	if s == nil || fields == nil {
		return
	}
	key, _ := fields["dedupe_key"].(string)
	if key == "" {
		site, _ := fields["site"].(string)
		vid, _ := fields["video_id"].(string)
		key = site + ":" + vid
	}
	s.mu.Lock()
	now := time.Now()
	if key != "" && key == s.lastWatchKey && now.Sub(s.lastWatchAt) < 10*time.Minute {
		s.mu.Unlock()
		return
	}
	s.lastWatchKey = key
	s.lastWatchAt = now
	s.mu.Unlock()
	delete(fields, "dedupe_key")
	fields["kind"] = "watch_meta"
	if s.db != nil {
		var tags []string
		if raw, ok := fields["tags"].([]string); ok {
			tags = raw
		} else if raw, ok := fields["tags"].([]any); ok {
			for _, v := range raw {
				if t, ok := v.(string); ok {
					tags = append(tags, t)
				}
			}
		}
		author := watchmeta.CleanAuthor(strField(fields, "author"))
		if author == "" {
			author = watchmeta.AuthorFromExemptLabel(strField(fields, "exempt_label"))
		}
		_ = s.db.UpsertWatch(store.WatchRec{
			DedupeKey:   key,
			Site:        strField(fields, "site"),
			VideoID:     strField(fields, "video_id"),
			Title:       strField(fields, "title"),
			Author:      author,
			Tags:        tags,
			URL:         strField(fields, "url"),
			Source:      strField(fields, "source"),
			WatchExempt: boolField(fields, "watch_exempt"),
			ExemptLabel: strField(fields, "exempt_label"),
			SeenAt:      now,
		})
		return
	}
	s.writeJSON("watch", fields)
}

// Play logs play traffic for duration stats.
// SQLite play_segments is primary; throttled play-*.jsonl is always kept as a
// recovery backup (so a mid-day empty DB / stats path bug cannot hide the day).
func (s *Store) Play(site string) {
	if s == nil || site == "" {
		return
	}
	now := time.Now()
	if s.db != nil {
		s.db.NotePlay(site, now)
	}
	s.mu.Lock()
	if last, ok := s.lastPlaySite[site]; ok && now.Sub(last) < 5*time.Second {
		s.mu.Unlock()
		return
	}
	s.lastPlaySite[site] = now
	s.mu.Unlock()
	s.writeJSON("play", map[string]any{
		"kind": "play_pulse",
		"site": site,
	})
}

// NoteFullBlock emits full_block_start once per transition into block for a site.
func (s *Store) NoteFullBlock(site string, active bool, until string) {
	if s == nil || site == "" {
		return
	}
	s.mu.Lock()
	was := s.prevFullBlock[site]
	s.prevFullBlock[site] = active
	s.mu.Unlock()
	if active && !was {
		s.Event("full_block_start", map[string]any{
			"site":  site,
			"until": until,
		})
	}
	if !active && was {
		s.Event("full_block_end", map[string]any{"site": site})
	}
}

// RecentEvents returns up to limit events from SQLite (preferred) or JSONL fallback.
func (s *Store) RecentEvents(limit int) []map[string]any {
	if s == nil || limit <= 0 {
		return nil
	}
	if s.db != nil {
		if ev := s.db.RecentEvents(limit); len(ev) > 0 {
			return ev
		}
	}
	now := time.Now()
	files := []string{
		filepath.Join(s.dir, "event-"+now.Format("2006-01-02")+".jsonl"),
		filepath.Join(s.dir, "event-"+now.AddDate(0, 0, -1).Format("2006-01-02")+".jsonl"),
	}
	var lines []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		parts := strings.Split(strings.TrimSpace(string(b)), "\n")
		lines = append(lines, parts...)
	}
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
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

// ListFiles returns log filenames sorted newest first.
func (s *Store) ListFiles() []string {
	if s == nil {
		return nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	return names
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	for _, f := range []*os.File{s.app, s.req, s.evt, s.wat, s.foc, s.play} {
		if f != nil {
			if e := f.Close(); e != nil && err == nil {
				err = e
			}
		}
	}
	return err
}

// Logger is kept as an alias-compatible wrapper for older call sites using Event().
type Logger = Store

// New keeps backward-compatible constructor used by main (writes into given dir).
func New(dir string) (*Store, error) {
	return NewStore(Options{Dir: dir, KeepDays: 14})
}

func strField(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return v
}

func boolField(m map[string]any, k string) bool {
	v, _ := m[k].(bool)
	return v
}

func int64Field(m map[string]any, k string) int64 {
	switch t := m[k].(type) {
	case int:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	default:
		return 0
	}
}

func floatField(m map[string]any, k string) (float64, bool) {
	switch t := m[k].(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	default:
		return 0, false
	}
}

func parseFieldTime(m map[string]any, k string) time.Time {
	s, _ := m[k].(string)
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
