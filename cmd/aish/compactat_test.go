package main

import "testing"

// The compact_at row of aish status: off, never for an unknown window, or
// the share and the tokens; the share is rounded, as 0.29*100 falls short
// of 29 in floating point.
func TestCompactAt(t *testing.T) {
	for _, tc := range []struct {
		share  float64
		window int
		want   string
	}{
		{0, 200_000, "off (compact_at = 0)"},
		{0, 0, "off (compact_at = 0)"},
		{0.8, 0, "80% of the window; the window is unknown, so never"},
		{0.8, 200_000, "80% of the window, at 160k tokens"},
		{0.85, 200_000, "85% of the window, at 170k tokens"},
		{0.29, 1_000_000, "29% of the window, at 290k tokens"},
		{0.5, 8_000, "50% of the window, at 4.0k tokens"},
	} {
		if got := compactAt(tc.share, tc.window); got != tc.want {
			t.Errorf("compactAt(%v, %d) = %q, want %q", tc.share, tc.window, got, tc.want)
		}
	}
}
