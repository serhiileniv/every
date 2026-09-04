package runner

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// LoginShell is the shell a task's command runs through.
//
// Commands run through the user's login shell so PATH matches their terminal --
// which kills one of the two classic scheduler traps. Only bash and zsh accept
// the bundled -l; sh, dash and others reject it, so those get plain -c.
func (r *Runner) LoginShell() []string {
	if r.goos == "darwin" {
		// Hardcoded rather than $SHELL: launchd gives a task the same login
		// shell every time regardless of what the user set interactively.
		return []string{"/bin/zsh", "-lc"}
	}
	sh := r.env("SHELL")
	if sh == "" {
		sh = "/bin/bash"
	}
	base := filepath.Base(sh)
	if strings.HasSuffix(base, "bash") || strings.HasSuffix(base, "zsh") {
		return []string{sh, "-lc"}
	}
	return []string{sh, "-c"}
}

// WindowsShell selects the Windows command processor and its invocation flags.
// EVERY_SHELL overrides; COMSPEC is the system default; cmd.exe is the floor.
func (r *Runner) WindowsShell() []string {
	sh := r.env("EVERY_SHELL")
	if sh == "" {
		sh = r.env("COMSPEC")
	}
	if sh == "" {
		sh = "cmd.exe"
	}
	base := strings.ToLower(filepath.Base(sh))
	base = strings.TrimSuffix(base, ".exe")
	if base == "powershell" || base == "pwsh" {
		return []string{sh, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command"}
	}
	return []string{sh, "/d", "/s", "/c"}
}

// commandArgv builds the argv to spawn, plus a cleanup for anything temporary.
func (r *Runner) commandArgv(cmd string) (argv []string, cleanup func(), err error) {
	if r.goos != "windows" {
		return append(r.LoginShell(), cmd), func() {}, nil
	}

	// Passing a quoted command as the final argv element of cmd.exe adds
	// another Windows command-line escaping layer -- and Go's os/exec adds its
	// own on top, using MSVCRT rules that cmd.exe does not follow. A temporary
	// script gives cmd.exe and PowerShell the command text verbatim while
	// preserving cwd, output capture and timeout behavior.
	shell := r.WindowsShell()
	isPowerShell := shell[len(shell)-1] == "-Command"

	ext, content := ".cmd", ""
	if isPowerShell {
		// A UTF-8 BOM, so PowerShell reads non-ASCII correctly. Not to be
		// confused with the UTF-16 BOM the task XML needs.
		ext, content = ".ps1", "\uFEFF"+cmd+"\r\n"
	} else {
		// @echo off: a batch file runs with ECHO ON, so without it cmd copies
		// every line of the command into stdout and it lands in `every log`.
		content = "@echo off\r\n" + cmd + "\r\n"
	}

	f, err := os.CreateTemp("", "every-command*"+ext)
	if err != nil {
		return nil, func() {}, err
	}
	name := f.Name()
	cleanup = func() { os.Remove(name) }

	if _, err := f.WriteString(content); err != nil {
		f.Close()
		cleanup()
		return nil, func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return nil, func() {}, err
	}

	if isPowerShell {
		// -Command is replaced by -File; the script path is the argument.
		return append(shell[:len(shell)-1:len(shell)-1], "-File", name), cleanup, nil
	}
	return append(shell[:len(shell):len(shell)], name), cleanup, nil
}

// osaEscape escapes a string for embedding in an AppleScript double-quoted
// literal: every backslash doubled, every double quote backslash-prefixed.
func osaEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	return strings.ReplaceAll(s, `"`, `\"`)
}

// workdir picks the directory to run in, and a note to prepend when it had to
// fall back.
//
// The directory is probed for actual readability rather than mere existence:
// under launchd, a TCC-protected directory (Documents, Desktop, Downloads)
// passes an is-a-directory check and then fails on access. Falling back to the
// home directory silently would make a task look like it ran normally somewhere
// unexpected, so the note goes into the log.
func (r *Runner) workdir(cwd string) (dir string, note string) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	if cwd == "" {
		return home, ""
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return home, ""
	}
	f, err := os.Open(cwd)
	if err == nil {
		_, err = f.Readdirnames(1)
		f.Close()
		// io.EOF just means the directory is empty, which is readable.
		if err == nil || err.Error() == "EOF" {
			return cwd, ""
		}
	}
	return home, fmt.Sprintf("note: cwd %s not readable under scheduler — ran from %s\n", cwd, home)
}

var _ = runtime.GOOS
