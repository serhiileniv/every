package schedule

import "testing"

// A persisted weekday is clamped into 0-6 on load: a legacy 7 means Sunday,
// which is 0. Unlike the weekday delta in NextForEntry, nothing downstream
// compensates for a negative here -- it would reach the backend as a negative
// array index -- so the floored modulo is load-bearing and guarded.
func TestClampWeekdayFlooredModulo(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 0}, {6, 6}, {7, 0}, {8, 1}, {13, 6}, {14, 0},
		{-1, 6}, {-7, 0}, {-8, 6},
	} {
		if got := clampWeekday(tc.in); got != tc.want {
			t.Errorf("clampWeekday(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
