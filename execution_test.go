package golisp

import (
	"context"
	"errors"
	"testing"
)

func TestCallAndCallContext(t *testing.T) {
	g := NewInterp()
	if _, err := g.EvalString(`(define (add2 x) (+ x 2))`); err != nil {
		t.Fatal(err)
	}
	fn, ok := g.symbolValue(internString("add2"))
	if !ok {
		t.Fatal("add2 was not defined")
	}

	got, err := g.Call(fn, FixnumValue(40))
	if err != nil || Fixnum(got) != 42 {
		t.Fatalf("Call returned %v, %v", got, err)
	}

	ctx := context.Background()
	got, err = g.CallContext(ctx, fn, FixnumValue(40))
	if err != nil || Fixnum(got) != 42 {
		t.Fatalf("CallContext returned %v, %v", got, err)
	}
}

func TestInvocationContextVisibleToDefCallback(t *testing.T) {
	g := NewInterp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	callback := g.Def("callback", func(g *Interp, args []Value) Value {
		if g.Context() != ctx {
			raise(symError, "callback lost invocation context")
		}
		if len(args) != 2 {
			raise(symArgError, "callback expects two arguments")
		}
		result, err := g.Call(args[0], args[1])
		if err != nil {
			panic(err)
		}
		return result
	})
	if _, err := g.EvalString(`(define (identity x) x)`); err != nil {
		t.Fatal(err)
	}
	fn, ok := g.symbolValue(internString("identity"))
	if !ok {
		t.Fatal("identity was not defined")
	}

	got, err := g.CallContext(ctx, callback, fn, FixnumValue(7))
	if err != nil || Fixnum(got) != 7 {
		t.Fatalf("callback returned %v, %v", got, err)
	}
}

func TestEvalStringContextCancelsAndRestoresState(t *testing.T) {
	g := NewInterp()
	sp, tryTop, cellTop := g.sp, len(g.tryStack), len(g.cellPool)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := g.EvalStringContext(ctx, `(while #t)`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
	if g.sp != sp || len(g.tryStack) != tryTop || len(g.cellPool) != cellTop {
		t.Fatalf("VM state was not restored: sp=%d try=%d cells=%d", g.sp, len(g.tryStack), len(g.cellPool))
	}

	got, err := g.EvalString(`(+ 20 22)`)
	if err != nil || Fixnum(got) != 42 {
		t.Fatalf("interpreter unusable after cancellation: %v, %v", got, err)
	}
}

func TestCallContextCancelsInfiniteFunction(t *testing.T) {
	g := NewInterp()
	ctx, cancel := context.WithCancel(context.Background())
	g.Def("cancel!", func(g *Interp, args []Value) Value {
		cancel()
		return True
	})
	if _, err := g.EvalString(`(define (loop) (begin (cancel!) (loop)))`); err != nil {
		t.Fatal(err)
	}
	sp, tryTop, cellTop := g.sp, len(g.tryStack), len(g.cellPool)
	fn, ok := g.symbolValue(internString("loop"))
	if !ok {
		t.Fatal("loop was not defined")
	}

	_, err := g.CallContext(ctx, fn)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
	if g.sp != sp || len(g.tryStack) != tryTop || len(g.cellPool) != cellTop {
		t.Fatalf("VM state was not restored: sp=%d try=%d cells=%d", g.sp, len(g.tryStack), len(g.cellPool))
	}
}
