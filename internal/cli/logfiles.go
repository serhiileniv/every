package cli

import (
	"os"
	"strings"
)

// Reading a task's log means reading across the rotation boundary.
//
// Rotation renames the live log aside at 5 MB and starts a fresh one, keeping a
// single previous generation. Reading only the live file makes the moment just
// after a rotation look like history that has been lost: `every log` would show
// the one run since the rename and nothing else, on a task that has run
// thousands of times. The ledger is the durable record, but `log` is where
// people go to read what a run actually printed.

// logPath is the live log for a task; rotatedLogPath is the one generation
// kept beside it.
func (c *CLI) logPath(name string) string        { return c.Dirs.Logs + "/" + name + ".log" }
func (c *CLI) rotatedLogPath(name string) string { return c.logPath(name) + ".old" }

// logExists reports whether either generation is present. A log that has just
// been rotated away is still a log, so "no logs yet" must not be the answer
// while the previous generation is sitting there.
func (c *CLI) logExists(name string) bool {
	for _, p := range []string{c.logPath(name), c.rotatedLogPath(name)} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// tailLog returns the last n lines of a task's log, oldest first, reaching into
// the rotated generation only for what the live one cannot supply.
func (c *CLI) tailLog(name string, n int) ([]string, error) {
	lines, err := tailFile(c.logPath(name), n)
	if err != nil {
		return nil, err
	}
	if len(lines) >= n {
		return lines, nil
	}
	older, err := tailFile(c.rotatedLogPath(name), n-len(lines))
	if err != nil {
		return nil, err
	}
	return append(older, lines...), nil
}

// tailFile is tailLines with a missing file treated as empty rather than as a
// failure: either generation may legitimately be absent.
func tailFile(path string, n int) ([]string, error) {
	lines, err := tailLines(path, n)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return lines, nil
}

// logBlocks returns each run's captured output, oldest last, reaching into the
// rotated generation only when the live log holds fewer blocks than `want`.
//
// The conditional read is what keeps --with-output from holding both
// generations -- up to 10 MB -- in memory for the common case where the live
// log answers on its own.
func (c *CLI) logBlocks(name string, want int) ([]string, error) {
	blocks, err := blocksIn(c.logPath(name))
	if err != nil {
		return nil, err
	}
	if len(blocks) >= want {
		return blocks, nil
	}
	older, err := blocksIn(c.rotatedLogPath(name))
	if err != nil {
		return nil, err
	}
	return append(older, blocks...), nil
}

// blocksIn reads one generation and splits it into run blocks. A missing file
// is no blocks, not an error.
func blocksIn(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return splitLogBlocks(string(raw)), nil
}

// splitLogBlocks returns each run's output, without its header line.
func splitLogBlocks(s string) []string {
	var blocks []string
	var cur strings.Builder
	started := false

	for _, line := range strings.SplitAfter(s, "\n") {
		if strings.HasPrefix(line, "=== ") && strings.Contains(line, " exit=") {
			if started {
				blocks = append(blocks, cur.String())
				cur.Reset()
			}
			started = true
			continue
		}
		if started {
			cur.WriteString(line)
		}
	}
	if started {
		blocks = append(blocks, cur.String())
	}
	return blocks
}
