// Package tail reads the end of a file without loading the whole thing.
//
// The files this touches are bounded (logs rotate at 5 MB, run ledgers trim to
// 256 KB), but `list` reads every task's ledger and `log` only wants the last
// few lines -- seeking from the end keeps both cheap regardless of file size.
//
// Ported from lib/every/tail.rb.
package tail

import (
	"bytes"
	"io"
	"os"
)

const chunk = 16 * 1024

// Lines returns the last n lines of the file, in order, each keeping its
// trailing "\n" (the final line may lack one) -- the same shape as reading
// everything and taking the last n, but reading only enough bytes from the end
// to find n complete lines.
//
// Returning fewer than n lines when the file has fewer is part of the
// contract, not an edge case: store.LastRun uses exactly that as its signal
// that it has seen the whole file and can stop growing its window.
func Lines(path string, n int) ([]string, error) {
	// Checked before opening, so a non-positive n on a missing file is not an
	// error. Callers rely on this ordering.
	if n <= 0 {
		return nil, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := info.Size()

	var buf []byte
	var window int64
	for window < size {
		window = min64(window+chunk, size)
		if _, err := f.Seek(size-window, io.SeekStart); err != nil {
			return nil, err
		}
		buf = make([]byte, window)
		if _, err := io.ReadFull(f, buf); err != nil {
			return nil, err
		}
		// Strictly greater than n, not >=. That is the invariant: with more
		// than n newlines in the window, the n-th line from the end cannot
		// extend past the start of what was read, so it is whole. The possibly
		// truncated first line is then discarded by taking the last n.
		if bytes.Count(buf, []byte("\n")) > n {
			break
		}
	}

	return lastLines(buf, n), nil
}

// lastLines splits keeping the separator, the way Ruby's String#lines does.
// bufio.Scanner is the wrong tool here twice over: it strips the separator, and
// it also swallows a trailing \r, which would corrupt a log written on Windows.
func lastLines(buf []byte, n int) []string {
	if len(buf) == 0 {
		return nil
	}

	var lines []string
	start := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] == '\n' {
			lines = append(lines, string(buf[start:i+1]))
			start = i + 1
		}
	}
	if start < len(buf) {
		lines = append(lines, string(buf[start:]))
	}

	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
