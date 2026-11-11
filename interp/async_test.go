package interp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func asyncEval(t *testing.T, g *Interp, src string) Value {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := g.EvalStringContext(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Both workers must enter the host callback before either can return. The
// parent must continue evaluating while they are blocked. No timing speedup
// assumption is used to prove concurrent execution.
func TestParRunsBeforeWaitAndParentContinues(t *testing.T) {
	g := NewInterp()
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	g.Def("blocked-value", func(child *Interp, args []Value) Value {
		started <- struct{}{}
		select {
		case <-release:
			return FixnumValue(21)
		case <-child.Context().Done():
			panic(child.Context().Err())
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := g.EvalStringContext(ctx, `(: x (par (blocked-value))) (: y x) (: z (par (blocked-value)))`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("workers did not run concurrently")
		}
	}
	if got := asyncEval(t, g, `(+ 1 2)`); Fixnum(got) != 3 {
		t.Fatal(got)
	}
	if got := asyncEval(t, g, `(eq? x y)`); got != True {
		t.Fatal("assignment lost task identity")
	}
	once.Do(func() { close(release) })
	if got := asyncEval(t, g, `(+ (wait y) (wait z))`); Fixnum(got) != 42 {
		t.Fatal(got)
	}
}

func TestParValuesCapturesAndDependencies(t *testing.T) {
	cases := []struct{ src, want string }{
		{`(wait (par (+ 20 22)))`, `42`},
		{`(let ((x 40)) (wait (par (+ x 2))))`, `42`},
		{`(let ((x 40)) ((lambda () (wait (par (+ x 2))))))`, `42`},
		{`(wait (par (wait (par 42))))`, `42`},
		{`(: x (par 40)) (: y (par (+ (wait x) 2))) (wait y)`, `42`},
		{`(: x (par (par 42))) (wait (wait x))`, `42`},
		{`(: x (par (list 1 2))) (eq? (wait x) (wait x))`, `#t`},
		{`(: x (par (list 1 2))) (car (list x)) (wait x)`, `(1 2)`},
		{`(let ((x 1)) (list (wait (par (begin (set! x 2) x))) x))`, `(2 1)`},
		{`(: x (list 1)) (: task (par (begin (set-car! x 2) x))) (list (wait task) x)`, `((2) (1))`},
		{`(: x 1) (: task (par x)) (set! x 2) (wait task)`, `1`},
		{`(wait (par (eval '(+ 20 22))))`, `42`},
	}
	for _, tc := range cases {
		t.Run(tc.src, func(t *testing.T) {
			g := NewInterp()
			v := asyncEval(t, g, tc.src)
			if got := WriteStringOf(v); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestParErrorsAndValidation(t *testing.T) {
	for _, src := range []string{`(par)`, `(par 1 2)`, `(par . 1)`, `(wait)`, `(wait 1 2)`, `(wait 42)`, `(: x)`, `(: 1 2)`, `(: x 1 2)`, `(+ (par 1) 2)`} {
		t.Run(src, func(t *testing.T) {
			g := NewInterp()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := g.EvalStringContext(ctx, src); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	g := NewInterp()
	v := asyncEval(t, g, `(: x (par (error "boom"))) (trycatch (lambda () (wait x)) (lambda (e) 'caught))`)
	if v != Symbol("caught") {
		t.Fatal(v)
	}
	for i := 0; i < 2; i++ {
		if _, err := g.EvalString(`(wait x)`); err == nil || !strings.Contains(err.Error(), "boom") {
			t.Fatalf("error=%v", err)
		}
	}
	g.Def("panic!", func(g *Interp, args []Value) Value { panic("host panic") })
	if _, err := g.EvalString(`(wait (par (panic!)))`); err == nil || !strings.Contains(err.Error(), "host panic") {
		t.Fatalf("panic error=%v", err)
	}
	if got := asyncEval(t, g, `(+ 20 22)`); Fixnum(got) != 42 {
		t.Fatal(got)
	}
}

func TestParCancellation(t *testing.T) {
	g := NewInterp()
	started := make(chan struct{})
	g.Def("started!", func(g *Interp, args []Value) Value { close(started); return Nil })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := g.EvalStringContext(ctx, `(: task (par (begin (started!) (while #t))))`); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("task not started")
	}
	cancel()
	ctxWait, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	if _, err := g.EvalStringContext(ctxWait, `(wait task)`); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if got := asyncEval(t, g, `(+ 20 22)`); Fixnum(got) != 42 {
		t.Fatal(got)
	}
}

func TestParWaitCancellationDoesNotCancelTask(t *testing.T) {
	g := NewInterp()
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	g.Def("blocked", func(g *Interp, args []Value) Value {
		<-release
		return FixnumValue(42)
	})
	if _, err := g.EvalString(`(: task (par (blocked)))`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := g.EvalStringContext(ctx, `(wait task)`); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait error = %v", err)
	}
	once.Do(func() { close(release) })
	if got := asyncEval(t, g, `(wait task)`); Fixnum(got) != 42 {
		t.Fatal(got)
	}
}

func TestParModulesAndReturnedClosures(t *testing.T) {
	g := NewInterp()
	g.Def("force-gc!", func(g *Interp, args []Value) Value { g.gc(); return Nil })
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.lsp")
	if err := os.WriteFile(path, []byte(`
		(define private-value "module value")
		(define (start) (par (lambda () private-value)))
		(export start)`), 0600); err != nil {
		t.Fatal(err)
	}
	g.Def("module-path", g.NewString(path))
	v := asyncEval(t, g, `(: m (import module-path)) (: f (wait (m.start))) (force-gc!) (f)`)
	if GoString(v) != "module value" {
		t.Fatal(v)
	}
	v = asyncEval(t, g, `(: f (wait (par (let ((x "capture")) (lambda () x))))) (force-gc!) (f)`)
	if GoString(v) != "capture" {
		t.Fatal(v)
	}
}

func TestParSharedOutputAndConcurrentWaiters(t *testing.T) {
	g := NewInterp()
	var output bytes.Buffer
	g.stdout = &output
	v := asyncEval(t, g, `
		(: source (par (list "shared")))
		(: a (par (begin (princ "a") (wait source))))
		(: b (par (begin (princ "b") (wait source))))
		(princ "c")
		(list (wait a) (wait b))`)
	if WriteStringOf(v) != `(("shared") ("shared"))` || output.Len() != 3 {
		t.Fatalf("result=%s output=%q", WriteStringOf(v), output.String())
	}
	taskValue, _ := g.Lookup(internString("source"))
	task := AsAsyncTask(taskValue)
	<-task.state.done
	if task.state.child.sp != 0 || len(task.state.child.globals) != 0 {
		t.Fatal("completed task retained worker execution state")
	}
}

func TestParGCAndResultGraph(t *testing.T) {
	g := NewInterp()
	g.Def("force-gc!", func(g *Interp, args []Value) Value { g.gc(); runtime.GC(); return Nil })
	v := asyncEval(t, g, `
 (: task (let ((text "captured")) (par (begin (force-gc!) (list text (vector text))))))
 (force-gc!)
 (: value (wait task))
 (force-gc!)
 (list (car value) (aref (cadr value) 0) (eq? value (wait task)))`)
	if got := WriteStringOf(v); got != `("captured" "captured" #t)` {
		t.Fatal(got)
	}
	v = asyncEval(t, g, `(: cycle (list 1)) (set-cdr! cycle cycle) (: task2 (par cycle)) (: result (wait task2)) (eq? result (cdr result))`)
	if v != True {
		t.Fatal("cycle lost")
	}
	v = asyncEval(t, g, `(: a (list 1)) (: task3 (par (list a a))) (: r (wait task3)) (eq? (car r) (cadr r))`)
	if v != True {
		t.Fatal("alias lost")
	}
}
