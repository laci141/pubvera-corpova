package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateUTF8(t *testing.T) {
	long := strings.Repeat("a", 299) + "ő tail" // "ő" spans bytes 299-300
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"short unchanged", "short", 300, "short"},
		{"exact fit", strings.Repeat("a", 300), 300, strings.Repeat("a", 300)},
		{"cut inside multibyte backs off", long, 300, strings.Repeat("a", 299)},
		{"cut on boundary keeps char", long, 301, strings.Repeat("a", 299) + "ő"},
		{"empty", "", 10, ""},
		{"zero max", "ő", 0, ""},
	}
	for _, c := range cases {
		got := truncateUTF8(c.in, c.max)
		if got != c.want {
			t.Errorf("%s: got %d bytes, want %d bytes", c.name, len(got), len(c.want))
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: invalid UTF-8", c.name)
		}
	}
}

func TestParseSynthesisKeepsUTF8Valid(t *testing.T) {
	title := strings.Repeat("a", 299) + "ő tail"
	js := `{"stance":"supports","confidence":0.5,"excluded_studies":[{"title":"` + title + `","reason":"` + strings.Repeat("b", 199) + `ű x"}]}`
	syn, err := parseSynthesis(js)
	if err != nil {
		t.Fatal(err)
	}
	if len(syn.ExcludedStudies) != 1 {
		t.Fatalf("excluded = %d, want 1", len(syn.ExcludedStudies))
	}
	e := syn.ExcludedStudies[0]
	if !utf8.ValidString(e.Title) || !utf8.ValidString(e.Reason) {
		t.Errorf("invalid UTF-8 after cleaning: title=%v reason=%v", utf8.ValidString(e.Title), utf8.ValidString(e.Reason))
	}
}
