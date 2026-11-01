package config

import (
	"strings"
	"sync"
)

// WatchWhitelistRule is one row in the unified watch whitelist.
// site: "all" (or empty) = every video site; otherwise a site name (youtube/bilibili/…).
// kind: "channel" | "title"
// mode: "exact" | "contains" (fuzzy)
type WatchWhitelistRule struct {
	Site    string `yaml:"site"`
	Kind    string `yaml:"kind"`
	Keyword string `yaml:"keyword"`
	Mode    string `yaml:"mode"`
}

type whitelistState struct {
	mu    sync.RWMutex
	rules []WatchWhitelistRule
}

// SyncWatchRules merges explicit watch_whitelist_rules with legacy sources:
//   - watch_title_whitelist → kind=title, site=all, mode=contains
//   - per-site watch_whitelist.channels → kind=channel, mode=exact
// channel_ids / video_ids are intentionally ignored (deprecated).
// Channel rules for the same site+channel identity are collapsed to one row
// (prefer plain display name over @handle); channel mode is always exact.
func (c *Config) SyncWatchRules(siteChannels map[string][]string) {
	if c == nil {
		return
	}
	var out []WatchWhitelistRule
	seen := map[string]int{} // ruleKey → index in out
	add := func(r WatchWhitelistRule) {
		r = normalizeRule(r)
		if r.Keyword == "" {
			return
		}
		key := ruleKey(r)
		if idx, ok := seen[key]; ok {
			// Prefer plain display name over @handle when collapsing channels.
			if r.Kind == "channel" && strings.HasPrefix(out[idx].Keyword, "@") && !strings.HasPrefix(r.Keyword, "@") {
				out[idx] = r
			}
			return
		}
		seen[key] = len(out)
		out = append(out, r)
	}

	for _, r := range c.WatchWhitelistRules {
		add(r)
	}
	for _, k := range c.WatchTitleWhitelist {
		add(WatchWhitelistRule{Site: "all", Kind: "title", Keyword: k, Mode: "contains"})
	}
	for site, chs := range siteChannels {
		site = strings.ToLower(strings.TrimSpace(site))
		for _, ch := range chs {
			add(WatchWhitelistRule{Site: site, Kind: "channel", Keyword: strings.TrimSpace(ch), Mode: "exact"})
		}
	}

	c.wl.mu.Lock()
	c.wl.rules = out
	c.wl.mu.Unlock()
}

// WatchRules returns a copy of the normalized whitelist table.
func (c *Config) WatchRules() []WatchWhitelistRule {
	if c == nil {
		return nil
	}
	c.wl.mu.RLock()
	defer c.wl.mu.RUnlock()
	if len(c.wl.rules) == 0 {
		// Fallback before SyncWatchRules: explicit + title legacy only.
		var out []WatchWhitelistRule
		seen := map[string]int{}
		add := func(r WatchWhitelistRule) {
			r = normalizeRule(r)
			if r.Keyword == "" {
				return
			}
			key := ruleKey(r)
			if idx, ok := seen[key]; ok {
				if r.Kind == "channel" && strings.HasPrefix(out[idx].Keyword, "@") && !strings.HasPrefix(r.Keyword, "@") {
					out[idx] = r
				}
				return
			}
			seen[key] = len(out)
			out = append(out, r)
		}
		for _, r := range c.WatchWhitelistRules {
			add(r)
		}
		for _, k := range c.WatchTitleWhitelist {
			add(WatchWhitelistRule{Site: "all", Kind: "title", Keyword: k, Mode: "contains"})
		}
		return out
	}
	out := make([]WatchWhitelistRule, len(c.wl.rules))
	copy(out, c.wl.rules)
	return out
}

// HasWatchRules reports whether any text whitelist rule is active.
func (c *Config) HasWatchRules() bool {
	return len(c.WatchRules()) > 0
}

// HasTitleRules reports whether any title-kind rule exists (for body sniff gating).
func (c *Config) HasTitleRules() bool {
	for _, r := range c.WatchRules() {
		if r.Kind == "title" {
			return true
		}
	}
	return false
}

// MatchWatchMeta applies title/channel rules against structured fields.
// Returns (hit, label) where title hits use "title:<keyword>" labels.
func (c *Config) MatchWatchMeta(site, title, author string) (bool, string) {
	if c == nil {
		return false, ""
	}
	site = strings.ToLower(strings.TrimSpace(site))
	title = strings.TrimSpace(title)
	author = strings.TrimSpace(author)
	for _, r := range c.WatchRules() {
		if !ruleAppliesToSite(r, site) {
			continue
		}
		switch r.Kind {
		case "title":
			if title != "" && textMatch(title, r.Keyword, r.Mode) {
				return true, "title:" + r.Keyword
			}
		case "channel":
			if author != "" && channelFieldMatch(author, r.Keyword, r.Mode) {
				return true, r.Keyword
			}
		}
	}
	return false, ""
}

// MatchWatchHay applies channel rules (and title contains) against a URL/body haystack.
// Used when structured title/author are unavailable. Ignores deprecated ID matching.
func (c *Config) MatchWatchHay(site, hay string) (bool, string) {
	if c == nil || strings.TrimSpace(hay) == "" {
		return false, ""
	}
	site = strings.ToLower(strings.TrimSpace(site))
	lowerHay := strings.ToLower(hay)
	for _, r := range c.WatchRules() {
		if !ruleAppliesToSite(r, site) {
			continue
		}
		switch r.Kind {
		case "channel":
			if channelHayMatch(lowerHay, r.Keyword, r.Mode) {
				return true, r.Keyword
			}
		case "title":
			// Raw URL/body: keyword presence (full keyword, case-insensitive).
			// exact vs contains is applied on structured title fields in MatchWatchMeta.
			if strings.Contains(lowerHay, strings.ToLower(r.Keyword)) {
				return true, "title:" + r.Keyword
			}
		}
	}
	return false, ""
}

func normalizeRule(r WatchWhitelistRule) WatchWhitelistRule {
	r.Site = strings.ToLower(strings.TrimSpace(r.Site))
	if r.Site == "" {
		r.Site = "all"
	}
	r.Kind = strings.ToLower(strings.TrimSpace(r.Kind))
	r.Keyword = strings.TrimSpace(r.Keyword)
	r.Mode = strings.ToLower(strings.TrimSpace(r.Mode))
	if r.Kind != "channel" {
		r.Kind = "title"
	}
	if r.Kind == "channel" {
		// Channel rules always exact-match (name / @handle / compact equivalence).
		r.Mode = "exact"
	} else {
		if r.Mode == "" || r.Mode == "fuzzy" {
			r.Mode = "contains"
		}
		if r.Mode != "exact" {
			r.Mode = "contains"
		}
	}
	return r
}

func ruleKey(r WatchWhitelistRule) string {
	if r.Kind == "channel" {
		// Collapse "Volka English" + "@VolkaEnglish" under the same site.
		return r.Site + "|channel|" + channelIdentity(r.Keyword)
	}
	return r.Site + "|" + r.Kind + "|" + strings.ToLower(r.Keyword) + "|" + r.Mode
}

func channelIdentity(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "@")
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "　", "")
	return s
}

func ruleAppliesToSite(r WatchWhitelistRule, site string) bool {
	if r.Site == "" || r.Site == "all" {
		return true
	}
	return site != "" && r.Site == site
}

func textMatch(value, keyword, mode string) bool {
	value = strings.TrimSpace(value)
	keyword = strings.TrimSpace(keyword)
	if value == "" || keyword == "" {
		return false
	}
	if mode == "exact" {
		return strings.EqualFold(value, keyword)
	}
	return strings.Contains(strings.ToLower(value), strings.ToLower(keyword))
}

func channelFieldMatch(author, keyword, mode string) bool {
	author = strings.TrimSpace(author)
	keyword = strings.TrimSpace(keyword)
	if author == "" || keyword == "" {
		return false
	}
	if mode == "exact" {
		if strings.EqualFold(author, keyword) {
			return true
		}
		// "@VolkaEnglish" vs "VolkaEnglish"; "Volka English" vs "@VolkaEnglish"
		ac := channelIdentity(author)
		kc := channelIdentity(keyword)
		return ac != "" && ac == kc
	}
	al := strings.ToLower(author)
	kl := strings.ToLower(keyword)
	if strings.Contains(al, kl) {
		return true
	}
	// Compact form: "Volka English" ↔ "volkaenglish" / "@VolkaEnglish"
	ac := channelIdentity(author)
	kc := channelIdentity(keyword)
	return ac != "" && kc != "" && (ac == kc || strings.Contains(ac, kc) || strings.Contains(kc, ac))
}

func channelHayMatch(lowerHay, keyword, mode string) bool {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" || lowerHay == "" {
		return false
	}
	needles := channelNeedles(keyword, mode == "exact")
	for _, n := range needles {
		if n != "" && strings.Contains(lowerHay, n) {
			return true
		}
	}
	return false
}

func channelNeedles(c string, exact bool) []string {
	c = strings.TrimSpace(c)
	if c == "" {
		return nil
	}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			return
		}
		for _, e := range out {
			if e == s {
				return
			}
		}
		out = append(out, s)
	}
	add(c)
	if strings.HasPrefix(c, "@") {
		add(strings.TrimPrefix(c, "@"))
	} else {
		add("@" + c)
	}
	compact := channelIdentity(c)
	if compact != "" {
		add("/@" + compact)
		if !exact {
			add(compact)
		} else {
			// Exact channel: still match handle form without spaces in haystacks.
			add("@" + compact)
		}
	}
	return out
}
