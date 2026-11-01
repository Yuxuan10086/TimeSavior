package store

import (
	"database/sql"
	"encoding/json"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Dialog purpose / kind constants for llm_dialogs.
const (
	LLMKindWatchJudge = "watch_judge"
	LLMKindTitleShort = "title_short"

	// MaxLLMDialogTextBytes is a soft cap when persisting request/response text.
	// Prefer storing complete originals; if exceeded, truncate and append a note.
	MaxLLMDialogTextBytes = 64 * 1024
)

// LLMUsageSnapshot is day + lifetime LLM cost accounting.
type LLMUsageSnapshot struct {
	Day           string  `json:"day"`
	DayCostCNY    float64 `json:"day_cost_cny"`
	DayCalls      int     `json:"day_calls"`
	TotalCostCNY  float64 `json:"total_cost_cny"`
	TotalCalls    int     `json:"total_calls"`
	CircuitOpen   bool    `json:"circuit_open"`
	DailyLimitCNY float64 `json:"daily_limit_cny"`
}

// LLMDialog is one LLM HTTP call stored for the admin「大模型日志」page.
// Never contains API keys (see redactSecrets).
type LLMDialog struct {
	ID            int64   `json:"id"`
	TS            string  `json:"ts"`
	Day           string  `json:"day"`
	Site          string  `json:"site"`
	Kind          string  `json:"kind"` // watch_judge | title_short
	Allow         *bool   `json:"allow"`
	Reason        string  `json:"reason"`
	RequestRaw    string  `json:"request_raw"`    // full request transcript (system+user)
	PromptSummary string  `json:"prompt_summary"` // legacy alias of request_raw (same DB column)
	ResponseRaw   string  `json:"response_raw"`   // full model output text
	CostCNY       float64 `json:"cost_cny"`
	VideosJSON    string  `json:"videos_json"`
	Error         string  `json:"error,omitempty"`
}

// LLMDialogInput is the write payload for InsertLLMDialog.
type LLMDialogInput struct {
	Day           string
	Site          string
	Kind          string // watch_judge | title_short; empty → watch_judge
	CostCNY       float64
	Allow         *bool
	Reason        string
	ErrMsg        string
	RequestRaw    string // preferred: full request (system+user / messages)
	PromptSummary string // legacy alias; used only if RequestRaw empty
	ResponseRaw   string
	VideosJSON    string
	At            time.Time
}

// AddLLMUsage increments daily and lifetime cost counters.
func (d *DB) AddLLMUsage(day string, costCNY float64) error {
	if d == nil || day == "" {
		return nil
	}
	if costCNY < 0 {
		costCNY = 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(`
		INSERT INTO llm_usage(day, cost_cny, calls) VALUES (?,?,1)
		ON CONFLICT(day) DO UPDATE SET
		  cost_cny = llm_usage.cost_cny + excluded.cost_cny,
		  calls = llm_usage.calls + 1`, day, costCNY)
	if err != nil {
		return err
	}
	_, err = d.sql.Exec(`
		INSERT INTO llm_totals(id, total_cost_cny, total_calls) VALUES (1, ?, 1)
		ON CONFLICT(id) DO UPDATE SET
		  total_cost_cny = llm_totals.total_cost_cny + excluded.total_cost_cny,
		  total_calls = llm_totals.total_calls + 1`, costCNY)
	return err
}

// LogLLMCall writes an optional call log row (no API key).
func (d *DB) LogLLMCall(day, site string, costCNY float64, allow *bool, reason, errMsg string, at time.Time) error {
	if d == nil {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	if day == "" {
		day = dayKey(at)
	}
	ts := formatTS(at)
	var allowVal any
	if allow != nil {
		if *allow {
			allowVal = 1
		} else {
			allowVal = 0
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(`
		INSERT INTO llm_call_log(ts, day, site, cost_cny, allow, reason, error)
		VALUES (?,?,?,?,?,?,?)`,
		ts, day, site, costCNY, allowVal, reason, errMsg)
	return err
}

// InsertLLMDialog persists one LLM HTTP call (watch judge, title short, …).
func (d *DB) InsertLLMDialog(in LLMDialogInput) error {
	if d == nil {
		return nil
	}
	at := in.At
	if at.IsZero() {
		at = time.Now()
	}
	day := in.Day
	if day == "" {
		day = dayKey(at)
	}
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		kind = LLMKindWatchJudge
	}
	ts := formatTS(at)
	videos := strings.TrimSpace(in.VideosJSON)
	if videos == "" {
		videos = "[]"
	}
	var allowVal any
	if in.Allow != nil {
		if *in.Allow {
			allowVal = 1
		} else {
			allowVal = 0
		}
	}
	reqText := in.RequestRaw
	if strings.TrimSpace(reqText) == "" {
		reqText = in.PromptSummary
	}
	prompt := redactSecrets(truncateBytes(reqText, MaxLLMDialogTextBytes))
	resp := redactSecrets(truncateBytes(in.ResponseRaw, MaxLLMDialogTextBytes))
	reason := redactSecrets(truncateRunes(in.Reason, 200))
	errMsg := redactSecrets(truncateRunes(in.ErrMsg, 400))

	d.mu.Lock()
	defer d.mu.Unlock()
	_, err := d.sql.Exec(`
		INSERT INTO llm_dialogs(ts, day, site, kind, allow, reason, prompt_summary, response_raw, cost_cny, videos_json, error)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		ts, day, in.Site, kind, allowVal, reason, prompt, resp, in.CostCNY, videos, errMsg)
	if err != nil {
		return err
	}
	// Keep legacy call log in sync for accounting trails.
	_, _ = d.sql.Exec(`
		INSERT INTO llm_call_log(ts, day, site, cost_cny, allow, reason, error)
		VALUES (?,?,?,?,?,?,?)`,
		ts, day, in.Site, in.CostCNY, allowVal, reason, errMsg)
	return nil
}

// RecentLLMDialogs returns the newest dialogs (default 10).
func (d *DB) RecentLLMDialogs(limit int) ([]LLMDialog, error) {
	if d == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	rows, err := d.sql.Query(`
		SELECT id, ts, day, site, kind, allow, reason, prompt_summary, response_raw, cost_cny, videos_json, error
		FROM llm_dialogs
		ORDER BY id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LLMDialog
	for rows.Next() {
		var row LLMDialog
		var allowVal sql.NullInt64
		var storedReq string
		if err := rows.Scan(
			&row.ID, &row.TS, &row.Day, &row.Site, &row.Kind, &allowVal,
			&row.Reason, &storedReq, &row.ResponseRaw,
			&row.CostCNY, &row.VideosJSON, &row.Error,
		); err != nil {
			return out, err
		}
		if row.Kind == "" {
			row.Kind = LLMKindWatchJudge
		}
		if allowVal.Valid {
			v := allowVal.Int64 != 0
			row.Allow = &v
		}
		if row.VideosJSON == "" {
			row.VideosJSON = "[]"
		}
		// Same DB column: expose as request_raw + legacy prompt_summary.
		row.RequestRaw = storedReq
		row.PromptSummary = storedReq
		out = append(out, row)
	}
	return out, rows.Err()
}

// VideosJSONFromClips encodes a short video list for dialog storage.
func VideosJSONFromClips(clips []map[string]any) string {
	if len(clips) == 0 {
		return "[]"
	}
	b, err := json.Marshal(clips)
	if err != nil {
		return "[]"
	}
	s := string(b)
	if len(s) > 8000 {
		s = s[:8000]
	}
	return s
}

// LLMUsage returns usage for day plus lifetime totals.
func (d *DB) LLMUsage(day string) LLMUsageSnapshot {
	out := LLMUsageSnapshot{Day: day}
	if d == nil || day == "" {
		return out
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_ = d.sql.QueryRow(`SELECT cost_cny, calls FROM llm_usage WHERE day = ?`, day).
		Scan(&out.DayCostCNY, &out.DayCalls)
	_ = d.sql.QueryRow(`SELECT total_cost_cny, total_calls FROM llm_totals WHERE id = 1`).
		Scan(&out.TotalCostCNY, &out.TotalCalls)
	return out
}

// DayLLMCost returns today's accumulated cost (0 if none).
func (d *DB) DayLLMCost(day string) float64 {
	return d.LLMUsage(day).DayCostCNY
}

var (
	reBearerToken = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9_\-\.]{8,}`)
	reSkKey       = regexp.MustCompile(`(?i)\bsk-[A-Za-z0-9]{8,}\b`)
	reAPIKeyKV    = regexp.MustCompile(`(?i)(api[_-]?key["']?\s*[:=]\s*["']?)[^"'\s,}{]{6,}`)
)

func redactSecrets(s string) string {
	if s == "" {
		return s
	}
	s = reBearerToken.ReplaceAllString(s, `${1}[REDACTED]`)
	s = reSkKey.ReplaceAllString(s, `sk-[REDACTED]`)
	s = reAPIKeyKV.ReplaceAllString(s, `${1}[REDACTED]`)
	return s
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// truncateBytes keeps as much of s as fits in n bytes (rune-safe). Soft cap note when cut.
func truncateBytes(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	cut := s[:n]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…[截断·上限64KB]"
}
