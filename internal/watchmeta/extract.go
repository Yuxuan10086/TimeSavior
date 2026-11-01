package watchmeta

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strings"
)

// Meta is structured watch metadata for later stats (logged as JSONL).
type Meta struct {
	Site    string   `json:"site"`
	VideoID string   `json:"video_id"`
	Title   string   `json:"title"`
	Author  string   `json:"author"`
	Tags    []string `json:"tags,omitempty"`
	URL     string   `json:"url,omitempty"`
	Source  string   `json:"source"`
}

var (
	reYTVideoID  = regexp.MustCompile(`"videoId"\s*:\s*"([^"]{6,20})"`)
	reYTTitle    = regexp.MustCompile(`"title"\s*:\s*"((?:\\.|[^"\\])*)"`)
	reYTAuthor   = regexp.MustCompile(`"author"\s*:\s*"((?:\\.|[^"\\])*)"`)
	reYTKeywords = regexp.MustCompile(`"keywords"\s*:\s*(\[[^\]]*\]|"[^"]*")`)
	reYTOwner    = regexp.MustCompile(`"ownerChannelName"\s*:\s*"((?:\\.|[^"\\])*)"`)
	reYTSimple   = regexp.MustCompile(`"simpleText"\s*:\s*"((?:\\.|[^"\\])*)"`)
	reYTUpdate   = regexp.MustCompile(`"updateTitleAction"[\s\S]{0,400}?"simpleText"\s*:\s*"((?:\\.|[^"\\])*)"`)
)

// Extract pulls publisher/title/tags from known player/view response bodies.
func Extract(site, host, path string, body []byte) *Meta {
	body = maybeDecompress(body)
	if len(body) == 0 {
		return nil
	}
	site = strings.ToLower(site)
	path = strings.ToLower(path)
	host = strings.ToLower(host)

	switch {
	case site == "youtube" || strings.Contains(host, "youtube"):
		return extractYouTube(path, body)
	case site == "bilibili" || strings.Contains(host, "bilibili") || strings.Contains(host, "biliapi"):
		return extractBilibili(path, body)
	case site == "douyin" || strings.Contains(host, "douyin"):
		return extractDouyin(path, body)
	default:
		return nil
	}
}

// ShouldSniff reports whether this response is worth reading for metadata.
func ShouldSniff(site, host, path string) bool {
	site = strings.ToLower(site)
	host = strings.ToLower(host)
	path = strings.ToLower(path)
	switch {
	case site == "youtube" || strings.Contains(host, "youtube"):
		return strings.HasPrefix(path, "/watch") ||
			strings.Contains(path, "/youtubei/v1/player") ||
			strings.Contains(path, "/youtubei/v1/next") ||
			strings.Contains(path, "/youtubei/v1/updated_metadata") ||
			strings.Contains(path, "/youtubei/v1/get_watch") ||
			strings.Contains(path, "/youtubei/v1/player/")
	case site == "bilibili" || strings.Contains(host, "bilibili") || strings.Contains(host, "biliapi"):
		return strings.Contains(path, "/x/web-interface/view") ||
			strings.Contains(path, "/x/web-interface/wbi/view") ||
			strings.Contains(path, "/x/web-interface/wbi/view/detail") ||
			strings.HasPrefix(path, "/video/") ||
			strings.Contains(path, "/x/player/wbi/playurl") ||
			strings.Contains(path, "/x/player/playurl")
	case site == "douyin" || strings.Contains(host, "douyin"):
		return strings.Contains(path, "/aweme/detail") ||
			strings.Contains(path, "/aweme/v") && strings.Contains(path, "detail") ||
			strings.HasPrefix(path, "/video/")
	default:
		return false
	}
}

func extractYouTube(path string, body []byte) *Meta {
	if strings.Contains(path, "updated_metadata") {
		return extractYouTubeUpdated(body)
	}

	chunk := body
	if i := bytes.Index(body, []byte(`"videoDetails"`)); i >= 0 {
		end := i + 16000
		if end > len(body) {
			end = len(body)
		}
		chunk = body[i:end]
	}

	id := firstSub(reYTVideoID, chunk)
	title := CleanVideoTitle("youtube", unescapeJSONString(firstSub(reYTTitle, chunk)))
	author := unescapeJSONString(firstSub(reYTAuthor, chunk))
	if author == "" {
		author = unescapeJSONString(firstSub(reYTOwner, chunk))
	}
	tags := parseKeywords(firstSub(reYTKeywords, chunk))

	if id == "" && title == "" && author == "" {
		return nil
	}
	m := &Meta{
		Site:    "youtube",
		VideoID: id,
		Title:   title,
		Author:  author,
		Tags:    tags,
		Source:  "youtube_player",
	}
	if id != "" {
		m.URL = "https://www.youtube.com/watch?v=" + id
	}
	if strings.HasPrefix(path, "/watch") {
		m.Source = "youtube_watch_html"
	}
	return m
}

func extractYouTubeUpdated(body []byte) *Meta {
	title := CleanVideoTitle("youtube", unescapeJSONString(firstSub(reYTUpdate, body)))
	if title == "" {
		// Fallback: first simpleText near title-ish payloads is noisy; require updateTitleAction miss then skip.
		return nil
	}
	id := firstSub(reYTVideoID, body)
	m := &Meta{
		Site:    "youtube",
		VideoID: id,
		Title:   title,
		Source:  "youtube_updated_metadata",
	}
	if id != "" {
		m.URL = "https://www.youtube.com/watch?v=" + id
	}
	return m
}

func extractBilibili(path string, body []byte) *Meta {
	var raw map[string]any
	if json.Unmarshal(body, &raw) == nil {
		data, _ := raw["data"].(map[string]any)
		// /view/detail nests under data.View
		if data != nil {
			if view, ok := data["View"].(map[string]any); ok {
				data = view
			}
		}
		if data != nil {
			title := CleanVideoTitle("bilibili", asString(data["title"]))
			bvid := asString(data["bvid"])
			if title != "" || bvid != "" {
				tags := []string{}
				if tname := asString(data["tname"]); tname != "" {
					tags = append(tags, tname)
				}
				if tagArr, ok := data["tag"].([]any); ok {
					for _, item := range tagArr {
						if m, ok := item.(map[string]any); ok {
							if n := asString(m["tag_name"]); n != "" {
								tags = append(tags, n)
							}
						}
					}
				}
				author := ""
				if owner, ok := data["owner"].(map[string]any); ok {
					author = asString(owner["name"])
				}
				m := &Meta{
					Site:    "bilibili",
					VideoID: bvid,
					Title:   title,
					Author:  author,
					Tags:    cleanTags(tags),
					Source:  "bilibili_view",
				}
				if m.VideoID != "" {
					m.URL = "https://www.bilibili.com/video/" + m.VideoID
				}
				return m
			}
		}
	}

	// HTML fallback: __INITIAL_STATE__
	chunk := body
	if i := bytes.Index(body, []byte(`"videoData"`)); i >= 0 {
		end := i + 8000
		if end > len(body) {
			end = len(body)
		}
		chunk = body[i:end]
	}
	reBVID := regexp.MustCompile(`"bvid"\s*:\s*"(BV[^"]+)"`)
	reTitle := regexp.MustCompile(`"title"\s*:\s*"((?:\\.|[^"\\])*)"`)
	reName := regexp.MustCompile(`"name"\s*:\s*"((?:\\.|[^"\\])*)"`)
	id := firstSub(reBVID, chunk)
	title := CleanVideoTitle("bilibili", unescapeJSONString(firstSub(reTitle, chunk)))
	author := unescapeJSONString(firstSub(reName, chunk))
	if id == "" && title == "" {
		return nil
	}
	m := &Meta{
		Site:    "bilibili",
		VideoID: id,
		Title:   title,
		Author:  author,
		Source:  "bilibili_html",
	}
	if id != "" {
		m.URL = "https://www.bilibili.com/video/" + id
	}
	_ = path
	return m
}

func extractDouyin(path string, body []byte) *Meta {
	var env map[string]any
	if json.Unmarshal(body, &env) != nil {
		return extractDouyinLoose(body)
	}
	aweme := digMap(env, "aweme_detail")
	if aweme == nil {
		aweme = digMap(env, "aweme_details")
	}
	if aweme == nil {
		if arr, ok := env["aweme_list"].([]any); ok && len(arr) > 0 {
			if m, ok := arr[0].(map[string]any); ok {
				aweme = m
			}
		}
	}
	if aweme == nil {
		return extractDouyinLoose(body)
	}
	m := &Meta{Site: "douyin", Source: "douyin_detail"}
	m.Title = asString(aweme["desc"])
	m.VideoID = asString(aweme["aweme_id"])
	if m.VideoID == "" {
		m.VideoID = asString(aweme["group_id"])
	}
	if author := digMap(aweme, "author"); author != nil {
		m.Author = asString(author["nickname"])
		if m.Author == "" {
			m.Author = asString(author["unique_id"])
		}
	}
	for _, key := range []string{"text_extra", "cha_list", "video_tag"} {
		collectDouyinTags(aweme[key], &m.Tags)
	}
	if m.VideoID != "" {
		m.URL = "https://www.douyin.com/video/" + m.VideoID
	}
	if m.Title == "" && m.Author == "" && m.VideoID == "" {
		return nil
	}
	_ = path
	return m
}

func extractDouyinLoose(body []byte) *Meta {
	reDesc := regexp.MustCompile(`"desc"\s*:\s*"((?:\\.|[^"\\])*)"`)
	reNick := regexp.MustCompile(`"nickname"\s*:\s*"((?:\\.|[^"\\])*)"`)
	reID := regexp.MustCompile(`"aweme_id"\s*:\s*"?(\d+)"?`)
	title := unescapeJSONString(firstSub(reDesc, body))
	author := unescapeJSONString(firstSub(reNick, body))
	id := firstSub(reID, body)
	if title == "" && author == "" && id == "" {
		return nil
	}
	m := &Meta{Site: "douyin", VideoID: id, Title: title, Author: author, Source: "douyin_loose"}
	if id != "" {
		m.URL = "https://www.douyin.com/video/" + id
	}
	return m
}

func collectDouyinTags(v any, tags *[]string) {
	switch t := v.(type) {
	case []any:
		for _, item := range t {
			if m, ok := item.(map[string]any); ok {
				for _, k := range []string{"hashtag_name", "cha_name", "tag_name", "title"} {
					if s := asString(m[k]); s != "" {
						*tags = append(*tags, s)
					}
				}
			}
		}
	}
}

func digMap(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strings.TrimSuffix(strings.TrimSuffix(jsonNumber(t), ".0"), ".")
	case json.Number:
		return t.String()
	default:
		return ""
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func firstSub(re *regexp.Regexp, b []byte) string {
	m := re.FindSubmatch(b)
	if len(m) < 2 {
		return ""
	}
	return string(m[1])
}

func unescapeJSONString(s string) string {
	if s == "" {
		return ""
	}
	var out string
	if err := json.Unmarshal([]byte(`"`+s+`"`), &out); err == nil {
		return out
	}
	s = strings.ReplaceAll(s, `\/`, `/`)
	s = strings.ReplaceAll(s, `\n`, " ")
	s = strings.ReplaceAll(s, `\"`, `"`)
	return s
}

func parseKeywords(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if strings.HasPrefix(raw, "[") {
		var arr []string
		if json.Unmarshal([]byte(raw), &arr) == nil {
			return cleanTags(arr)
		}
	}
	raw, _ = url.QueryUnescape(raw)
	raw = strings.Trim(raw, `"`)
	parts := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == '|'
	})
	return cleanTags(parts)
}

func cleanTags(in []string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		k := strings.ToLower(t)
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, t)
		if len(out) >= 20 {
			break
		}
	}
	return out
}

// maybeDecompress handles responses that are still gzipped despite Accept-Encoding removal.
func maybeDecompress(body []byte) []byte {
	if len(body) >= 2 && body[0] == 0x1f && body[1] == 0x8b {
		r, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return body
		}
		defer r.Close()
		out, err := io.ReadAll(io.LimitReader(r, 2<<20))
		if err != nil && len(out) == 0 {
			return body
		}
		return out
	}
	return body
}

// DedupeKey identifies a watch item for de-duplication.
func (m *Meta) DedupeKey() string {
	if m == nil {
		return ""
	}
	id := m.VideoID
	if id == "" {
		id = m.Title + "|" + m.Author
	}
	return m.Site + ":" + id
}

// Useful reports whether the meta has enough fields to log.
func (m *Meta) Useful() bool {
	return m != nil && (m.Title != "" || m.Author != "" || m.VideoID != "")
}

// Silence unused regexp warning if update path changes.
var _ = reYTSimple
