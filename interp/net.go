package interp

import (
	"net"
	"strconv"
	"strings"
	"sync/atomic"
)

// Listener wraps a net.Listener as a boxed value so Lisp can accept
// connections without exposing the socket internals.
type Listener struct {
	ln     net.Listener
	name   string
	closed atomic.Bool
}

func (l *Listener) close() error {
	if l.closed.Swap(true) {
		return nil
	}
	return l.ln.Close()
}

func (l *Listener) addr() string { return l.ln.Addr().String() }

// port returns the bound port, which matters when the caller asks for port 0.
func (l *Listener) port() int {
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

func hostPort(host string, port int64) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // bare IPv6 literal
	}
	return net.JoinHostPort(host, strconv.FormatInt(port, 10))
}

func connStream(conn net.Conn, addr string) *IOStream {
	return &IOStream{
		rd:     conn,
		wr:     conn,
		closer: conn,
		name:   addr,
		kind:   "tcp",
	}
}

func bTCPConnect(g *Interp, args []Value) Value {
	host := GoString(args[0])
	port := toInt(args[1], "tcp-connect")
	addr := hostPort(host, port)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		raise(symIOError, "tcp-connect: %s", err)
	}
	return g.box(boxStream{connStream(conn, addr)})
}

func bTCPListen(g *Interp, args []Value) Value {
	host := "127.0.0.1"
	if !IsNil(args[0]) {
		host = GoString(args[0])
	}
	port := toInt(args[1], "tcp-listen")
	addr := hostPort(host, port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		raise(symIOError, "tcp-listen: %s", err)
	}
	return g.box(boxListener{&Listener{ln: ln, name: addr}})
}

func bTCPAccept(g *Interp, args []Value) Value {
	l := AsListener(args[0])
	if l == nil {
		raise(symTypeError, "tcp-accept: expected listener")
	}
	if l.closed.Load() {
		raise(symIOError, "tcp-accept: listener is closed")
	}
	// net.Listener supports concurrent calls; do not lock across Accept,
	// since Close must be able to interrupt it.
	conn, err := l.ln.Accept()
	if err != nil {
		raise(symIOError, "tcp-accept: %s", err)
	}
	return g.box(boxStream{connStream(conn, conn.RemoteAddr().String())})
}

func bListenerPort(g *Interp, args []Value) Value {
	l := AsListener(args[0])
	if l == nil {
		raise(symTypeError, "listener-port: expected listener")
	}
	return FixnumValue(int64(l.port()))
}

func bListenerAddr(g *Interp, args []Value) Value {
	l := AsListener(args[0])
	if l == nil {
		raise(symTypeError, "listener-addr: expected listener")
	}
	return g.NewString(l.addr())
}

// bIOWrite keeps the historical (io.write stream values...) shape but now
// honours the stream argument; without a stream it writes to stdout.
func bIOWrite(g *Interp, args []Value) Value {
	if len(args) >= 1 && AsStream(args[0]) != nil {
		return bWrite(g, args)
	}
	var sb strings.Builder
	for _, a := range args {
		printValue(&sb, a, false)
	}
	if _, err := g.stdout.Write([]byte(sb.String())); err != nil {
		raise(symIOError, "io.write: %s", err)
	}
	return Nil
}
