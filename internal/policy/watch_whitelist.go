package policy

import (
	"bytes"
	"net/url"
	"strings"
	"time"

	"timesavior/internal/rules"
)

// RequestMeta carries URL bits used for whitelist matching.
type RequestMeta struct {
	Host     string
	Path     string
	RawQuery string
	Referer  string
}

func (e *Engine) Evaluate(host, path string, now time.Time) Result {
	return e.EvaluateMeta(RequestMeta{Host: host, Path: path}, now)
}

func (e *Engine) EvaluateMeta(meta RequestMeta, now time.Time) Result {
	host := meta.Host
	path := meta.Path
	site, cat := e.store.Match(host, path)
	if site == nil {
		return Result{Decision: Allow, Reason: ReasonUnmanaged, Category: cat}
	}

	if e.cfg.ProbeOnly {
		return Result{Decision: Allow, Reason: ReasonProbeOnly, Site: site.Name, Category: cat}
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	e.rollPolicyDay(now)
	e.observeRequestLocked(site, meta, now)

	// First managed-site feed hit inside the daily window starts the unlock clock.
	if cat == rules.CatFeed {
		e.maybeStartFeedUnlockLocked(now)
	}

	if e.InFreeWindow(now) {
		e.resetSiteLocked(site.Name)
		return Result{Decision: Allow, Reason: ReasonFreeMode, Site: site.Name, Category: cat}
	}

	if now.Before(e.tempUnlockUntil) {
		return Result{Decision: Allow, Reason: ReasonTempUnlock, Site: site.Name, Category: cat}
	}

	st := e.getSite(site.Name)
	if now.Before(st.fullBlockUntil) {
		if cat == rules.CatAsset {
			return Result{Decision: Allow, Reason: ReasonAllowed, Site: site.Name, Category: cat}
		}
		return Result{Decision: Block, Reason: ReasonFullBlock, Site: site.Name, Category: cat}
	}

	switch cat {
	case rules.CatFeed:
		return Result{Decision: Block, Reason: ReasonFeedBlocked, Site: site.Name, Category: cat}
	case rules.CatPlay:
		if site.TracksWatch() {
			if st.watchExempt {
				// Keep exemption alive while this whitelisted playback continues.
				st.exemptUntil = now.Add(20 * time.Minute)
			} else {
				e.recordWatch(site, st, now)
				if now.Before(st.fullBlockUntil) {
					return Result{Decision: Block, Reason: ReasonFullBlock, Site: site.Name, Category: cat}
				}
			}
		}
		return Result{Decision: Allow, Reason: ReasonAllowed, Site: site.Name, Category: cat}
	case rules.CatUnknown:
		if e.cfg.UnknownDefault == "block" {
			return Result{Decision: Block, Reason: ReasonUnknownBlock, Site: site.Name, Category: cat}
		}
		return Result{Decision: Allow, Reason: ReasonAllowed, Site: site.Name, Category: cat}
	case rules.CatSearch, rules.CatContent, rules.CatAsset:
		return Result{Decision: Allow, Reason: ReasonAllowed, Site: site.Name, Category: cat}
	default:
		return Result{Decision: Allow, Reason: ReasonAllowed, Site: site.Name, Category: cat}
	}
}

// ObserveResponse sniffs watch/player bodies for channel-name / title-keyword whitelist hits.
// channel_ids / video_ids are not used.
func (e *Engine) ObserveResponse(host, path string, body []byte, now time.Time) {
	site, _ := e.store.Match(host, path)
	if site == nil || e.cfg == nil || !e.cfg.HasWatchRules() {
		return
	}
	pl := strings.ToLower(path)
	sniffPath := strings.HasPrefix(pl, "/watch") ||
		strings.Contains(pl, "/youtubei/v1/player") ||
		strings.Contains(pl, "/youtubei/v1/next") ||
		strings.Contains(pl, "/x/web-interface/view") ||
		strings.Contains(pl, "/x/web-interface/wbi/view")
	if !sniffPath || len(body) == 0 {
		return
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	e.rollPolicyDay(now)
	st := e.getSite(site.Name)

	hit, label := e.cfg.MatchWatchHay(site.Name, string(body))
	if hit {
		st.watchExempt = true
		st.exemptLabel = label
		st.exemptUntil = now.Add(20 * time.Minute)
		e.schedulePersistLocked()
		return
	}

	if strings.HasPrefix(pl, "/watch") || strings.Contains(pl, "/youtubei/v1/player") {
		// Definite non-whitelist watch/player → resume scoring unless title-exempt.
		if !strings.HasPrefix(st.exemptLabel, "title:") {
			st.watchExempt = false
			st.exemptLabel = ""
			st.exemptUntil = time.Time{}
			e.schedulePersistLocked()
		}
	}
}

func (e *Engine) observeRequestLocked(site *rules.SiteRule, meta RequestMeta, now time.Time) {
	st := e.getSite(site.Name)
	if !st.exemptUntil.IsZero() && now.After(st.exemptUntil) {
		st.watchExempt = false
		st.exemptLabel = ""
		st.exemptUntil = time.Time{}
		e.schedulePersistLocked()
	}
	if e.cfg == nil || !e.cfg.HasWatchRules() {
		return
	}
	hay := strings.ToLower(strings.Join([]string{
		meta.Path,
		meta.RawQuery,
		meta.Referer,
	}, " "))
	u, _ := url.Parse("http://x/?" + meta.RawQuery)
	if u != nil {
		if v := u.Query().Get("v"); v != "" {
			hay += " " + strings.ToLower(v)
		}
	}
	if hit, label := e.cfg.MatchWatchHay(site.Name, hay); hit {
		st.watchExempt = true
		st.exemptLabel = label
		st.exemptUntil = now.Add(20 * time.Minute)
		e.schedulePersistLocked()
	}
}

// matchTitleKeywordsInBody is kept for tests that sniff raw bodies without going through ObserveResponse.
func matchTitleKeywordsInBody(body []byte, keywords []string) (bool, string) {
	if len(body) == 0 || len(keywords) == 0 {
		return false, ""
	}
	lower := bytes.ToLower(body)
	for _, k := range keywords {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if bytes.Contains(body, []byte(k)) || bytes.Contains(lower, []byte(strings.ToLower(k))) {
			return true, k
		}
	}
	return false, ""
}
