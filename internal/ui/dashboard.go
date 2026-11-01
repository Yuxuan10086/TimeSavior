package ui

import (
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"timesavior/internal/audit"
	"timesavior/internal/config"
	"timesavior/internal/policy"
	"timesavior/internal/rules"
	"timesavior/internal/upstream"
)

type DashboardSite struct {
	Name              string
	Kind              string
	KindLabel         string
	Enabled           bool
	Domains           []string
	PlayDomains       []string
	IdleResetMinutes  int
	BlockAfterMinutes int
	WatchMinutes      int
	FullBlockActive   bool
	FullBlockUntil    string
	TracksWatch       bool
	WatchExempt       bool
	ExemptLabel       string
	WhitelistChannels []string
	StatusLabel       string
}

type DashboardFreeWindow struct {
	Start    string
	End      string
	Weekdays string
}

type DashboardWhitelistRule struct {
	Site        string // raw: all / youtube / …
	SiteLabel   string // 全部 / YouTube / …
	Kind        string // channel / title
	KindLabel   string // 频道 / 标题
	Keyword     string
	Mode        string // exact / contains
	ModeLabel   string // 精确匹配 / 模糊匹配
}

type DashboardData struct {
	GeneratedAt          string
	PolicyDay            string
	DayStartHour         int
	Mode                 string
	ModePlain            string
	Upstream             string
	ProbeOnly            bool
	TempUnlockActive     bool
	TempUnlockUntil      string
	TempUnlockStarted    bool
	TempUnlockStartedAt  string
	TempUnlockUsed       int
	TempUnlockLimit      int
	TempUnlockMins       int
	TempUnlockRemain     int
	FeedUnlockWindow     bool
	UnlockWindowStart    string
	UnlockWindowEndLabel string
	Listen               string
	AdminURL             string
	UnlockURL            string
	UnknownDefault       string
	LogDir               string
	KeepDays             int
	DBPath               string
	FreeWindows          []DashboardFreeWindow
	Sites                []DashboardSite
	WhitelistRules       []DashboardWhitelistRule
	LLMEnabled           bool
	LLMStatus            string
	LLMStatusLabel       string
	LLMDayCost           string
	LLMTotalCost         string
	LLMDayCalls          int
	LLMDailyLimit        string
	LLMCircuitOpen       bool
	LLMCostEstimated     bool
	LLMModel             string
}

// WhitelistStatusPayload is embedded in status.json / SSE for live dashboard patching.
func WhitelistStatusPayload(cfg *config.Config, store *rules.Store) map[string]any {
	_ = store
	rules := collectWhitelistRules(cfg)
	rows := make([]map[string]any, 0, len(rules))
	for _, r := range rules {
		rows = append(rows, map[string]any{
			"site":       r.Site,
			"site_label": r.SiteLabel,
			"kind":       r.Kind,
			"kind_label": r.KindLabel,
			"keyword":    r.Keyword,
			"mode":       r.Mode,
			"mode_label": r.ModeLabel,
		})
	}
	return map[string]any{
		"watch_whitelist_rules": rows,
	}
}

func collectWhitelistRules(cfg *config.Config) []DashboardWhitelistRule {
	if cfg == nil {
		return nil
	}
	raw := cfg.WatchRules()
	out := make([]DashboardWhitelistRule, 0, len(raw))
	for _, r := range raw {
		site := r.Site
		if site == "" {
			site = "all"
		}
		kind := r.Kind
		if kind == "" {
			kind = "title"
		}
		mode := r.Mode
		if mode == "" {
			mode = "contains"
		}
		out = append(out, DashboardWhitelistRule{
			Site:      site,
			SiteLabel: whitelistSiteLabel(site),
			Kind:      kind,
			KindLabel: whitelistKindLabel(kind),
			Keyword:   r.Keyword,
			Mode:      mode,
			ModeLabel: whitelistModeLabel(mode),
		})
	}
	return out
}

func whitelistSiteLabel(site string) string {
	switch strings.ToLower(strings.TrimSpace(site)) {
	case "", "all":
		return "全部"
	case "youtube":
		return "YouTube"
	case "bilibili":
		return "B站"
	case "douyin":
		return "抖音"
	case "zhihu":
		return "知乎"
	default:
		return site
	}
}

func whitelistKindLabel(kind string) string {
	switch strings.ToLower(kind) {
	case "channel":
		return "频道"
	default:
		return "标题"
	}
}

func whitelistModeLabel(mode string) string {
	switch strings.ToLower(mode) {
	case "exact":
		return "精确匹配"
	default:
		return "模糊匹配"
	}
}

func BuildDashboard(
	cfg *config.Config,
	engine *policy.Engine,
	store *rules.Store,
	up *upstream.Manager,
	adminURL string,
	al *audit.Store,
) DashboardData {
	now := time.Now()
	st := engine.Status(now)
	mode := "推荐拦截中"
	modePlain := "soft"
	if v, _ := st["free_mode"].(bool); v {
		mode = "自由时段"
		modePlain = "free"
	}
	if v, _ := st["temp_unlock_active"].(bool); v {
		mode = "临时解锁中"
		modePlain = "unlock"
	}

	var windows []DashboardFreeWindow
	for _, w := range cfg.FreeWindows {
		days := "每天"
		if len(w.Weekdays) > 0 {
			days = strings.Join(w.Weekdays, ", ")
		}
		windows = append(windows, DashboardFreeWindow{
			Start: w.Start, End: w.End, Weekdays: days,
		})
	}

	siteStatus, _ := st["sites"].(map[string]any)
	var sites []DashboardSite
	for _, site := range store.Sites() {
		ds := DashboardSite{
			Name:              site.Name,
			Kind:              site.Kind,
			KindLabel:         "视频",
			Enabled:           site.Enabled,
			Domains:           site.Domains,
			PlayDomains:       site.PlayDomains,
			IdleResetMinutes:  site.EntertainmentSession.WatchIdleResetMinutes,
			BlockAfterMinutes: site.EntertainmentSession.WatchBlockAfterMinutes,
			TracksWatch:       site.TracksWatch(),
			WhitelistChannels: append([]string{}, site.WatchWhitelist.Channels...),
		}
		if site.Kind == "text" {
			ds.KindLabel = "图文"
		}
		if raw, ok := siteStatus[site.Name].(map[string]any); ok {
			if v, ok := raw["watch_minutes"].(int); ok {
				ds.WatchMinutes = v
			}
			if v, ok := raw["full_block_active"].(bool); ok {
				ds.FullBlockActive = v
			}
			if v, ok := raw["full_block_until"].(string); ok {
				ds.FullBlockUntil = v
			}
			if v, ok := raw["watch_exempt"].(bool); ok {
				ds.WatchExempt = v
			}
			if v, ok := raw["exempt_label"].(string); ok {
				ds.ExemptLabel = v
			}
		}
		switch {
		case !ds.Enabled:
			ds.StatusLabel = "未启用"
		case modePlain == "free" || modePlain == "unlock":
			ds.StatusLabel = "暂时放行"
		case ds.FullBlockActive:
			ds.StatusLabel = "整站封锁"
		case ds.WatchExempt:
			ds.StatusLabel = "白名单中"
		case !ds.TracksWatch:
			ds.StatusLabel = "拦推荐流"
		default:
			ds.StatusLabel = "管控中"
		}
		sites = append(sites, ds)
	}
	wlRules := collectWhitelistRules(cfg)

	upStatus := "direct"
	if up != nil {
		upStatus = up.Status()
	}
	tempUntil, _ := st["temp_unlock_until"].(string)
	tempUsed, _ := st["temp_unlock_used"].(int)
	tempActive, _ := st["temp_unlock_active"].(bool)
	tempStarted, _ := st["temp_unlock_started"].(bool)
	tempStartedAt, _ := st["temp_unlock_started_at"].(string)
	inWindow, _ := st["feed_unlock_window"].(bool)
	policyDay, _ := st["policy_day"].(string)

	logDir := ""
	dbPath := ""
	if al != nil {
		logDir = al.Dir()
		if db := al.DB(); db != nil {
			dbPath = db.Path()
		}
	}

	dayLimit := cfg.TempUnlockDailyLimit
	if dayLimit <= 0 {
		dayLimit = 1
	}
	remain := dayLimit - tempUsed
	if remain < 0 {
		remain = 0
	}
	unlockMins := cfg.TempUnlockMinutes
	if unlockMins <= 0 {
		unlockMins = 30
	}
	winStart := cfg.TempUnlockWindowStart
	if winStart == "" {
		winStart = "21:30"
	}
	dayHour := cfg.DayStartHour
	if dayHour <= 0 {
		dayHour = 4
	}
	winEndLabel := fmt.Sprintf("次日 %02d:00", dayHour)

	llmEnabled := false
	llmStatus := "disabled"
	llmStatusLabel := "未启用"
	llmDayCost := "0"
	llmTotalCost := "0"
	llmDayCalls := 0
	llmDailyLimit := "—"
	llmCircuit := false
	llmEstimated := false
	llmModel := ""
	if raw, ok := st["llm"].(map[string]any); ok {
		if v, ok := raw["enabled"].(bool); ok {
			llmEnabled = v
		}
		if v, ok := raw["status"].(string); ok {
			llmStatus = v
		}
		if v, ok := raw["circuit_open"].(bool); ok {
			llmCircuit = v
		}
		if v, ok := raw["cost_estimated_only"].(bool); ok {
			llmEstimated = v
		}
		if v, ok := raw["day_calls"].(int); ok {
			llmDayCalls = v
		}
		if v, ok := raw["model"].(string); ok {
			llmModel = v
		}
		llmDayCost = fmtCost(raw["day_cost_cny"])
		llmTotalCost = fmtCost(raw["total_cost_cny"])
		if mon, ok := raw["cost_monitor"].(bool); ok && mon {
			llmDailyLimit = fmtCost(raw["daily_limit_cny"])
		} else {
			llmDailyLimit = "未启用熔断"
		}
		switch llmStatus {
		case "circuit_open":
			llmStatusLabel = "费用熔断"
		case "monitor_off":
			llmStatusLabel = "费用监控关"
		case "ok":
			llmStatusLabel = "正常"
		default:
			llmStatusLabel = "未启用"
		}
	}

	return DashboardData{
		GeneratedAt:          now.Format("2006-01-02 15:04:05"),
		PolicyDay:            policyDay,
		DayStartHour:         dayHour,
		Mode:                 mode,
		ModePlain:            modePlain,
		Upstream:             upStatus,
		ProbeOnly:            cfg.ProbeOnly,
		TempUnlockActive:     tempActive,
		TempUnlockUntil:      tempUntil,
		TempUnlockStarted:    tempStarted,
		TempUnlockStartedAt:  tempStartedAt,
		TempUnlockUsed:       tempUsed,
		TempUnlockLimit:      dayLimit,
		TempUnlockMins:       unlockMins,
		TempUnlockRemain:     remain,
		FeedUnlockWindow:     inWindow,
		UnlockWindowStart:    winStart,
		UnlockWindowEndLabel: winEndLabel,
		Listen:               cfg.Listen,
		AdminURL:             adminURL,
		UnlockURL:            UnlockURL(cfg.Listen),
		UnknownDefault:       cfg.UnknownDefault,
		LogDir:               logDir,
		KeepDays:             cfg.Logging.KeepDays,
		DBPath:               dbPath,
		FreeWindows:          windows,
		Sites:                sites,
		WhitelistRules:       wlRules,
		LLMEnabled:           llmEnabled,
		LLMStatus:            llmStatus,
		LLMStatusLabel:       llmStatusLabel,
		LLMDayCost:           llmDayCost,
		LLMTotalCost:         llmTotalCost,
		LLMDayCalls:          llmDayCalls,
		LLMDailyLimit:        llmDailyLimit,
		LLMCircuitOpen:       llmCircuit,
		LLMCostEstimated:     llmEstimated,
		LLMModel:             llmModel,
	}
}

func fmtCost(v any) string {
	switch x := v.(type) {
	case float64:
		return strconv.FormatFloat(x, 'f', 4, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', 4, 64)
	case int:
		return strconv.Itoa(x)
	case string:
		return x
	default:
		return "0"
	}
}

func WriteDashboard(w http.ResponseWriter, data DashboardData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

var dashboardTmpl = template.Must(template.New("dashboard").Funcs(template.FuncMap{
	"join": strings.Join,
	"joinComma": func(ss []string) string {
		return strings.Join(ss, ", ")
	},
	"siteLabel": func(name string) string {
		switch name {
		case "youtube":
			return "YouTube"
		case "bilibili":
			return "B站"
		case "douyin":
			return "抖音"
		case "zhihu":
			return "知乎"
		default:
			return name
		}
	},
}).Parse(dashboardHTML))

var dashboardHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>TimeSavior · 状态</title>
` + fontLinks + `
<style>
` + SharedCSS + `
.mode-banner {
  display:flex; flex-wrap:wrap; gap:.75rem 1.25rem; align-items:center;
  justify-content:space-between;
}
.mode-banner .title {
  font-family:var(--display); font-size:1.35rem; font-weight:700;
  letter-spacing:-.02em; margin:0;
}
#live-root { min-height:8rem; }
</style>
</head>
<body>
<div class="shell">
  <header class="topbar">
    <div>
      <p class="brand-mark">TimeSavior</p>
      <h1>状态看板</h1>
    </div>
    <div class="live" title="事件推送"><span class="dot" aria-hidden="true"></span><span id="live">连接中</span></div>
  </header>
` + shellNav("dashboard") + `
  <div id="live-root">
  <section class="panel accent-edge">
    <h2>当前限制</h2>
    <div class="mode-banner">
      <div>
        <p class="title" id="m-mode">{{.Mode}}</p>
      </div>
      <div class="chip-row">
        <span class="chip">策略日 <b id="m-day">{{.PolicyDay}}</b></span>
        <span class="chip">换日 <b>{{.DayStartHour}}:00</b></span>
      </div>
    </div>
  </section>

  <section class="panel">
    <h2>临时解锁</h2>
    <p class="hint" style="margin:0 0 .85rem">{{.UnlockWindowStart}}–{{.UnlockWindowEndLabel}} 内首次打开管控站推荐页，自动解锁 {{.TempUnlockMins}} 分钟；未打开不顺延。</p>
    <div class="metrics">
      <div class="metric">
        <div class="k">今日额度</div>
        <div class="v" id="m-remain">{{if eq .TempUnlockRemain 0}}已用{{else}}未用{{end}}</div>
      </div>
      <div class="metric">
        <div class="k">可用窗口</div>
        <div class="v" id="m-window">{{if .FeedUnlockWindow}}进行中{{else}}未开始{{end}}</div>
      </div>
      <div class="metric">
        <div class="k">时长</div>
        <div class="v">{{.TempUnlockMins}} 分钟</div>
      </div>
      <div class="metric">
        <div class="k">当前状态</div>
        <div class="v" id="m-unlock">{{if .TempUnlockActive}}至 {{.TempUnlockUntil}}{{else if .TempUnlockStarted}}今日已用完{{else}}未开始{{end}}</div>
      </div>
    </div>
  </section>

  <section class="panel">
    <h2>AI 判定费用</h2>
    <div class="metrics">
      <div class="metric">
        <div class="k" id="m-llm-day-k">{{if .LLMCostEstimated}}今日费用（仅估算）{{else}}今日 AI 费用{{end}}</div>
        <div class="v" id="m-llm-day">¥{{.LLMDayCost}}</div>
      </div>
      <div class="metric">
        <div class="k" id="m-llm-total-k">{{if .LLMCostEstimated}}累计费用（仅估算）{{else}}历史累计费用{{end}}</div>
        <div class="v" id="m-llm-total">¥{{.LLMTotalCost}}</div>
      </div>
      <div class="metric">
        <div class="k">日限额</div>
        <div class="v" id="m-llm-limit">{{if .LLMCostEstimated}}{{.LLMDailyLimit}}{{else}}¥{{.LLMDailyLimit}}{{end}}</div>
      </div>
      <div class="metric">
        <div class="k">状态</div>
        <div class="v" id="m-llm-status">{{.LLMStatusLabel}}{{if .LLMCircuitOpen}} · 熔断{{end}}</div>
      </div>
    </div>
  </section>

  <section class="panel">
    <h2>各站点状态</h2>
    <table class="data">
      <thead>
        <tr><th>站点</th><th>类型</th><th>状态</th><th>连续观看</th></tr>
      </thead>
      <tbody>
      {{range .Sites}}
        <tr data-site="{{.Name}}">
          <td>
            <strong>{{siteLabel .Name}}</strong>
            {{if not .Enabled}}<span class="tag">已禁用</span>{{end}}
          </td>
          <td>{{.KindLabel}}</td>
          <td class="js-site-status">{{.StatusLabel}}</td>
          <td class="mono js-site-watch">{{if .TracksWatch}}{{.WatchMinutes}} / {{.BlockAfterMinutes}} 分钟{{else}}—{{end}}</td>
        </tr>
      {{else}}
        <tr><td colspan="4" class="muted">未加载站点规则</td></tr>
      {{end}}
      </tbody>
    </table>
  </section>

  <section class="panel">
    <h2>白名单</h2>
    <table class="data">
      <thead>
        <tr><th>站点</th><th>类型</th><th>关键词</th><th>模式</th></tr>
      </thead>
      <tbody id="wl-rules">
      {{range .WhitelistRules}}
        <tr>
          <td>{{.SiteLabel}}</td>
          <td>{{.KindLabel}}</td>
          <td class="mono">{{.Keyword}}</td>
          <td>{{.ModeLabel}}</td>
        </tr>
      {{else}}
        <tr><td colspan="4" class="muted">暂无</td></tr>
      {{end}}
      </tbody>
    </table>
  </section>

  <section class="panel">
    <h2>解封规则</h2>
    <p class="hint" style="margin:0">{{.UnlockWindowStart}}–{{.UnlockWindowEndLabel}} 内首次打开推荐页起 {{.TempUnlockMins}} 分钟；到期后恢复推荐拦截。取消见「临时解锁」页。</p>
  </section>
  </div>

  <details class="advanced">
    <summary>高级 / 技术信息</summary>
    <div class="tech">
      代理监听 <code>{{.Listen}}</code><br/>
      上游 <span id="m-up">{{.Upstream}}</span>
      · 探针模式 {{if .ProbeOnly}}开{{else}}关{{end}}
      · 未知路径 <code>{{.UnknownDefault}}</code><br/>
      日志 <code>{{.LogDir}}</code> · 保留 {{.KeepDays}} 天
      {{if .DBPath}}· 数据库 <code>{{.DBPath}}</code>{{end}}<br/>
      <a href="/__timesavior/status">status.json</a>
      · <a href="/__timesavior/stats?days=1">stats.json</a>
    </div>
  </details>

  <p class="err" id="err"></p>
</div>
<script>
` + liveSSEJS + `
(function () {
  const esc = (s) => String(s ?? "").replace(/[&<>"']/g, c =>
    ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  function fmtTime(iso) {
    if (!iso) return "";
    const d = new Date(iso);
    if (isNaN(d)) return iso;
    const p = (n) => String(n).padStart(2,"0");
    return d.getFullYear()+"-"+p(d.getMonth()+1)+"-"+p(d.getDate())+" "+p(d.getHours())+":"+p(d.getMinutes())+":"+p(d.getSeconds());
  }
  function modeOf(d) {
    if (d.temp_unlock_active) return {title:"临时解锁中", plain:"unlock"};
    if (d.free_mode) return {title:"自由时段", plain:"free"};
    return {title:"推荐拦截中", plain:"soft"};
  }
  function siteStatus(st, tracks, modePlain) {
    if (modePlain === "free" || modePlain === "unlock") return "暂时放行";
    if (st && st.full_block_active) return "整站封锁";
    if (st && st.watch_exempt) return "白名单中";
    if (!tracks) return "拦推荐流";
    return "管控中";
  }
  function siteLabel(name) {
    return ({youtube:"YouTube", bilibili:"B站", douyin:"抖音", zhihu:"知乎"})[name] || name;
  }
  function patchWhitelist(d) {
    const rules = d.watch_whitelist_rules;
    if (rules == null) return;
    const body = document.getElementById("wl-rules");
    if (!body) return;
    let html;
    if (!rules.length) {
      html = '<tr><td colspan="4" class="muted">暂无</td></tr>';
    } else {
      html = rules.map(r => {
        return '<tr>' +
          '<td>'+esc(r.site_label || r.site || "")+'</td>' +
          '<td>'+esc(r.kind_label || r.kind || "")+'</td>' +
          '<td class="mono">'+esc(r.keyword || "")+'</td>' +
          '<td>'+esc(r.mode_label || r.mode || "")+'</td>' +
          '</tr>';
      }).join("");
    }
    if (body.dataset.html !== html) {
      body.innerHTML = html;
      body.dataset.html = html;
    }
  }
  function setText(id, text) {
    const el = document.getElementById(id);
    if (el && el.textContent !== text) el.textContent = text;
  }
  function patchStatus(d) {
    const err = document.getElementById("err");
    err.textContent = "";
    const mode = modeOf(d);
    setText("m-mode", mode.title);
    if (d.upstream) setText("m-up", d.upstream);
    if (d.policy_day != null) setText("m-day", d.policy_day);

    const used = d.temp_unlock_used ?? 0;
    const started = !!(d.temp_unlock_started || used > 0);
    setText("m-remain", started ? "已用" : "未用");
    setText("m-window", d.feed_unlock_window ? "进行中" : "未开始");
    const unlock = document.getElementById("m-unlock");
    if (unlock) {
      if (d.temp_unlock_active) {
        unlock.textContent = "至 " + fmtTime(d.temp_unlock_until);
      } else if (started) {
        unlock.textContent = "今日已用完";
      } else {
        unlock.textContent = "未开始";
      }
    }

    const llm = d.llm || {};
    const fmtYen = (n) => "¥" + (Number(n)||0).toFixed(4);
    const estOnly = !!llm.cost_estimated_only;
    setText("m-llm-day-k", estOnly ? "今日费用（仅估算）" : "今日 AI 费用");
    setText("m-llm-total-k", estOnly ? "累计费用（仅估算）" : "历史累计费用");
    setText("m-llm-day", fmtYen(llm.day_cost_cny));
    setText("m-llm-total", fmtYen(llm.total_cost_cny));
    if (llm.cost_monitor) setText("m-llm-limit", fmtYen(llm.daily_limit_cny));
    else setText("m-llm-limit", "未启用熔断");
    let llmLabel = "未启用";
    if (llm.status === "ok") llmLabel = "正常";
    else if (llm.status === "monitor_off") llmLabel = "费用监控关";
    else if (llm.status === "circuit_open") llmLabel = "费用熔断";
    if (llm.circuit_open && llm.status !== "circuit_open") llmLabel = "费用熔断";
    setText("m-llm-status", llmLabel);

    const sites = d.sites || {};
    document.querySelectorAll("tr[data-site]").forEach(tr => {
      const name = tr.getAttribute("data-site");
      const st = sites[name] || {};
      const watch = tr.querySelector(".js-site-watch");
      const tracks = watch && watch.textContent.trim() !== "—";
      const blockAfter = tracks ? (watch.textContent.split("/")[1] || "").replace(/[^0-9]/g,"") : "0";
      const label = siteStatus(st, tracks, mode.plain);
      const status = tr.querySelector(".js-site-status");
      if (status) {
        if (st.full_block_active && mode.plain === "soft") {
          const html = '<span class="danger">整站封锁</span>' +
            (st.full_block_until ? '<div class="muted">至 '+esc(fmtTime(st.full_block_until))+'</div>' : '');
          if (status.dataset.html !== html) {
            status.innerHTML = html;
            status.dataset.html = html;
          }
        } else if (status.textContent !== label) {
          status.textContent = label;
          delete status.dataset.html;
        }
      }
      if (watch && tracks) {
        const next = (st.watch_minutes ?? 0) + " / " + blockAfter + " 分钟";
        if (watch.textContent !== next) watch.textContent = next;
      }
    });
    patchWhitelist(d);
    const live = document.getElementById("live");
    if (live) live.textContent = "已更新 " + new Date().toLocaleTimeString();
  }
  tsConnectLive({
    status: (d) => { if (d && (d.sites || d.policy_day != null || d.free_mode != null || d.watch_whitelist_rules != null)) patchStatus(d); }
  });
})();
</script>
</body>
</html>`

// AdminURL builds the dashboard URL (admin listens on proxyPort+1).
func AdminURL(listen string) string {
	host, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://127.0.0.1:18889/"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return "http://127.0.0.1:18889/"
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s:%d/", host, port+1)
}

// UnlockURL is the temp-unlock challenge page.
func UnlockURL(listen string) string {
	base := strings.TrimRight(AdminURL(listen), "/")
	return base + "/unlock"
}
