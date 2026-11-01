package titleshort

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDefaultShortMaxRunes(t *testing.T) {
	if DefaultShortMaxRunes != 23 {
		t.Fatalf("DefaultShortMaxRunes=%d want 23", DefaultShortMaxRunes)
	}
}

func TestTruncateRunesFallback(t *testing.T) {
	src := strings.Repeat("测", 40)
	got := truncateRunes(src, DefaultShortMaxRunes)
	n := utf8.RuneCountInString(got)
	if n != DefaultShortMaxRunes {
		t.Fatalf("truncate len=%d want %d got=%q", n, DefaultShortMaxRunes, got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix: %q", got)
	}
}

func TestResolveUnderTrigger(t *testing.T) {
	s := New(nil, nil, Options{})
	display, full := s.Resolve("短标题")
	if display != "短标题" || full != "" {
		t.Fatalf("got display=%q full=%q", display, full)
	}
}

func TestResolveTruncateFallback(t *testing.T) {
	s := New(nil, nil, Options{})
	long := strings.Repeat("长", DefaultTriggerRunes+5)
	display, full := s.Resolve(long)
	if full != long {
		t.Fatalf("full want source, got %q", full)
	}
	if utf8.RuneCountInString(display) > DefaultShortMaxRunes {
		t.Fatalf("display too long: %d %q", utf8.RuneCountInString(display), display)
	}
}
