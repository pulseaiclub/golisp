package interp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticDefinitionLocationsAndFrames(t *testing.T) {
	g := NewInterp()
	_, err := g.EvalString("\n(define (fail x) (error x))\n(define (caller x) (+ 1 (fail x)))\n(caller \"boom\")")
	if err == nil || err.Error() != "boom" {
		t.Fatalf("err=%v", err)
	}
	text := FormatError(err)
	if !strings.Contains(text, "fail (<string>:2:1)") || !strings.Contains(text, "caller (<string>:3:1)") {
		t.Fatal(text)
	}
	trace := ErrorTrace(err)
	trace[0].Function = "changed"
	if strings.Contains(FormatError(err), "changed") {
		t.Fatal("trace aliases error")
	}
}

func TestDiagnosticFileAndParse(t *testing.T) {
	g := NewInterp()
	path := filepath.Join(t.TempDir(), "broken.lsp")
	if err := os.WriteFile(path, []byte("\n(define (fail x) (error x))\n(fail \"boom\")"), 0600); err != nil {
		t.Fatal(err)
	}
	err := g.LoadFile(path)
	if err == nil || !strings.Contains(FormatError(err), path+":2:1") {
		t.Fatal(FormatError(err))
	}
	_, err = g.EvalString("\n  (list 1")
	if err == nil || !strings.Contains(FormatError(err), "<string>:2:") {
		t.Fatal(FormatError(err))
	}
	if g.source.Line != 0 {
		t.Fatal("source not restored")
	}
}

func TestDiagnosticSharedAsyncErrorImmutable(t *testing.T) {
	g := NewInterp()
	if _, err := g.EvalString(`(: task (par (error "shared")))`); err != nil {
		t.Fatal(err)
	}
	_, a := g.EvalString(`(wait task)`)
	before := FormatError(a)
	_, b := g.EvalString("\n(wait task)")
	if a == nil || b == nil || before != FormatError(a) {
		t.Fatal("shared error mutated")
	}
	if !strings.Contains(FormatError(b), "shared") {
		t.Fatal(b)
	}
}

func TestDynamicNamespaceRestoredAfterError(t *testing.T) {
	g := NewInterp()
	asyncEval(t, g, `(: x 10) (: f (wait (par (lambda () (begin (eval '(set! x 11)) (error "boom")))))) (set! x 20)`)
	if _, err := g.EvalString(`(f)`); err == nil {
		t.Fatal("expected error")
	}
	if g.activeEnv != nil {
		t.Fatal("namespace leaked")
	}
	if got := asyncEval(t, g, `(eval 'x)`); got != FixnumValue(20) {
		t.Fatal(got)
	}
}
