package paths

import (
	"os"
	"path/filepath"
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
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = home + strings.TrimPrefix(p, "~")
	}

	if goos == "windows" {
		p = windowsPath(p)
	}

	if !isAbs(p) {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		p = wd + "/" + p
	}

	// filepath.Clean does the lexical . / .. collapsing and the trailing-slash
	// stripping, and leaves symlinks alone -- unlike filepath.EvalSymlinks.
	p = filepath.Clean(p)
	if goos == "windows" {
		p = windowsPath(p)
	}
	return p, nil
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
