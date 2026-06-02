package util

import (
	"bytes"
	"io"
	"os"
)

// logWriter implements an io.Writer which accepts a pre-configured log function
// and then buffers and splits on new lines to output content. The buffer is the
// size of 1 memory page.
type logWriter struct {
	fn  func(logLine string)
	buf *bytes.Buffer
}

// NewLogWriter returns an io.Writer which outputs to a *zap.Logger.
func NewLogWriter(fn func(logLine string)) io.Writer {
	return &logWriter{
		fn:  fn,
		buf: bytes.NewBuffer(make([]byte, 0, os.Getpagesize())),
	}
}

func (l *logWriter) Write(p []byte) (n int, err error) {
	line, remainder, found := bytes.Cut(p, []byte{'\n'})
	if !found && len(line) > l.buf.Available() {
		// Exceeded available buffer - so dump buffer + line
		// Got a newline - dump buffer + line
		l.fn(string(append(l.buf.Bytes(), line...)))
		l.buf.Reset()
	} else if !found {
		// Buffer up non-newline terminated string
		l.buf.Write(line)
	} else if found {
		// Dump buffer + line
		l.fn(string(append(l.buf.Bytes(), line...)))
		l.buf.Reset()
		// Write remainder
		l.buf.Write(remainder)
	}
	return len(p), nil
}
