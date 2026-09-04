// Package runner executes a task's command and records what happened.
//
// Ported from lib/every/runner.rb.
package runner

import "fmt"

// halfOutput is how much of each end of the output is kept.
const halfOutput = 32 * 1024

// bounded keeps the first and last halfOutput bytes of a stream and counts what
// it dropped in between.
//
// Captured output is bounded so a chatty task can't OOM the run: errors show up
// at both ends, so both ends are what is worth keeping. The full stream still
// flows to the command; we just don't hold it.
type bounded struct {
	head    []byte
	tail    []byte
	dropped int64
}

func (b *bounded) write(chunk []byte) {
	if len(chunk) == 0 {
		return
	}

	// Fill head to exactly halfOutput before anything reaches tail.
	if room := halfOutput - len(b.head); room > 0 {
		n := room
		if n > len(chunk) {
			n = len(chunk)
		}
		b.head = append(b.head, chunk[:n]...)
		chunk = chunk[n:]
		if len(chunk) == 0 {
			return
		}
	}

	b.tail = append(b.tail, chunk...)
	if over := len(b.tail) - halfOutput; over > 0 {
		b.dropped += int64(over)
		// Shift in place. copy handles the overlap correctly because the
		// destination starts before the source; re-slicing alone would leave
		// the underlying array growing without bound.
		copy(b.tail, b.tail[over:])
		b.tail = b.tail[:halfOutput]
	}
}

// appendRaw adds bytes to head unconditionally, past the size limit.
//
// The timeout marker uses this: Ruby appended it to the head buffer, so it
// lands before any truncation gap rather than at the very end, and head can
// exceed halfOutput by the marker's length. Both are observable in a log.
func (b *bounded) appendRaw(s string) {
	b.head = append(b.head, s...)
}

// bytes assembles the final body.
//
// The tail is appended whenever it exists; the truncation marker is injected
// only when bytes were actually dropped. Output between 32 and 64 KB therefore
// keeps head and tail concatenated with nothing between them -- a marker
// claiming "0 bytes truncated" was a real bug, and there is a regression test
// for it.
func (b *bounded) bytes() []byte {
	out := b.head
	if len(b.tail) == 0 {
		return out
	}
	if b.dropped > 0 {
		out = append(out, fmt.Sprintf("\n... [%d bytes truncated] ...\n", b.dropped)...)
	}
	return append(out, b.tail...)
}
