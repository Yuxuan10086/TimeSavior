package activity

import "testing"

func TestClassFromHitsVideoPlay(t *testing.T) {
	r := NewRecent(0, 0)
	g, _ := r.ClassFromHits([]Hit{{Host: "rr1.googlevideo.com", Site: "youtube", Category: "play", Kind: "video"}})
	if g != "browser_video" {
		t.Fatalf("got %q", g)
	}
}

func TestClassFromHitsAI(t *testing.T) {
	g, _ := ClassFromHits([]Hit{{Host: "chatgpt.com", Site: "", Category: "unknown"}})
	if g != "browser_ai" {
		t.Fatalf("got %q", g)
	}
}

func TestClassFromHitsZhihuNews(t *testing.T) {
	r := NewRecent(0, 0)
	g, _ := r.ClassFromHits([]Hit{{Host: "www.zhihu.com", Site: "zhihu", Category: "content", Kind: "text"}})
	if g != "browser_news" {
		t.Fatalf("got %q", g)
	}
}

func TestClassFromHitsInconclusive(t *testing.T) {
	r := NewRecent(0, 0)
	g, _ := r.ClassFromHits([]Hit{{Host: "example.com", Site: "", Category: "unknown"}})
	if g != "" {
		t.Fatalf("want empty, got %q", g)
	}
}

func TestClassFromHitsNewsBeatsOlderAI(t *testing.T) {
	r := NewRecent(0, 0)
	// Newest-first: Zhihu then stale ChatGPT → news.
	g, _ := r.ClassFromHits([]Hit{
		{Host: "www.zhihu.com", Site: "zhihu", Category: "content", Kind: "text"},
		{Host: "chatgpt.com", Site: "", Category: "unknown"},
	})
	if g != "browser_news" {
		t.Fatalf("zhihu-after-chatgpt got %q want news", g)
	}
	// Newest-first: ChatGPT then older Zhihu → AI.
	g2, _ := r.ClassFromHits([]Hit{
		{Host: "chatgpt.com", Site: "", Category: "unknown"},
		{Host: "www.zhihu.com", Site: "zhihu", Category: "content", Kind: "text"},
	})
	if g2 != "browser_ai" {
		t.Fatalf("chatgpt-after-zhihu got %q want ai", g2)
	}
}
