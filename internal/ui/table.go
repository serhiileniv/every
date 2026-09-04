package ui

import (
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Table renders the `every list` grid.
//
// Column width is the widest cell including the header; cells are left-padded
// to that width and joined with two spaces. The last column is padded too, so
// rows carry trailing spaces -- that is what the Ruby produced, and the e2e
// scripts compare output, so it is reproduced rather than trimmed.
type Table struct {
	Headers []string
	Rows    [][]string
}

// Render writes the table, colorizing status cells unless color is disabled.
func (t Table) Render(w io.Writer, c Color) error {
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = width(h)
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			if i < len(widths) && width(cell) > widths[i] {
				widths[i] = width(cell)
			}
		}
	}

	// The header row is never colorized.
	if _, err := io.WriteString(w, formatRow(t.Headers, widths)+"\n"); err != nil {
		return err
	}
	for _, row := range t.Rows {
		line := formatRow(row, widths)
		if c.Enabled {
			line = colorizeStatus(line, c)
		}
		if _, err := io.WriteString(w, line+"\n"); err != nil {
			return err
		}
	}
	return nil
}

// width counts characters, not bytes.
//
// Ruby's ljust pads by character count. Using len() would pad a task name
// containing any non-ASCII by too few columns and visibly misalign the table --
// and non-ASCII commands are already covered by the e2e suite, so names follow.
func width(s string) int { return utf8.RuneCountInString(s) }

func formatRow(cells []string, widths []int) string {
	padded := make([]string, len(cells))
	for i, cell := range cells {
		w := 0
		if i < len(widths) {
			w = widths[i]
		}
		padded[i] = cell + strings.Repeat(" ", max(0, w-width(cell)))
	}
	return strings.Join(padded, "  ")
}

var (
	reOK          = regexp.MustCompile(`\bok\b`)
	reFail        = regexp.MustCompile(`\bFAIL\(\d+\)`)
	reUnscheduled = regexp.MustCompile(`\bunscheduled\b`)
)

// colorizeStatus paints the status word in an already-rendered row.
//
// Each pattern replaces only its FIRST match, matching Ruby's String#sub rather
// than gsub. It matters: a task named "ok-backup" would otherwise have its name
// colored instead of its status. The word boundaries are what keep that name
// from matching at all.
func colorizeStatus(line string, c Color) string {
	line = replaceFirst(line, reOK, c.Green)
	line = replaceFirst(line, reFail, c.Red)
	line = replaceFirst(line, reUnscheduled, c.Red)
	return line
}

func replaceFirst(s string, re *regexp.Regexp, paint func(string) string) string {
	loc := re.FindStringIndex(s)
	if loc == nil {
		return s
	}
	return s[:loc[0]] + paint(s[loc[0]:loc[1]]) + s[loc[1]:]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
