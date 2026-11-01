package watchmeta

import (
	"strings"
	"testing"
)

func TestExtractYouTubePlayer(t *testing.T) {
	body := []byte(`{"videoDetails":{"videoId":"bo47JoSxl1s","title":"The ONLY way to become fluent in ENGLISH","author":"Volka English","keywords":["english","learn"]}}`)
	m := Extract("youtube", "www.youtube.com", "/youtubei/v1/player", body)
	if m == nil || !m.Useful() {
		t.Fatal("expected meta")
	}
	if m.VideoID != "bo47JoSxl1s" || m.Author != "Volka English" {
		t.Fatalf("unexpected meta: %#v", m)
	}
	if m.Title == "" || len(m.Tags) != 2 {
		t.Fatalf("title/tags: %#v", m)
	}
}

func TestExtractBilibiliView(t *testing.T) {
	body := []byte(`{"code":0,"data":{"bvid":"BV1xx411c7mD","title":"测试标题","tname":"知识","owner":{"name":"某UP主"},"tag":[{"tag_name":"编程"},{"tag_name":"Go"}]}}`)
	m := Extract("bilibili", "api.bilibili.com", "/x/web-interface/wbi/view", body)
	if m == nil || m.VideoID != "BV1xx411c7mD" || m.Author != "某UP主" {
		t.Fatalf("unexpected: %#v", m)
	}
	if len(m.Tags) < 2 {
		t.Fatalf("tags: %#v", m.Tags)
	}
}

func TestExtractBilibiliViewDetail(t *testing.T) {
	body := []byte(`{"code":0,"data":{"View":{"bvid":"BV1detailTest01","title":"详情页标题","owner":{"name":"UP主甲"},"tname":"科技"}}}`)
	m := Extract("bilibili", "api.bilibili.com", "/x/web-interface/wbi/view/detail", body)
	if m == nil || m.VideoID != "BV1detailTest01" || m.Title != "详情页标题" || m.Author != "UP主甲" {
		t.Fatalf("unexpected: %#v", m)
	}
}

func TestExtractYouTubeUpdatedMetadata(t *testing.T) {
	body := []byte(`{"actions":[{"updateTitleAction":{"title":{"simpleText":"Live Title Update"}}}],"videoId":"abcdefghijk"}`)
	// videoId may appear as nested field; ensure title path works even without id
	m := Extract("youtube", "www.youtube.com", "/youtubei/v1/updated_metadata", body)
	if m == nil || m.Title != "Live Title Update" {
		t.Fatalf("unexpected: %#v", m)
	}
}

func TestShouldSniffUpdatedMetadata(t *testing.T) {
	if !ShouldSniff("youtube", "www.youtube.com", "/youtubei/v1/updated_metadata") {
		t.Fatal("expected sniff")
	}
}

func TestFromWindowTitle(t *testing.T) {
	m := FromWindowTitle("LEARN ENGLISH at Home - YouTube 和另外 19 个网页 - Microsoft Edge")
	if m == nil || m.Site != "youtube" || !strings.Contains(m.Title, "LEARN ENGLISH") {
		t.Fatalf("yt: %#v", m)
	}
	m = FromWindowTitle("Go 并发编程_哔哩哔哩_bilibili - Microsoft Edge")
	if m == nil || m.Site != "bilibili" || m.Title != "Go 并发编程" {
		t.Fatalf("bili: %#v", m)
	}
}

func TestFromWindowTitleDirtyBilibiliTabbit(t *testing.T) {
	m := FromWindowTitle("具身智能-哔哩哔哩_bilibili - Tabbit")
	if m == nil || m.Site != "bilibili" || m.Title != "具身智能" {
		t.Fatalf("want 具身智能, got %#v", m)
	}
	m = FromWindowTitle("具身智能-哔哩哔哩_bilibili - Tabbit - Microsoft Edge")
	if m == nil || m.Title != "具身智能" {
		t.Fatalf("stacked suffix: %#v", m)
	}
}

func TestCleanVideoTitleJunkYouTube(t *testing.T) {
	if got := CleanVideoTitle("youtube", "最热门"); got != "" {
		t.Fatalf("junk 最热门 should clear, got %q", got)
	}
	if got := CleanVideoTitle("youtube", "最热门 白名单"); got != "" {
		t.Fatalf("junk 最热门白名单 should clear, got %q", got)
	}
	if got := CleanVideoTitle("youtube", "YouTube白名单"); got != "" {
		t.Fatalf("junk YouTube白名单 should clear, got %q", got)
	}
	if got := CleanVideoTitle("bilibili", "具身智能-哔哩哔哩_bilibili - Tabbit"); got != "具身智能" {
		t.Fatalf("bili clean: %q", got)
	}
	if got := CleanVideoTitle("youtube", "The ONLY way to become fluent in ENGLISH"); !strings.Contains(got, "fluent") {
		t.Fatalf("keep real title: %q", got)
	}
}

func TestAuthorFromExemptLabel(t *testing.T) {
	if got := AuthorFromExemptLabel("title:雅思"); got != "" {
		t.Fatalf("title keyword must not become author: %q", got)
	}
	if got := AuthorFromExemptLabel("Volka English"); got != "Volka English" {
		t.Fatalf("channel label: %q", got)
	}
	if got := AuthorFromExemptLabel("volka english"); got != "Volka English" {
		t.Fatalf("legacy lower needle: %q", got)
	}
	if got := AuthorFromExemptLabel("白名单"); got != "" {
		t.Fatalf("junk label: %q", got)
	}
}

func TestCleanAuthorStripsWhitelistSuffix(t *testing.T) {
	if got := CleanAuthor("Volka English 白名单"); got != "Volka English" {
		t.Fatalf("strip cn: %q", got)
	}
	if got := CleanAuthor("Volka English whitelist"); got != "Volka English" {
		t.Fatalf("strip en: %q", got)
	}
	if got := CleanAuthor("Volka English · 白名单中"); got != "Volka English" {
		t.Fatalf("strip mid-dot: %q", got)
	}
	if got := CleanAuthor("Volka English"); got != "Volka English" {
		t.Fatalf("keep clean: %q", got)
	}
}

func TestFromWindowTitleRejectsHomepage(t *testing.T) {
	if m := FromWindowTitle("最热门 - YouTube - Microsoft Edge"); m != nil {
		t.Fatalf("expected nil for homepage, got %#v", m)
	}
	if m := FromWindowTitle("首页_哔哩哔哩_bilibili"); m != nil {
		t.Fatalf("expected nil for bili home, got %#v", m)
	}
}

func TestExtractDouyinDetail(t *testing.T) {
	body := []byte(`{"aweme_detail":{"aweme_id":"712345","desc":"今日分享","author":{"nickname":"测试作者"},"text_extra":[{"hashtag_name":"英语"}]}}`)
	m := Extract("douyin", "www.douyin.com", "/aweme/v1/web/aweme/detail", body)
	if m == nil || m.Author != "测试作者" || m.Title != "今日分享" {
		t.Fatalf("unexpected: %#v", m)
	}
}
