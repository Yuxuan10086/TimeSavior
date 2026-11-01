package rules

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Category string

const (
	CatFeed    Category = "feed"
	CatSearch  Category = "search"
	CatContent Category = "content"
	CatPlay    Category = "play"
	CatAsset   Category = "asset"
	CatUnknown Category = "unknown"
)

// SessionCfg: SoftBlock feeds; FullBlock only after continuous watch.
type SessionCfg struct {
	// Idle gap that breaks a continuous watch session.
	WatchIdleResetMinutes int `yaml:"watch_idle_reset_minutes"`
	// Continuous watch duration that triggers site-wide FullBlock.
	WatchBlockAfterMinutes int `yaml:"watch_block_after_minutes"`

	// Deprecated fields kept for old yaml compatibility (ignored by new engine).
	WindowMinutes         int `yaml:"window_minutes"`
	MinFeedOrPlayRequests int `yaml:"min_feed_or_play_requests"`
	IdleResetMinutes      int `yaml:"idle_reset_minutes"`
	FullBlockMinutes      int `yaml:"full_block_minutes"`
	FeedWeight            int `yaml:"feed_weight"`
	PlayWeight            int `yaml:"play_weight"`
	ContentWeight         int `yaml:"content_weight"`
	ScoreIntervalSeconds  int `yaml:"score_interval_seconds"`
}

type PathsCfg struct {
	Feed    []string `yaml:"feed"`
	Search  []string `yaml:"search"`
	Content []string `yaml:"content"`
	Play    []string `yaml:"play"`
	Asset   []string `yaml:"asset"`
}

// WatchWhitelist: legacy per-site channel list (migrated into config.watch_whitelist_rules at startup).
// channel_ids / video_ids are deprecated and ignored by matching (kept for yaml compatibility).
type WatchWhitelist struct {
	// Channels: display names or @handles, e.g. "Example Channel", "@ExampleChannel"
	Channels []string `yaml:"channels"`
	// ChannelIDs: deprecated — no longer used for matching.
	ChannelIDs []string `yaml:"channel_ids"`
	// VideoIDs: deprecated — no longer used for matching.
	VideoIDs []string `yaml:"video_ids"`
}

type SiteRule struct {
	Name                 string         `yaml:"name"`
	Enabled              bool           `yaml:"enabled"`
	Kind                 string         `yaml:"kind"`
	Domains              []string       `yaml:"domains"`
	PlayDomains          []string       `yaml:"play_domains"`
	EntertainmentSession SessionCfg     `yaml:"entertainment_session"`
	WatchWhitelist       WatchWhitelist `yaml:"watch_whitelist"`
	Paths                PathsCfg       `yaml:"paths"`

	feedRe    []*regexp.Regexp
	searchRe  []*regexp.Regexp
	contentRe []*regexp.Regexp
	playRe    []*regexp.Regexp
	assetRe   []*regexp.Regexp
}

type Store struct {
	mu    sync.RWMutex
	sites []*SiteRule
	dir   string
}

func LoadDir(dir string) (*Store, error) {
	s := &Store{dir: dir}
	if err := s.Reload(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Reload() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	var sites []*SiteRule
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		path := filepath.Join(s.dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var site SiteRule
		if err := yaml.Unmarshal(data, &site); err != nil {
			return fmt.Errorf("parse %s: %w", name, err)
		}
		if site.Name == "" {
			site.Name = strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml")
		}
		site.applyDefaults()
		if err := site.compile(); err != nil {
			return fmt.Errorf("compile %s: %w", name, err)
		}
		sites = append(sites, &site)
	}
	s.mu.Lock()
	s.sites = sites
	s.mu.Unlock()
	return nil
}

func (s *SiteRule) applyDefaults() {
	if s.Kind == "" {
		s.Kind = "video"
	}
	es := &s.EntertainmentSession
	if es.WatchIdleResetMinutes <= 0 {
		if es.IdleResetMinutes > 0 {
			es.WatchIdleResetMinutes = es.IdleResetMinutes
		} else {
			es.WatchIdleResetMinutes = 10
		}
	}
	if es.WatchBlockAfterMinutes <= 0 {
		es.WatchBlockAfterMinutes = 20
	}
}

// TracksWatch reports whether continuous play duration can trigger FullBlock.
func (s *SiteRule) TracksWatch() bool {
	return strings.EqualFold(s.Kind, "video")
}

// HasWatchWhitelist reports whether legacy channel-name entries exist (IDs ignored).
func (s *SiteRule) HasWatchWhitelist() bool {
	for _, c := range s.WatchWhitelist.Channels {
		if strings.TrimSpace(c) != "" {
			return true
		}
	}
	return false
}

// WhitelistNeedles returns lowercased channel-name needles (IDs excluded).
func (s *SiteRule) WhitelistNeedles() []string {
	var out []string
	seen := map[string]struct{}{}
	add := func(n string) {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			return
		}
		if _, ok := seen[n]; ok {
			return
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	for _, c := range s.WatchWhitelist.Channels {
		for _, n := range ChannelNeedles(c) {
			add(n)
		}
	}
	return out
}

func (s *SiteRule) compile() error {
	var err error
	if s.feedRe, err = compileAll(s.Paths.Feed); err != nil {
		return err
	}
	if s.searchRe, err = compileAll(s.Paths.Search); err != nil {
		return err
	}
	if s.contentRe, err = compileAll(s.Paths.Content); err != nil {
		return err
	}
	if s.playRe, err = compileAll(s.Paths.Play); err != nil {
		return err
	}
	if s.assetRe, err = compileAll(s.Paths.Asset); err != nil {
		return err
	}
	return nil
}

func compileAll(patterns []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func (s *Store) Sites() []*SiteRule {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*SiteRule, len(s.sites))
	copy(out, s.sites)
	return out
}

func (s *Store) Match(host, path string) (*SiteRule, Category) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if i := strings.Index(host, ":"); i >= 0 {
		host = host[:i]
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, site := range s.sites {
		if !site.Enabled {
			continue
		}
		if !hostMatch(host, site.Domains) && !hostMatch(host, site.PlayDomains) {
			continue
		}
		if strings.HasPrefix(host, "search.") {
			return site, CatSearch
		}
		// Video CDN hosts: treat as play for watch-time accounting.
		if hostMatch(host, site.PlayDomains) {
			return site, CatPlay
		}
		return site, site.categorize(path)
	}
	return nil, CatUnknown
}

func hostMatch(host string, patterns []string) bool {
	for _, p := range patterns {
		p = strings.ToLower(p)
		if strings.HasPrefix(p, "*.") {
			suf := p[1:]
			if host == p[2:] || strings.HasSuffix(host, suf) {
				return true
			}
			continue
		}
		if host == p {
			return true
		}
	}
	return false
}

func (s *SiteRule) categorize(path string) Category {
	if path == "" {
		path = "/"
	}
	if matchAny(s.assetRe, path) {
		return CatAsset
	}
	if matchAny(s.searchRe, path) {
		return CatSearch
	}
	if matchAny(s.playRe, path) {
		return CatPlay
	}
	if matchAny(s.contentRe, path) {
		return CatContent
	}
	if matchAny(s.feedRe, path) {
		return CatFeed
	}
	return CatUnknown
}

func matchAny(res []*regexp.Regexp, path string) bool {
	for _, re := range res {
		if re.MatchString(path) {
			return true
		}
	}
	return false
}
