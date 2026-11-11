package interp

import (
	"context"
	"fmt"
	"runtime"
	"sync"
)

// asyncTask is a handle owned by one interpreter. Only that interpreter may
// access result/imported. Snapshots make new local handles sharing state, never
// sharing a cached Lisp value across heaps.
type asyncTask struct {
	owner    *Interp
	state    *taskState
	result   Value
	imported bool
}

// taskState is published before the worker starts. result and err become
// immutable before done is closed; readers must receive from done first. child
// retains the completed heap, including the result's NaN-boxed pointers.
// There is deliberately no reference to the spawning interpreter here.
type taskState struct {
	done   chan struct{}
	ctx    context.Context
	child  *Interp
	once   sync.Once
	result Value
	err    error
	scope  *taskScope
}

func (s *taskState) finish(result Value, err error) {
	s.once.Do(func() {
		s.result, s.err = result, err
		close(s.done)
	})
}

func (s *taskState) run(thunk Value) {
	result, err := Nil, error(nil)
	returned := false
	defer func() {
		if r := recover(); !returned {
			result = Nil
			if e, ok := r.(error); ok {
				err = e
			} else {
				// Also handles panic(nil) with GODEBUG=panicnil=1.
				err = &LispError{kind: symError, msg: fmt.Sprintf("par: panic: %v", r)}
			}
		}
		if err != nil {
			s.child = nil
		}
		if s.scope != nil {
			s.scope.fail(err)
		}
		s.finish(result, err)
		if s.scope != nil {
			s.scope.leave()
		}
	}()
	result, err = s.child.CallContext(s.ctx, thunk)
	if err == nil {
		// Keep only the result graph after completion, not the worker's full
		// environment and stale VM stack. Other waiters read this immutable
		// heap only after done closes.
		worker := s.child
		defer runtime.KeepAlive(worker)
		resultHeap := &Interp{heap: newHeap()}
		result = newAsyncResultCopier(resultHeap, s.ctx, worker).value(result)
		s.child = resultHeap
	}
	returned = true
}

func (g *Interp) newAsyncTask(thunk Value) Value {
	ctx := g.Context()
	scope := g.taskScope
	if scope != nil {
		scope.enter()
	}
	launched := false
	defer func() {
		if scope != nil && !launched {
			scope.leave()
		}
	}()
	child, copiedThunk := g.snapshotAsync(ctx, thunk)
	child.taskScope = scope
	s := &taskState{done: make(chan struct{}), ctx: ctx, child: child, scope: scope}
	t := &asyncTask{owner: g, state: s}
	v := g.box(boxAsyncTask{t: t})
	// Only the isolated child and its thunk are reachable from this worker.
	go s.run(copiedThunk)
	launched = true
	return v
}

func (g *Interp) waitAsyncTask(t *asyncTask) Value {
	if t == nil || t.owner != g {
		raise(symTypeError, "wait: task belongs to another interpreter")
	}
	ctx := g.Context()
	if err := ctx.Err(); err != nil {
		panic(err)
	}
	s := t.state
	select {
	case <-ctx.Done():
		panic(ctx.Err())
	case <-s.done:
	}
	if s.err != nil {
		// Preserve error identity (including context cancellation and host
		// errors), rather than converting every failure into a new LispError.
		panic(s.err)
	}
	if !t.imported {
		// The completed child never executes again. Nested workers use their
		// own heaps, so they cannot mutate the graph being imported here.
		defer runtime.KeepAlive(s)
		c := newAsyncCopier(g, ctx)
		result := c.value(s.result)
		t.result, t.imported = result, true
	}
	return t.result
}

// mark follows only the local imported value. The shared state retains its
// child's Go allocations; tracing that foreign heap would race other waiters.
func (t *asyncTask) mark(g *Interp) {
	if t != nil && t.imported {
		g.mark(t.result)
	}
}

func (t *asyncTask) String() string { return fmt.Sprintf("#<task %p>", t.state) }
