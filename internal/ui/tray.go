package ui

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"time"

	"github.com/getlantern/systray"

	"timesavior/internal/policy"
	"timesavior/internal/upstream"
)

type App struct {
	Engine    *policy.Engine
	Upstream  *upstream.Manager
	Dashboard string
	UnlockURL string
	Title     string

	mu         sync.Mutex
	statusItem *systray.MenuItem
	upItem     *systray.MenuItem
	lastTip    string
}

func (a *App) Run() {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		select {}
	}
	// Keep GUI/message pump on this OS thread (required for reliable tray menus).
	runtime.LockOSThread()
	systray.Run(a.onReady, a.onExit)
}

func (a *App) onReady() {
	systray.SetTitle("TS")
	systray.SetTooltip("TimeSavior")
	systray.SetIcon(iconData)

	a.statusItem = systray.AddMenuItem("状态: 启动中", "当前管控状态")
	a.statusItem.Disable()
	a.upItem = systray.AddMenuItem("上游: ...", "Clash / 直连")
	a.upItem.Disable()
	systray.AddSeparator()
	mStatus := systray.AddMenuItem("刷新状态", "刷新托盘状态")
	mDash := systray.AddMenuItem("打开状态看板", "只读网页：用法与当前规则")
	mStats := systray.AddMenuItem("打开使用统计", "应用前台时长与视频播放时长")
	mUnlock := systray.AddMenuItem("临时解锁", "打开网页挑战（等待+算术题）")

	log.Printf("tray ready")
	go a.refreshLoop()

	go func() {
		for {
			select {
			case <-mStatus.ClickedCh:
				a.refresh()
			case <-mDash.ClickedCh:
				openURL(a.Dashboard)
			case <-mStats.ClickedCh:
				url := a.Dashboard
				if url != "" {
					// dashboard is http://127.0.0.1:18889/ → stats sibling
					if len(url) > 0 && url[len(url)-1] == '/' {
						url = url + "stats"
					} else {
						url = url + "/stats"
					}
				}
				openURL(url)
			case <-mUnlock.ClickedCh:
				url := a.UnlockURL
				if url == "" {
					url = a.Dashboard
				}
				openURL(url)
			}
		}
	}()
}

func (a *App) onExit() {}

func (a *App) refreshLoop() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	a.refresh()
	for range t.C {
		a.refresh()
	}
}

func (a *App) refresh() {
	now := time.Now()
	st := a.Engine.Status(now)
	mode := "SoftBlock"
	if v, _ := st["free_mode"].(bool); v {
		mode = "FreeMode"
	}
	if v, _ := st["temp_unlock_active"].(bool); v {
		mode = "TempUnlock"
	}
	up := "direct"
	if a.Upstream != nil {
		up = a.Upstream.Status()
	}
	a.mu.Lock()
	if a.statusItem != nil {
		a.statusItem.SetTitle(fmt.Sprintf("状态: %s", mode))
	}
	if a.upItem != nil {
		a.upItem.SetTitle(fmt.Sprintf("上游: %s", up))
	}
	tip := fmt.Sprintf("TimeSavior | %s | 上游 %s", mode, up)
	changed := tip != a.lastTip
	if changed {
		a.lastTip = tip
	}
	a.mu.Unlock()
	if changed {
		systray.SetTooltip(tip)
	}
}
