// Package paths resolves the directories every stores its state in.
//
// Ported from lib/every.rb. The precedence rules and their quirks are a
// compatibility surface: an existing install must keep finding its own store
// after the upgrade, so the odd cases are reproduced deliberately rather than
// tidied up. Each one is marked below.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Exit codes (sysexits.h convention): 0 ok - 64 usage/bad args -
// 66 no such task/log - 1 other failure. Runs also surface 124 (timeout) and
// 128+signum (killed by a signal); see internal/runner.
const (
	ExitUsage   = 64
	ExitNoInput = 66
)

// Dirs is the resolved set of locations for one process. Ruby computed these
// as constants at require time, which is why its tests set EVERY_HOME before
// the first require. Resolving into a value instead lets tests build one
// directly, and lets a single process resolve for a synthetic environment.
type Dirs struct {
	Data   string // tasks.json, logs/, runs/, .lock
	Logs   string
	Runs   string
	Config string // systemd user units live here
	Agents string // ~/Library/LaunchAgents (computed on every platform, as in Ruby)
}

// goos is the platform the resolution rules branch on. It exists as a variable
// rather than a direct runtime.GOOS reference so the Windows branches stay
// testable from any host -- xdg_test.rb stubs Every.windows? for exactly this,
// and dropping the coverage on macOS/Linux would leave the LOCALAPPDATA and
// APPDATA paths unexercised until someone ran CI on Windows.
var goos = runtime.GOOS

// Env is the environment lookup a resolution reads from. Tests pass a map;
// production passes OS.
type Env func(string) string

// OS reads the real process environment.
func OS(key string) string { return os.Getenv(key) }

// MapEnv adapts a map for tests.
func MapEnv(m map[string]string) Env {
	return func(k string) string { return m[k] }
}

// Resolve computes every directory from one environment.
func Resolve(env Env) (Dirs, error) {
	data, err := DataDir(env)
	if err != nil {
		return Dirs{}, err
	}
	cfg, err := ConfigDir(env)
	if err != nil {
		return Dirs{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Dirs{}, err
	}
	return Dirs{
		Data:   data,
		Logs:   filepath.Join(data, "logs"),
		Runs:   filepath.Join(data, "runs"),
		Config: cfg,
		Agents: filepath.Join(home, "Library", "LaunchAgents"),
	}, nil
}

// DataDir resolves the state directory.
//
// Precedence: EVERY_HOME (explicit) -> $XDG_DATA_HOME/every ->
// ~/.local/share/every (the XDG default anyway, so existing installs are
// unchanged). Per the XDG spec, a non-absolute XDG_DATA_HOME is ignored.
func DataDir(env Env) (string, error) {
	if explicit := env("EVERY_HOME"); explicit != "" {
		return ExpandPath(explicit)
	}

	xdg := env("XDG_DATA_HOME")

	if goos == "windows" && xdg == "" {
		local := env("LOCALAPPDATA")
		if local == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			local = filepath.Join(home, "AppData", "Local")
		}
		return windowsPath(join(local, "every")), nil
	}

	// Absoluteness is tested with a leading "/" only, exactly as Ruby does.
	// The consequence on Windows is real and load-bearing for compatibility:
	// XDG_DATA_HOME=C:/x is NOT absolute by this test, so it falls through to
	// the default -- and it is non-empty, so the LOCALAPPDATA branch above was
	// already skipped. Reproduced, not fixed.
	if strings.HasPrefix(xdg, "/") {
		return ExpandPath(join(xdg, "every"))
	}
	// A slash literal, not filepath.Join: Join uses the host separator, and a
	// "~\.local\..." is not a tilde path any expansion rule recognizes.
	return ExpandPath("~/.local/share/every")
}

// ConfigDir resolves where systemd user units live: $XDG_CONFIG_HOME/systemd/user
// (default ~/.config/systemd/user). Non-absolute XDG_CONFIG_HOME is ignored.
//
// Note the asymmetry with DataDir, which is intentional and asserted by
// xdg_test.rb: the data dir runs every branch through expand-path, while the
// config dir returns the XDG branch verbatim and expands only the fallback.
func ConfigDir(env Env) (string, error) {
	xdg := env("XDG_CONFIG_HOME")

	if goos == "windows" && xdg == "" {
		appdata := env("APPDATA")
		if appdata == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			appdata = filepath.Join(home, "AppData", "Roaming")
		}
		return windowsPath(join(appdata, "every")), nil
	}

	if strings.HasPrefix(xdg, "/") {
		return join(xdg, "systemd", "user"), nil
	}
	return ExpandPath("~/.config/systemd/user")
}

// join mirrors Ruby's File.join, which always inserts a forward slash
// regardless of platform. filepath.Join would use a backslash on Windows and
// would also Clean the result, collapsing the mixed separators that
// windowsPath exists to normalize.
func join(parts ...string) string {
	return strings.Join(parts, "/")
}

// windowsPath normalizes to one separator.
//
// LOCALAPPDATA/APPDATA come back with backslashes, and joining adds a forward
// slash, so the raw result is mixed: "C:\\Users\\me\\AppData\\Local/every". It
// works -- Windows accepts either separator -- but it is what doctor, list and
// error messages print. One separator throughout is the same location on disk,
// so nothing moves.
func windowsPath(p string) string {
	return strings.ReplaceAll(p, `\`, "/")
}
