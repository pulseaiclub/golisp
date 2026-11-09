package interp

import (
	"net"
	"strconv"
)

// Listener wraps a net.Listener as a boxed value so Lisp can accept
// connections without exposing the socket internals.
//
// The type lives in the core because it participates in the boxed-type and
// GC machinery. The tcp-* builtins that create listeners live in the
// optional golisp/tcp package.
type Listener struct {
	ln     net.Listener
	name   string
	closed bool
}

// NewListener boxes a Go listener as a Lisp value. The optional golisp/tcp
// package uses this to hand listening sockets to Lisp. Use AsListener to
// recover the *Listener from the boxed value.
func (g *Interp) NewListener(ln net.Listener, name string) Value {
	return g.box(boxListener{&Listener{ln: ln, name: name}})
}

func (l *Listener) close() error {
	if l.closed {
		return nil
	}
	l.closed = true
	return l.ln.Close()
}

// IsClosed reports whether Close has been called on the listener.
func (l *Listener) IsClosed() bool { return l.closed }

// Accept waits for and returns the next connection.
func (l *Listener) Accept() (net.Conn, error) { return l.ln.Accept() }

// Addr returns the listener's local network address.
func (l *Listener) Addr() string { return l.ln.Addr().String() }

// Port returns the bound port, which matters when the caller asked for
// port 0 and needs the real one.
func (l *Listener) Port() int {
	if tcp, ok := l.ln.Addr().(*net.TCPAddr); ok {
		return tcp.Port
	}
	_, p, err := net.SplitHostPort(l.ln.Addr().String())
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}
