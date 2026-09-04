package main

import (
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/serhiileniv/every/internal/paths"
)

// launcherPath is the path the scheduler should invoke: this program, as it was
// invoked.
//
// Deliberately NOT os.Executable(), which resolves symlinks. Recording the
// unresolved symlink is what lets `brew upgrade` and a re-install reach
// already-scheduled tasks without rewriting a single unit -- the symlink is the
// interface, and the target moves under it on every upgrade. See DECISIONS.md.
func launcherPath() (string, error) {
	arg0 := commandName()
	if strings.ContainsRune(arg0, filepath.Separator) {
		return paths.ExpandPath(arg0)
	}
	// Invoked by bare name, so PATH decided which one ran; ask PATH the same
	// question rather than guessing an install location.
	if resolved, err := exec.LookPath(arg0); err == nil {
		return paths.ExpandPath(resolved)
	}
	return paths.ExpandPath(arg0)
}
