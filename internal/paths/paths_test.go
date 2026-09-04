package paths

import (
	"os"
	"path/filepath"
	"testing"
)

// Assertions ported from test/xdg_test.rb. Path resolution honors the XDG Base
// Directory spec without breaking the existing default or the EVERY_HOME
// override.

// withGOOS stands in for the Ruby suite's Every.stub(:windows?, ...).
func withGOOS(t *testing.T, v string) {
	t.Helper()
	prev := goos
	goos = v
	t.Cleanup(func() { goos = prev })
}

func mustExpand(t *testing.T, p string) string {
	t.Helper()
	got, err := ExpandPath(p)
	if err != nil {
		t.Fatalf("ExpandPath(%q): %v", p, err)
	}
	return got
}

func mustDataDir(t *testing.T, env map[string]string) string {
	t.Helper()
	got, err := DataDir(MapEnv(env))
	if err != nil {
		t.Fatalf("DataDir(%v): %v", env, err)
	}
	return got
}

func mustConfigDir(t *testing.T, env map[string]string) string {
	t.Helper()
	got, err := ConfigDir(MapEnv(env))
	if err != nil {
		t.Fatalf("ConfigDir(%v): %v", env, err)
	}
	return got
}

func TestDataDir(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want func(t *testing.T) string
	}{
		{
			name: "EVERY_HOME overrides everything",
			goos: "darwin",
			env:  map[string]string{"EVERY_HOME": "/custom/x", "XDG_DATA_HOME": "/xdg"},
			want: func(t *testing.T) string { return mustExpand(t, "/custom/x") },
		},
		{
			name: "XDG_DATA_HOME is honored",
			goos: "darwin",
			env:  map[string]string{"XDG_DATA_HOME": "/xdg"},
			want: func(t *testing.T) string { return mustExpand(t, "/xdg/every") },
		},
		{
			name: "default when neither is set",
			goos: "darwin",
			env:  map[string]string{},
			want: func(t *testing.T) string { return mustExpand(t, "~/.local/share/every") },
		},
		{
			// XDG spec: a non-absolute XDG_DATA_HOME must be ignored.
			name: "relative XDG_DATA_HOME is ignored",
			goos: "darwin",
			env:  map[string]string{"XDG_DATA_HOME": "relative/path"},
			want: func(t *testing.T) string { return mustExpand(t, "~/.local/share/every") },
		},
		{
			name: "windows uses LOCALAPPDATA",
			goos: "windows",
			env:  map[string]string{"LOCALAPPDATA": "C:/Users/Alice/AppData/Local"},
			want: func(*testing.T) string { return "C:/Users/Alice/AppData/Local/every" },
		},
		{
			// Real Windows hands back backslashes and the join then adds a
			// forward slash. The result must not be a mixed-separator path,
			// since it is what doctor and error messages print.
			name: "windows normalises backslashes",
			goos: "windows",
			env:  map[string]string{"LOCALAPPDATA": `C:\Users\Alice\AppData\Local`},
			want: func(*testing.T) string { return "C:/Users/Alice/AppData/Local/every" },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withGOOS(t, tc.goos)
			want := tc.want(t)
			if got := mustDataDir(t, tc.env); got != want {
				t.Errorf("DataDir() = %q, want %q", got, want)
			}
		})
	}
}

func TestConfigDir(t *testing.T) {
	cases := []struct {
		name string
		goos string
		env  map[string]string
		want func(t *testing.T) string
	}{
		{
			// Returned verbatim, NOT expanded -- the asymmetry with DataDir is
			// deliberate and asserted by the Ruby suite.
			name: "XDG_CONFIG_HOME is honored verbatim",
			goos: "darwin",
			env:  map[string]string{"XDG_CONFIG_HOME": "/cfg"},
			want: func(*testing.T) string { return "/cfg/systemd/user" },
		},
		{
			name: "default when unset",
			goos: "darwin",
			env:  map[string]string{},
			want: func(t *testing.T) string { return mustExpand(t, "~/.config/systemd/user") },
		},
		{
			name: "windows uses APPDATA",
			goos: "windows",
			env:  map[string]string{"APPDATA": "C:/Users/Alice/AppData/Roaming"},
			want: func(*testing.T) string { return "C:/Users/Alice/AppData/Roaming/every" },
		},
		{
			name: "windows normalises backslashes",
			goos: "windows",
			env:  map[string]string{"APPDATA": `C:\Users\Alice\AppData\Roaming`},
			want: func(*testing.T) string { return "C:/Users/Alice/AppData/Roaming/every" },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withGOOS(t, tc.goos)
			want := tc.want(t)
			if got := mustConfigDir(t, tc.env); got != want {
				t.Errorf("ConfigDir() = %q, want %q", got, want)
			}
		})
	}
}

// A non-absolute XDG_DATA_HOME on Windows falls through to the default rather
// than to the LOCALAPPDATA branch, because the absoluteness test is a leading
// "/" and the branch guard is "XDG unset". Not a nice rule, but it is the one
// existing installs resolved under, so it is pinned rather than corrected.
func TestWindowsDriveQualifiedXDGIsNotAbsolute(t *testing.T) {
	withGOOS(t, "windows")
	got := mustDataDir(t, map[string]string{
		"XDG_DATA_HOME": "C:/xdg",
		"LOCALAPPDATA":  "C:/Users/Alice/AppData/Local",
	})
	if want := mustExpand(t, "~/.local/share/every"); got != want {
		t.Errorf("DataDir() = %q, want the default %q", got, want)
	}
}

// The purely lexical behavior, with the platform pinned so the rules being
// asserted are the ones under test rather than the host's.
func TestExpandPathLexical(t *testing.T) {
	withGOOS(t, "darwin")
	for _, tc := range []struct{ in, want string }{
		{"/a/b", "/a/b"},
		{"/a/b/", "/a/b"},     // trailing slash stripped
		{"/a/./b", "/a/b"},    // . collapsed
		{"/a/b/../c", "/a/c"}, // .. collapsed lexically, no filesystem access
		{"/a//b", "/a/b"},     // duplicate separators collapsed
	} {
		if got := mustExpand(t, tc.in); got != tc.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The parts that depend on this machine -- the home directory and the working
// directory -- with NO platform stub.
//
// Stubbing here was a contradiction: it asked for POSIX rules while feeding in
// real Windows paths, so a tilde expanded to a drive-qualified home which the
// POSIX rule then judged non-absolute and prefixed with the working directory.
// These cases have to answer for the host they run on.
func TestExpandPathHostDependent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct{ in, want string }{
		{"/a/b", "/a/b"},
		{"/a/b/", "/a/b"},     // trailing slash stripped
		{"/a/./b", "/a/b"},    // . collapsed
		{"/a/b/../c", "/a/c"}, // .. collapsed lexically, no filesystem access
		{"~", home},           //
		{"~/x", filepath.Join(home, "x")},
		{"rel/x", filepath.Join(wd, "rel/x")}, // absolutized against cwd
	}
	for _, tc := range cases {
		if got := mustExpand(t, tc.in); got != tc.want {
			t.Errorf("ExpandPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The unresolved path is what gets written into every scheduler unit, and
// recording the symlink rather than its target is what lets an upgrade reach
// already-scheduled tasks. Guard it so nobody "improves" this into EvalSymlinks.
func TestExpandPathDoesNotResolveSymlinks(t *testing.T) {
	// No platform stub: the path comes from the real filesystem, so the
	// rules applied to it have to be the real ones too.
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if got, want := mustExpand(t, link), windowsPath(link); got != want {
		t.Errorf("ExpandPath(%q) = %q, want the symlink itself (%q)", link, got, want)
	}
}
