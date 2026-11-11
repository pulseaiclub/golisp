package interp

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const defaultTaskLimit = 1024

// A scope contains no Lisp values or interpreter state. It can be shared by
// workers; enrollment happens before snapshot allocation or goroutine launch.
type taskScope struct {
	cancel context.CancelCauseFunc
	wg     sync.WaitGroup
	mu     sync.Mutex
	active int
	limit  int
	err    error

	// Cumulative managed-heap bytes copied by snapshots and completion graphs.
	// Worker execution, Go allocations, arena slack and wait imports are excluded.
	// Descendants inherit this counter; an explicit nested scope has its own.
	copyLimit int64 // zero means unlimited
	copyUsed  int64
}

func (s *taskScope) enter() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		panic(s.err)
	}
	if s.active >= s.limit {
		raise(symError, "task-scope: active task limit (%d) exceeded", s.limit)
	}
	s.active++
	s.wg.Add(1)
}

func (s *taskScope) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && s.err == nil {
		s.err = err
		s.cancel(err)
	}
}

// chargeCopy reserves bytes before allocating an object's destination shell.
// Failed and discarded copies are not refunded. A caught budget error still
// cancels the scope, just like a failed worker.
func (s *taskScope) chargeCopy(bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		panic(s.err)
	}
	if s.copyLimit > 0 && bytes > s.copyLimit-s.copyUsed {
		err := &LispError{kind: symError, msg: fmt.Sprintf("task-scope: copy-bytes budget (%d) exceeded", s.copyLimit)}
		s.err = err
		s.cancel(err)
		panic(err)
	}
	s.copyUsed += bytes
}

func (s *taskScope) leave() {
	s.mu.Lock()
	s.active--
	s.mu.Unlock()
	s.wg.Done()
}

func (s *taskScope) failure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func bTaskScope(g *Interp, args []Value) (result Value) {
	if (len(args) != 2 && len(args) != 3) || AsFunction(args[0]) == nil {
		raise(symArgError, "task-scope: expected a body, task limit and optional copy-bytes budget")
	}
	if !IsFixnum(args[1]) || Fixnum(args[1]) <= 0 {
		raise(symArgError, "task-scope: limit must be a positive integer")
	}
	var copyLimit int64
	if len(args) == 3 {
		if !IsFixnum(args[2]) || Fixnum(args[2]) < 0 {
			raise(symArgError, "task-scope: copy-bytes must be a nonnegative integer")
		}
		copyLimit = Fixnum(args[2])
	}
	parent := g.Context()
	ctx, cancel := context.WithCancelCause(parent)
	scope := &taskScope{cancel: cancel, limit: int(Fixnum(args[1])), copyLimit: copyLimit}
	previous := g.taskScope
	g.taskScope = scope
	returned := false
	// Always join on panic as well as ordinary error returns. A blocking host
	// callback must cooperate with context cancellation for cleanup to finish.
	defer func() {
		r := recover()
		if !returned {
			cancel(nil)
		}
		scope.wg.Wait()
		cancel(nil)
		g.taskScope = previous
		if r != nil {
			panic(r)
		}
	}()
	result, err := g.CallContext(ctx, args[0])
	if err != nil {
		cancel(err)
	}
	scope.wg.Wait()
	// Cancellation caused by a sibling must not hide the original task error.
	if parent.Err() != nil && (err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		err = parent.Err()
	} else if taskErr := scope.failure(); taskErr != nil &&
		(err == nil || errors.Is(err, context.Canceled)) {
		err = taskErr
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		panic(err)
	}
	returned = true
	return result
}
