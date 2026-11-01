package rules

import (
	"reflect"
	"testing"
)

func TestDisplayChannelsDedupesHandle(t *testing.T) {
	got := DisplayChannels([]string{"Example Channel", "@ExampleChannel", "  ", "@ExampleChannel"})
	want := []string{"Example Channel"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestPreferredChannelLabel(t *testing.T) {
	chs := []string{"Example Channel", "@ExampleChannel"}
	if got := PreferredChannelLabel(chs, "@ExampleChannel"); got != "Example Channel" {
		t.Fatalf("got %q", got)
	}
	if got := PreferredChannelLabel(chs, "example channel"); got != "Example Channel" {
		t.Fatalf("case-insensitive key: %q", got)
	}
}
