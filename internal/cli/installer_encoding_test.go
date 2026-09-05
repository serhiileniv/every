package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// install.ps1 must be pure ASCII.
//
// Windows PowerShell 5.1 -- still the shell every Windows box ships with, and
// the one `powershell -File install.ps1` runs -- decodes a .ps1 without a
// byte-order mark as ANSI, not UTF-8. A UTF-8 em dash then arrives as three
// Windows-1252 characters, the last of which is U+201D RIGHT DOUBLE QUOTATION
// MARK. PowerShell accepts curly quotes as string delimiters, so that byte
// closes the string it appears in and the parse collapses several lines later:
//
//	install.ps1:92 Missing closing '}' in statement block or type definition.
//
// The installer became unrunnable from a file -- the form its own .EXAMPLE
// block documents -- while `irm | iex` kept working, because HTTP carries
// charset=utf-8 and nothing has to be guessed. CI never saw it: both the lint
// and install jobs use pwsh 7, which reads a BOM-less file as UTF-8.
//
// A BOM would fix the file case and risk the piped one, where the BOM becomes a
// literal U+FEFF at the head of the string handed to iex. Staying ASCII is the
// only encoding that cannot be misread by either path, so it is enforced here
// rather than left to whoever next types an em dash.
func TestInstallerIsASCII(t *testing.T) {
	root := repoRoot(t)

	// install.sh is deliberately not covered: POSIX sh is byte-transparent and
	// never guesses an encoding, so prose there is free to use whatever it likes.
	for _, name := range []string{"install.ps1"} {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			line := 1
			for i, b := range raw {
				if b == '\n' {
					line++
				}
				if b > 0x7F {
					t.Fatalf("%s:%d: non-ASCII byte %#x at offset %d -- "+
						"Windows PowerShell 5.1 decodes this file as ANSI and "+
						"mis-parses it; write it in ASCII (-- for an em dash)",
						name, line, b, i)
				}
			}
		})
	}
}
