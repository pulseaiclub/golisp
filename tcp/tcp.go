// Package tcp adds the optional TCP builtins to an interpreter. The
// interpreter core ships without networking; install the primitives with
// Register:
//
//	g := interp.NewInterp()
//	tcp.Register(g)
//
// Lisp then gets (tcp-connect host port), (tcp-listen host port),
// (tcp-accept listener), (listener-port listener), and (listener-addr
// listener). Connections are ordinary Lisp streams, so read-line,
// write-line, and close work on them unchanged.
package tcp

import (
	gonet "net"
	"strconv"
	"strings"

	"golisp/interp"
)

var (
	symIOError   = interp.SymID(interp.Symbol("io-error"))
	symTypeError = interp.SymID(interp.Symbol("type-error"))
)

func hostPort(host string, port int64) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]" // bare IPv6 literal
	}
	return gonet.JoinHostPort(host, strconv.FormatInt(port, 10))
}

func bTCPConnect(g *interp.Interp, args []interp.Value) interp.Value {
	host := interp.GoString(args[0])
	port := interp.ToInt(args[1], "tcp-connect")
	conn, err := gonet.Dial("tcp", hostPort(host, port))
	if err != nil {
		interp.Raise(symIOError, "tcp-connect: %s", err)
	}
	return g.NewStream(conn.RemoteAddr().String(), "tcp", conn, conn, conn)
}

func bTCPListen(g *interp.Interp, args []interp.Value) interp.Value {
	host := "127.0.0.1"
	if !interp.IsNil(args[0]) {
		host = interp.GoString(args[0])
	}
	port := interp.ToInt(args[1], "tcp-listen")
	ln, err := gonet.Listen("tcp", hostPort(host, port))
	if err != nil {
		interp.Raise(symIOError, "tcp-listen: %s", err)
	}
	return g.NewListener(ln, hostPort(host, port))
}

func bTCPAccept(g *interp.Interp, args []interp.Value) interp.Value {
	l := interp.AsListener(args[0])
	if l == nil {
		interp.Raise(symTypeError, "tcp-accept: expected listener")
	}
	if l.IsClosed() {
		interp.Raise(symIOError, "tcp-accept: listener is closed")
	}
	conn, err := l.Accept()
	if err != nil {
		interp.Raise(symIOError, "tcp-accept: %s", err)
	}
	return g.NewStream(conn.RemoteAddr().String(), "tcp", conn, conn, conn)
}

func bListenerPort(g *interp.Interp, args []interp.Value) interp.Value {
	l := interp.AsListener(args[0])
	if l == nil {
		interp.Raise(symTypeError, "listener-port: expected listener")
	}
	return interp.FixnumValue(int64(l.Port()))
}

func bListenerAddr(g *interp.Interp, args []interp.Value) interp.Value {
	l := interp.AsListener(args[0])
	if l == nil {
		interp.Raise(symTypeError, "listener-addr: expected listener")
	}
	return g.NewString(l.Addr())
}

// Register installs the TCP builtins on g, replacing any previous bindings.
func Register(g *interp.Interp) {
	g.DefBuiltin("tcp-connect", 2, bTCPConnect)
	g.DefBuiltin("tcp-listen", 2, bTCPListen)
	g.DefBuiltin("tcp-accept", 1, bTCPAccept)
	g.DefBuiltin("listener-port", 1, bListenerPort)
	g.DefBuiltin("listener-addr", 1, bListenerAddr)
}
