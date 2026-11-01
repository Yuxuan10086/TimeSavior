package rules

import (
	"reflect"
	"testing"
)

func TestDisplayChannelsDedupesHandle(t *testing.T) {
	got := DisplayChannels([]string{"Volka English", "@VolkaEnglish", "  ", "@VolkaEnglish"})
	want := []string{"Volka English"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v want %#v", got, want)
	}
}

func TestPreferredChannelLabel(t *testing.T) {
	chs := []string{"Volka English", "@VolkaEnglish"}
	if got := PreferredChannelLabel(chs, "@VolkaEnglish"); got != "Volka English" {
		t.Fatalf("got %q", got)
	}
	if got := PreferredChannelLabel(chs, "volka english"); got != "Volka English" {
		t.Fatalf("case-insensitive key: %q", got)
	}
}
