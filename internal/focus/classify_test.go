package focus

import (
	"testing"

	"timesavior/internal/activity"
)

func TestClassifyBrowserBuckets(t *testing.T) {
	cases := []struct {
		exe, title, want string
	}{
		{"msedge.exe", "xxx - YouTube - Microsoft Edge", BucketBrowserVideo},
		{"chrome.exe", "【搞笑】xxx_哔哩哔哩_bilibili", BucketBrowserVideo},
		{"msedge.exe", "抖音 - 记录美好生活", BucketBrowserVideo},
		// AI keywords in title alone → news (domain-only AI).
		{"chrome.exe", "ChatGPT", BucketBrowserNews},
		{"msedge.exe", "Gemini", BucketBrowserNews},
		{"chrome.exe", "Kimi.ai - 会推理解的 AI 助手", BucketBrowserNews},
		{"chrome.exe", "Claude", BucketBrowserNews},
		{"msedge.exe", "如何看待 ChatGPT - 知乎", BucketBrowserNews},
		{"msedge.exe", "如何看待xxx - 知乎", BucketBrowserNews},
		{"chrome.exe", "知乎", BucketBrowserNews},
		{"chrome.exe", "随便什么网页", BucketBrowserNews},
		{"Cursor.exe", "main.go - timeSavior - Cursor", "Cursor"},
		{"weixin.exe", "微信", "微信"},
	}
	for _, c := range cases {
		got := Classify(c.exe, c.title)
		if got.Bucket != c.want {
			t.Fatalf("%s | %q => got %q want %q", c.exe, c.title, got.Bucket, c.want)
		}
	}
}

func TestClassifyExUsesProxyHits(t *testing.T) {
	hits := []activity.Hit{{Host: "chatgpt.com", Category: "unknown"}}
	// Empty/chrome-only title: trust proxy→AI.
	got := ClassifyEx("msedge.exe", "Microsoft Edge", hits)
	if got.Bucket != BucketBrowserAI {
		t.Fatalf("chrome-only title should trust proxy AI, got %q source=%s", got.Bucket, got.Source)
	}
	// Substantive non-AI title: stale proxy must not override (same as video guard).
	got2 := ClassifyEx("msedge.exe", "随便什么网页 - Microsoft Edge", hits)
	if got2.Bucket != BucketBrowserNews {
		t.Fatalf("stale AI proxy must not override news title, got %q source=%s", got2.Bucket, got2.Source)
	}
	// AI product title + AI domain → AI.
	got3 := ClassifyEx("msedge.exe", "ChatGPT - Microsoft Edge", hits)
	if got3.Bucket != BucketBrowserAI {
		t.Fatalf("ChatGPT+domain => %q", got3.Bucket)
	}
	// Title alone never yields AI.
	got4 := ClassifyEx("msedge.exe", "ChatGPT - Microsoft Edge", nil)
	if got4.Bucket != BucketBrowserNews {
		t.Fatalf("ChatGPT title without domain => %q want news", got4.Bucket)
	}
	// Zhihu article mentioning ChatGPT stays news even with stale AI hosts.
	got5 := ClassifyEx("msedge.exe", "如何看待 ChatGPT - 知乎 - Microsoft Edge", hits)
	if got5.Bucket != BucketBrowserNews {
		t.Fatalf("zhihu+ChatGPT must be news, got %q", got5.Bucket)
	}
}

func TestClassifyExRejectsLocalAdminAndPDFAsAI(t *testing.T) {
	hits := []activity.Hit{{Host: "chatgpt.com", Category: "unknown"}}
	got := ClassifyEx("msedge.exe", "TimeSavior · 统计 - Microsoft Edge", hits)
	if got.Bucket != BucketBrowserNews {
		t.Fatalf("local stats page must not be AI, got %q source=%s", got.Bucket, got.Source)
	}
	got2 := ClassifyEx("chrome.exe", "paper.pdf - Google Chrome", hits)
	if got2.Bucket != BucketBrowserNews {
		t.Fatalf("pdf must not be AI, got %q source=%s", got2.Bucket, got2.Source)
	}
	got3 := ClassifyEx("msedge.exe", "浙江第一悬案破案记 - Microsoft Edge", hits)
	if got3.Bucket != BucketBrowserNews {
		t.Fatalf("news title must not follow stale AI proxy, got %q", got3.Bucket)
	}
}

func TestBrowserBucketsPeerToApps(t *testing.T) {
	a := ClassifyEx("msedge.exe", "ChatGPT", []activity.Hit{{Host: "chatgpt.com"}})
	b := Classify("cursor.exe", "foo")
	if a.Bucket == b.Bucket {
		t.Fatal("browser-AI and Cursor must be different peer buckets")
	}
	if a.Group == "app" || b.Group != "app" {
		t.Fatalf("groups: ai=%s cursor=%s", a.Group, b.Group)
	}
}

func TestClassifySystemAppBucket(t *testing.T) {
	exes := []string{
		"WindowsTerminal.exe", "wt.exe", "explorer.exe",
		"SnippingTool.exe", "ScreenClippingHost.exe",
		"ShellExperienceHost.exe", "SearchHost.exe",
		"StartMenuExperienceHost.exe", "ShellHost.exe",
		"ApplicationFrameHost.exe", "SystemSettings.exe",
		"Taskmgr.exe", "dwm.exe", "RuntimeBroker.exe",
		"TextInputHost.exe", "LockApp.exe",
		"SecureSystemUIServer.exe",
	}
	for _, exe := range exes {
		got := Classify(exe, "")
		if got.Bucket != BucketSystemApp {
			t.Fatalf("%s => bucket %q want %q", exe, got.Bucket, BucketSystemApp)
		}
		if got.Group != "system" && got.Group != "app" {
			t.Fatalf("%s => group %q", exe, got.Group)
		}
	}
	// Must not swallow real apps into the system bucket.
	for _, exe := range []string{"weixin.exe", "Cursor.exe", "chrome.exe", "msedge.exe"} {
		got := Classify(exe, "x")
		if got.Bucket == BucketSystemApp {
			t.Fatalf("%s must not be %s", exe, BucketSystemApp)
		}
	}
}

func TestClassifyExProxyVideoNeedsTitleHint(t *testing.T) {
	hits := []activity.Hit{{Host: "rr1.googlevideo.com", Site: "youtube", Category: "play", Kind: "video"}}
	title := "英语真题在线 - 大学英语六级真题，英语六级，PDF下载 | 英语真题在线 - Microsoft Edge"
	got := ClassifyEx("msedge.exe", title, hits)
	if got.Bucket != BucketBrowserNews {
		t.Fatalf("CET page must stay news despite stale video proxy, got %q source=%s", got.Bucket, got.Source)
	}
	// Empty/chrome-only title may still follow proxy.
	got2 := ClassifyEx("msedge.exe", "Microsoft Edge", hits)
	if got2.Bucket != BucketBrowserVideo {
		t.Fatalf("chrome-only title should trust proxy video, got %q", got2.Bucket)
	}
	// Explicit video title keeps video even without relying solely on proxy.
	got3 := ClassifyEx("msedge.exe", "xxx - YouTube - Microsoft Edge", nil)
	if got3.Bucket != BucketBrowserVideo {
		t.Fatalf("youtube title => %q", got3.Bucket)
	}
}

func TestIsLockScreen(t *testing.T) {
	if !IsLockScreen("LockApp.exe", "Windows 默认锁屏界面") {
		t.Fatal("LockApp")
	}
	if !IsLockScreen("LogonUI.exe", "") {
		t.Fatal("LogonUI")
	}
	if !IsLockScreen("", "锁屏") {
		t.Fatal("title 锁屏")
	}
	if IsLockScreen("explorer.exe", "此电脑") {
		t.Fatal("explorer must not be lock")
	}
}

func TestHasVideoTitleHintNarrow(t *testing.T) {
	if HasVideoTitleHint("英语真题在线 - 大学英语六级真题") {
		t.Fatal("CET must not match video hints")
	}
	if !HasVideoTitleHint("【搞笑】xxx_哔哩哔哩_bilibili") {
		t.Fatal("bilibili should match")
	}
}

func TestDetailLabel(t *testing.T) {
	if got := DetailLabel("system", "explorer.exe", ""); got != "资源管理器" {
		t.Fatalf("explorer => %q", got)
	}
	if got := DetailLabel("system", "WindowsTerminal.exe", ""); got != "Windows Terminal" {
		t.Fatalf("wt => %q", got)
	}
	if got := DetailLabel("browser_ai", "msedge.exe", "ChatGPT - Microsoft Edge"); got != "ChatGPT" {
		t.Fatalf("browser title => %q", got)
	}
	if !IsAggregateBucket("system", BucketSystemApp) {
		t.Fatal("system should aggregate")
	}
	if IsAggregateBucket("app", "Cursor") {
		t.Fatal("Cursor should not aggregate")
	}
}
