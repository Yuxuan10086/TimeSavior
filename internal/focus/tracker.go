package focus

import (
	"log"
	"sync"
	"time"

	"timesavior/internal/activity"
	"timesavior/internal/audit"
	"timesavior/internal/watchmeta"
)

// Tracker polls the foreground window at a fixed rate and logs only
// session boundaries (activation start/end), not every poll tick.
// Browser tab switches that change the stats bucket also end a session.
type Tracker struct {
	audit        *audit.Store
	recent       *activity.Recent
	interval     time.Duration
	enabled      bool
	onWatchTitle func(site, title string) (exempt bool, label string)

	mu      sync.Mutex
	stopCh  chan struct{}
	doneCh  chan struct{}
	current *openSession
}

type openSession struct {
	info           Info
	class          Classification
	startedAt      time.Time
	lastTitle      string
	lastNotedTitle string // last title fed to policy/LLM; audit only on change
}

// Options configures the tracker.
type Options struct {
	Enabled  bool
	Interval time.Duration // default 2s = 0.5Hz
	Recent   *activity.Recent
	// OnWatchTitle is called when a browser video window title is derived (for title whitelist / LLM context).
	// Invoked live while the window stays focused (not only on switch), so 20-minute judges see titles.
	OnWatchTitle func(site, title string) (exempt bool, label string)
}

func NewTracker(al *audit.Store, opt Options) *Tracker {
	if opt.Interval <= 0 {
		opt.Interval = 2 * time.Second
	}
	return &Tracker{
		audit:        al,
		recent:       opt.Recent,
		interval:     opt.Interval,
		enabled:      opt.Enabled,
		onWatchTitle: opt.OnWatchTitle,
	}
}

func (t *Tracker) Start() {
	if t == nil || !t.enabled {
		return
	}
	t.mu.Lock()
	if t.stopCh != nil {
		t.mu.Unlock()
		return
	}
	t.stopCh = make(chan struct{})
	t.doneCh = make(chan struct{})
	t.mu.Unlock()

	log.Printf("focus tracker started (interval=%s); sessions logged on switch only", t.interval)
	go t.loop()
}

func (t *Tracker) Stop() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.stopCh == nil {
		t.mu.Unlock()
		return
	}
	close(t.stopCh)
	done := t.doneCh
	t.mu.Unlock()
	<-done
}

func (t *Tracker) loop() {
	defer close(t.doneCh)
	ticker := time.NewTicker(t.interval)
	defer ticker.Stop()
	t.tick(time.Now())
	for {
		select {
		case <-t.stopCh:
			t.mu.Lock()
			t.flushLocked(time.Now())
			t.mu.Unlock()
			return
		case now := <-ticker.C:
			t.tick(now)
		}
	}
}

func (t *Tracker) classify(info Info, now time.Time) Classification {
	var hits []activity.Hit
	if t.recent != nil {
		hits = t.recent.Snapshot(now)
	}
	return ClassifyEx(info.ExeName, info.Title, hits)
}

func (t *Tracker) tick(now time.Time) {
	info, err := Foreground()
	if err != nil || info.HWND == 0 {
		t.mu.Lock()
		t.flushLocked(now)
		t.mu.Unlock()
		return
	}
	class := t.classify(info, now)
	key := info.Key()

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.current != nil && t.current.info.Key() == key && t.current.class.Bucket == class.Bucket {
		if info.Title != "" {
			t.current.lastTitle = info.Title
			t.current.info.Title = info.Title
		}
		// Re-classify when proxy activity may have changed the bucket source.
		t.current.class = class
		// Keep feeding watch titles while staying on the same window (LLM clips + whitelist).
		t.noteLiveWatchTitleLocked(t.current.lastTitle, class, false)
		return
	}
	t.flushLocked(now)
	t.current = &openSession{
		info:      info,
		class:     class,
		startedAt: now,
		lastTitle: info.Title,
	}
	t.noteLiveWatchTitleLocked(info.Title, class, true)
}

// noteLiveWatchTitleLocked pushes browser video titles into policy while focused.
// Always calls OnWatchTitle (extends clip LastSeen); audit.Watch only when the derived title changes.
func (t *Tracker) noteLiveWatchTitleLocked(title string, class Classification, forceAudit bool) {
	if class.Group != "browser_video" || t.onWatchTitle == nil {
		return
	}
	meta := watchmeta.FromWindowTitle(title)
	if meta == nil || !meta.Useful() {
		return
	}
	ex, label := t.onWatchTitle(meta.Site, meta.Title)
	changed := t.current == nil || t.current.lastNotedTitle != meta.Title
	if t.current != nil {
		t.current.lastNotedTitle = meta.Title
	}
	if t.audit == nil || (!forceAudit && !changed) {
		return
	}
	fields := map[string]any{
		"site":       meta.Site,
		"video_id":   meta.VideoID,
		"title":      meta.Title,
		"author":     meta.Author,
		"tags":       meta.Tags,
		"url":        meta.URL,
		"source":     meta.Source,
		"dedupe_key": meta.DedupeKey(),
	}
	if ex {
		fields["watch_exempt"] = true
		fields["exempt_label"] = label
	}
	t.audit.Watch(fields)
}

// Snapshot returns the in-progress focus session as a focus_session map (for live stats).
func (t *Tracker) Snapshot(now time.Time) map[string]any {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return nil
	}
	cur := t.current
	if !now.After(cur.startedAt) {
		now = cur.startedAt.Add(t.interval)
	}
	title := cur.lastTitle
	if title == "" {
		title = cur.info.Title
	}
	class := cur.class
	if class.Bucket == "" {
		class = t.classify(cur.info, now)
	}
	return map[string]any{
		"title":        title,
		"exe":          cur.info.ExeName,
		"exe_path":     cur.info.ExePath,
		"pid":          cur.info.PID,
		"hwnd":         cur.info.Key(),
		"bucket":       class.Bucket,
		"group":        class.Group,
		"is_browser":   class.IsBrowser,
		"class_source": class.Source,
		"started_at":   cur.startedAt.Format(time.RFC3339Nano),
		"ended_at":     now.Format(time.RFC3339Nano),
		"duration_sec": now.Sub(cur.startedAt).Seconds(),
		"open":         true,
	}
}

func (t *Tracker) flushLocked(now time.Time) {
	if t.current == nil {
		return
	}
	cur := t.current
	t.current = nil
	ended := now
	if !ended.After(cur.startedAt) {
		ended = cur.startedAt.Add(t.interval)
	}
	dur := ended.Sub(cur.startedAt).Seconds()
	title := cur.lastTitle
	if title == "" {
		title = cur.info.Title
	}
	class := cur.class
	if class.Bucket == "" {
		class = Classify(cur.info.ExeName, title)
	}
	if class.Group == "browser_video" {
		// End-of-session note (covers final title if live ticks missed it).
		t.noteLiveWatchTitleLocked(title, class, true)
	}
	if t.audit == nil {
		return
	}
	t.audit.Focus(map[string]any{
		"title":        title,
		"exe":          cur.info.ExeName,
		"exe_path":     cur.info.ExePath,
		"pid":          cur.info.PID,
		"hwnd":         cur.info.Key(),
		"bucket":       class.Bucket,
		"group":        class.Group,
		"is_browser":   class.IsBrowser,
		"class_source": class.Source,
		"started_at":   cur.startedAt.Format(time.RFC3339Nano),
		"ended_at":     ended.Format(time.RFC3339Nano),
		"duration_sec": dur,
	})
}
