package policy

import (
	"strings"
	"testing"
	"time"

	"timesavior/internal/rules"
)

func TestStartUnlockChallengeExplainsFeedRule(t *testing.T) {
	cfg := feedUnlockCfg()
	e := New(cfg, &rules.Store{})
	now := time.Date(2026, 7, 24, 22, 0, 0, 0, time.Local)

	st := e.StartUnlockChallenge(now)
	if st["phase"] != challengeIdle {
		t.Fatalf("challenge must stay idle, got %v", st["phase"])
	}
	msg, _ := st["message"].(string)
	if !strings.Contains(msg, "推荐页") {
		t.Fatalf("want feed-rule message, got %q", msg)
	}
	if active, _ := st["temp_unlock_active"].(bool); active {
		t.Fatal("button must not start unlock")
	}
}

func TestStartUnlockChallengeOutsideWindow(t *testing.T) {
	e := New(feedUnlockCfg(), &rules.Store{})
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.Local)
	st := e.StartUnlockChallenge(now)
	msg, _ := st["message"].(string)
	if !strings.Contains(msg, "可用窗口") && !strings.Contains(msg, "21:30") {
		t.Fatalf("want outside-window hint, got %q", msg)
	}
}

func TestGenerateMathQuestionBounds(t *testing.T) {
	for i := 0; i < 200; i++ {
		q := generateMathQuestion(time.Now().Add(5 * time.Second))
		if q.answer < 0 || q.answer > 20 {
			t.Fatalf("answer out of range: %d", q.answer)
		}
		if len(q.Options) != 4 {
			t.Fatalf("want 4 options, got %d", len(q.Options))
		}
		found := false
		for _, o := range q.Options {
			if o < 0 || o > 20 {
				t.Fatalf("option out of range: %d", o)
			}
			if o == q.answer {
				found = true
			}
		}
		if !found {
			t.Fatal("options missing answer")
		}
	}
}

func TestCancelTempUnlockActiveFeedUnlock(t *testing.T) {
	store, err := rules.LoadDir("../../rules")
	if err != nil {
		t.Fatal(err)
	}
	e := New(feedUnlockCfg(), store)
	now := time.Date(2026, 7, 25, 22, 0, 0, 0, time.Local)
	_ = e.Evaluate("www.youtube.com", "/", now)
	used := e.Status(now)["temp_unlock_used"].(int)
	st := e.CancelTempUnlock(now.Add(time.Minute))
	if active, _ := st["temp_unlock_active"].(bool); active {
		t.Fatal("cancel should clear temp unlock")
	}
	if st["phase"] != challengeIdle {
		t.Fatalf("want idle after cancel, got %v", st["phase"])
	}
	if st["daily_used"].(int) != used {
		t.Fatalf("cancel must not refund quota: before=%d after=%v", used, st["daily_used"])
	}
	if !e.tempUnlockUntil.IsZero() {
		t.Fatal("engine tempUnlockUntil should be cleared")
	}
}
