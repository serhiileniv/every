package cli

import "testing"

// The guard that makes the feature safe: every's first argument is a verb OR a
// schedule, so a suggester firing on anything unrecognised would answer a
// correct `every 15m` with "did you mean list".
func TestSuggestCommand(t *testing.T) {
	for tok, want := range map[string]string{
		// Typos worth correcting.
		"lst":    "list", // ties ls/list -- same command, so still an answer
		"lis":    "list",
		"logg":   "log",
		"reusme": "resume",
		"doctr":  "doctor",
		"verson": "version",
		"inspec": "inspect",

		// Short tokens: one edit only. And where two commands are equally
		// close -- "rn" is one edit from both "rm" and "run" -- neither is
		// suggested, because picking by list order is being confidently wrong
		// half the time.
		"rn": "",
		"lg": "",
		"xy": "",

		// Nowhere near anything.
		"frobnicate": "",
		"banana":     "",
		"":           "",
	} {
		if got := suggestCommand(tok); got != want {
			t.Errorf("suggestCommand(%q) = %q, want %q", tok, got, want)
		}
	}
}

// Schedule tokens must never be corrected. These are the invocations a user
// typed correctly and merely forgot the `--` on.
func TestSuggestCommandLeavesScheduleWordsAlone(t *testing.T) {
	for _, tok := range []string{"15m", "2h", "90s", "hourly", "monday", "monthly", "once", "day"} {
		if got := suggestCommand(tok); got != "" {
			// hourly/monday/monthly/once/day are far from every verb; the real
			// protection for the rest is the parser check at the call site,
			// asserted by the surface table.
			t.Errorf("suggestCommand(%q) = %q, want no suggestion", tok, got)
		}
	}
}
