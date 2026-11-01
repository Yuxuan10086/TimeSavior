package focus

import (
	"path/filepath"
	"strings"

	"timesavior/internal/activity"
)

// Bucket labels are peer-level categories for stats (browser subtypes ≈ Cursor).
const (
	BucketBrowserVideo = "浏览器-视频"
	BucketBrowserAI    = "浏览器-AI"
	BucketBrowserNews  = "浏览器-资讯"
	BucketSystemApp    = "系统应用"
)

var browserExes = map[string]bool{
	"msedge.exe": true, "chrome.exe": true, "firefox.exe": true,
	"brave.exe": true, "opera.exe": true, "vivaldi.exe": true,
	"arc.exe": true, "iexplore.exe": true, "safari.exe": true,
	"chromium.exe": true,
	// msedgewebview2.exe intentionally omitted: embeds inside apps are not browser tabs
	"360chrome.exe": true, "360se.exe": true, "qqbrowser.exe": true,
	"sogouexplorer.exe": true, "liebao.exe": true,
}

// systemShellExes are Windows shell / system-tool hosts merged into BucketSystemApp.
// Do not put WeChat, Cursor, browsers, or third-party apps here.
var systemShellExes = map[string]bool{
	"explorer.exe":                      true,
	"windowsterminal.exe":               true,
	"wt.exe":                            true,
	"snippingtool.exe":                  true,
	"screenclippinghost.exe":            true,
	"shellexperiencehost.exe":           true,
	"searchhost.exe":                    true,
	"searchapp.exe":                     true,
	"startmenuexperiencehost.exe":       true,
	"shellhost.exe":                     true,
	"applicationframehost.exe":          true,
	"systemsettings.exe":                true,
	"systemsettingsadminflows.exe":      true,
	"taskmgr.exe":                       true,
	"dwm.exe":                           true,
	"runtimebroker.exe":                 true,
	"textinputhost.exe":                 true,
	"lockapp.exe":                       true,
	"securesystemuiserver.exe":          true,
	"sihost.exe":                        true,
	"ctfmon.exe":                        true,
	"fontdrvhost.exe":                   true,
	"conhost.exe":                       true,
	"dllhost.exe":                       true,
	"backgroundtaskhost.exe":            true,
	"pickerhost.exe":                    true,
	"openwith.exe":                      true,
	"credwiz.exe":                       true,
	"consent.exe":                       true,
	"useraccountcontrolsettings.exe":    true,
	"windows.immersivecontrolpanel.exe": true,
}

var appDisplayNames = map[string]string{
	"cursor.exe":       "Cursor",
	"code.exe":         "VS Code",
	"devenv.exe":       "Visual Studio",
	"idea64.exe":       "IntelliJ IDEA",
	"pycharm64.exe":    "PyCharm",
	"webstorm64.exe":   "WebStorm",
	"goland64.exe":     "GoLand",
	"weixin.exe":       "微信",
	"wechat.exe":       "微信",
	"qq.exe":           "QQ",
	"dingtalk.exe":     "钉钉",
	"feishu.exe":       "飞书",
	"lark.exe":         "飞书",
	"telegram.exe":     "Telegram",
	"discord.exe":      "Discord",
	"slack.exe":        "Slack",
	"spotify.exe":      "Spotify",
	"obs64.exe":        "OBS",
	"obs32.exe":        "OBS",
	"cmd.exe":          "命令提示符",
	"powershell.exe":   "PowerShell",
	"pwsh.exe":         "PowerShell",
	"notepad.exe":      "记事本",
	"notepad++.exe":    "Notepad++",
	"winword.exe":      "Word",
	"excel.exe":        "Excel",
	"powerpnt.exe":     "PowerPoint",
	"outlook.exe":      "Outlook",
	"onenote.exe":      "OneNote",
	"teams.exe":        "Teams",
	"steam.exe":        "Steam",
	"clash.exe":        "Clash",
	"clash-verge.exe":  "Clash Verge",
	"verge-mihomo.exe": "Clash Verge",
}

// systemDisplayNames are fine-grained labels under BucketSystemApp.
var systemDisplayNames = map[string]string{
	"explorer.exe":                      "资源管理器",
	"windowsterminal.exe":               "Windows Terminal",
	"wt.exe":                            "Windows Terminal",
	"snippingtool.exe":                  "截图",
	"screenclippinghost.exe":            "截图",
	"shellexperiencehost.exe":           "系统界面",
	"searchhost.exe":                    "搜索",
	"searchapp.exe":                     "搜索",
	"startmenuexperiencehost.exe":       "开始菜单",
	"shellhost.exe":                     "系统界面",
	"applicationframehost.exe":          "UWP 宿主",
	"systemsettings.exe":                "设置",
	"systemsettingsadminflows.exe":      "设置",
	"taskmgr.exe":                       "任务管理器",
	"dwm.exe":                           "桌面窗口管理器",
	"runtimebroker.exe":                 "Runtime Broker",
	"textinputhost.exe":                 "输入法",
	"lockapp.exe":                       "锁屏",
	"securesystemuiserver.exe":          "安全界面",
	"sihost.exe":                        "Shell 基础设施",
	"ctfmon.exe":                        "输入法",
	"fontdrvhost.exe":                   "字体驱动",
	"conhost.exe":                       "控制台宿主",
	"dllhost.exe":                       "COM 宿主",
	"backgroundtaskhost.exe":            "后台任务",
	"pickerhost.exe":                    "选择器",
	"openwith.exe":                      "打开方式",
	"credwiz.exe":                       "凭据向导",
	"consent.exe":                       "UAC",
	"useraccountcontrolsettings.exe":    "UAC 设置",
	"windows.immersivecontrolpanel.exe": "设置",
}

// videoTitleHints are explicit video-site markers only.
// Keep narrow: generic words like「英语」must never appear here (CET/真题 pages).
var videoTitleHints = []string{
	"youtube", "youtu.be", "bilibili", "哔哩哔哩", "b站", "b 站",
	"douyin", "抖音", "tiktok", "youku", "优酷",
	"iqiyi", "爱奇艺", "腾讯视频", "v.qq.com", "芒果tv",
	"netflix", "bilivideo",
}

// aiTitleHints confirm proxy→AI / detect article false-positives.
// They must NOT classify browser_ai by title alone (domain-only).
var aiTitleHints = []string{
	"chatgpt", "openai", "chat.openai",
	"gemini", "bard", "google ai", "aistudio", "ai studio",
	"kimi", "moonshot", "kimi.ai",
	"claude", "anthropic",
	"copilot", "bing chat", "microsoft copilot",
	"perplexity", "poe", "character.ai", "characterai",
	"deepseek", "通义", "tongyi", "qwen", "千问",
	"文心", "wenxin", "ernie bot", "erniebot",
	"豆包", "doubao", "星火", "sparkdesk", "讯飞星火",
	"grok", "x.ai", "mistral", "huggingface", "meta.ai",
	"phind", "you.com", "notebooklm", "cursor.com", "devin",
	"coze", "扣子", "智谱", "zhipu", "chatglm", "glm-",
	"siliconflow", "openrouter", "together.ai",
}

// newsArticleTitleHints: pages that mention AI products but are news/articles.
var newsArticleTitleHints = []string{
	"知乎", "zhihu", "微博", "weibo", "今日头条", "toutiao",
	"豆瓣", "douban", "wikipedia", "维基", "百度百科",
	"36kr", "虎嗅", "雪球", "什么值得买", "smzdm",
}

// Classification is the stats bucket for a focus session.
type Classification struct {
	Bucket    string // peer-level label, e.g. 浏览器-AI / Cursor
	Group     string // browser_video | browser_ai | browser_news | app | system
	IsBrowser bool
	Source    string // proxy | title | exe
}

// Classify maps exe+title into a peer-level stats bucket (title heuristics only).
func Classify(exeName, title string) Classification {
	return ClassifyEx(exeName, title, nil)
}

// ClassifyEx prefers recent MITM host activity for browsers, then title, then exe.
func ClassifyEx(exeName, title string, hits []activity.Hit) Classification {
	exe := strings.ToLower(strings.TrimSpace(filepath.Base(exeName)))
	if exe == "" {
		exe = "unknown"
	}
	titleL := strings.ToLower(title)

	if browserExes[exe] || isBrowserExe(exe) {
		byTitle := classifyBrowserByTitle(titleL)
		if len(hits) > 0 {
			if g, why := activity.ClassFromHits(hits); g != "" {
				// Stale MITM hits from another tab often outlive the tab switch.
				// Only trust proxy→video when title also matches, or is empty/chrome-only.
				// browser_ai is domain-only: trust proxy unless admin/PDF/video/news-article title.
				if g == "browser_video" && byTitle.Group != "browser_video" && browserTitleHasSubstance(title) {
					return byTitle
				}
				if g == "browser_ai" {
					if isLocalAdminTitle(title) || looksLikeBrowserFileTitle(title) {
						return byTitle
					}
					if byTitle.Group == "browser_video" && browserTitleHasSubstance(title) {
						return byTitle
					}
					// Substantive non-AI titles (or AI-mentioning news articles) win over stale AI hosts.
					if browserTitleHasSubstance(title) && (!HasAITitleHint(title) || looksLikeNewsArticleTitle(title)) {
						return byTitle
					}
				}
				return browserClass(g, why)
			}
		}
		if isLocalAdminTitle(title) {
			return browserClass("browser_news", "title:local")
		}
		return byTitle
	}

	if systemShellExes[exe] || isSystemShellExe(exe) {
		return Classification{Bucket: BucketSystemApp, Group: "system", IsBrowser: false, Source: "exe"}
	}

	name := appDisplayNames[exe]
	if name == "" {
		name = prettyExe(exe)
	}
	return Classification{Bucket: name, Group: "app", IsBrowser: false, Source: "exe"}
}

func classifyBrowserByTitle(titleL string) Classification {
	switch {
	case containsAny(titleL, videoTitleHints):
		return browserClass("browser_video", "title")
	default:
		// AI is domain-only; title keywords (chatgpt/claude/…) stay news.
		return browserClass("browser_news", "title")
	}
}

// browserTitleHasSubstance reports a page title beyond browser chrome / empty.
func browserTitleHasSubstance(title string) bool {
	core := sanitizeBrowserDetail(title)
	if core == "" || core == "其它" {
		return false
	}
	switch strings.ToLower(core) {
	case "microsoft edge", "google chrome", "chrome", "firefox",
		"brave", "opera", "vivaldi", "edge", "chromium":
		return false
	}
	return true
}

// IsLockScreen reports lock-screen / logon UI that must not enter app stats.
func IsLockScreen(exeName, title string) bool {
	exe := strings.ToLower(strings.TrimSpace(filepath.Base(exeName)))
	switch exe {
	case "lockapp.exe", "logonui.exe":
		return true
	}
	if systemDisplayNames[exe] == "锁屏" {
		return true
	}
	t := strings.TrimSpace(title)
	if t == "锁屏" || strings.Contains(t, "锁屏界面") {
		return true
	}
	label := DetailLabel("system", exe, title)
	return label == "锁屏"
}

// HasVideoTitleHint reports whether title carries an explicit video-site marker.
func HasVideoTitleHint(title string) bool {
	return containsAny(strings.ToLower(title), videoTitleHints)
}

// HasAITitleHint reports whether title carries an explicit AI-site marker.
// Used to confirm proxy→AI and to detect title-only historical mislabels — not to classify alone.
func HasAITitleHint(title string) bool {
	return containsAny(strings.ToLower(title), aiTitleHints)
}

// looksLikeNewsArticleTitle reports news/Q&A shells (e.g. 知乎 posts about ChatGPT).
func looksLikeNewsArticleTitle(title string) bool {
	return containsAny(strings.ToLower(title), newsArticleTitleHints)
}

// ClassSourceHasAIDomain reports stored class_source came from AI proxy hosts.
func ClassSourceHasAIDomain(src string) bool {
	s := strings.ToLower(strings.TrimSpace(src))
	return strings.HasPrefix(s, "proxy:")
}

// isLocalAdminTitle matches TimeSavior's own browser UI (dashboard / stats / unlock).
func isLocalAdminTitle(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	if lower == "" {
		return false
	}
	return strings.HasPrefix(lower, "timesavior ·") || strings.HasPrefix(lower, "timesavior -")
}

// looksLikeBrowserFileTitle is a downloaded/local file opened in the browser (e.g. paper.pdf).
func looksLikeBrowserFileTitle(title string) bool {
	core := strings.ToLower(sanitizeBrowserDetail(title))
	if core == "" {
		return false
	}
	for _, ext := range []string{".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md", ".epub"} {
		if strings.HasSuffix(core, ext) {
			return true
		}
	}
	return false
}

func isSystemShellExe(exe string) bool {
	base := strings.TrimSuffix(exe, ".exe")
	switch base {
	case "wt", "dwm", "sihost", "ctfmon", "conhost", "dllhost":
		return true
	}
	// Secure System UI / Immersive Control Panel style hosts
	if strings.Contains(base, "securesystemui") ||
		strings.Contains(base, "immersivecontrolpanel") ||
		strings.Contains(base, "startmenuexperience") ||
		strings.Contains(base, "shellexperience") ||
		strings.Contains(base, "screenclipping") ||
		strings.Contains(base, "snippingtool") ||
		strings.Contains(base, "textinputhost") ||
		strings.Contains(base, "applicationframehost") {
		return true
	}
	return false
}

func browserClass(group, source string) Classification {
	switch group {
	case "browser_video":
		return Classification{Bucket: BucketBrowserVideo, Group: group, IsBrowser: true, Source: source}
	case "browser_ai":
		return Classification{Bucket: BucketBrowserAI, Group: group, IsBrowser: true, Source: source}
	default:
		return Classification{Bucket: BucketBrowserNews, Group: "browser_news", IsBrowser: true, Source: source}
	}
}

func isBrowserExe(exe string) bool {
	base := strings.TrimSuffix(exe, ".exe")
	return strings.Contains(base, "chrome") ||
		strings.Contains(base, "firefox") ||
		strings.Contains(base, "msedge") ||
		strings.Contains(base, "browser")
}

func containsAny(hay string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(hay, n) {
			return true
		}
	}
	return false
}

func prettyExe(exe string) string {
	base := filepath.Base(exe)
	base = strings.TrimSuffix(base, ".exe")
	if base == "" || base == "." {
		return "未知应用"
	}
	runes := []rune(base)
	if len(runes) <= 12 && !strings.Contains(base, " ") {
		r0 := runes[0]
		if r0 >= 'a' && r0 <= 'z' {
			runes[0] = r0 - 'a' + 'A'
		}
		return string(runes)
	}
	return base
}

// IsAggregateBucket reports peer buckets that fold multiple concrete apps/tabs.
func IsAggregateBucket(group, bucket string) bool {
	if group == "system" || strings.HasPrefix(group, "browser_") {
		return true
	}
	switch bucket {
	case BucketSystemApp, BucketBrowserVideo, BucketBrowserAI, BucketBrowserNews:
		return true
	}
	return false
}

// DetailLabel is the child-row name under an aggregate bucket.
// System apps use exe display names; browser buckets use a cleaned title short name.
func DetailLabel(group, exe, title string) string {
	exe = strings.ToLower(strings.TrimSpace(filepath.Base(exe)))
	if group == "system" || group == "" && systemShellExes[exe] {
		if n := systemDisplayNames[exe]; n != "" {
			return n
		}
		if n := appDisplayNames[exe]; n != "" {
			return n
		}
		if exe == "" || exe == "." {
			return "系统组件"
		}
		return prettyExe(exe)
	}
	if strings.HasPrefix(group, "browser_") {
		t := sanitizeBrowserDetail(title)
		if t == "" {
			return "其它"
		}
		return t
	}
	if n := appDisplayNames[exe]; n != "" {
		return n
	}
	return prettyExe(exe)
}

func sanitizeBrowserDetail(title string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return ""
	}
	// Strip common browser / multi-tab / extension suffixes (inline to avoid import cycles).
	for i := 0; i < 4; i++ {
		prev := t
		t = stripSuffixCI(t, " - Microsoft Edge", " - Google Chrome", " - Chrome",
			" - Firefox", " - Brave", " - Opera", " - Vivaldi")
		t = stripSuffixCI(t, " - Tabbit", " - uBlock Origin", " - Dark Reader")
		if idx := strings.Index(t, "和另外"); idx > 0 {
			t = strings.TrimSpace(t[:idx])
		}
		if i2 := strings.Index(strings.ToLower(t), " and "); i2 > 0 {
			rest := strings.ToLower(t[i2:])
			if strings.Contains(rest, "more page") {
				t = strings.TrimSpace(t[:i2])
			}
		}
		t = strings.TrimSpace(t)
		if t == prev {
			break
		}
	}
	runes := []rune(t)
	const max = 48
	if len(runes) > max {
		t = string(runes[:max-1]) + "…"
	}
	return t
}

func stripSuffixCI(s string, suffixes ...string) string {
	lower := strings.ToLower(s)
	for _, suf := range suffixes {
		sufL := strings.ToLower(suf)
		if strings.HasSuffix(lower, sufL) {
			return strings.TrimSpace(s[:len(s)-len(suf)])
		}
	}
	return s
}
