package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type FreeWindow struct {
	Start string `yaml:"start"`
	End   string `yaml:"end"`
	// Weekdays: optional filter. Empty = every day.
	// Accepts: mon/tue/wed/thu/fri/sat/sun, or monday..., or 0-6 (Sun=0).
	Weekdays []string `yaml:"weekdays"`
}

type ClashUpstream struct {
	Enabled      bool  `yaml:"enabled"`
	Host         string `yaml:"host"`
	Port         int   `yaml:"port"`
	ProbeSeconds int   `yaml:"probe_seconds"`
	ProbePorts   []int `yaml:"probe_ports"`
}

type WatchdogCfg struct {
	Enabled      bool `yaml:"enabled"`
	CheckSeconds int  `yaml:"check_seconds"`
}

type LoggingCfg struct {
	RequestLog        bool    `yaml:"request_log"`
	AuditLog          bool    `yaml:"audit_log"`
	WatchMeta         bool    `yaml:"watch_meta"` // log video title/author/tags
	FocusTrack        bool    `yaml:"focus_track"`
	FocusPollHz       float64 `yaml:"focus_poll_hz"` // default 0.5
	KeepDays          int     `yaml:"keep_days"`     // JSONL audit log retention
	RawKeepDays       int     `yaml:"raw_keep_days"` // SQLite raw facts TTL (daily_* never pruned)
	RequestAllManaged bool    `yaml:"request_all_managed"` // if false (default): only blocks; play duration is in SQLite
}

// LLMJudgeCfg configures SiliconFlow-assisted watch judgment after continuous watch threshold.
type LLMJudgeCfg struct {
	Enabled              bool    `yaml:"enabled"`
	BaseURL              string  `yaml:"base_url"`
	Model                string  `yaml:"model"`
	SecretsFile          string  `yaml:"secrets_file"`
	CostMonitorEnabled   bool    `yaml:"cost_monitor_enabled"` // default false: voucher spend is not in /user/info
	DailyCostLimitCNY    float64 `yaml:"daily_cost_limit_cny"` // only used when CostMonitorEnabled
	CircuitFallbackBlock *bool   `yaml:"circuit_fallback_block"` // nil/true = FullBlock when circuit open
	APIKey               string  `yaml:"-"`                     // loaded from secrets / env; never serialized
}

// FallbackBlocks reports whether reaching cost limit should FullBlock at threshold (default true).
func (c LLMJudgeCfg) FallbackBlocks() bool {
	if c.CircuitFallbackBlock == nil {
		return true
	}
	return *c.CircuitFallbackBlock
}

// CostMonitorOn reports whether balance-delta cost tracking and daily circuit are active.
func (c LLMJudgeCfg) CostMonitorOn() bool {
	return c.CostMonitorEnabled
}

type secretsFile struct {
	SiliconFlowAPIKey string `yaml:"siliconflow_api_key"`
}

type Config struct {
	Listen                    string        `yaml:"listen"`
	DataDir                   string        `yaml:"data_dir"`
	RulesDir                  string        `yaml:"rules_dir"`
	ProbeOnly                 bool          `yaml:"probe_only"`
	ExitPassword              string        `yaml:"exit_password"` // legacy; unused (no tray exit)
	ExitCooldownSeconds       int           `yaml:"exit_cooldown_seconds"`
	TempUnlockEnabled         bool          `yaml:"temp_unlock_enabled"`
	TempUnlockMinutes         int           `yaml:"temp_unlock_minutes"`
	TempUnlockWindowStart     string        `yaml:"temp_unlock_window_start"` // HH:MM within policy day; default 21:30
	TempUnlockDailyLimit      int           `yaml:"temp_unlock_daily_limit"`
	TempUnlockWeeklyLimit     int           `yaml:"temp_unlock_weekly_limit"`
	TempUnlockWaitSeconds     int           `yaml:"temp_unlock_wait_seconds"`
	TempUnlockQuestionSeconds int           `yaml:"temp_unlock_question_seconds"`
	TempUnlockStreakNeeded    int           `yaml:"temp_unlock_streak_needed"`
	ProxyGuardSeconds         int           `yaml:"proxy_guard_seconds"`
	UnknownDefault            string        `yaml:"unknown_default"`
	DayStartHour              int                  `yaml:"day_start_hour"` // policy day boundary, default 4
	MinWatchSec               int                  `yaml:"min_watch_sec"`  // drop isolated play < N sec (default 15)
	WatchWhitelistRules       []WatchWhitelistRule `yaml:"watch_whitelist_rules"`
	WatchTitleWhitelist       []string             `yaml:"watch_title_whitelist"` // deprecated: migrated into WatchWhitelistRules
	LLMJudge                  LLMJudgeCfg          `yaml:"llm_judge"`
	FreeWindows               []FreeWindow         `yaml:"free_windows"`
	ClashUpstream             ClashUpstream        `yaml:"clash_upstream"`
	Watchdog                  WatchdogCfg          `yaml:"watchdog"`
	Logging                   LoggingCfg           `yaml:"logging"`

	mu       sync.RWMutex
	basePath string
	wl       whitelistState
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	cfg.applyDefaults()
	abs, _ := filepath.Abs(path)
	cfg.basePath = filepath.Dir(abs)
	// Relative data/rules paths resolve from process working directory (project root),
	// not from the config file directory.
	if !filepath.IsAbs(cfg.DataDir) {
		cfg.DataDir, _ = filepath.Abs(cfg.DataDir)
	}
	if !filepath.IsAbs(cfg.RulesDir) {
		cfg.RulesDir, _ = filepath.Abs(cfg.RulesDir)
	}
	cfg.loadSecrets()
	// Normalize explicit + legacy title keywords; site channels merged later via SyncWatchRules.
	cfg.SyncWatchRules(nil)
	return &cfg, nil
}

// loadSecrets reads API key from SILICONFLOW_API_KEY or configs/secrets.yaml.
func (c *Config) loadSecrets() {
	if c == nil {
		return
	}
	if k := strings.TrimSpace(os.Getenv("SILICONFLOW_API_KEY")); k != "" {
		c.LLMJudge.APIKey = k
		return
	}
	name := c.LLMJudge.SecretsFile
	if name == "" {
		name = "secrets.yaml"
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.basePath, name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var sec secretsFile
	if err := yaml.Unmarshal(data, &sec); err != nil {
		return
	}
	c.LLMJudge.APIKey = strings.TrimSpace(sec.SiliconFlowAPIKey)
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:18888"
	}
	if c.DataDir == "" {
		c.DataDir = "data"
	}
	if c.RulesDir == "" {
		c.RulesDir = "rules"
	}
	if c.ExitPassword == "" {
		c.ExitPassword = "timesavior"
	}
	if c.ExitCooldownSeconds <= 0 {
		c.ExitCooldownSeconds = 60
	}
	if c.TempUnlockMinutes <= 0 {
		c.TempUnlockMinutes = 30
	}
	if strings.TrimSpace(c.TempUnlockWindowStart) == "" {
		c.TempUnlockWindowStart = "21:30"
	}
	if c.TempUnlockDailyLimit <= 0 {
		c.TempUnlockDailyLimit = 1
	}
	if c.TempUnlockWeeklyLimit <= 0 {
		c.TempUnlockWeeklyLimit = 1
	}
	if c.TempUnlockWaitSeconds <= 0 {
		c.TempUnlockWaitSeconds = 60
	}
	if c.TempUnlockQuestionSeconds <= 0 {
		c.TempUnlockQuestionSeconds = 5
	}
	if c.TempUnlockStreakNeeded <= 0 {
		c.TempUnlockStreakNeeded = 10
	}
	if c.ProxyGuardSeconds <= 0 {
		c.ProxyGuardSeconds = 5
	}
	if c.UnknownDefault == "" {
		c.UnknownDefault = "allow"
	}
	if c.DayStartHour <= 0 {
		c.DayStartHour = 4
	}
	if c.MinWatchSec <= 0 {
		c.MinWatchSec = 15
	}
	if c.ClashUpstream.Host == "" {
		c.ClashUpstream.Host = "127.0.0.1"
	}
	if c.ClashUpstream.Port == 0 {
		c.ClashUpstream.Port = 7890
	}
	if c.ClashUpstream.ProbeSeconds <= 0 {
		c.ClashUpstream.ProbeSeconds = 3
	}
	if len(c.ClashUpstream.ProbePorts) == 0 {
		c.ClashUpstream.ProbePorts = []int{7890, 7897, 1080}
	}
	if c.Watchdog.CheckSeconds <= 0 {
		c.Watchdog.CheckSeconds = 1
	}
	// free_windows optional; empty means no calendar free-mode (feed-triggered unlock is primary).
	if c.Logging.KeepDays <= 0 {
		c.Logging.KeepDays = 14
	}
	if c.Logging.RawKeepDays <= 0 {
		c.Logging.RawKeepDays = 7
	}
	if c.Logging.FocusPollHz <= 0 {
		c.Logging.FocusPollHz = 0.5
	}
	lj := &c.LLMJudge
	if lj.BaseURL == "" {
		lj.BaseURL = "https://api.siliconflow.cn/v1"
	}
	if lj.Model == "" {
		lj.Model = "deepseek-ai/DeepSeek-V4-Flash"
	}
	if lj.SecretsFile == "" {
		lj.SecretsFile = "secrets.yaml"
	}
	if lj.DailyCostLimitCNY <= 0 {
		lj.DailyCostLimitCNY = 0.10
	}
}

// TitleWhitelist returns title-kind keywords from the normalized rule table.
func (c *Config) TitleWhitelist() []string {
	if c == nil {
		return nil
	}
	var out []string
	seen := map[string]struct{}{}
	for _, r := range c.WatchRules() {
		if r.Kind != "title" || r.Keyword == "" {
			continue
		}
		k := strings.ToLower(r.Keyword)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, r.Keyword)
	}
	return out
}

// MatchTitleWhitelist reports whether title matches any title-kind rule.
// Returns (matched, matchedKeyword).
func (c *Config) MatchTitleWhitelist(title string) (bool, string) {
	hit, label := c.MatchWatchMeta("", title, "")
	if !hit || !strings.HasPrefix(label, "title:") {
		return false, ""
	}
	return true, strings.TrimPrefix(label, "title:")
}

func (c *Config) BasePath() string {
	return c.basePath
}

func (c *Config) ListenHostPort() (host string, port int, err error) {
	h, pstr, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return "", 0, fmt.Errorf("invalid listen address %q: %w", c.Listen, err)
	}
	p, err := strconv.Atoi(pstr)
	if err != nil {
		return "", 0, fmt.Errorf("invalid listen port in %q: %w", c.Listen, err)
	}
	return h, p, nil
}
