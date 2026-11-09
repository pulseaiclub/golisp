package interp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileStreams(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")

	g := NewInterp()
	script := `
(define out (open "` + path + `" :write))
(write-line out "alpha")
(write-line out "beta")
(write out "gamma")
(close out)
`
	if _, err := g.EvalString(script); err != nil {
		t.Fatal(err)
	}
	// "gamma" has no trailing newline, so the file ends mid-line.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "alpha\nbeta\ngamma" {
		t.Fatalf("file = %q", data)
	}

	v, err := g.EvalString(`(read-file "` + path + `")`)
	if err != nil {
		t.Fatal(err)
	}
	if GoString(v) != "alpha\nbeta\ngamma" {
		t.Fatalf("read-file = %q", GoString(v))
	}

	// read-line walks the file and then reports end of stream.
	v, err = g.EvalString(`(define in (open "` + path + `")) (read-line in)`)
	if err != nil || GoString(v) != "alpha" {
		t.Fatalf("first line = %v err=%v", v, err)
	}
	v, err = g.EvalString(`(list (read-line in) (read-line in) (read-line in) (read-line in))`)
	if err != nil {
		t.Fatal(err)
	}
	// The first line was consumed above, so only beta and gamma remain.
	if got := DisplayString(v); got != `(beta gamma #<eof> #<eof>)` {
		t.Fatalf("lines = %s", got)
	}
}

func TestFileHelpers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")

	g := NewInterp()
	if _, err := g.EvalString(`(write-file "` + path + `" "one")`); err != nil {
		t.Fatal(err)
	}
	if _, err := g.EvalString(`(append-file "` + path + `" " two")`); err != nil {
		t.Fatal(err)
	}
	v, err := g.EvalString(`(read-file "` + path + `")`)
	if err != nil || GoString(v) != "one two" {
		t.Fatalf("read-file = %v err=%v", v, err)
	}
	v, err = g.EvalString(`(file-exists? "` + path + `")`)
	if err != nil || v != True {
		t.Fatalf("file-exists? = %v err=%v", v, err)
	}
	if _, err := g.EvalString(`(delete-file "` + path + `")`); err != nil {
		t.Fatal(err)
	}
	v, _ = g.EvalString(`(file-exists? "` + path + `")`)
	if v != False {
		t.Fatalf("file still exists: %v", v)
	}
}

func TestStreamErrorsAreLispConditions(t *testing.T) {
	g := NewInterp()
	// Opening a missing file must raise, not crash.
	if _, err := g.EvalString(`(open "/nonexistent/definitely/missing")`); err == nil {
		t.Fatal("expected open to fail")
	}
	// Reading a write-only stream must raise.
	dir := t.TempDir()
	path := filepath.Join(dir, "w.txt")
	if _, err := g.EvalString(`(define s (open "` + path + `" :write)) (read-line s)`); err == nil {
		t.Fatal("expected read-line on a write-only stream to fail")
	}
	// Writing a read-only stream must raise.
	if _, err := g.EvalString(`(define r (open "` + path + `")) (write r "x")`); err == nil {
		t.Fatal("expected write on a read-only stream to fail")
	}
}
