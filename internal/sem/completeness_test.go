package sem

import "testing"

func TestCompletenessLevel(t *testing.T) {
	cases := []struct {
		name                            string
		failures, files, parsed, symbol int
		want                            string
	}{
		{"empty scope", 0, 0, 0, 0, "ok"},
		{"clean full parse", 0, 100, 100, 4000, "ok"},
		// The subdir/mis-scope bug: a stray config file parses fine (no failure)
		// but the real source was never discovered. Must NOT report "ok".
		{"parsed but no symbols", 0, 3, 3, 0, "degraded"},
		{"majority unparsed", 0, 100, 30, 500, "unsafe"},
		{"a few hard failures", 2, 100, 98, 4000, "degraded"},
		{"mostly failures", 30, 100, 70, 500, "unsafe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := completenessLevel(tc.failures, tc.files, tc.parsed, tc.symbol)
			if got != tc.want {
				t.Fatalf("completenessLevel(f=%d files=%d parsed=%d sym=%d) = %q, want %q",
					tc.failures, tc.files, tc.parsed, tc.symbol, got, tc.want)
			}
		})
	}
}
