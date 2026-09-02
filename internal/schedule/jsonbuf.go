package schedule

import "bytes"

// jsonBuffer exists only so an encoder (which is the sole way to turn HTML
// escaping off) can stand in for json.Marshal.
type jsonBuffer struct{ bytes.Buffer }

func (b *jsonBuffer) trimTrailingNewline() []byte {
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}
