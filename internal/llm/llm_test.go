package llm

import (
	"strings"
	"testing"
)

func TestParseVerdictOrdered(t *testing.T) {
	v, err := ParseVerdict(`{"reason":"多为英语听力教程，偏学习","allow":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Allow || v.Reason == "" {
		t.Fatalf("got %+v", v)
	}
}

func TestParseVerdictFalse(t *testing.T) {
	v, err := ParseVerdict("```json\n{\"reason\":\"短视频八卦堆砌\",\"allow\":false}\n```")
	if err != nil {
		t.Fatal(err)
	}
	if v.Allow {
		t.Fatal("expected allow=false")
	}
}

func TestParseVerdictReasonCap(t *testing.T) {
	long := strings.Repeat("测", 100)
	v, err := ParseVerdict(`{"reason":"` + long + `","allow":true}`)
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(v.Reason)) > 80 {
		t.Fatalf("reason too long: %d", len([]rune(v.Reason)))
	}
}

func TestBuildUserPrompt(t *testing.T) {
	s := BuildUserPrompt("youtube", 20, []Clip{
		{Title: "雅思听力 Day1", Author: "A", DurationSec: 600},
	})
	if !strings.Contains(s, "youtube") || !strings.Contains(s, "雅思听力") || !strings.Contains(s, "600") {
		t.Fatalf("prompt missing fields: %s", s)
	}
}

func TestFormatMessagesRequest(t *testing.T) {
	got := FormatMessagesRequest("SYS", "USER")
	if !strings.Contains(got, "[system]\nSYS") || !strings.Contains(got, "[user]\nUSER") {
		t.Fatalf("format=%q", got)
	}
	shortUser := BuildShortTitleUserPrompt("长标题示例", 23)
	full := FormatMessagesRequest(ShortTitleSystem, shortUser)
	if !strings.Contains(full, ShortTitleSystem) || !strings.Contains(full, "长标题示例") {
		t.Fatalf("short request missing parts: %q", full)
	}
}
