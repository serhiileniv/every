package cli

import "testing"

// A temp dir and the data dir every reports are the same location spelled two
// ways on Windows: t.TempDir() gives backslashes, and every normalizes to
// forward slashes because that value is printed by `help` and `doctor`.
//
// Replacing only one form leaked an absolute, machine-specific path into a
// comparison meant to be portable -- which is exactly how the frozen surface
// table failed on Windows while passing everywhere else.
func TestScrubHomeHandlesBothSeparatorForms(t *testing.T) {
	const winHome = `C:\Users\RUNNER~1\AppData\Local\Temp\Test123\001`
	const posixForm = "C:/Users/RUNNER~1/AppData/Local/Temp/Test123/001"

	cases := []struct{ name, in, want string }{
		{
			name: "native form",
			in:   "data:  " + winHome + "\n",
			want: "data:  $EVERY_HOME\n",
		},
		{
			// The form every actually prints.
			name: "forward-slash form",
			in:   "data:  " + posixForm + "\n",
			want: "data:  $EVERY_HOME\n",
		},
		{
			name: "both in one string",
			in:   winHome + " and " + posixForm,
			want: "$EVERY_HOME and $EVERY_HOME",
		},
		{
			name: "unrelated text is untouched",
			in:   "no path here",
			want: "no path here",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubHome(tc.in, winHome); got != tc.want {
				t.Errorf("scrubHome(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// On a platform where the two forms are identical, scrubbing must still work
// and must not double-substitute.
func TestScrubHomeOnPOSIXPaths(t *testing.T) {
	const home = "/tmp/TestX/001"
	got := scrubHome("data:  "+home+"\n", home)
	if want := "data:  $EVERY_HOME\n"; got != want {
		t.Errorf("scrubHome = %q, want %q", got, want)
	}
}
