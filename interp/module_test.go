package interp

import (
	"os"
	"path/filepath"
	"testing"
)

// writeModule creates a module file with the given source and returns the
// path relative to dir.
func writeModule(t *testing.T, dir, name, src string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

// runMain writes main.lisp into dir and evaluates it. Relative imports in
// the script resolve against dir, exactly like a real entry file.
func runMain(t *testing.T, dir, mainSrc string) *Interp {
	t.Helper()
	writeModule(t, dir, "main.lisp", mainSrc)
	g := NewInterp()
	if err := g.LoadFile(filepath.Join(dir, "main.lisp")); err != nil {
		t.Fatalf("load: %v", err)
	}
	return g
}

func TestImportBasics(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "math.lisp", `
(define pi 3.5)
(define (add a b) (+ a b))
(define (hidden x) (* x 2))
(define (double x) (add x x))
(export add)
(export double)
(export pi)
`)
	g := runMain(t, dir, `
(define m (import "math.lisp"))
(define out (list (m.add 1 2) (m.double 21) m.pi))
`)
	v, err := g.EvalString(`out`)
	if err != nil {
		t.Fatal(err)
	}
	if got := DisplayString(v); got != "(3 42 3.5)" {
		t.Fatalf("module calls = %s", got)
	}

	// Unexported bindings are not reachable through the namespace.
	if _, err := g.EvalString(`(m.hidden 1)`); err == nil {
		t.Fatal("expected error for unexported binding")
	}
}

func TestImportNamed(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "m.lisp", `
(define (f x) (* x 10))
(define (g x) (+ x 1))
(export f)
(export g)
`)
	g := runMain(t, dir, `
(import* "m.lisp" f g)
(define out (list (f 3) (g 41)))
`)
	v, err := g.EvalString(`out`)
	if err != nil {
		t.Fatal(err)
	}
	if got := DisplayString(v); got != "(30 42)" {
		t.Fatalf("import* = %s", got)
	}
}

func TestImportIsolation(t *testing.T) {
	dir := t.TempDir()
	// Main and both modules define ident/helper; each keeps its own version.
	writeModule(t, dir, "a.lisp", `
(define (ident x) (list 'a x))
(define (helper x) (* x 100))
(define (use-helper x) (helper x))
(export ident)
(export use-helper)
`)
	writeModule(t, dir, "b.lisp", `
(define (ident x) (list 'b x))
(define (helper x) (* x 200))
(define (use-helper x) (helper x))
(export ident)
(export use-helper)
`)
	g := runMain(t, dir, `
(define (ident x) (list 'main x))
(define ia (import "a.lisp"))
(define ib (import "b.lisp"))
(define out (list (ident 1) (ia.ident 1) (ib.ident 1)
                  (ia.use-helper 1) (ib.use-helper 1)))
`)
	v, err := g.EvalString(`out`)
	if err != nil {
		t.Fatal(err)
	}
	if got := DisplayString(v); got != "((main 1) (a 1) (b 1) 100 200)" {
		t.Fatalf("isolation = %s", got)
	}
}

func TestImportCachedOnce(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "c.lisp", `
(define n 0)
(define (bump) (set! n (+ n 1)) n)
(bump)
(export bump)
`)
	g := runMain(t, dir, `
(define m1 (import "c.lisp"))
(define m2 (import "c.lisp"))
`)
	// The module body ran exactly once: the top-level (bump) left n at 1 and
	// both namespaces are the same table.
	v, err := g.EvalString(`(list (m1.bump) (m2.bump) (eq? m1 m2))`)
	if err != nil {
		t.Fatal(err)
	}
	if got := DisplayString(v); got != "(2 3 #t)" {
		t.Fatalf("cache = %s", got)
	}
}

func TestImportRelativePath(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "sub/deep.lisp", `
(define (v) "deep")
(export v)
`)
	writeModule(t, dir, "sub/mid.lisp", `
(define d (import "deep.lisp"))
(define (v) (d.v))
(export v)
`)
	g := runMain(t, dir, `
(define m (import "sub/mid.lisp"))
(define out (m.v))
`)
	v, err := g.EvalString(`out`)
	if err != nil {
		t.Fatal(err)
	}
	if GoString(v) != "deep" {
		t.Fatalf("relative import = %v", v)
	}
}

func TestImportCircular(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "a.lisp", `
(define (name) "a")
(export name)
(define b (import "b.lisp"))
(define (via-b) (b.name))
(export via-b)
`)
	writeModule(t, dir, "b.lisp", `
(define (name) "b")
(define a (import "a.lisp"))
(define (call-a) (a.name))
(export name)
(export call-a)
`)
	g := runMain(t, dir, `
(define bb (import "b.lisp"))
(define aa (import "a.lisp"))
(define out (list (bb.name) (bb.call-a) (aa.name) (aa.via-b)))
`)
	v, err := g.EvalString(`out`)
	if err != nil {
		t.Fatal(err)
	}
	if got := DisplayString(v); got != "(b a a b)" {
		t.Fatalf("circular = %s", got)
	}
}

func TestModuleMacros(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "mm.lisp", `
(define-macro (twice . body) (cons 'begin body))
(define count 0)
(define (bump2) (twice (set! count (+ count 1))))
(define (get) count)
(export bump2)
(export get)
`)
	g := runMain(t, dir, `
(define m (import "mm.lisp"))
(m.bump2)
(m.bump2)
(define out (m.get))
`)
	v, err := g.EvalString(`out`)
	if err != nil {
		t.Fatal(err)
	}
	if Fixnum(v) != 2 {
		t.Fatalf("module macro = %v", v)
	}
	// The module-private macro must not leak into the main namespace.
	if _, err := g.EvalString(`(twice 1)`); err == nil {
		t.Fatal("expected module macro to be private")
	}
}

func TestModuleShadowStdlib(t *testing.T) {
	dir := t.TempDir()
	// The module shadows the ambient `not`; main keeps the builtin one.
	writeModule(t, dir, "s.lisp", `
(define (not x) 'shadowed)
(define (try-not x) (not x))
(export try-not)
`)
	g := runMain(t, dir, `
(define m (import "s.lisp"))
(define out (list (m.try-not #f) (not #f)))
`)
	v, err := g.EvalString(`out`)
	if err != nil {
		t.Fatal(err)
	}
	if got := DisplayString(v); got != "(shadowed #t)" {
		t.Fatalf("shadow = %s", got)
	}
}

func TestImportErrors(t *testing.T) {
	dir := t.TempDir()
	g := NewInterp()
	writeModule(t, dir, "main.lisp", `(define m (import "missing.lisp"))`)
	if err := g.LoadFile(filepath.Join(dir, "main.lisp")); err == nil {
		t.Fatal("expected error for missing module")
	}

	writeModule(t, dir, "bad.lisp", `(define (f x`)
	writeModule(t, dir, "main2.lisp", `(define m (import "bad.lisp"))`)
	g2 := NewInterp()
	if err := g2.LoadFile(filepath.Join(dir, "main2.lisp")); err == nil {
		t.Fatal("expected parse error")
	}

	// export outside a module
	if _, err := g.EvalString(`(export x)`); err == nil {
		t.Fatal("expected export outside module to fail")
	}
	// importing a name the module does not export
	writeModule(t, dir, "small.lisp", `(define (only) 1)(export only)`)
	writeModule(t, dir, "main3.lisp", `(import* "small.lisp" missing-name)`)
	g3 := NewInterp()
	if err := g3.LoadFile(filepath.Join(dir, "main3.lisp")); err == nil {
		t.Fatal("expected missing export error")
	}
}

func TestModuleGCSurvival(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "gc.lisp", `
(define (make-adder n) (lambda (x) (+ x n)))
(define add5 (make-adder 5))
(define data (list 1 2 3))
(export add5)
(export data)
`)
	g := runMain(t, dir, `
(define m (import "gc.lisp"))
`)
	// Force several GC cycles; the module namespace and exports must survive
	// because the registry roots them.
	for i := 0; i < 5; i++ {
		for j := 0; j < 2000; j++ {
			if _, err := g.EvalString(`(list (vector 1 2 3) "garbage")`); err != nil {
				t.Fatal(err)
			}
		}
		g.gc()
	}
	v, err := g.EvalString(`(list (m.add5 37) m.data)`)
	if err != nil {
		t.Fatal(err)
	}
	if got := DisplayString(v); got != "(42 (1 2 3))" {
		t.Fatalf("after gc = %s", got)
	}
}
