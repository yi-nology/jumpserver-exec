package koko

import "io"

// buffer deliberately does NOT expose io.ReaderFrom.
//
// io.Copy(dst, src) checks whether dst implements ReaderFrom and calls it
// directly. A ReadFrom method that itself calls io.Copy(b, r) therefore
// recurses until the goroutine stack overflows and the process dies -- which
// looks exactly like an unexplained SIGKILL, because Go prints the fatal error
// to stderr while the runtime keeps growing the stack.
//
// Wrapping the buffer in writerOnly hides ReadFrom from io.Copy's fast path, so
// the copy falls back to a plain read/write loop.
type writerOnly struct{ w io.Writer }

func (w writerOnly) Write(p []byte) (int, error) { return w.w.Write(p) }

func (b *buffer) ReadFrom(r io.Reader) (int64, error) {
	return io.Copy(writerOnly{b}, r)
}
