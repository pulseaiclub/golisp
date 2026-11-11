package interp

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

// IOStream is the common stream object behind files, standard handles and
// sockets. A stream may be readable, writable, or both; buffering is set up
// lazily so a write-only or read-only handle never allocates the other side.
type IOStream struct {
	rd     io.Reader
	wr     io.Writer
	closer io.Closer

	// Keep read and write locks separate so a blocked read permits writes.
	readMu  sync.Mutex // protects br and reads from rd
	writeMu sync.Mutex // protects bw and writes to wr
	br      *bufio.Reader
	bw      *bufio.Writer

	name   string
	kind   string // "file", "tcp", "stdio"
	closed atomic.Bool
}

func (s *IOStream) errClosed() error {
	return errors.New(s.name + ": stream is closed")
}

// reader requires readMu to remain locked until the caller finishes reading.
func (s *IOStream) reader() (*bufio.Reader, error) {
	if s.closed.Load() {
		return nil, s.errClosed()
	}
	if s.br == nil {
		if s.rd == nil {
			return nil, errors.New(s.name + ": stream is not readable")
		}
		s.br = bufio.NewReader(s.rd)
	}
	return s.br, nil
}

func (s *IOStream) writeString(text string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return s.errClosed()
	}
	if s.wr == nil {
		return errors.New(s.name + ": stream is not writable")
	}
	if s.bw == nil {
		if _, ok := s.wr.(*os.File); ok {
			s.bw = bufio.NewWriter(s.wr)
		}
	}
	if s.bw != nil {
		_, err := s.bw.WriteString(text)
		return err
	}
	_, err := io.WriteString(s.wr, text)
	return err
}

func (s *IOStream) flush() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.flushLocked()
}

func (s *IOStream) flushLocked() error {
	if s.bw != nil {
		return s.bw.Flush()
	}
	return nil
}

func (s *IOStream) close() error {
	if s.closed.Swap(true) {
		return nil
	}
	// TCP writes are unbuffered. Close without waiting for either I/O lock
	// so net.Conn.Close can interrupt blocked reads and writes.
	if s.kind == "tcp" && s.closer != nil {
		return s.closer.Close()
	}
	// Files must flush buffered output before closing. Their Close method
	// can safely run alongside a read, so do not acquire readMu here.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var first error
	if err := s.flushLocked(); err != nil {
		first = err
	}
	if s.closer != nil {
		if err := s.closer.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// readLine returns one line without its terminator. ok is false at end of
// stream.
func (s *IOStream) readLine() (line string, ok bool, err error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	br, err := s.reader()
	if err != nil {
		return "", false, err
	}
	text, err := br.ReadString('\n')
	if text == "" {
		if err == io.EOF {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		return "", false, nil
	}
	if err == io.EOF {
		// final line without a trailing newline
		return strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r"), true, nil
	}
	if err != nil {
		return "", false, err
	}
	return strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r"), true, nil
}

func (s *IOStream) readAll() (string, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.closed.Load() {
		return "", s.errClosed()
	}
	var r io.Reader = s.rd
	if r == nil {
		return "", errors.New(s.name + ": stream is not readable")
	}
	if s.br != nil {
		r = s.br
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *IOStream) readN(n int) (string, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	br, err := s.reader()
	if err != nil {
		return "", err
	}
	buf := make([]byte, n)
	got, err := io.ReadFull(br, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	return string(buf[:got]), nil
}

func streamFor(v Value, what string) *IOStream {
	s := AsStream(v)
	if s == nil {
		raise(symTypeError, "%s: expected io-stream, got %s", what, typeName(v))
	}
	return s
}

// --- standard streams -------------------------------------------------------

func (g *Interp) installStdStreams() {
	stdin := &IOStream{rd: os.Stdin, name: "stdin", kind: "stdio"}
	stdout := &IOStream{wr: os.Stdout, name: "stdout", kind: "stdio"}
	stderr := &IOStream{wr: os.Stderr, name: "stderr", kind: "stdio"}

	g.setSymbol(internString("*stdin*"), g.box(boxStream{stdin}))
	g.setSymbol(internString("*stdout*"), g.box(boxStream{stdout}))
	g.setSymbol(internString("*stderr*"), g.box(boxStream{stderr}))
}

// --- file streams -----------------------------------------------------------

func openModeFlags(mode string) (int, error) {
	switch strings.TrimPrefix(mode, ":") {
	case "", "read", "r":
		return os.O_RDONLY, nil
	case "write", "w":
		return os.O_WRONLY | os.O_CREATE | os.O_TRUNC, nil
	case "append", "a":
		return os.O_WRONLY | os.O_CREATE | os.O_APPEND, nil
	case "read-write", "rw":
		return os.O_RDWR | os.O_CREATE, nil
	case "new":
		return os.O_WRONLY | os.O_CREATE | os.O_EXCL, nil
	}
	return 0, errors.New("unknown mode " + mode)
}

func bOpen(g *Interp, args []Value) Value {
	if len(args) < 1 || len(args) > 2 {
		raise(symArgError, "open: expected path and optional mode")
	}
	path := GoString(args[0])
	mode := "read"
	if len(args) == 2 {
		mode = memberName(args[1])
	}
	flags, err := openModeFlags(mode)
	if err != nil {
		raise(symIOError, "open: %s", err)
	}
	f, err := os.OpenFile(path, flags, 0o666)
	if err != nil {
		raise(symIOError, "open: %s", err)
	}
	s := &IOStream{name: path, kind: "file", closer: f}
	if flags&os.O_WRONLY == 0 {
		s.rd = f
	}
	if flags&os.O_WRONLY != 0 || flags&os.O_RDWR != 0 {
		s.wr = f
	}
	return g.box(boxStream{s})
}

func bReadFile(g *Interp, args []Value) Value {
	data, err := os.ReadFile(GoString(args[0]))
	if err != nil {
		raise(symIOError, "read-file: %s", err)
	}
	return g.NewString(string(data))
}

func bWriteFile(g *Interp, args []Value) Value {
	path := GoString(args[0])
	if err := os.WriteFile(path, []byte(textOf(args[1])), 0o666); err != nil {
		raise(symIOError, "write-file: %s", err)
	}
	return fixnum1
}

func bAppendFile(g *Interp, args []Value) Value {
	path := GoString(args[0])
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o666)
	if err != nil {
		raise(symIOError, "append-file: %s", err)
	}
	defer f.Close()
	if _, err := f.WriteString(textOf(args[1])); err != nil {
		raise(symIOError, "append-file: %s", err)
	}
	return fixnum1
}

func bFileExists(g *Interp, args []Value) Value {
	_, err := os.Stat(GoString(args[0]))
	return BoolValue(err == nil)
}

func bDeleteFile(g *Interp, args []Value) Value {
	if err := os.Remove(GoString(args[0])); err != nil {
		raise(symIOError, "delete-file: %s", err)
	}
	return True
}

func textOf(v Value) string {
	if IsString(v) {
		return GoString(v)
	}
	return DisplayString(v)
}

// --- stream builtins --------------------------------------------------------

func bClose(g *Interp, args []Value) Value {
	if l := AsListener(args[0]); l != nil {
		if err := l.close(); err != nil {
			raise(symIOError, "close: %s", err)
		}
		return True
	}
	if err := streamFor(args[0], "close").close(); err != nil {
		raise(symIOError, "close: %s", err)
	}
	return True
}

func bFlush(g *Interp, args []Value) Value {
	if err := streamFor(args[0], "flush").flush(); err != nil {
		raise(symIOError, "flush: %s", err)
	}
	return True
}

func bReadLine(g *Interp, args []Value) Value {
	line, ok, err := streamFor(args[0], "read-line").readLine()
	if err != nil {
		raise(symIOError, "read-line: %s", err)
	}
	if !ok {
		return EOFVal
	}
	return g.NewString(line)
}

func bReadAll(g *Interp, args []Value) Value {
	text, err := streamFor(args[0], "read-all").readAll()
	if err != nil {
		raise(symIOError, "read-all: %s", err)
	}
	return g.NewString(text)
}

func bReadN(g *Interp, args []Value) Value {
	n := int(toInt(args[1], "read-n"))
	text, err := streamFor(args[0], "read-n").readN(n)
	if err != nil {
		raise(symIOError, "read-n: %s", err)
	}
	return g.NewString(text)
}

func bWrite(g *Interp, args []Value) Value {
	if len(args) < 1 {
		raise(symArgError, "write: expected stream")
	}
	s := streamFor(args[0], "write")
	var sb strings.Builder
	for _, a := range args[1:] {
		printValue(&sb, a, false)
	}
	if err := s.writeString(sb.String()); err != nil {
		raise(symIOError, "write: %s", err)
	}
	return Nil
}

func bWriteLine(g *Interp, args []Value) Value {
	if len(args) < 1 {
		raise(symArgError, "write-line: expected stream")
	}
	s := streamFor(args[0], "write-line")
	var sb strings.Builder
	for _, a := range args[1:] {
		printValue(&sb, a, false)
	}
	sb.WriteByte('\n')
	if err := s.writeString(sb.String()); err != nil {
		raise(symIOError, "write-line: %s", err)
	}
	return Nil
}

func bStreamName(g *Interp, args []Value) Value {
	return g.NewString(streamFor(args[0], "stream.name").name)
}
