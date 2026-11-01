package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"timesavior/internal/activity"
	"timesavior/internal/audit"
	"timesavior/internal/config"
	"timesavior/internal/focus"
	"timesavior/internal/guard"
	"timesavior/internal/live"
	"timesavior/internal/llm"
	"timesavior/internal/policy"
	"timesavior/internal/proxy"
	"timesavior/internal/rules"
	"timesavior/internal/stats"
	"timesavior/internal/store"
	"timesavior/internal/sysproxy"
	"timesavior/internal/titleshort"
	"timesavior/internal/ui"
	"timesavior/internal/upstream"
)

// publishTitleShortLLMLog is wired in serveAdmin once the live hub exists.
var publishTitleShortLLMLog func()

func main() {
	cfgPath := flag.String("config", "configs/default.yaml", "config file path")
	noTray := flag.Bool("no-tray", false, "run without system tray")
	noSysProxy := flag.Bool("no-sysproxy", false, "do not set Windows system proxy")
	probeOnly := flag.Bool("probe", false, "log-only mode, do not block")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if *probeOnly {
		cfg.ProbeOnly = true
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		log.Fatalf("data dir: %v", err)
	}

	ruleStore, err := rules.LoadDir(cfg.RulesDir)
	if err != nil {
		log.Fatalf("load rules: %v", err)
	}
	engine := policy.New(cfg, ruleStore)

	db, err := store.Open(store.Options{
		Path:        filepath.Join(cfg.DataDir, "timesavior.db"),
		KeepDays:    cfg.Logging.RawKeepDays,
		MinWatchSec: cfg.MinWatchSec,
	})
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer db.Close()
	stats.SetMinWatchSec(cfg.MinWatchSec)
	logDir := filepath.Join(cfg.DataDir, "logs")
	if n := stats.BackfillPlayDB(db, logDir, cfg.Logging.RawKeepDays); n > 0 {
		log.Printf("play backfill: rebuilt %d day(s) from JSONL ∪ SQLite", n)
	}
	db.StartMaintenance(time.Hour)
	engine.AttachStore(db)
	defer engine.FlushPersist()

	if cfg.LLMJudge.Enabled && cfg.LLMJudge.APIKey != "" {
		llmClient := llm.NewClient(cfg.LLMJudge.BaseURL, cfg.LLMJudge.APIKey, cfg.LLMJudge.Model)
		engine.AttachLLM(llmClient, db)
		stats.SetTitleShortener(titleshort.New(db, llmClient, titleshort.Options{
			OnUpdated: func(_, _ string) {
				stats.BumpAfterShortName()
			},
			OnLLMDialog: func() {
				if publishTitleShortLLMLog != nil {
					publishTitleShortLLMLog()
				}
			},
		}))
		if cfg.LLMJudge.CostMonitorOn() {
			log.Printf("LLM 判定已启用 model=%s 费用监控开 limit=¥%.2f/日", cfg.LLMJudge.Model, cfg.LLMJudge.DailyCostLimitCNY)
		} else {
			log.Printf("LLM 判定已启用 model=%s 费用监控关（代金券余额差不可靠，不自动熔断；看板费用仅估算）", cfg.LLMJudge.Model)
		}
	} else if cfg.LLMJudge.Enabled {
		log.Printf("LLM 判定已配置但未找到 API Key（检查 secrets.yaml 或 SILICONFLOW_API_KEY）；达阈值后将直接 FullBlock")
		stats.SetTitleShortener(titleshort.New(db, nil, titleshort.Options{}))
	} else {
		stats.SetTitleShortener(titleshort.New(db, nil, titleshort.Options{}))
	}

	al, err := audit.NewStore(audit.Options{
		Dir:      logDir,
		KeepDays: cfg.Logging.KeepDays,
		DB:       db,
	})
	if err != nil {
		log.Fatalf("audit: %v", err)
	}
	defer al.Close()
	al.AttachStdLog()
	al.Event("app_start", map[string]any{
		"listen":    cfg.Listen,
		"probe":     cfg.ProbeOnly,
		"log_dir":   al.Dir(),
		"db_path":   db.Path(),
		"keep_days":     cfg.Logging.KeepDays,
		"raw_keep_days": cfg.Logging.RawKeepDays,
	})

	recent := activity.NewRecent(45*time.Second, 64)

	focusInterval := time.Duration(float64(time.Second) / cfg.Logging.FocusPollHz)
	ft := focus.NewTracker(al, focus.Options{
		Enabled:  cfg.Logging.FocusTrack,
		Interval: focusInterval,
		Recent:   recent,
		OnWatchTitle: func(site, title string) (bool, string) {
			return engine.NoteWatchMeta(site, title, "", nil, "", time.Now())
		},
	})
	ft.Start()
	defer ft.Stop()

	up := upstream.New(cfg.ClashUpstream)
	up.Start()
	defer up.Stop()

	srv, err := proxy.New(cfg, engine, up, al, recent)
	if err != nil {
		log.Fatalf("proxy: %v", err)
	}

	host, port, err := cfg.ListenHostPort()
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	var snap sysproxy.Snapshot
	if !*noSysProxy {
		snap, _ = sysproxy.Get()
		if sysproxy.LooksLikeClash(snap.ProxyServer, cfg.ClashUpstream.ProbePorts) {
			log.Printf("警告: 当前系统代理指向 Clash (%s)。TimeSavior 将接管系统代理；请关闭 Clash「系统代理」与 TUN，仅保留本地端口。", snap.ProxyServer)
			al.Event("clash_sysproxy_detected", map[string]any{"proxy": snap.ProxyServer})
		}
		if err := sysproxy.Set(host, port); err != nil {
			log.Printf("设置系统代理失败: %v", err)
		} else {
			log.Printf("系统代理已设为 %s:%d", host, port)
			al.Event("sysproxy_set", map[string]any{"server": fmt.Sprintf("%s:%d", host, port)})
		}
		go guardSysProxy(cfg, host, port, al)
	}

	// Mutual supervision: keep watchdog alive so Task Manager kill of either side recovers quickly.
	absCfg, _ := filepath.Abs(*cfgPath)
	wd := filepath.Join(guard.SelfDir(), "timesavior-watchdog.exe")
	if _, err := os.Stat(wd); err != nil {
		wd = filepath.Join(guard.SelfDir(), "timesavior-watchdog")
	}
	go guard.EnsurePeer(wd, []string{"-config", absCfg, "-core", mustAbsExe()}, time.Second)

	go serveAdmin(engine, up, ruleStore, cfg, al, ft, db)

	go func() {
		if err := srv.ListenAndServe(); err != nil {
			log.Fatalf("proxy serve: %v", err)
		}
	}()

	admin := ui.AdminURL(cfg.Listen)
	unlock := ui.UnlockURL(cfg.Listen)

	if *noTray {
		log.Printf("CA 证书: %s （请安装到“受信任的根证书颁发机构”）", srv.CACertPath())
		log.Printf("看板: %s", admin)
		log.Printf("解锁: %s", unlock)
		select {} // no tray / no exit UI — stay up; use uninstall to stop
	}

	app := &ui.App{
		Engine:    engine,
		Upstream:  up,
		Dashboard: admin,
		UnlockURL: unlock,
	}
	log.Printf("CA 证书: %s", srv.CACertPath())
	log.Printf("看板: %s", admin)
	log.Printf("解锁: %s", unlock)
	app.Run()
	// Tray should not quit; if it does, restore proxy then block so watchdog can relaunch.
	cleanup(cfg, !*noSysProxy, snap, al)
	select {}
}

func mustAbsExe() string {
	self, err := os.Executable()
	if err != nil {
		return "timesavior.exe"
	}
	abs, err := filepath.Abs(self)
	if err != nil {
		return self
	}
	return abs
}

func cleanup(cfg *config.Config, restore bool, snap sysproxy.Snapshot, al *audit.Logger) {
	if restore {
		if err := sysproxy.Restore(snap); err != nil {
			log.Printf("恢复系统代理失败: %v", err)
		} else {
			al.Event("sysproxy_restored", map[string]any{"server": snap.ProxyServer, "enable": snap.ProxyEnable})
		}
	}
}

func guardSysProxy(cfg *config.Config, host string, port int, al *audit.Logger) {
	iv := time.Duration(cfg.ProxyGuardSeconds) * time.Second
	t := time.NewTicker(iv)
	defer t.Stop()
	for range t.C {
		rewrote, current, err := sysproxy.Ensure(host, port)
		if err != nil {
			continue
		}
		if rewrote {
			msg := map[string]any{"from": current, "to": fmt.Sprintf("%s:%d", host, port)}
			if sysproxy.LooksLikeClash(current, cfg.ClashUpstream.ProbePorts) {
				msg["note"] = "detected clash system proxy; wrote back to TimeSavior. Disable Clash system proxy/TUN."
			}
			al.Event("sysproxy_rewritten", msg)
			log.Printf("系统代理被改写为 %s，已写回 TimeSavior", current)
		}
	}
}

func watchRules(dir string, store *rules.Store, al *audit.Logger, onReload func()) {
	var lastMod time.Time
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for range t.C {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var newest time.Time
		for _, e := range entries {
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
		if newest.After(lastMod) && !lastMod.IsZero() {
			if err := store.Reload(); err != nil {
				log.Printf("规则热更新失败: %v", err)
			} else {
				log.Printf("规则已热更新")
				al.Event("rules_reloaded", nil)
				if onReload != nil {
					onReload()
				}
			}
		}
		if !newest.IsZero() {
			lastMod = newest
		}
	}
}

func adminPort(proxyPort int) int {
	return proxyPort + 1
}

func serveAdmin(engine *policy.Engine, up *upstream.Manager, ruleStore *rules.Store, cfg *config.Config, al *audit.Store, ft *focus.Tracker, db *store.DB) {
	_, port, err := cfg.ListenHostPort()
	if err != nil {
		return
	}
	adminURL := ui.AdminURL(cfg.Listen)
	hub := live.New()
	publishTitleShortLLMLog = func() {
		hub.Publish(live.KindLLMLog, map[string]any{"reason": "title_short"})
	}
	stats.SetShortNameNotify(func() {
		hub.PublishDebounced(live.KindStats, map[string]any{"reason": "title_short"}, 400*time.Millisecond)
	})

	buildStatus := func() map[string]any {
		st := engine.Status(time.Now())
		st["upstream"] = up.Status()
		st["sites_loaded"] = len(ruleStore.Sites())
		st["probe_only"] = cfg.ProbeOnly
		st["dashboard"] = adminURL
		st["unlock"] = ui.UnlockURL(cfg.Listen)
		st["log_dir"] = al.Dir()
		if sqlDB := al.DB(); sqlDB != nil {
			st["db_path"] = sqlDB.Path()
		}
		st["recent_events"] = al.RecentEvents(20)
		for k, v := range ui.WhitelistStatusPayload(cfg, ruleStore) {
			st[k] = v
		}
		return st
	}

	go watchRules(cfg.RulesDir, ruleStore, al, func() {
		engine.SyncWatchRules()
		hub.Publish(live.KindStatus, buildStatus())
	})

	engine.SetOnChange(func(kind policy.ChangeKind) {
		switch kind {
		case policy.ChangeUnlock:
			hub.Publish(live.KindUnlock, engine.UnlockChallengeState(time.Now()))
		case policy.ChangeStatus:
			hub.PublishDebounced(live.KindStatus, buildStatus(), 250*time.Millisecond)
		case policy.ChangeLLMLog:
			hub.Publish(live.KindLLMLog, map[string]any{"reason": "judge"})
		}
	})
	if db != nil {
		db.SetOnFactsChange(func() {
			stats.InvalidateCache()
			hub.PublishDebounced(live.KindStats, map[string]any{"reason": "facts"}, 400*time.Millisecond)
		})
	}

	// Advance challenge timers / unlock expiry; push only when fingerprint changes.
	// Also detect free_mode / policy_day flips without client polling.
	go func() {
		var modeFP string
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			now := time.Now()
			engine.TickChallenge(now)
			st := engine.Status(now)
			fp := fmt.Sprintf("%v|%v|%v|%v",
				st["free_mode"], st["temp_unlock_active"], st["policy_day"], st["temp_unlock_until"])
			if fp != modeFP {
				modeFP = fp
				hub.Publish(live.KindStatus, buildStatus())
			}
		}
	}()

	// Soft tick for open-focus duration while a session is live (not a full UI poll).
	go func() {
		t := time.NewTicker(12 * time.Second)
		defer t.Stop()
		for range t.C {
			if hub.ClientCount() == 0 || ft == nil {
				continue
			}
			if ft.Snapshot(time.Now()) == nil {
				continue
			}
			stats.InvalidateCache()
			hub.Publish(live.KindStats, map[string]any{"reason": "open_focus"})
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		ui.WriteDashboard(w, ui.BuildDashboard(cfg, engine, ruleStore, up, adminURL, al))
	})
	mux.HandleFunc("/unlock", func(w http.ResponseWriter, r *http.Request) {
		ui.WriteUnlockPage(w)
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		days := 1
		if v := r.URL.Query().Get("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				days = n
			}
		}
		from := r.URL.Query().Get("from")
		to := r.URL.Query().Get("to")
		ui.WriteStatsPage(w, adminURL, days, from, to)
	})
	mux.HandleFunc("/llm-logs", func(w http.ResponseWriter, r *http.Request) {
		ui.WriteLLMLogsPage(w)
	})
	mux.Handle("/__timesavior/events", hub)
	mux.HandleFunc("/__timesavior/llm-logs", func(w http.ResponseWriter, r *http.Request) {
		limit := 10
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				limit = n
			}
		}
		items := []store.LLMDialog{}
		keep := 0
		if db != nil {
			keep = db.KeepDays()
			if rows, err := db.RecentLLMDialogs(limit); err == nil && rows != nil {
				items = rows
			}
		}
		writeJSON(w, map[string]any{
			"items":      items,
			"limit":      limit,
			"keep_days":  keep,
			"generated_at": time.Now().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("/__timesavior/stats", func(w http.ResponseWriter, r *http.Request) {
		q := stats.Query{Days: 1}
		if v := r.URL.Query().Get("days"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				q.Days = n
			}
		}
		q.From = r.URL.Query().Get("from")
		q.To = r.URL.Query().Get("to")
		var open map[string]any
		if ft != nil {
			open = ft.Snapshot(time.Now())
		}
		writeJSON(w, stats.BuildCachedQuery(al.Dir(), q, open, al.DB()))
	})
	mux.HandleFunc("/__timesavior/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, buildStatus())
	})
	mux.HandleFunc("/__timesavior/unlock/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		st := engine.StartUnlockChallenge(time.Now())
		al.Event("unlock_challenge_start", map[string]any{"phase": st["phase"]})
		writeJSON(w, st)
	})
	mux.HandleFunc("/__timesavior/unlock/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, engine.UnlockChallengeState(time.Now()))
	})
	mux.HandleFunc("/__timesavior/unlock/answer", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			ID     string `json:"id"`
			Choice int    `json:"choice"`
		}
		raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		_ = json.Unmarshal(raw, &body)
		st := engine.AnswerUnlockChallenge(body.ID, body.Choice, time.Now())
		if st["phase"] == "success" {
			al.Event("temp_unlock", map[string]any{"ok": true, "via": "challenge"})
		}
		writeJSON(w, st)
	})
	mux.HandleFunc("/__timesavior/unlock/cancel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		st := engine.CancelTempUnlock(time.Now())
		al.Event("temp_unlock_cancel", map[string]any{
			"phase": st["phase"], "active": st["temp_unlock_active"],
		})
		writeJSON(w, st)
	})
	addr := fmt.Sprintf("127.0.0.1:%d", adminPort(port))
	log.Printf("dashboard: %s", adminURL)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Printf("dashboard listen failed on %s: %v", addr, err)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
