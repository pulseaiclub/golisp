package interp

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestUnwindProtectCleansUpFailedBody(t *testing.T) {
	g := NewInterp()
	var cleaned atomic.Int32
	g.Def("cleanup!", func(g *Interp, args []Value) Value {
		cleaned.Add(1)
		return Nil
	})

	_, err := g.EvalString(`(unwind-protect (error "body failed") (cleanup!))`)
	if err == nil || !strings.Contains(err.Error(), "body failed") {
		t.Fatalf("error = %v, want body failure", err)
	}
	if got := cleaned.Load(); got != 1 {
		t.Fatalf("cleanup count = %d, want 1", got)
	}
}

func TestUnwindProtectNestedOrdering(t *testing.T) {
	g := NewInterp()
	var events []string
	g.Def("record!", func(g *Interp, args []Value) Value {
		events = append(events, GoString(args[0]))
		return Nil
	})

	got, err := g.EvalString(`
		(unwind-protect
			(begin (record! "outer-body")
				(unwind-protect
					(begin (record! "inner-body") 42)
					(record! "inner-cleanup")))
			(record! "outer-cleanup"))`)
	if err != nil || Fixnum(got) != 42 {
		t.Fatalf("result = %v, error = %v", got, err)
	}
	want := []string{"outer-body", "inner-body", "inner-cleanup", "outer-cleanup"}
	if len(events) != len(want) {
		t.Fatalf("events = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("events = %v, want %v", events, want)
		}
	}
}

func TestUnwindProtectCancellationRunsCleanupWithoutCancellation(t *testing.T) {
	g := NewInterp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var cleaned atomic.Int32
	g.Def("cancel!", func(g *Interp, args []Value) Value {
		cancel()
		return Nil
	})
	g.Def("cleanup!", func(g *Interp, args []Value) Value {
		if g.Context().Err() != nil {
			raise(symError, "cleanup context is canceled")
		}
		cleaned.Add(1)
		return Nil
	})

	_, err := g.EvalStringContext(ctx, `(unwind-protect (begin (cancel!) (while #t)) (cleanup!))`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if got := cleaned.Load(); got != 1 {
		t.Fatalf("cleanup count = %d, want 1", got)
	}
}

func TestUnwindProtectCleanupErrorTakesPrecedence(t *testing.T) {
	g := NewInterp()
	_, err := g.EvalString(`(unwind-protect (error "body failed") (error "cleanup failed"))`)
	if err == nil || !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("error = %v, want cleanup failure", err)
	}
	if strings.Contains(err.Error(), "body failed") {
		t.Fatalf("cleanup error was not given precedence: %v", err)
	}
}

func TestUnwindProtectRootsResultDuringCleanupGC(t *testing.T) {
	g := NewInterp()
	g.Def("force-gc!", func(g *Interp, args []Value) Value {
		g.gc()
		return Nil
	})

	got, err := g.EvalString(`(unwind-protect (list "protected result") (force-gc!))`)
	if err != nil {
		t.Fatal(err)
	}
	if DisplayString(got) != `(protected result)` {
		t.Fatalf("result = %s, want protected result", DisplayString(got))
	}
}

func TestUnwindProtectPreservesHostPanicAfterCleanup(t *testing.T) {
	g := NewInterp()
	var cleaned atomic.Int32
	g.Def("panic!", func(g *Interp, args []Value) Value { panic("host panic") })
	g.Def("cleanup!", func(g *Interp, args []Value) Value {
		cleaned.Add(1)
		return Nil
	})

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_, _ = g.EvalString(`(unwind-protect (panic!) (cleanup!))`)
	}()
	if recovered != "host panic" {
		t.Fatalf("panic = %v, want host panic", recovered)
	}
	if got := cleaned.Load(); got != 1 {
		t.Fatalf("cleanup count = %d, want 1", got)
	}
}

func TestUnwindProtectClosesResourceOnBodyError(t *testing.T) {
	g := NewInterp()
	var closed atomic.Int32
	stream := &IOStream{name: "test", kind: "test", closer: closeFunc(func() error {
		closed.Add(1)
		return nil
	})}
	g.Def("test-stream", g.box(boxStream{s: stream}))
	g.Def("open-test", func(g *Interp, args []Value) Value { return g.getGlobal(internString("test-stream")) })

	_, err := g.EvalString(`(unwind-protect (error "body failed") (close (open-test)))`)
	if err == nil || !strings.Contains(err.Error(), "body failed") {
		t.Fatalf("error = %v, want body failure", err)
	}
	if got := closed.Load(); got != 1 {
		t.Fatalf("close count = %d, want 1", got)
	}
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }
