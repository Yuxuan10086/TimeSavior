package rules

import "testing"

func TestHostMatchAndCategorize(t *testing.T) {
	store, err := LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	site, cat := store.Match("www.bilibili.com", "/x/web-interface/wbi/index")
	if site == nil || site.Name != "bilibili" {
		t.Fatalf("expected bilibili, got %#v", site)
	}
	if cat != CatFeed {
		t.Fatalf("expected feed, got %s", cat)
	}
	_, cat = store.Match("www.bilibili.com", "/search?keyword=go")
	if cat != CatSearch {
		t.Fatalf("expected search, got %s", cat)
	}
	_, cat = store.Match("www.zhihu.com", "/hot")
	if cat != CatFeed {
		t.Fatalf("zhihu hot should be feed, got %s", cat)
	}

	site, cat = store.Match("www.youtube.com", "/")
	if site == nil || site.Name != "youtube" {
		t.Fatalf("expected youtube, got %#v", site)
	}
	if cat != CatFeed {
		t.Fatalf("youtube home should be feed, got %s", cat)
	}
	_, cat = store.Match("www.youtube.com", "/results?search_query=go")
	if cat != CatSearch {
		t.Fatalf("youtube results should be search, got %s", cat)
	}
	_, cat = store.Match("www.youtube.com", "/watch?v=dQw4w9WgXcQ")
	if cat != CatContent {
		t.Fatalf("youtube watch should be content, got %s", cat)
	}
	_, cat = store.Match("rr1---sn-example.googlevideo.com", "/videoplayback")
	if cat != CatPlay {
		t.Fatalf("googlevideo should be play, got %s", cat)
	}
}
