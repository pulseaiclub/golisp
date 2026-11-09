package tcp

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golisp/interp"
)

// The interpreter core must not carry networking; the builtins appear only
// after Register.
func TestNotInstalledByDefault(t *testing.T) {
	g := interp.NewInterp()
	v, err := g.EvalString(`(bound? 'tcp-listen)`)
	if err != nil {
		t.Fatal(err)
	}
	if v != interp.False {
		t.Fatalf("tcp-listen present before Register: %v", v)
	}
	Register(g)
	if v, err = g.EvalString(`(bound? 'tcp-listen)`); err != nil || v != interp.True {
		t.Fatalf("tcp-listen after Register = %v err=%v", v, err)
	}
}

func TestTCPRoundTrip(t *testing.T) {
	// The server side is written in Lisp so both directions of the API get
	// exercised: listen, accept, read, echo, close.
	dir := t.TempDir()
	portFile := filepath.Join(dir, "port")

	server := interp.NewInterp()
	Register(server)
	srvSrc := `
(define ln (tcp-listen "127.0.0.1" 0))
(write-file "` + portFile + `" (number->string (listener-port ln)))
(define conn (tcp-accept ln))
(write-line conn (string "echo:" (read-line conn)))
(close conn)
(close ln)
`
	done := make(chan error, 1)
	go func() {
		_, err := server.EvalString(srvSrc)
		done <- err
	}()

	// Wait for the server to publish its ephemeral port, surfacing a server
	// error instead of spinning until the deadline.
	var port int
	deadline := time.Now().Add(5 * time.Second)
	for port == 0 {
		select {
		case err := <-done:
			t.Fatalf("server failed before listening: %v", err)
		default:
		}
		if data, err := os.ReadFile(portFile); err == nil && len(data) > 0 {
			port, _ = strconv.Atoi(string(data))
		}
		if port == 0 {
			if time.Now().After(deadline) {
				t.Fatal("server did not report a port")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	client := interp.NewInterp()
	Register(client)
	clientSrc := `
(define c (tcp-connect "127.0.0.1" ` + strconv.Itoa(port) + `))
(write-line c "hello")
(define reply (read-line c))
(close c)
reply
`
	v, err := client.EvalString(clientSrc)
	if err != nil {
		t.Fatal(err)
	}
	if interp.GoString(v) != "echo:hello" {
		t.Fatalf("reply = %q", interp.GoString(v))
	}
	if err := <-done; err != nil {
		t.Fatalf("server: %v", err)
	}
}
