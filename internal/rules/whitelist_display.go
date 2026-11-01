package rules

import "strings"

// DisplayChannels merges near-duplicate whitelist channel entries for UI.
// "Example Channel" + "@ExampleChannel" → "Example Channel" (prefer display name).
func DisplayChannels(channels []string) []string {
	type group struct {
		display string
		handle  string
		order   int
	}
	byKey := map[string]*group{}
	var order []string
	for _, raw := range channels {
		c := strings.TrimSpace(raw)
		if c == "" {
			continue
		}
		key := ChannelKey(c)
		if key == "" {
			continue
		}
		g, ok := byKey[key]
		if !ok {
			g = &group{order: len(order)}
			byKey[key] = g
			order = append(order, key)
		}
		if strings.HasPrefix(c, "@") {
			if g.handle == "" {
				g.handle = c
			}
		} else if g.display == "" {
			g.display = c
		}
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		g := byKey[key]
		switch {
		case g.display != "":
			out = append(out, g.display)
		case g.handle != "":
			out = append(out, g.handle)
		}
	}
	return out
}

// PreferredChannelLabel returns the best display name for a matched channel
// within the configured list (prefer plain name over @handle).
func PreferredChannelLabel(channels []string, matched string) string {
	matched = strings.TrimSpace(matched)
	if matched == "" {
		return ""
	}
	key := ChannelKey(matched)
	var display, handle string
	for _, raw := range channels {
		c := strings.TrimSpace(raw)
		if c == "" || ChannelKey(c) != key {
			continue
		}
		if strings.HasPrefix(c, "@") {
			if handle == "" {
				handle = c
			}
		} else if display == "" {
			display = c
		}
	}
	if display != "" {
		return display
	}
	if handle != "" {
		return handle
	}
	return matched
}

// ChannelKey normalizes a channel name/@handle for dedupe and matching groups.
func ChannelKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "@")
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "　", "")
	return s
}

// ChannelNeedles returns lowercased match needles derived from one channel entry.
func ChannelNeedles(c string) []string {
	c = strings.TrimSpace(c)
	if c == "" {
		return nil
	}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s == "" {
			return
		}
		for _, e := range out {
			if e == s {
				return
			}
		}
		out = append(out, s)
	}
	add(c)
	if strings.HasPrefix(c, "@") {
		add(strings.TrimPrefix(c, "@"))
	} else {
		add("@" + c)
	}
	compact := strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(c, "@"), " ", ""))
	if compact != "" {
		add("/@" + compact)
	}
	return out
}
