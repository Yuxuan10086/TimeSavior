package policy

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"timesavior/internal/config"
	"timesavior/internal/llm"
	"timesavior/internal/rules"
)

type Decision string

const (
	Allow Decision = "allow"
	Block Decision = "block"
)

type Reason string

const (
	ReasonFreeMode     Reason = "free_mode"
	ReasonTempUnlock   Reason = "temp_unlock"
	ReasonProbeOnly    Reason = "probe_only"
	ReasonFullBlock    Reason = "full_block"
	ReasonFeedBlocked  Reason = "feed_blocked"
	ReasonUnknownBlock Reason = "unknown_blocked"
	ReasonAllowed      Reason = "allowed"
	ReasonUnmanaged    Reason = "unmanaged"
)

type Result struct {
	Decision Decision
	Reason   Reason
	Site     string
	Category rules.Category
}

// siteState tracks continuous watch (play/CDN), not blocked feed attempts.
type siteState struct {
	watchStarted   time.Time // start of confirmed continuous watch (≥ min_watch_sec)
	provStarted    time.Time // provisional burst start (prefetch filtered until confirmed)
	lastPlayAt     time.Time
	fullBlockUntil time.Time // cleared at next 04:00 day boundary
	watchExempt    bool      // whitelist channel/video/title: do not score watch time
	exemptLabel    string
	exemptUntil    time.Time
	judgePending   bool // LLM judgment in flight for this site
	sessionClips   []sessionClip
}

// ChangeKind identifies what changed for live UI subscribers.
type ChangeKind string

const (
	ChangeStatus ChangeKind = "status"
	ChangeUnlock ChangeKind = "unlock"
	ChangeLLMLog ChangeKind = "llm_log"
)

type Engine struct {
	cfg   *config.Config
	store *rules.Store

	mu                  sync.Mutex
	sites               map[string]*siteState
	tempUnlockUntil     time.Time
	tempUnlockStartedAt time.Time // first feed open that started today's unlock (zero if not started)
	tempUnlockCount     int       // per policy day: 0 = not started, >=1 = already started/used
	tempUnlockWeekKey   string
	tempUnlockWeekCount int // retained for status/compat; feed unlock is once per policy day
	policyDay           string
	challenge           *challengeSession

	runtimeStore RuntimeStore
	persistTimer *time.Timer

	llmClient *llm.Client
	llmUsage  LLMUsageStore

	onChange func(ChangeKind)
	notifyAt time.Time // last status notify (debounce)
}

func New(cfg *config.Config, store *rules.Store) *Engine {
	e := &Engine{
		cfg:   cfg,
		store: store,
		sites: make(map[string]*siteState),
	}
	e.SyncWatchRules()
	return e
}

// SyncWatchRules merges config + legacy site channel lists into the unified whitelist table.
func (e *Engine) SyncWatchRules() {
	if e == nil || e.cfg == nil {
		return
	}
	siteCh := map[string][]string{}
	if e.store != nil {
		for _, site := range e.store.Sites() {
			var chs []string
			for _, c := range site.WatchWhitelist.Channels {
				c = strings.TrimSpace(c)
				if c != "" {
					chs = append(chs, c)
				}
			}
			if len(chs) > 0 {
				siteCh[site.Name] = chs
			}
		}
	}
	e.cfg.SyncWatchRules(siteCh)
}

// SetOnChange registers a listener for policy / unlock state changes (SSE).
func (e *Engine) SetOnChange(fn func(ChangeKind)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.onChange = fn
	e.mu.Unlock()
}

func (e *Engine) notifyLocked(kind ChangeKind, immediate bool) {
	fn := e.onChange
	if fn == nil {
		return
	}
	if !immediate && kind == ChangeStatus {
		now := time.Now()
		if now.Sub(e.notifyAt) < 500*time.Millisecond {
			return
		}
		e.notifyAt = now
	}
	go fn(kind)
}

// PolicyDayKey: calendar day that starts at DayStartHour (default 04:00).
func PolicyDayKey(now time.Time, dayStartHour int) string {
	if dayStartHour < 0 || dayStartHour > 23 {
		dayStartHour = 4
	}
	shifted := now.Add(-time.Duration(dayStartHour) * time.Hour)
	return shifted.Format("2006-01-02")
}

// NextDayBoundary returns the next local DayStartHour after now (or equal if exactly on boundary+epsilon).
func NextDayBoundary(now time.Time, dayStartHour int) time.Time {
	if dayStartHour < 0 || dayStartHour > 23 {
		dayStartHour = 4
	}
	y, m, d := now.Date()
	loc := now.Location()
	boundary := time.Date(y, m, d, dayStartHour, 0, 0, 0, loc)
	if !now.Before(boundary) {
		boundary = boundary.AddDate(0, 0, 1)
	}
	return boundary
}

func (e *Engine) MatchSite(host, path string) (*rules.SiteRule, rules.Category) {
	return e.store.Match(host, path)
}

func (e *Engine) dayStartHour() int {
	h := e.cfg.DayStartHour
	if h <= 0 {
		return 4
	}
	return h
}

func (e *Engine) InFreeWindow(now time.Time) bool {
	for _, w := range e.cfg.FreeWindows {
		if !weekdayAllowed(now.Weekday(), w.Weekdays) {
			continue
		}
		if inWindow(now, w.Start, w.End) {
			return true
		}
	}
	return false
}

// unlockWindowStartHM returns minutes-from-midnight for feed-unlock open time (default 21:30).
func (e *Engine) unlockWindowStartHM() int {
	raw := "21:30"
	if e != nil && e.cfg != nil && strings.TrimSpace(e.cfg.TempUnlockWindowStart) != "" {
		raw = e.cfg.TempUnlockWindowStart
	}
	mins, ok := parseHM(raw)
	if !ok {
		return 9*60 + 30
	}
	return mins
}

// unlockMinutes returns the feed-triggered unlock duration (default 30).
func (e *Engine) unlockMinutes() int {
	if e != nil && e.cfg != nil && e.cfg.TempUnlockMinutes > 0 {
		return e.cfg.TempUnlockMinutes
	}
	return 30
}

// FeedUnlockWindowBounds returns [open, close) for the current policy day.
// Open = policy-day calendar date at TempUnlockWindowStart; close = next DayStartHour.
func (e *Engine) FeedUnlockWindowBounds(now time.Time) (open, close time.Time) {
	dayStart := e.dayStartHour()
	key := PolicyDayKey(now, dayStart)
	t, err := time.ParseInLocation("2006-01-02", key, now.Location())
	if err != nil {
		return time.Time{}, time.Time{}
	}
	hm := e.unlockWindowStartHM()
	open = time.Date(t.Year(), t.Month(), t.Day(), hm/60, hm%60, 0, 0, now.Location())
	close = NextDayBoundary(open, dayStart)
	return open, close
}

// InFeedUnlockWindow is true from window start (default 21:30) until next policy day boundary (04:00).
func (e *Engine) InFeedUnlockWindow(now time.Time) bool {
	open, close := e.FeedUnlockWindowBounds(now)
	if open.IsZero() || close.IsZero() {
		return false
	}
	return !now.Before(open) && now.Before(close)
}

// maybeStartFeedUnlockLocked starts today's 30-minute unlock on first managed-site feed hit
// inside the available window. Caller holds e.mu. Returns true if unlock is (now) active.
func (e *Engine) maybeStartFeedUnlockLocked(now time.Time) bool {
	if e.cfg != nil && !e.cfg.TempUnlockEnabled {
		return now.Before(e.tempUnlockUntil)
	}
	if now.Before(e.tempUnlockUntil) {
		return true
	}
	if e.tempUnlockCount > 0 {
		// Already started/used this policy day (including cancelled or expired).
		return false
	}
	if !e.InFeedUnlockWindow(now) {
		return false
	}
	mins := e.unlockMinutes()
	until := now.Add(time.Duration(mins) * time.Minute)
	_, close := e.FeedUnlockWindowBounds(now)
	if !close.IsZero() && until.After(close) {
		until = close
	}
	e.tempUnlockCount = 1
	e.tempUnlockWeekCount++
	e.tempUnlockStartedAt = now
	e.tempUnlockUntil = until
	e.ClearFullBlockLocked()
	if e.challenge == nil {
		e.challenge = &challengeSession{}
	}
	e.challenge.phase = challengeSuccess
	e.challenge.question = nil
	e.challenge.streak = 0
	e.challenge.lastMsg = fmt.Sprintf("推荐页已打开，临时解锁 %d 分钟", mins)
	// Persist start immediately so restart mid-window keeps until/started.
	e.cancelPersistTimerLocked()
	if e.runtimeStore != nil {
		if err := e.runtimeStore.SaveRuntime(e.snapshotLocked()); err != nil {
			log.Printf("policy: feed-unlock persist: %v", err)
		}
	}
	e.notifyLocked(ChangeUnlock, true)
	e.notifyLocked(ChangeStatus, true)
	return true
}

func weekdayAllowed(wd time.Weekday, filter []string) bool {
	if len(filter) == 0 {
		return true
	}
	for _, raw := range filter {
		if matchWeekday(wd, raw) {
			return true
		}
	}
	return false
}

func matchWeekday(wd time.Weekday, raw string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "0", "sun", "sunday":
		return wd == time.Sunday
	case "1", "mon", "monday":
		return wd == time.Monday
	case "2", "tue", "tues", "tuesday":
		return wd == time.Tuesday
	case "3", "wed", "wednesday":
		return wd == time.Wednesday
	case "4", "thu", "thur", "thurs", "thursday":
		return wd == time.Thursday
	case "5", "fri", "friday":
		return wd == time.Friday
	case "6", "sat", "saturday":
		return wd == time.Saturday
	default:
		return false
	}
}

func inWindow(now time.Time, start, end string) bool {
	s, ok1 := parseHM(start)
	en, ok2 := parseHM(end)
	if !ok1 || !ok2 {
		return false
	}
	mins := now.Hour()*60 + now.Minute()
	if s <= en {
		return mins >= s && mins < en
	}
	return mins >= s || mins < en
}

func parseHM(s string) (mins int, ok bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

func (e *Engine) getSite(name string) *siteState {
	st, ok := e.sites[name]
	if !ok {
		st = &siteState{}
		e.sites[name] = st
	}
	return st
}

func (e *Engine) resetSiteLocked(name string) {
	st := e.getSite(name)
	if st.watchStarted.IsZero() && st.provStarted.IsZero() && st.lastPlayAt.IsZero() && st.fullBlockUntil.IsZero() &&
		!st.watchExempt && st.exemptLabel == "" && st.exemptUntil.IsZero() &&
		!st.judgePending && len(st.sessionClips) == 0 {
		return
	}
	st.watchStarted = time.Time{}
	st.provStarted = time.Time{}
	st.lastPlayAt = time.Time{}
	st.fullBlockUntil = time.Time{}
	st.watchExempt = false
	st.exemptLabel = ""
	st.exemptUntil = time.Time{}
	st.judgePending = false
	st.sessionClips = nil
	e.schedulePersistLocked()
}

func (e *Engine) minWatchDuration() time.Duration {
	sec := 15
	if e != nil && e.cfg != nil && e.cfg.MinWatchSec > 0 {
		sec = e.cfg.MinWatchSec
	}
	return time.Duration(sec) * time.Second
}

func (e *Engine) recordWatch(site *rules.SiteRule, st *siteState, now time.Time) {
	idle := time.Duration(site.EntertainmentSession.WatchIdleResetMinutes) * time.Minute
	if idle <= 0 {
		idle = 10 * time.Minute
	}
	needMin := site.EntertainmentSession.WatchBlockAfterMinutes
	if needMin <= 0 {
		needMin = 20
	}
	need := time.Duration(needMin) * time.Minute
	minWatch := e.minWatchDuration()
	// Before confirmation, require tighter continuity so sparse prefetch cannot pad wall-clock.
	provIdle := minWatch * 2
	if provIdle < 30*time.Second {
		provIdle = 30 * time.Second
	}
	if provIdle > idle {
		provIdle = idle
	}

	if st.lastPlayAt.IsZero() || now.Sub(st.lastPlayAt) > idle {
		// New continuous session (provisional until ≥ min_watch_sec of dense play).
		st.watchStarted = time.Time{}
		st.provStarted = now
		st.sessionClips = nil
		st.judgePending = false
	} else if st.watchStarted.IsZero() && !st.provStarted.IsZero() && now.Sub(st.lastPlayAt) > provIdle {
		// Isolated short burst ended; do not accumulate toward FullBlock.
		st.provStarted = now
		st.sessionClips = nil
		st.judgePending = false
	}
	st.lastPlayAt = now

	if st.provStarted.IsZero() {
		st.provStarted = now
	}
	if st.watchStarted.IsZero() {
		if minWatch <= 0 || now.Sub(st.provStarted) >= minWatch {
			st.watchStarted = st.provStarted
		}
	}
	if !st.watchStarted.IsZero() && now.Sub(st.watchStarted) >= need {
		// Threshold reached: LLM judge (or immediate FullBlock if LLM unavailable / circuit).
		_ = e.maybeStartJudgeLocked(site.Name, st, needMin, now)
	}
	e.schedulePersistLocked()
}

func (e *Engine) rollPolicyDay(now time.Time) {
	day := PolicyDayKey(now, e.dayStartHour())
	rolled := false
	if e.policyDay == "" {
		e.policyDay = day
		rolled = true
	} else if day != e.policyDay {
		// New day at 04:00: clear full blocks, watch sessions, daily feed-unlock quota.
		// Keep an still-active temp unlock (e.g. multi-day medical leave) across the boundary.
		e.policyDay = day
		keepUnlock := now.Before(e.tempUnlockUntil)
		if !keepUnlock {
			e.tempUnlockCount = 0
			e.tempUnlockUntil = time.Time{}
			e.tempUnlockStartedAt = time.Time{}
			e.challenge = &challengeSession{phase: challengeIdle}
		} else if e.tempUnlockCount == 0 {
			e.tempUnlockCount = 1
		}
		for _, st := range e.sites {
			st.fullBlockUntil = time.Time{}
			st.watchStarted = time.Time{}
			st.provStarted = time.Time{}
			st.lastPlayAt = time.Time{}
			st.judgePending = false
			st.sessionClips = nil
		}
		rolled = true
	}
	weekBefore := e.tempUnlockWeekKey
	e.rollUnlockWeekLocked(now)
	if rolled || e.tempUnlockWeekKey != weekBefore {
		e.schedulePersistLocked()
	}
}

func (e *Engine) rollUnlockWeekLocked(now time.Time) {
	key := CalendarWeekKey(now)
	if e.tempUnlockWeekKey == "" {
		e.tempUnlockWeekKey = key
		return
	}
	if key == e.tempUnlockWeekKey {
		return
	}
	e.tempUnlockWeekKey = key
	e.tempUnlockWeekCount = 0
}

// CalendarWeekKey is Monday-based local week id (YYYY-MM-DD of that Monday).
func CalendarWeekKey(now time.Time) string {
	d := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	wd := int(d.Weekday())
	if wd == 0 {
		wd = 7
	}
	monday := d.AddDate(0, 0, -(wd - 1))
	return monday.Format("2006-01-02")
}

func (e *Engine) ClearFullBlock(site string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if site == "" {
		e.ClearFullBlockLocked()
		return
	}
	e.resetSiteLocked(site)
}

func (e *Engine) ClearFullBlockLocked() {
	for name := range e.sites {
		e.resetSiteLocked(name)
	}
}

func (e *Engine) Status(now time.Time) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rollPolicyDay(now)
	sites := map[string]any{}
	for name, st := range e.sites {
		watched := time.Duration(0)
		if !st.watchStarted.IsZero() && !st.lastPlayAt.IsZero() {
			watched = st.lastPlayAt.Sub(st.watchStarted)
		}
		sites[name] = map[string]any{
			"watch_minutes":     int(watched.Minutes()),
			"last_play_at":      formatTime(st.lastPlayAt),
			"full_block_until":  formatTime(st.fullBlockUntil),
			"full_block_active": now.Before(st.fullBlockUntil),
			"watch_exempt":      st.watchExempt && (st.exemptUntil.IsZero() || now.Before(st.exemptUntil)),
			"exempt_label":      st.exemptLabel,
			"judge_pending":     st.judgePending,
		}
	}
	phase := challengeIdle
	streak := 0
	if e.challenge != nil {
		// Expire success → idle so Status matches challenge APIs.
		if e.challenge.phase == challengeSuccess && !now.Before(e.tempUnlockUntil) {
			e.challenge.phase = challengeIdle
		}
		phase = e.challenge.phase
		streak = e.challenge.streak
	}
	winOpen, winClose := e.FeedUnlockWindowBounds(now)
	return map[string]any{
		"policy_day":               PolicyDayKey(now, e.dayStartHour()),
		"day_start_hour":           e.dayStartHour(),
		"free_mode":                e.InFreeWindow(now),
		"feed_unlock_window":       e.InFeedUnlockWindow(now),
		"feed_unlock_window_start": formatTime(winOpen),
		"feed_unlock_window_end":   formatTime(winClose),
		"temp_unlock_started":      e.tempUnlockCount > 0,
		"temp_unlock_started_at":   formatTime(e.tempUnlockStartedAt),
		"temp_unlock_until":        formatTime(e.tempUnlockUntil),
		"temp_unlock_active":       now.Before(e.tempUnlockUntil),
		"temp_unlock_used":         e.tempUnlockCount,
		"temp_unlock_limit":        e.cfg.TempUnlockDailyLimit,
		"temp_unlock_minutes":      e.unlockMinutes(),
		"temp_unlock_week_used":    e.tempUnlockWeekCount,
		"temp_unlock_week_limit":   e.weekLimit(),
		"challenge_phase":          phase,
		"challenge_streak":         streak,
		"sites":                    sites,
		"llm":                      e.llmStatusLocked(now),
	}
}

func (e *Engine) weekLimit() int {
	if e.cfg.TempUnlockWeeklyLimit > 0 {
		return e.cfg.TempUnlockWeeklyLimit
	}
	return 1
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

func (e *Engine) FullBlockActive(site string, now time.Time) (active bool, until time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rollPolicyDay(now)
	st := e.getSite(site)
	if now.Before(st.fullBlockUntil) {
		return true, st.fullBlockUntil
	}
	return false, time.Time{}
}

