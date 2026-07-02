package sem

import (
	"strings"
	"testing"
)

func TestLooksMinified(t *testing.T) {
	overlong := strings.Repeat("x", maxMinifiedLineLen+1)
	normal := strings.Repeat("const answer = 42; // short line\n", 400) // ~13KB
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"empty", "", false},
		{"ordinary source", normal, false},
		{"single overlong line dominating", overlong, true},
		{"single-line bundle no trailing newline", strings.Repeat("var a=1;", 20000), true},
		{"few lines all overlong", overlong + "\n" + overlong + "\n" + overlong + "\n", true},
		{"giant data lines embedded in real source", normal + overlong + "\n" + normal + overlong + "\n" + normal, false},
		{"long line just under threshold", strings.Repeat("y", maxMinifiedLineLen) + "\n", false},
	}
	for _, tc := range cases {
		if got := looksMinified(tc.content); got != tc.want {
			t.Errorf("%s: looksMinified = %v, want %v", tc.name, got, tc.want)
		}
	}
}
