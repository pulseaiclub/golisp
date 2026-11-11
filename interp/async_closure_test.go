package interp

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAsyncResultCopierRetainsAmbient(t *testing.T) {
	source := NewInterp()
	value := asyncEval(t, source, `(: x (list "source")) (: f (lambda () x)) (list x f f)`)
	compact := &Interp{heap: newHeap()}
	copier := newAsyncResultCopier(compact, context.Background(), source)
	result := copier.value(value)
	defer runtime.KeepAlive(source)
	defer runtime.KeepAlive(compact)
	if copier.ambient == nil || len(compact.globals) != 0 {
		t.Fatal("closure did not retain a private ambient namespace")
	}

	receiver := NewInterp()
	imported := newAsyncCopier(receiver, context.Background()).value(result)
	receiver.Def("result", imported)
	got := asyncEval(t, receiver, `
		(: x (list "receiver"))
		(list (eq? (car result) ((cadr result)))
		      (eq? (cadr result) (car (cdr (cdr result)))))`)
	if WriteStringOf(got) != `(#t #t)` {
		t.Fatal(WriteStringOf(got))
	}
	fn := AsFunction(Car(Cdr(imported)))
	if fn.env == copier.ambient || fn.env.get(internString("f")) != Car(Cdr(imported)) {
		t.Fatal("namespace cycle or heap isolation lost")
	}
	receiver.gc()
	runtime.GC()
	if got := asyncEval(t, receiver, `((cadr result))`); WriteStringOf(got) != `("source")` {
		t.Fatal(WriteStringOf(got))
	}
}

func TestAsyncResultCopierCapturesAmbientLazily(t *testing.T) {
	source := NewInterp()
	value := asyncEval(t, source, `(: unused (lambda () 42)) (list "plain")`)
	dst := &Interp{heap: newHeap()}
	copier := newAsyncResultCopier(dst, context.Background(), source)
	copier.value(value)
	defer runtime.KeepAlive(source)
	if copier.ambient != nil || len(copier.functions) != 0 {
		t.Fatal("plain result retained source globals")
	}
}

func TestAsyncClosurePreservedGlobals(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"spawn snapshot", `(: x 10) (: task (par (lambda () x))) (set! x 20) (: f (wait task)) (f)`, `10`},
		{"worker mutation", `(: x 10) (: task (par (begin (set! x 30) (lambda () x)))) (set! x 20) (: f (wait task)) (list (f) x)`, `(30 20)`},
		{"snapshot function ambient", `(: x 10) (: read-x (lambda () x)) (list (wait (par (begin (set! x 30) (read-x)))) x)`, `(30 10)`},
		{"nested shared mutations", `
			(: x 10)
			(: pair (wait (par (list (lambda () (set! x (+ x 1)))
			                         (lambda () (lambda () x))))))
			(: read-x ((cadr pair)))
			((car pair))
			(list (read-x) x)`, `(11 10)`},
		{"global and capture alias", `
			(: x (list 10))
			(: pair (wait (par (let ((alias x))
			  (list (lambda () (set-car! alias 30)) (lambda () (car x)))))))
			((car pair))
			(list ((cadr pair)) (car x))`, `(30 10)`},
		{"snapshot retained closure", `
			(: x 10)
			(: f (wait (par (lambda () (set! x (+ x 1)) x))))
			(: task (par (list (f) (f))))
			(list (wait task) (f) x)`, `((11 12) 11 10)`},
		{"nested tasks", `(: x 10) (: task (par (wait (par (lambda () x))))) (set! x 20) (: f (wait task)) (f)`, `10`},
		{"compiled bound", `(: task (par (lambda () (bound? 'receiver-only)))) (: receiver-only 20) (: f (wait task)) (f)`, `#f`},
		{"retained bound", `(: x 10) (: f (wait (par (lambda () (bound? 'x))))) (f)`, `#t`},
		{"repeated wait shares mutations", `(: x 10) (: task (par (lambda () (set! x (+ x 1)) x))) (: f (wait task)) (list (f) ((wait task)) x)`, `(11 12 10)`},
		{"dotted globals", `(: dotted.value 10) (: task (par (lambda () dotted.value))) (set! dotted.value 20) (: f (wait task)) (f)`, `10`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := NewInterp()
			got := asyncEval(t, g, tc.src)
			if WriteStringOf(got) != tc.want {
				t.Fatalf("got %s, want %s", WriteStringOf(got), tc.want)
			}
		})
	}
}

func TestAsyncClosureAbsentGlobalDoesNotRebind(t *testing.T) {
	g := NewInterp()
	asyncEval(t, g, `(: task (par (lambda () absent-in-worker))) (: absent-in-worker 42) (: f (wait task))`)
	if _, err := g.EvalString(`(f)`); err == nil || !strings.Contains(err.Error(), "has no value") {
		t.Fatalf("expected retained unbound name, got %v", err)
	}
}

func TestAsyncClosureModuleFallback(t *testing.T) {
	dir := t.TempDir()
	writeModule(t, dir, "closure.lsp", `
		(define private-value "private")
		(define (read-values) (list private-value ambient-value))
		(define (read-absent) absent-in-module-worker)
		(export read-values)
		(export read-absent)`)
	g := NewInterp()
	g.Def("module-path", g.NewString(filepath.Join(dir, "closure.lsp")))
	asyncEval(t, g, `
		(: ambient-value "source")
		(: m (import module-path))
		(: task (par (list m.read-values m.read-absent)))
		(set! ambient-value "receiver")
		(: absent-in-module-worker 42)
		(: pair (wait task))
		(set! task #f)
		(set! m #f)`)
	g.gc()
	runtime.GC()
	if got := asyncEval(t, g, `((car pair))`); WriteStringOf(got) != `("private" "source")` {
		t.Fatal(WriteStringOf(got))
	}
	if _, err := g.EvalString(`((cadr pair))`); err == nil || !strings.Contains(err.Error(), "has no value") {
		t.Fatalf("expected sealed module fallback, got %v", err)
	}
}

func TestAsyncClosureGCAndNamespaceGrowth(t *testing.T) {
	g := NewInterp()
	asyncEval(t, g, `
		(: retained-text (list "kept"))
		(: pair (wait (par (list
		  (lambda () (set! grown.value retained-text))
		  (lambda () ((car pair)) grown.value)))))
		(: original-pair pair)`)
	// The outer reader calls a closure that grows their shared namespace.
	// Put the returned pair into that namespace without rooting its source.
	pair, _ := g.Lookup(internString("pair"))
	env := AsFunction(Car(pair)).env
	if env == nil {
		t.Fatal("result closure has no retained namespace")
	}
	env.setGlobal(internString("pair"), pair)
	// Force growth even when the process-wide symbol was interned earlier.
	grownID := internString("grown.value")
	if int(grownID) < len(env.globals) {
		env.globals = env.globals[:grownID:grownID]
	}
	asyncEval(t, g, `(: retained-text #f)`)
	g.gc()
	runtime.GC()
	if got := asyncEval(t, g, `((cadr original-pair))`); WriteStringOf(got) != `("kept")` {
		t.Fatal(WriteStringOf(got))
	}
	// The value is still owned by the receiver's heap after all task roots die.
	g.gc()
	if got := asyncEval(t, g, `((cadr original-pair))`); WriteStringOf(got) != `("kept")` {
		t.Fatal(WriteStringOf(got))
	}
}

// Only compiled global operations inherit the retained environment. Dynamic
// eval and first-class bound? still use the receiving interpreter's dynamic
// namespace, matching their existing builtin contract.
func TestAsyncClosureDynamicScopeLimit(t *testing.T) {
	g := NewInterp()
	got := asyncEval(t, g, `
		(: x 10)
		(: f (wait (par (lambda () (eval 'x)))))
		(set! x 20)
		(f)`)
	if got != FixnumValue(20) {
		t.Fatalf("dynamic eval = %s", WriteStringOf(got))
	}
	got = asyncEval(t, g, `
		(: f (wait (par (lambda () ((lambda (predicate) (predicate 'dynamic-only)) bound?)))))
		(: dynamic-only 42)
		(f)`)
	if got != True {
		t.Fatalf("first-class bound? = %s", WriteStringOf(got))
	}
}
