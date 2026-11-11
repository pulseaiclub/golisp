package interp

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskScopeJoinsDescendantsAndReturnsBody(t *testing.T) {
	g := NewInterp()
	var completed atomic.Int32
	g.Def("complete!", func(g *Interp, args []Value) Value { completed.Add(1); return Nil })
	v := asyncEval(t, g, `(task-scope (par (par (complete!))) 42)`)
	if Fixnum(v) != 42 || completed.Load() != 1 {
		t.Fatalf("result=%v completed=%d", v, completed.Load())
	}
	v = asyncEval(t, g, `(let ((n 40)) (task-scope (: a (par (+ n 2))) (wait a)))`)
	if Fixnum(v) != 42 {
		t.Fatal(v)
	}
	v = asyncEval(t, g, `(: t (task-scope (par 42))) (wait t)`)
	if Fixnum(v) != 42 {
		t.Fatal(v)
	}
}

func TestTaskScopeFailureCancelsAndJoins(t *testing.T) {
	for _, body := range []string{
		`(task-scope (par (block!)) (par (begin (await-start!) (error "child failed"))) (while #t))`,
		`(task-scope (par (block!)) (await-start!) (error "body failed"))`,
	} {
		t.Run(body, func(t *testing.T) {
			g := NewInterp()
			started := make(chan struct{})
			exited := make(chan struct{})
			g.Def("block!", func(g *Interp, args []Value) Value {
				close(started)
				<-g.Context().Done()
				close(exited)
				panic(g.Context().Err())
			})
			g.Def("await-start!", func(g *Interp, args []Value) Value {
				select {
				case <-started:
				case <-g.Context().Done():
					panic(g.Context().Err())
				}
				return Nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := g.EvalStringContext(ctx, body)
			if err == nil || !strings.Contains(err.Error(), "failed") {
				t.Fatalf("error=%v", err)
			}
			select {
			case <-exited:
			default:
				t.Fatal("scope returned before worker exited")
			}
			if got := asyncEval(t, g, `(+ 20 22)`); Fixnum(got) != 42 {
				t.Fatal(got)
			}
		})
	}
}

func TestTaskScopeUnwaitedFailureAndLimit(t *testing.T) {
	g := NewInterp()
	v := asyncEval(t, g, `(trycatch (lambda () (task-scope (par (error "lost error")) 42)) (lambda (e) 'caught))`)
	if v != Symbol("caught") {
		t.Fatal(v)
	}
	started := make(chan struct{})
	g.Def("block!", func(g *Interp, args []Value) Value { close(started); <-g.Context().Done(); panic(g.Context().Err()) })
	g.Def("await-start!", func(g *Interp, args []Value) Value { <-started; return Nil })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := g.EvalStringContext(ctx, `(task-scope :limit 1 (par (block!)) (await-start!) (par 42))`)
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("error=%v", err)
	}
	for _, src := range []string{`(task-scope :limit)`, `(task-scope :limit 0 42)`, `(task-scope :limit -1 42)`, `(task-scope :limit "x" 42)`, `(task-scope . x)`} {
		if _, err := g.EvalString(src); err == nil {
			t.Fatalf("expected error: %s", src)
		}
	}
}

func TestTaskScopeNestedScopesHaveIndependentBudgets(t *testing.T) {
	g := NewInterp()
	got := asyncEval(t, g, `(task-scope :limit 1
		(par (task-scope :limit 2
			(: a (par 20)) (: b (par 22)) (+ (wait a) (wait b)))))`)
	result, err := g.Call(builtinWaitForTest(g), got)
	if err != nil || Fixnum(result) != 42 {
		t.Fatalf("result=%v err=%v", result, err)
	}
}

func builtinWaitForTest(g *Interp) Value {
	v, _ := g.Lookup(internString("wait"))
	return v
}

func TestTaskScopeFailurePublishedBeforeDone(t *testing.T) {
	g := NewInterp()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	scope := &taskScope{cancel: cancel, limit: 1}
	g.taskScope = scope
	v := asyncEval(t, g, `(par (error "failure"))`)
	task := AsAsyncTask(v)
	<-task.state.done
	if scope.failure() == nil || ctx.Err() == nil {
		t.Fatal("task completion published before group failure")
	}
	scope.wg.Wait()
}

func TestTaskScopeParentCancellation(t *testing.T) {
	g := NewInterp()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.Def("cancel!", func(g *Interp, args []Value) Value { cancel(); return Nil })
	_, err := g.EvalStringContext(ctx, `(task-scope (par (while #t)) (cancel!))`)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if g.taskScope != nil {
		t.Fatal("scope not restored")
	}
}
