package watchmeta

import (
	"regexp"
	"strings"
)

var (
	reEdgeExtraPagesCN = regexp.MustCompile(`\s*和另外\s*\d+\s*个网页.*$`)
	reEdgeExtraPagesEN = regexp.MustCompile(`(?i)\s*and\s+\d+\s+more\s+pages?.*$`)
	reBrowserSuffix    = regexp.MustCompile(`(?i)\s*-\s*(microsoft\s*edge|google\s*chrome|chrome|firefox|brave|opera|vivaldi)\s*$`)
	// Browser extensions / helpers often append " - Name" after the site suffix.
	reExtensionSuffix = regexp.MustCompile(`(?i)\s*-\s*(tabbit|ublock\s*origin|adblock(?:\s*plus)?|tampermonkey|violentmonkey|stylus|bitwarden|1password|lastpass|grammarly|honey|rakuten|momentum|dark\s*reader)\s*$`)
	reBiliSiteTail    = regexp.MustCompile(`(?i)\s*[-_–—]\s*哔哩哔哩(?:\s*[_\-–—]\s*bilibili)?\s*$`)
	reBiliSiteTailEN  = regexp.MustCompile(`(?i)\s*[-_–—]\s*bilibili\s*$`)
	// Policy chrome sometimes leaked into author ("Example Channel 白名单").
	reAuthorExemptTail = regexp.MustCompile(`(?i)[\s·\-–—_|]*(白名单中?|whitelist)\s*$`)
)

// junkTitles are homepage / chrome labels that must never be shown as video titles.
var junkTitles = map[string]struct{}{
	"最热门": {}, "热门": {}, "首页": {}, "推荐": {}, "直播": {}, "动态": {},
	"trending": {}, "home": {}, "shorts": {}, "subscriptions": {},
	"youtube": {}, "bilibili": {}, "哔哩哔哩": {}, "b站": {}, "b 站": {},
	"抖音": {}, "douyin": {}, "tiktok": {},
	"白名单": {}, "最热门白名单": {}, "白名单中": {},
	"youtube白名单": {}, "youtubewhitelist": {},
	"bilibili白名单": {}, "b站白名单": {}, "抖音白名单": {},
}

// SanitizeWindowNoise strips browser / Edge multi-tab / extension suffixes from a raw window title.
func SanitizeWindowNoise(title string) string {
	t := strings.TrimSpace(title)
	if t == "" {
		return ""
	}
	// Repeat a few times: suffixes may stack ("… - Tabbit - Microsoft Edge").
	for i := 0; i < 4; i++ {
		prev := t
		t = reBrowserSuffix.ReplaceAllString(t, "")
		t = reExtensionSuffix.ReplaceAllString(t, "")
		t = reEdgeExtraPagesCN.ReplaceAllString(t, "")
		t = reEdgeExtraPagesEN.ReplaceAllString(t, "")
		t = strings.TrimSpace(t)
		if t == prev {
			break
		}
	}
	return t
}

// CleanVideoTitle removes site/browser noise from a stored or sniffed title for display.
// Returns empty string when the result is unusable junk.
func CleanVideoTitle(site, title string) string {
	t := SanitizeWindowNoise(title)
	if t == "" {
		return ""
	}
	site = strings.ToLower(strings.TrimSpace(site))
	switch {
	case site == "bilibili" || strings.Contains(t, "哔哩哔哩") || strings.Contains(strings.ToLower(t), "bilibili"):
		t = stripBilibiliSite(t)
	case site == "youtube" || strings.Contains(strings.ToLower(t), "youtube"):
		t = stripSiteSuffix(t, " - YouTube", " – YouTube", " | YouTube")
	case site == "douyin" || strings.Contains(t, "抖音") || strings.Contains(strings.ToLower(t), "douyin") || strings.Contains(strings.ToLower(t), "tiktok"):
		t = stripSiteSuffix(t, " - 抖音", " – 抖音", " - Douyin", " - TikTok", " - tiktok")
	}
	t = strings.TrimSpace(t)
	// Trailing extension may remain if it preceded a removed site marker incorrectly.
	t = SanitizeWindowNoise(t)
	if IsJunkTitle(t) {
		return ""
	}
	// Still looks like a raw window title — reject.
	lower := strings.ToLower(t)
	if strings.Contains(t, "哔哩哔哩") || strings.Contains(lower, "bilibili") ||
		strings.Contains(lower, "youtube") || strings.HasSuffix(lower, " tabbit") ||
		strings.Contains(lower, " - tabbit") {
		return ""
	}
	return t
}

// IsJunkTitle reports known non-video labels (homepage tabs, UI chrome, etc.).
func IsJunkTitle(title string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return true
	}
	compact := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(t, " ", ""), "　", ""))
	if _, ok := junkTitles[compact]; ok {
		return true
	}
	if _, ok := junkTitles[strings.ToLower(t)]; ok {
		return true
	}
	// Policy chrome leaked into title (e.g. "YouTube白名单", "B站 · 白名单").
	if strings.Contains(compact, "白名单") || strings.Contains(compact, "whitelist") {
		return true
	}
	return false
}

// CleanAuthor strips leaked whitelist chrome from an author/channel display name.
// Keeps the real channel name; never leaves "白名单" / "whitelist" glued on.
func CleanAuthor(author string) string {
	a := strings.TrimSpace(author)
	if a == "" {
		return ""
	}
	for i := 0; i < 3; i++ {
		prev := a
		a = reAuthorExemptTail.ReplaceAllString(a, "")
		a = strings.TrimSpace(a)
		if a == prev {
			break
		}
	}
	if a == "" || IsJunkTitle(a) {
		return ""
	}
	return a
}

// AuthorFromExemptLabel turns a whitelist exempt_label into a display author.
// Skips title-keyword labels ("title:…").
func AuthorFromExemptLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" || strings.HasPrefix(label, "title:") {
		return ""
	}
	label = CleanAuthor(label)
	if label == "" {
		return ""
	}
	// Historic match needles were lowercased; restore a readable display form.
	if label == strings.ToLower(label) && !strings.HasPrefix(label, "@") {
		parts := strings.Fields(label)
		for i, p := range parts {
			r := []rune(p)
			if len(r) == 0 {
				continue
			}
			parts[i] = strings.ToUpper(string(r[0])) + string(r[1:])
		}
		label = strings.Join(parts, " ")
	}
	return label
}

// FromWindowTitle derives watch meta from a browser window title when API sniffing misses.
// Examples:
//
//	"Some Video - YouTube - Microsoft Edge"
//	"标题_哔哩哔哩_bilibili"
//	"具身智能-哔哩哔哩_bilibili - Tabbit"
//	"描述 - 抖音"
func FromWindowTitle(title string) *Meta {
	t := SanitizeWindowNoise(title)
	if t == "" {
		return nil
	}

	lower := strings.ToLower(t)
	switch {
	case strings.Contains(lower, "youtube"):
		name := CleanVideoTitle("youtube", t)
		if name == "" || strings.EqualFold(name, "YouTube") {
			return nil
		}
		return &Meta{Site: "youtube", Title: name, Source: "focus_title"}
	case strings.Contains(t, "哔哩哔哩") || strings.Contains(lower, "bilibili") || strings.Contains(t, "b站") || strings.Contains(t, "B站"):
		name := CleanVideoTitle("bilibili", t)
		if name == "" {
			return nil
		}
		return &Meta{Site: "bilibili", Title: name, Source: "focus_title"}
	case strings.Contains(t, "抖音") || strings.Contains(lower, "douyin") || strings.Contains(lower, "tiktok"):
		name := CleanVideoTitle("douyin", t)
		if name == "" {
			return nil
		}
		return &Meta{Site: "douyin", Title: name, Source: "focus_title"}
	default:
		return nil
	}
}

func stripBilibiliSite(s string) string {
	name := s
	for _, sep := range []string{
		"_哔哩哔哩_bilibili", "_哔哩哔哩", "-哔哩哔哩_bilibili", "-哔哩哔哩",
		" - 哔哩哔哩", " – 哔哩哔哩", " - bilibili", " - Bilibili",
	} {
		if i := strings.LastIndex(strings.ToLower(name), strings.ToLower(sep)); i > 0 {
			name = strings.TrimSpace(name[:i])
			break
		}
	}
	name = reBiliSiteTail.ReplaceAllString(name, "")
	name = reBiliSiteTailEN.ReplaceAllString(name, "")
	return strings.TrimSpace(name)
}

func stripSiteSuffix(s string, suffixes ...string) string {
	for _, suf := range suffixes {
		if i := strings.LastIndex(s, suf); i > 0 {
			return s[:i]
		}
		// case-insensitive fallback
		li := strings.LastIndex(strings.ToLower(s), strings.ToLower(suf))
		if li > 0 {
			return s[:li]
		}
	}
	return s
}
