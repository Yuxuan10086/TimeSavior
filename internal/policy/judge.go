package policy

import (
	"context"
	"log"
	"strings"
	"time"

	"timesavior/internal/llm"
	"timesavior/internal/store"
)

// LLMUsageStore persists AI cost accounting and judge dialogs.
type LLMUsageStore interface {
	AddLLMUsage(day string, costCNY float64) error
	InsertLLMDialog(in store.LLMDialogInput) error
	LLMUsage(day string) store.LLMUsageSnapshot
}

// AttachLLM wires the SiliconFlow client and usage store for watch judgment.
func (e *Engine) AttachLLM(client *llm.Client, usage LLMUsageStore) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.llmClient = client
	e.llmUsage = usage
}

func (e *Engine) llmReadyLocked() bool {
	if e.cfg == nil || !e.cfg.LLMJudge.Enabled {
		return false
	}
	if e.llmClient == nil || e.cfg.LLMJudge.APIKey == "" {
		return false
	}
	return true
}

func (e *Engine) circuitOpenLocked(now time.Time) bool {
	if e.llmUsage == nil || e.cfg == nil {
		return false
	}
	if !e.cfg.LLMJudge.CostMonitorOn() {
		return false
	}
	day := PolicyDayKey(now, e.dayStartHour())
	u := e.llmUsage.LLMUsage(day)
	limit := e.cfg.LLMJudge.DailyCostLimitCNY
	if limit <= 0 {
		limit = 0.10
	}
	return u.DayCostCNY >= limit
}

// LLMStatus returns cost / circuit fields for dashboard and SSE.
func (e *Engine) LLMStatus(now time.Time) map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.llmStatusLocked(now)
}

func (e *Engine) llmStatusLocked(now time.Time) map[string]any {
	day := PolicyDayKey(now, e.dayStartHour())
	limit := 0.10
	enabled := false
	monitor := false
	fallbackBlock := true
	if e.cfg != nil {
		enabled = e.cfg.LLMJudge.Enabled && e.cfg.LLMJudge.APIKey != ""
		monitor = e.cfg.LLMJudge.CostMonitorOn()
		if e.cfg.LLMJudge.DailyCostLimitCNY > 0 {
			limit = e.cfg.LLMJudge.DailyCostLimitCNY
		}
		fallbackBlock = e.cfg.LLMJudge.FallbackBlocks()
	}
	var snap store.LLMUsageSnapshot
	if e.llmUsage != nil {
		snap = e.llmUsage.LLMUsage(day)
	}
	circuit := monitor && snap.DayCostCNY >= limit && limit > 0
	status := "ok"
	switch {
	case !enabled:
		status = "disabled"
	case circuit:
		status = "circuit_open"
	case !monitor:
		status = "monitor_off"
	}
	return map[string]any{
		"enabled":             enabled,
		"cost_monitor":        monitor,
		"cost_estimated_only": !monitor,
		"status":              status,
		"circuit_open":        circuit,
		"fallback_block":      fallbackBlock,
		"day":                 day,
		"day_cost_cny":        roundCost(snap.DayCostCNY),
		"day_calls":           snap.DayCalls,
		"total_cost_cny":      roundCost(snap.TotalCostCNY),
		"total_calls":         snap.TotalCalls,
		"daily_limit_cny":     limit,
		"model":               llmModelName(e),
		"judge_pending_sites": e.pendingSitesLocked(),
	}
}

func llmModelName(e *Engine) string {
	if e == nil || e.cfg == nil {
		return ""
	}
	return e.cfg.LLMJudge.Model
}

func (e *Engine) pendingSitesLocked() []string {
	var out []string
	for name, st := range e.sites {
		if st.judgePending {
			out = append(out, name)
		}
	}
	return out
}

func roundCost(v float64) float64 {
	// 6 decimal places is enough for token-level CNY.
	return float64(int(v*1e6+0.5)) / 1e6
}

type sessionClip struct {
	Title     string
	Author    string
	Tags      []string
	VideoID   string
	FirstSeen time.Time
	LastSeen  time.Time
}

func (c *sessionClip) durationSec() float64 {
	if c == nil {
		return 0
	}
	d := c.LastSeen.Sub(c.FirstSeen).Seconds()
	if d < 1 {
		return 1
	}
	return d
}

// NoteWatchMeta records title/author for the current watch window and applies title whitelist.
// Returns whether the title matched the title whitelist (exempt).
func (e *Engine) NoteWatchMeta(site, title, author string, tags []string, videoID string, now time.Time) (exempt bool, label string) {
	if e == nil || site == "" {
		return false, ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rollPolicyDay(now)
	st := e.getSite(site)
	e.rememberClipLocked(st, title, author, tags, videoID, now)

	if e.cfg != nil {
		if hit, label := e.cfg.MatchWatchMeta(site, title, author); hit {
			st.watchExempt = true
			st.exemptLabel = label
			st.exemptUntil = now.Add(20 * time.Minute)
			e.schedulePersistLocked()
			return true, st.exemptLabel
		}
	}
	return false, ""
}

func (e *Engine) rememberClipLocked(st *siteState, title, author string, tags []string, videoID string, now time.Time) {
	if st == nil {
		return
	}
	key := strings.TrimSpace(videoID)
	if key == "" {
		key = strings.TrimSpace(title)
	}
	if key == "" {
		return
	}
	for i := range st.sessionClips {
		c := &st.sessionClips[i]
		ck := c.VideoID
		if ck == "" {
			ck = c.Title
		}
		if ck == key {
			if title != "" {
				c.Title = title
			}
			if author != "" {
				c.Author = author
			}
			if len(tags) > 0 {
				c.Tags = append([]string{}, tags...)
			}
			c.LastSeen = now
			return
		}
	}
	st.sessionClips = append(st.sessionClips, sessionClip{
		Title:     title,
		Author:    author,
		Tags:      append([]string{}, tags...),
		VideoID:   videoID,
		FirstSeen: now,
		LastSeen:  now,
	})
	// Cap memory.
	if len(st.sessionClips) > 40 {
		st.sessionClips = st.sessionClips[len(st.sessionClips)-40:]
	}
}

func (e *Engine) clipsSinceLocked(st *siteState, since time.Time) []llm.Clip {
	minSec := 0.0
	if e != nil {
		minSec = e.minWatchDuration().Seconds()
	}
	var out []llm.Clip
	for _, c := range st.sessionClips {
		if !since.IsZero() && c.LastSeen.Before(since) {
			continue
		}
		dur := c.durationSec()
		// Drop preloads / fleeting plays below min_watch_sec (same as stats).
		if minSec > 0 && dur < minSec {
			continue
		}
		out = append(out, llm.Clip{
			Title:       c.Title,
			Author:      c.Author,
			Tags:        append([]string{}, c.Tags...),
			VideoID:     c.VideoID,
			DurationSec: dur,
		})
	}
	return out
}

// maybeStartJudgeLocked is called when continuous watch crosses the threshold.
// Caller holds e.mu. Returns true if FullBlock was applied synchronously.
func (e *Engine) maybeStartJudgeLocked(siteName string, st *siteState, needMinutes int, now time.Time) bool {
	if st.judgePending {
		return false
	}
	ready := e.llmReadyLocked()
	circuit := e.circuitOpenLocked(now)
	fallbackBlock := true
	if e.cfg != nil {
		fallbackBlock = e.cfg.LLMJudge.FallbackBlocks()
	}

	if !ready || circuit {
		if !ready || fallbackBlock {
			st.fullBlockUntil = NextDayBoundary(now, e.dayStartHour())
			st.watchStarted = time.Time{}
			st.provStarted = time.Time{}
			st.lastPlayAt = time.Time{}
			st.sessionClips = nil
			st.judgePending = false
			return true
		}
		// Circuit open but configured to allow: reset timer without block.
		st.watchStarted = now
		st.provStarted = now
		st.lastPlayAt = now
		st.sessionClips = nil
		return false
	}

	clips := e.clipsSinceLocked(st, st.watchStarted)
	st.judgePending = true
	windowMin := needMinutes
	if windowMin <= 0 {
		windowMin = 20
	}
	client := e.llmClient
	usage := e.llmUsage
	day := PolicyDayKey(now, e.dayStartHour())
	trackBalance := e.cfg != nil && e.cfg.LLMJudge.CostMonitorOn()
	go e.runJudge(siteName, clips, windowMin, day, client, usage, trackBalance)
	return false
}

func (e *Engine) runJudge(site string, clips []llm.Clip, windowMin int, day string, client *llm.Client, usage LLMUsageStore, trackBalance bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()

	userPrompt := llm.BuildUserPrompt(site, windowMin, clips)
	requestRaw := llm.FormatMessagesRequest(llm.SystemPrompt, userPrompt)
	videosJSON := store.VideosJSONFromClips(clipsToMaps(clips))

	var (
		cost    float64
		allow   *bool
		reason  string
		errMsg  string
		respRaw string
	)
	res, err := client.Judge(ctx, site, windowMin, clips, trackBalance)
	now := time.Now()
	if err != nil {
		errMsg = err.Error()
		log.Printf("llm judge %s: %v", site, err)
		// Safe fallback: treat failure as block.
		a := false
		allow = &a
		reason = "判定失败，默认拦截"
		respRaw = ""
	} else {
		a := res.Verdict.Allow
		allow = &a
		reason = res.Verdict.Reason
		cost = res.CostCNY
		respRaw = res.RawContent
		if res.CostEstimated {
			log.Printf("llm judge %s: cost ¥%.6f (token estimate only)", site, cost)
		}
	}
	if usage != nil {
		_ = usage.AddLLMUsage(day, cost)
		_ = usage.InsertLLMDialog(store.LLMDialogInput{
			Day:         day,
			Site:        site,
			Kind:        store.LLMKindWatchJudge,
			CostCNY:     cost,
			Allow:       allow,
			Reason:      reason,
			ErrMsg:      errMsg,
			RequestRaw:  requestRaw,
			ResponseRaw: respRaw,
			VideosJSON:  videosJSON,
			At:          now,
		})
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	st := e.getSite(site)
	st.judgePending = false
	if allow != nil && *allow {
		st.watchStarted = now
		st.provStarted = now
		st.lastPlayAt = now
		st.sessionClips = nil
		log.Printf("llm judge %s: allow (%s)", site, reason)
	} else {
		st.fullBlockUntil = NextDayBoundary(now, e.dayStartHour())
		st.watchStarted = time.Time{}
		st.provStarted = time.Time{}
		st.lastPlayAt = time.Time{}
		st.sessionClips = nil
		log.Printf("llm judge %s: block (%s)", site, reason)
	}
	e.schedulePersistLocked()
	e.notifyLocked(ChangeStatus, true)
	e.notifyLocked(ChangeLLMLog, true)
}

func clipsToMaps(clips []llm.Clip) []map[string]any {
	out := make([]map[string]any, 0, len(clips))
	for _, c := range clips {
		m := map[string]any{
			"title":        c.Title,
			"duration_sec": c.DurationSec,
		}
		if c.Author != "" {
			m["author"] = c.Author
		}
		if len(c.Tags) > 0 {
			m["tags"] = c.Tags
		}
		out = append(out, m)
	}
	return out
}
