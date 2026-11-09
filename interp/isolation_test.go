package interp

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
)

func TestInterpStateIsIsolated(t *testing.T) {
	first := NewInterp()
	second := NewInterp()

	if _, err := first.EvalString(`(define private-value 41)`); err != nil {
		t.Fatal(err)
	}
	if got, err := first.EvalString(`(+ private-value 1)`); err != nil || Fixnum(got) != 42 {
		t.Fatalf("first binding = %v, err=%v", got, err)
	}
	if _, err := second.EvalString(`private-value`); err == nil {
		t.Fatal("binding leaked to second interpreter")
	}

	if _, err := first.EvalString(`(define-macro (twice x) (list '+ x x))`); err != nil {
		t.Fatal(err)
	}
	if got, err := first.EvalString(`(twice 21)`); err != nil || Fixnum(got) != 42 {
		t.Fatalf("first macro = %v, err=%v", got, err)
	}
	if _, err := second.EvalString(`(twice 21)`); err == nil {
		t.Fatal("macro leaked to second interpreter")
	}

	first.Def("private-go", func() int { return 7 })
	if got, err := first.EvalString(`(private-go)`); err != nil || Fixnum(got) != 7 {
		t.Fatalf("first Go binding = %v, err=%v", got, err)
	}
	if _, err := second.EvalString(`(private-go)`); err == nil {
		t.Fatal("Go binding leaked to second interpreter")
	}

	h, err := first.LoadPlugin(testPlugin{closed: new([]string)})
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := second.EvalString(`demo.answer`); err == nil {
		t.Fatal("plugin binding leaked to second interpreter")
	}
}

func TestInterpConcurrentCreationAndEvaluation(t *testing.T) {
	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			g := NewInterp()
			if _, err := g.EvalString("(define local " + strconv.Itoa(i) + ")"); err != nil {
				errs <- err
				return
			}
			got, err := g.EvalString("(+ local " + strconv.Itoa(i) + ")")
			if err != nil {
				errs <- err
				return
			}
			if Fixnum(got) != int64(2*i) {
				errs <- errors.New("interpreter state crossed goroutines")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestEvalContextAndErrorRestoration(t *testing.T) {
	g := NewInterp()
	if _, err := g.EvalString(`(define stable 9)`); err != nil {
		t.Fatal(err)
	}
	if _, err := g.EvalString(`(error "expected")`); err == nil {
		t.Fatal("expected evaluation error")
	}
	if got, err := g.EvalString(`stable`); err != nil || Fixnum(got) != 9 {
		t.Fatalf("state after error = %v, err=%v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.EvalContext(ctx, Symbol("stable")); err == nil {
		t.Fatal("cancelled context was accepted")
	}
}

func BenchmarkNewInterp(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = NewInterp()
	}
}

func BenchmarkParallelNewInterp(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = NewInterp()
		}
	})
}

func BenchmarkParallelInterpEval(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		g := NewInterp()
		for pb.Next() {
			if _, err := g.EvalString(`(+ 20 22)`); err != nil {
				b.Fatal(err)
			}
		}
	})
}
