package interp

import "context"

// invokeThunk preserves both Go errors returned by CallContext and panics that
// CallContext deliberately rethrows. A boolean distinguishes panic(nil) from a
// normal return.
func invokeThunk(g *Interp, ctx context.Context, thunk Value) (result Value, err error, panicked bool, panicValue any) {
	returned := false
	defer func() {
		if !returned {
			panicked = true
			panicValue = recover()
		}
	}()
	result, err = g.CallContext(ctx, thunk)
	returned = true
	return result, err, false, nil
}

// bUnwindProtect expects a zero-argument protected thunk and a zero-argument
// cleanup thunk. Cleanup runs exactly once after normal return, a Lisp or Go
// error, a panic, or context cancellation from the protected call.
//
// Cleanup runs with context.WithoutCancel(g.Context()). This deliberately has
// no timeout: cleanup must be allowed to finish even when the original
// invocation was canceled. If cleanup fails, its error or panic takes
// precedence over the protected failure. If cleanup succeeds, the original
// protected error or panic is preserved.
func bUnwindProtect(g *Interp, args []Value) Value {
	if len(args) != 2 || AsFunction(args[0]) == nil || AsFunction(args[1]) == nil {
		raise(symArgError, "unwind-protect: expected two zero-argument thunks")
	}

	rootTop := len(g.cellPool)
	bodyRoot := &FrameCell{v: args[0]}
	cleanupRoot := &FrameCell{v: args[1]}
	resultRoot := &FrameCell{}
	g.cellPool = append(g.cellPool, bodyRoot, cleanupRoot, resultRoot)
	defer g.popCells(rootTop)

	result, protectedErr, protectedPanicked, protectedPanic := invokeThunk(g, g.Context(), bodyRoot.v)
	resultRoot.v = result

	// Context values are inherited; cancellation and deadlines are removed.
	// resultRoot keeps the protected result alive if cleanup calls GC.
	_, cleanupErr, cleanupPanicked, cleanupPanic := invokeThunk(
		g, context.WithoutCancel(g.Context()), cleanupRoot.v,
	)
	if cleanupPanicked {
		panic(cleanupPanic)
	}
	if cleanupErr != nil {
		panic(cleanupErr)
	}
	if protectedPanicked {
		panic(protectedPanic)
	}
	if protectedErr != nil {
		panic(protectedErr)
	}
	return resultRoot.v
}
