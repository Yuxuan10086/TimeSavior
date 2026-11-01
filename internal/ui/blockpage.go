package ui

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"

	"timesavior/internal/policy"
)

func BlockHTML(reason policy.Reason, site string) string {
	title, body, detail := reasonText(reason, site)
	searchBox := ""
	primaryHead := "说明"
	if reason == policy.ReasonFeedBlocked || reason == policy.ReasonUnknownBlock {
		searchBox = searchFormHTML(site)
		if searchBox != "" {
			primaryHead = "搜索"
		}
	}
	return strings.NewReplacer(
		"__TITLE__", html.EscapeString(title),
		"__BODY__", html.EscapeString(body),
		"__DETAIL__", html.EscapeString(detail),
		"__PRIMARY__", html.EscapeString(primaryHead),
		"__SEARCH__", searchBox,
		"__NAV__", blockShellNav(),
	).Replace(blockHTML)
}

// blockShellNav mirrors shellNav but uses absolute admin URLs — this page is
// injected on remote hosts via the MITM proxy.
func blockShellNav() string {
	base := "http://127.0.0.1:18889"
	item := func(href, label string) string {
		return `<a href="` + base + href + `">` + label + `</a>`
	}
	return `<nav class="nav" aria-label="主导航">` +
		item("/", "状态看板") +
		item("/stats", "使用统计") +
		item("/unlock", "临时解锁") +
		item("/llm-logs", "大模型日志") +
		`</nav>`
}

var blockHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>TimeSavior · __TITLE__</title>
` + fontLinks + `
<style>
` + SharedCSS + `
.search {
  display:flex; gap:.4rem; margin-top:.15rem;
  background:var(--elevated); border:1px solid var(--line);
  border-radius:var(--radius); padding:.4rem; max-width:28rem;
}
.search input {
  flex:1; min-width:0; border:0; outline:0; background:transparent;
  color:var(--fg); font:inherit; font-size:1rem; padding:.55rem .65rem;
}
.search button {
  border:0; border-radius:8px; padding:.55rem 1rem; cursor:pointer;
  background:var(--accent); color:#f3f8fb; font:inherit; font-weight:600;
}
.links { margin-top:.75rem; font-size:.9rem; }
</style>
</head>
<body>
<div class="shell">
  <header class="topbar">
    <div>
      <p class="brand-mark">TimeSavior</p>
      <h1>__TITLE__</h1>
      <p class="sub">__BODY__</p>
    </div>
  </header>
__NAV__
  <section class="panel accent-edge">
    <h2>__PRIMARY__</h2>
    <p class="hint">__DETAIL__</p>
    __SEARCH__
  </section>
  <section class="panel">
    <h2>其它</h2>
    <p class="hint" style="margin:0">可用窗口内首次打开推荐页起 30 分钟临时放行；取消见上方「临时解锁」。</p>
  </section>
  <p class="foot">TimeSavior</p>
</div>
</body>
</html>`

func searchFormHTML(site string) string {
	action, param, placeholder, openURL := searchTarget(site)
	if action == "" {
		return ""
	}
	return fmt.Sprintf(`
    <form class="search" method="get" action="%s">
      <input type="search" name="%s" placeholder="%s" required autofocus />
      <button type="submit">搜索</button>
    </form>
    <div class="links"><a href="%s">打开搜索页</a></div>`,
		html.EscapeString(action),
		html.EscapeString(param),
		html.EscapeString(placeholder),
		html.EscapeString(openURL),
	)
}

func searchTarget(site string) (action, param, placeholder, openURL string) {
	switch strings.ToLower(site) {
	case "bilibili":
		return "https://search.bilibili.com/all", "keyword", "搜索 B 站", "https://search.bilibili.com/"
	case "zhihu":
		return "https://www.zhihu.com/search", "q", "搜索知乎", "https://www.zhihu.com/search"
	case "douyin":
		return "https://www.douyin.com/search/", "keyword", "搜索抖音", "https://www.douyin.com/search/"
	case "youtube":
		return "https://www.youtube.com/results", "search_query", "搜索 YouTube", "https://www.youtube.com/results"
	default:
		return "", "", "", ""
	}
}

// reasonText returns title, subtitle, and panel detail line.
func reasonText(reason policy.Reason, site string) (title, body, detail string) {
	s := siteOr(site)
	switch reason {
	case policy.ReasonFeedBlocked:
		return "推荐流已屏蔽",
			s + " · 搜索与内容页仍可用",
			"拦截：首页 / 推荐信息流"
	case policy.ReasonFullBlock:
		return "整站已屏蔽",
			s + " · 连续观看达上限，至次日 4:00 恢复",
			"自由时段或临时解锁可提前恢复"
	case policy.ReasonUnknownBlock:
		return "路径已拦截",
			s + " · 该路径未归类为搜索或内容",
			"可用下方搜索进入目标页"
	default:
		return "访问已拦截",
			"当前策略不允许此请求",
			"请改用搜索或等待自由时段"
	}
}

func siteOr(s string) string {
	if s == "" {
		return "受控站点"
	}
	return s
}

func WriteBlock(w http.ResponseWriter, r *http.Request, reason policy.Reason, site string) {
	accept := r.Header.Get("Accept")
	isAPI := strings.Contains(accept, "application/json") ||
		strings.HasPrefix(r.URL.Path, "/api") ||
		strings.HasPrefix(r.URL.Path, "/x/") ||
		strings.HasPrefix(r.URL.Path, "/aweme/") ||
		strings.HasPrefix(r.URL.Path, "/youtubei/") ||
		strings.Contains(r.URL.Path, "/api/")

	if isAPI {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":403,"message":"blocked by TimeSavior","reason":"` + string(reason) + `"}`))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte(BlockHTML(reason, site)))
}

// SearchRedirect helps build escaped query for tests/helpers.
func SearchRedirect(site, q string) string {
	action, param, _, _ := searchTarget(site)
	if action == "" {
		return ""
	}
	u, err := url.Parse(action)
	if err != nil {
		return action
	}
	v := u.Query()
	v.Set(param, q)
	u.RawQuery = v.Encode()
	return u.String()
}
