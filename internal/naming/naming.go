// Package naming validates task names at the boundary where they become
// filesystem paths.
//
// A task name is used to build a plist path, a systemd unit filename, a Task
// Scheduler URI, a log file and a ledger file. `every add` sanitizes what the
// user types, but that is the only gate, and it is not the only way a name
// reaches those paths: the store is a plain JSON file anyone can edit, and a
// task recorded by some future version arrives unsanitized too.
//
// A name like "../../../../tmp/x" written straight into tasks.json makes
// `every resume` open a plist outside ~/Library/LaunchAgents. Verified against
// both implementations -- the Ruby attempts
// ~/Library/LaunchAgents/com.every.../../../../tmp/x.plist -- so this is a
// pre-existing hole being closed, not a regression being patched.
//
// The check belongs here rather than only at the input, because sanitizing on
// input protects one path in and this protects every path out.
package naming

import (
	"fmt"
	"regexp"
	"strings"
)

// MaxLen bounds a name so the generated unit filename cannot exceed the
// 255-byte limit, which used to be an ENAMETOOLONG crash.
const MaxLen = 100

// valid is the exact character set `every add` sanitizes down to, so a name
// this rejects is one no supported version could have produced.
var valid = regexp.MustCompile(`^[a-z0-9_.-]+$`)

// Validate reports why a name must not be turned into a path.
//
// Deliberately NOT called when merely reading or listing: a store containing a
// bad name should still be inspectable, so the user can see the thing they
// need to remove. It is called before anything writes a file or talks to a
// scheduler.
func Validate(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("task name is empty")
	case len(name) > MaxLen:
		return fmt.Errorf("task name is too long (max %d chars)", MaxLen)
	case name == "." || name == "..":
		return fmt.Errorf("task name %q is not a usable filename", name)
	case strings.ContainsAny(name, `/\`):
		return fmt.Errorf("task name %q contains a path separator", name)
	case !valid.MatchString(name):
		return fmt.Errorf("task name %q contains characters that are not allowed (a-z 0-9 . _ -)", name)
	}
	return nil
}

// IsValid is the boolean form, for callers that want to skip rather than fail.
func IsValid(name string) bool { return Validate(name) == nil }
