package activity

import (
	"strings"
	"sync"
	"time"
)

// Hit is one recent proxied request used to infer the active browser page class.
type Hit struct {
	At       time.Time
	Host     string
	Site     string // rules site name, may be empty for unmanaged (e.g. ChatGPT)
	Category string // play/feed/content/...
	Kind     string // video/text/ai/… from rules when known
}

// Recent keeps a short sliding window of MITM hosts for focus classification.
type Recent struct {
	mu     sync.Mutex
	ttl    time.Duration
	maxN   int
	hits   []Hit
	aiDom  []string
	vidSite map[string]bool
}

func NewRecent(ttl time.Duration, maxN int) *Recent {
	if ttl <= 0 {
		ttl = 45 * time.Second
	}
	if maxN <= 0 {
		maxN = 64
	}
	return &Recent{
		ttl:  ttl,
		maxN: maxN,
		aiDom: defaultAIDomains(),
		vidSite: map[string]bool{
			"youtube": true, "bilibili": true, "douyin": true,
		},
	}
}

func (r *Recent) Observe(host, site, category, kind string, at time.Time) {
	if r == nil || host == "" {
		return
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(at)
	r.hits = append(r.hits, Hit{
		At: at, Host: host, Site: strings.ToLower(site),
		Category: strings.ToLower(category), Kind: strings.ToLower(kind),
	})
	if len(r.hits) > r.maxN {
		r.hits = r.hits[len(r.hits)-r.maxN:]
	}
}

// Snapshot returns hits newest-first within TTL.
func (r *Recent) Snapshot(now time.Time) []Hit {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	out := make([]Hit, len(r.hits))
	copy(out, r.hits)
	// newest first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (r *Recent) pruneLocked(now time.Time) {
	cut := now.Add(-r.ttl)
	i := 0
	for i < len(r.hits) && r.hits[i].At.Before(cut) {
		i++
	}
	if i > 0 {
		r.hits = append([]Hit(nil), r.hits[i:]...)
	}
}

// ClassFromHits maps recent proxy activity to a browser group.
// Returns ("", "") when inconclusive — caller should fall back to title heuristics.
func ClassFromHits(hits []Hit) (group, reason string) {
	return defaultClassifier().ClassFromHits(hits)
}

func defaultClassifier() *Recent {
	return &Recent{
		aiDom:   defaultAIDomains(),
		vidSite: map[string]bool{"youtube": true, "bilibili": true, "douyin": true},
	}
}

func (r *Recent) ClassFromHits(hits []Hit) (group, reason string) {
	if r == nil {
		return ClassFromHits(hits)
	}
	if len(hits) == 0 {
		return "", ""
	}
	// First decisive signal wins (Snapshot passes newest-first).
	// Managed news sites beat older AI hosts so Zhihu-after-ChatGPT stays news.
	for _, h := range hits {
		if h.Category == "play" || h.Kind == "video" || r.vidSite[h.Site] {
			return "browser_video", "proxy:" + nonzero(h.Site, h.Host)
		}
		if h.Site != "" && !r.vidSite[h.Site] && h.Kind != "video" && h.Kind != "ai" && !r.hostIsAI(h.Host) {
			return "browser_news", "proxy:" + h.Site
		}
		if r.hostIsAI(h.Host) || h.Kind == "ai" {
			return "browser_ai", "proxy:" + nonzero(h.Site, h.Host)
		}
	}
	return "", ""
}

func (r *Recent) hostIsAI(host string) bool {
	for _, d := range r.aiDom {
		if hostMatch(host, d) {
			return true
		}
	}
	return false
}

// HostIsAI is exported for tests / title-less checks.
func (r *Recent) HostIsAI(host string) bool {
	if r == nil {
		return false
	}
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i > 0 {
		host = host[:i]
	}
	return r.hostIsAI(host)
}

func nonzero(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func hostMatch(host, pattern string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	host = strings.ToLower(strings.TrimSpace(host))
	if pattern == "" || host == "" {
		return false
	}
	if strings.HasPrefix(pattern, "*.") {
		suf := pattern[1:] // .example.com
		return strings.HasSuffix(host, suf) || host == strings.TrimPrefix(pattern, "*.")
	}
	return host == pattern || strings.HasSuffix(host, "."+pattern)
}

func defaultAIDomains() []string {
	return []string{
		"chatgpt.com", "*.chatgpt.com",
		"chat.openai.com", "openai.com", "*.openai.com",
		"gemini.google.com", "aistudio.google.com", "bard.google.com",
		"claude.ai", "*.claude.ai", "anthropic.com", "*.anthropic.com",
		"kimi.moonshot.cn", "kimi.com", "*.kimi.com", "moonshot.cn", "*.moonshot.cn",
		"www.perplexity.ai", "perplexity.ai", "*.perplexity.ai",
		"poe.com", "*.poe.com",
		"character.ai", "*.character.ai",
		"chat.deepseek.com", "deepseek.com", "*.deepseek.com",
		"tongyi.aliyun.com", "qianwen.aliyun.com", "dashscope.aliyun.com",
		"yiyan.baidu.com", "ernie.baidu.com",
		"xinghuo.xfyun.cn",
		"doubao.com", "*.doubao.com", "www.doubao.com",
		"chatglm.cn", "*.chatglm.cn", "open.bigmodel.cn",
		"coze.com", "*.coze.com", "www.coze.cn", "coze.cn",
		"grok.x.ai", "x.ai", "*.x.ai",
		"copilot.microsoft.com", "www.bing.com", // bing chat often on bing; weak signal
		"notebooklm.google.com",
		"huggingface.co", "chat.huggingface.co",
		"meta.ai", "www.meta.ai",
		"you.com", "phind.com",
		"openrouter.ai", "*.openrouter.ai",
		"siliconflow.cn", "*.siliconflow.cn",
		"cursor.com", "www.cursor.com",
	}
}
