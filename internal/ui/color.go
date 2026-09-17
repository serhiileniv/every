// Package ui renders every's terminal output: a pinch of color, and the table.
//
// Ported from lib/every/color.rb and the rendering half of lib/every/cli.rb.
package ui

import (
	"fmt"
	"io"
	"os"
)

// A pinch of ANSI, applied honestly. Color is a hint, never load-bearing:
// every colored string is still readable with the codes stripped, and they are
// stripped whenever the destination isn't an interactive terminal (a pipe, a
// file, CI), when NO_COLOR is set (https://no-color.org), or when TERM says the
// terminal is dumb. That keeps `every list | grep ok` and CI logs clean while
// still feeling alive at a real prompt.
type Color struct {
	// Enabled reports whether codes should be emitted at all.
	Enabled bool
}

const (
	codeBold   = 1
	codeDim    = 2
	codeRed    = 31
	codeGreen  = 32
	codeYellow = 33
)

// Mode is an explicit --color choice. ModeAuto means "decide from the
// environment", which is what every invocation without the flag asks for.
type Mode int

const (
	ModeAuto Mode = iota
	ModeAlways
	ModeNever
)

// ParseMode reads a --color value. Only the three conventional spellings are
// accepted; anything else is the caller's usage error to report.
func ParseMode(v string) (Mode, bool) {
	switch v {
	case "auto":
		return ModeAuto, true
	case "always", "force", "yes":
		return ModeAlways, true
	case "never", "none", "no":
		return ModeNever, true
	}
	return ModeAuto, false
}

// NewColor decides once, for one output stream.
func NewColor(w io.Writer, env func(string) string, hasEnv func(string) bool) Color {
	return NewColorMode(w, env, hasEnv, ModeAuto)
}

// NewColorMode is NewColor with an explicit --color choice.
func NewColorMode(w io.Writer, env func(string) string, hasEnv func(string) bool, m Mode) Color {
	return Color{Enabled: colorAllowed(w, env, hasEnv, m)}
}

// colorAllowed resolves the colour question, highest precedence first:
//
//	--color=always|never   the user said so on this invocation
//	NO_COLOR               an opt-OUT beats an opt-in, per no-color.org
//	CLICOLOR_FORCE         an opt-in for pipes: `every list | less -R`
//	TERM=dumb              the terminal cannot render it
//	isatty                 the default guess
func colorAllowed(w io.Writer, env func(string) string, hasEnv func(string) bool, m Mode) bool {
	switch m {
	case ModeAlways:
		return true
	case ModeNever:
		return false
	}
	// NO_COLOR disables by KEY PRESENCE, even when empty: NO_COLOR="" still
	// disables. An unset TERM is not "dumb" and therefore still allows color.
	if hasEnv("NO_COLOR") {
		return false
	}
	if env("TERM") == "dumb" {
		return false
	}
	if v := env("CLICOLOR_FORCE"); v != "" && v != "0" {
		return true
	}
	return isTerminal(w)
}

// isTerminal reports whether w is an interactive terminal. Anything that is not
// an *os.File -- a buffer in a test, a pipe wrapper -- is not.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func (c Color) paint(code int, text string) string {
	if !c.Enabled {
		return text
	}
	return fmt.Sprintf("\x1b[%dm%s\x1b[0m", code, text)
}

func (c Color) Green(s string) string  { return c.paint(codeGreen, s) }
func (c Color) Red(s string) string    { return c.paint(codeRed, s) }
func (c Color) Yellow(s string) string { return c.paint(codeYellow, s) }
func (c Color) Dim(s string) string    { return c.paint(codeDim, s) }
func (c Color) Bold(s string) string   { return c.paint(codeBold, s) }

// OSEnv and OSHasEnv adapt the process environment for NewColor.
func OSEnv(k string) string { return os.Getenv(k) }

func OSHasEnv(k string) bool {
	_, ok := os.LookupEnv(k)
	return ok
}
