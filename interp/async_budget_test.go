package interp

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func copyBudgetScope(t *testing.T, limit int64) (context.Context, *taskScope) {
	t.Helper()
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	return ctx, &taskScope{cancel: cancel, limit: defaultTaskLimit, copyLimit: limit}
}

func copyBudgetPanic(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = r.(error)
		}
	}()
	f()
	return nil
}

func TestAsyncCopyBudgetExactCells(t *testing.T) {
	source := &Interp{heap: newHeap()}
	defer runtime.KeepAlive(source)
	text := source.NewString("123456789") // 4 cells, including padding
	pair := source.Cons(text, Nil)        // 3 cells
	SetCdr(pair, pair)
	root := source.NewVector(3, pair) // 4 cells; all entries alias the cycle
	for _, limit := range []int64{87, 88, 0} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			ctx, scope := copyBudgetScope(t, limit)
			dst := &Interp{heap: newHeap()}
			defer runtime.KeepAlive(dst)
			copier := newAsyncCopier(dst, ctx, scope)
			var result Value
			err := copyBudgetPanic(func() { result = copier.value(root) })
			if limit == 87 {
				if err == nil || !strings.Contains(err.Error(), "copy-bytes") || ctx.Err() == nil {
					t.Fatalf("error=%v context=%v", err, ctx.Err())
				}
				if scope.copyUsed != 56 || dst.heap.bytes != 7 {
					t.Fatalf("allocated beyond budget: used=%d cells=%d", scope.copyUsed, dst.heap.bytes)
				}
			} else {
				if err != nil || scope.copyUsed != 88 || dst.heap.bytes != 11 {
					t.Fatalf("error=%v used=%d cells=%d", err, scope.copyUsed, dst.heap.bytes)
				}
				p := VectorRef(result, 0)
				if p == pair || Cdr(p) != p || VectorRef(result, 2) != p {
					t.Fatal("cycle, alias or isolation lost")
				}
				copier.value(root)
				if scope.copyUsed != 88 {
					t.Fatal("alias charged twice")
				}
			}
			if Cdr(pair) != pair || Car(pair) != text || GoString(text) != "123456789" {
				t.Fatal("copy changed source")
			}
		})
	}
}

func TestAsyncCopyBudgetStopsBeforeLaunch(t *testing.T) {
	g := NewInterp()
	var launched atomic.Bool
	g.Def("launched!", func(g *Interp, args []Value) Value { launched.Store(true); return Nil })
	ctx, scope := copyBudgetScope(t, 1)
	g.taskScope = scope
	_, err := g.EvalStringContext(ctx, `(par (launched!))`)
	scope.wg.Wait()
	if err == nil || !strings.Contains(err.Error(), "copy-bytes") {
		t.Fatalf("error=%v", err)
	}
	if launched.Load() || scope.active != 0 || scope.copyUsed != 0 || scope.failure() == nil {
		t.Fatalf("launched=%v active=%d used=%d error=%v", launched.Load(), scope.active, scope.copyUsed, scope.failure())
	}
	g.taskScope = nil
	if got := asyncEval(t, g, `(+ 20 22)`); Fixnum(got) != 42 {
		t.Fatal(got)
	}
}

func TestTaskScopeCopyBudgetArguments(t *testing.T) {
	g := NewInterp()
	g.Def("budget-scope", bTaskScope)
	for _, budget := range []string{"-1", `"x"`, "1.5"} {
		_, err := g.EvalString(`(budget-scope (lambda () 42) 2 ` + budget + `)`)
		if err == nil || !strings.Contains(err.Error(), "copy-bytes") {
			t.Fatalf("budget=%s error=%v", budget, err)
		}
	}
	for _, args := range []string{"2", "2 0", "2 1"} {
		got := asyncEval(t, g, `(budget-scope (lambda () 42) `+args+`)`)
		if Fixnum(got) != 42 {
			t.Fatal(got)
		}
	}
}

func BenchmarkAsyncSnapshot(b *testing.B) {
	for _, size := range []int{10, 10000} {
		b.Run(fmt.Sprintf("globals-%d", size), func(b *testing.B) {
			g := NewInterp()
			for i := 0; i < size; i++ {
				g.Def(fmt.Sprintf("snapshot-global-%d", i), g.Cons(FixnumValue(int64(i)), Nil))
			}
			thunk, err := g.EvalString(`(lambda () 42)`)
			if err != nil {
				b.Fatal(err)
			}
			defer runtime.KeepAlive(g)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				child, _ := g.snapshotAsync(context.Background(), thunk)
				runtime.KeepAlive(child)
			}
		})
	}
	b.Run("long-list-100000", func(b *testing.B) {
		g := NewInterp()
		list := Nil
		for i := 0; i < 100000; i++ {
			list = g.Cons(FixnumValue(int64(i)), list)
		}
		g.Def("snapshot-long-list", list)
		thunk, err := g.EvalString(`(lambda () 42)`)
		if err != nil {
			b.Fatal(err)
		}
		defer runtime.KeepAlive(g)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			child, _ := g.snapshotAsync(context.Background(), thunk)
			runtime.KeepAlive(child)
		}
	})
}

func TestAsyncCopyBudgetCompletionAndWait(t *testing.T) {
	g := NewInterp()
	thunk := asyncEval(t, g, `(lambda () (list 1 2 3))`)
	_, scope := copyBudgetScope(t, 0)
	g.taskScope = scope
	g.snapshotAsync(context.Background(), thunk)
	snapshotBytes := scope.copyUsed
	// Reset only the measurement scope; the real scope must never refund.
	_, scope = copyBudgetScope(t, snapshotBytes+72)
	g.taskScope = scope
	task := AsAsyncTask(g.newAsyncTask(thunk))
	scope.wg.Wait()
	if task.state.err != nil || scope.copyUsed != snapshotBytes+72 {
		t.Fatalf("error=%v used=%d snapshot=%d", task.state.err, scope.copyUsed, snapshotBytes)
	}
	before := g.heap.bytes
	first := g.waitAsyncTask(task)
	after := g.heap.bytes
	second := g.waitAsyncTask(task)
	if first != second || after-before != 9 || g.heap.bytes != after || scope.copyUsed != snapshotBytes+72 {
		t.Fatal("wait charged budget or repeated import")
	}
	if WriteStringOf(first) != "(1 2 3)" {
		t.Fatal(WriteStringOf(first))
	}
}

func TestAsyncCopyBudgetCompletionFailureCancelsSibling(t *testing.T) {
	g := NewInterp()
	started := make(chan struct{})
	exited := make(chan struct{})
	g.Def("block-budget!", func(g *Interp, args []Value) Value {
		close(started)
		<-g.Context().Done()
		close(exited)
		panic(g.Context().Err())
	})
	g.Def("await-budget!", func(g *Interp, args []Value) Value {
		select {
		case <-started:
		case <-g.Context().Done():
			panic(g.Context().Err())
		}
		return Nil
	})
	g.Def("budget-scope", bTaskScope)
	runner, _ := g.Lookup(internString("budget-scope"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Capture the scope and clamp its remaining allowance inside the result
	// worker, after both snapshots have completed but before compaction.
	var scope *taskScope
	g.Def("clamp-budget!", func(child *Interp, args []Value) Value {
		scope = child.taskScope
		scope.mu.Lock()
		scope.copyLimit = scope.copyUsed + 48
		scope.mu.Unlock()
		return child.Cons(FixnumValue(1), child.Cons(FixnumValue(2), child.Cons(FixnumValue(3), Nil)))
	})
	body := asyncEval(t, g, `(lambda ()
		(par (block-budget!)) (await-budget!) (par (clamp-budget!)) 42)`)
	_, err := g.CallContext(ctx, runner, body, FixnumValue(2), FixnumValue(0))
	if err == nil || !strings.Contains(err.Error(), "copy-bytes") {
		t.Fatalf("error=%v", err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("scope returned before sibling exited")
	}
	if scope == nil || scope.copyUsed != scope.copyLimit || g.taskScope != nil {
		t.Fatal("completion did not exhaust budget or scope was not restored")
	}
}

func TestAsyncCopyBudgetDescendantsAndMonotonicity(t *testing.T) {
	g := NewInterp()
	ctx, scope := copyBudgetScope(t, 0)
	g.taskScope = scope
	var descendants atomic.Int32
	g.Def("inherited-budget!", func(child *Interp, args []Value) Value {
		if child.taskScope != scope {
			panic("descendant lost scope")
		}
		descendants.Add(1)
		return Nil
	})
	for i := 0; i < 2; i++ {
		before := scope.copyUsed
		_, err := g.EvalStringContext(ctx, `(par (begin (inherited-budget!) (par (inherited-budget!))))`)
		if err != nil {
			t.Fatal(err)
		}
		scope.wg.Wait()
		if scope.copyUsed <= before || scope.active != 0 {
			t.Fatalf("budget refunded: before=%d after=%d active=%d", before, scope.copyUsed, scope.active)
		}
	}
	if descendants.Load() != 4 {
		t.Fatal(descendants.Load())
	}
}

func TestAsyncCopyBudgetSharedConcurrentLimit(t *testing.T) {
	source := &Interp{heap: newHeap()}
	defer runtime.KeepAlive(source)
	value := source.Cons(FixnumValue(42), Nil)
	_, scope := copyBudgetScope(t, 24*100)
	var wg sync.WaitGroup
	var allocated atomic.Int64
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			dst := &Interp{heap: newHeap()}
			defer runtime.KeepAlive(dst)
			for j := 0; j < 100; j++ {
				copier := newAsyncCopier(dst, context.Background(), scope)
				if err := copyBudgetPanic(func() { copier.value(value) }); err != nil {
					break
				}
			}
			allocated.Add(int64(dst.heap.bytes) * 8)
		}()
	}
	wg.Wait()
	if scope.copyUsed != 2400 || allocated.Load() != 2400 || scope.failure() == nil {
		t.Fatalf("used=%d allocated=%d failure=%v", scope.copyUsed, allocated.Load(), scope.failure())
	}
}

func TestAsyncCopyLongConsTail(t *testing.T) {
	source := &Interp{heap: newHeap()}
	dst := &Interp{heap: newHeap()}
	defer runtime.KeepAlive(source)
	defer runtime.KeepAlive(dst)
	const length = 300000
	list := FixnumValue(-1) // Include a dotted tail.
	for i := 0; i < length; i++ {
		list = source.Cons(FixnumValue(int64(i)), list)
	}
	ctx, scope := copyBudgetScope(t, length*24)
	copied := newAsyncCopier(dst, ctx, scope).value(list)
	for i := length - 1; i >= 0; i-- {
		if !IsCons(copied) || Fixnum(Car(copied)) != int64(i) {
			t.Fatalf("wrong element at %d", i)
		}
		copied = Cdr(copied)
	}
	if copied != FixnumValue(-1) || scope.copyUsed != length*24 {
		t.Fatalf("tail=%v used=%d", copied, scope.copyUsed)
	}
}

func TestAsyncCopyConsCarReachesTail(t *testing.T) {
	source := &Interp{heap: newHeap()}
	dst := &Interp{heap: newHeap()}
	defer runtime.KeepAlive(source)
	defer runtime.KeepAlive(dst)
	tail := source.Cons(FixnumValue(42), Nil)
	root := source.Cons(tail, tail)
	SetCdr(tail, root)
	copied := newAsyncCopier(dst, context.Background()).value(root)
	if Car(copied) != Cdr(copied) || Cdr(Car(copied)) != copied {
		t.Fatal("car-to-tail alias or cycle lost")
	}
}
