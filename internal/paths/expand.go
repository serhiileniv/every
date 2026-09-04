package paths

import (
	"os"
	"path"
	"strings"
)

// ExpandPath is Ruby's File.expand_path, which has no single Go equivalent.
// It combines four operations, and every caller here depends on at least two:
//
//	~ / ~/... expansion from the home directory
//	absolutization against the working directory
//	lexical . and .. collapsing, WITHOUT touching the filesystem
//	trailing-slash stripping
//
// It deliberately does not resolve symlinks. That matters beyond tidiness:
// the scheduler records this kind of path in every unit it writes, and
// recording the unresolved symlink is what lets `brew upgrade` and a
// re-install reach already-scheduled tasks without rewriting a single unit.
// See DECISIONS.md, "The symlink is the interface".
//
// Ruby also accepts ~user for an arbitrary user, and raises for one it cannot
// look up. Nothing in every passes such a path -- EVERY_HOME and the XDG vars
// are the only inputs -- so a leading ~ followed by anything but a separator
// is left alone rather than growing a passwd lookup for a case that does not
// occur.
func ExpandPath(p string) (string, error) {
	// Normalize separators FIRST, so the tilde check below sees one spelling.
	// It previously ran after, which meant a "~\.local\share\every" built by
	// filepath.Join on Windows never matched "~/" and was never expanded -- the
	// tilde survived into the resolved path and the whole thing was then
	// treated as relative. A real bug, reachable whenever XDG_DATA_HOME is set
	// but not absolute on Windows.
	p = windowsPath(p)

	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = windowsPath(home) + strings.TrimPrefix(p, "~")
	}

	if !isAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		p = windowsPath(wd) + "/" + p
	} else if goos == "windows" && strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "//") {
		// A rooted path with no drive is relative to the CURRENT drive on
		// Windows, and File.expand_path prefixes it accordingly: "/custom/x"
		// becomes "D:/custom/x". Without this the two implementations resolve
		// the same EVERY_HOME to different directories, which is how the
		// dual-unit comparison ended up rendering units for two machines.
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		if drive := windowsPath(wd); len(drive) >= 2 && drive[1] == ':' {
			p = drive[:2] + p
		}
	}

	// path.Clean, not filepath.Clean: the latter rewrites separators to the
	// HOST's, which made this function's result depend on where it ran rather
	// than on the platform it was asked about. Everything here is already
	// slash-form, and slashes are what the callers want -- the data dir is
	// printed by help, doctor and error messages, and a mixed-separator path
	// is what windowsPath exists to avoid.
	//
	// It still does the lexical . and .. collapsing and the trailing-slash
	// stripping, and still leaves symlinks alone -- unlike EvalSymlinks, which
	// would break the property that a unit records the symlink it was invoked
	// through.
	return path.Clean(p), nil
}

// isAbs answers for the path shape after windowsPath has run, so a Windows
// path is "C:/..." rather than "C:\...". filepath.IsAbs is correct per-platform
// but is asked here only about the already-normalized form.
func isAbs(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	if goos == "windows" {
		// A drive-qualified path, "C:/x". A bare "C:x" is drive-relative and
		// genuinely not absolute, which matches Ruby.
		if len(p) >= 3 && p[1] == ':' && p[2] == '/' {
			return true
		}
		// A UNC share.
		if strings.HasPrefix(p, "//") {
			return true
		}
	}
	return false
}
