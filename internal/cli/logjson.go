package cli

import (
	"encoding/base64"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/serhiileniv/every/internal/store"
	"github.com/serhiileniv/every/internal/tail"
)

// logPayload is `every log <name> --json`.
type logPayload struct {
	Name    string     `json:"name"`
	Entries []logEntry `json:"entries"`
}

// logEntry is one recorded run.
//
// Output is absent unless --with-output was asked for. That default is right
// for three reasons, and the third is the one that is easy to miss: a caller
// listing recent runs wants to know what happened rather than receive megabytes
// of a chatty task's stdout; the response size becomes predictable instead of
// unbounded; and the default path then reads only the JSONL ledger and never
// opens the .log at all, so it costs the same whether the log is empty or at
// its 5 MB rotation limit.
type logEntry struct {
	At      string         `json:"at"`
	Exit    int            `json:"exit"`
	Seconds store.Duration `json:"seconds"`
	Output  string         `json:"output,omitempty"`
	// OutputB64 carries output that is not valid UTF-8. One task printing a
	// JPEG must not make the whole response unparseable.
	OutputB64 string `json:"output_b64,omitempty"`
}

// logJSON reports a task's run history from the ledger.
func (c *CLI) logJSON(name string, n int, withOutput bool) error {
	s, err := store.Load(c.Dirs.Data)
	if err != nil {
		return err
	}
	// Deliberately NOT gated on the task existing: the text form reports "no
	// logs" for an unknown name too, because logs outlive the task that made
	// them and `every rm` keeps them on purpose.
	_ = s

	// The same condition the text form treats as a failure, treated the same
	// way here. An empty list would arguably be friendlier, but the two forms
	// of one command must agree about whether it failed -- otherwise a program
	// and a person reading the same exit code reach opposite conclusions, and
	// the exit code is the part every caller sees.
	if _, statErr := os.Stat(c.Dirs.Logs + "/" + name + ".log"); statErr != nil {
		return noInputCoded(CodeNoLogs, name,
			"no logs yet for %s (has it run? check: every list)", rubyInspect(name))
	}

	lines, err := tail.Lines(s.RunsPath(name), n)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	payload := logPayload{Name: name, Entries: []logEntry{}}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var run store.Run
		if uErr := unmarshalJSON([]byte(line), &run); uErr != nil {
			continue // a torn or corrupt record is skipped, never fatal
		}
		payload.Entries = append(payload.Entries, logEntry{
			At: run.At, Exit: run.Exit, Seconds: run.Dur,
		})
	}

	if withOutput {
		if err := c.attachOutput(name, &payload); err != nil {
			return err
		}
	}
	return emitJSON(c.Stdout, payload)
}

// attachOutput slices the .log by its `=== ` headers and pairs each block with
// the ledger entry it belongs to.
//
// Only reached under --with-output, which is why the ugly part -- parsing our
// own log format back apart, and coping with arbitrary bytes in it -- is off
// the path a caller takes by default.
func (c *CLI) attachOutput(name string, payload *logPayload) error {
	raw, err := os.ReadFile(c.Dirs.Logs + "/" + name + ".log")
	if err != nil {
		if os.IsNotExist(err) {
			return nil // ran but wrote nothing, or the log was rotated away
		}
		return err
	}

	blocks := splitLogBlocks(string(raw))
	// Pair from the end: the ledger holds the last N runs and the log may hold
	// more or fewer, so aligning the newest of each is the only correspondence
	// that survives rotation and trimming.
	for i := 1; i <= len(payload.Entries) && i <= len(blocks); i++ {
		body := blocks[len(blocks)-i]
		e := &payload.Entries[len(payload.Entries)-i]
		if utf8.ValidString(body) {
			e.Output = body
			continue
		}
		e.OutputB64 = base64.StdEncoding.EncodeToString([]byte(body))
	}
	return nil
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
