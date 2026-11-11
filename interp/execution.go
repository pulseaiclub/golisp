package interp

import "context"

func (g *Interp) withInvocation(ctx context.Context, run func() (Value, error)) (result Value, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	sp := g.sp
	tryTop := len(g.tryStack)
	cellTop := len(g.cellPool)
	previous := g.ctx
	g.ctx = ctx
	defer func() {
		g.sp = sp
		if len(g.tryStack) > tryTop {
			g.tryStack = g.tryStack[:tryTop]
		}
		g.popCells(cellTop)
		g.ctx = previous
		if r := recover(); r != nil {
			switch e := r.(type) {
			case error:
				err = e
				result = Nil
			default:
				panic(r)
			}
		}
	}()
	g.checkContext()
	return run()
}

func (g *Interp) checkContext() {
	if g.ctx == nil {
		return
	}
	if err := g.ctx.Err(); err != nil {
		panic(err)
	}
}

func (g *Interp) Context() context.Context {
	if g.ctx != nil {
		return g.ctx
	}
	return context.Background()
}

// withExecution restores transient VM state before returning from a host call.
// Nested calls inherit the context through withInvocation.
func (g *Interp) withExecution(run func() Value) (result Value, err error) {
	sp := g.sp
	tryTop := len(g.tryStack)
	cellTop := len(g.cellPool)
	defer func() {
		g.sp = sp
		if len(g.tryStack) > tryTop {
			g.tryStack = g.tryStack[:tryTop]
		}
		g.popCells(cellTop)
		if r := recover(); r != nil {
			switch e := r.(type) {
			case error:
				err = e
				result = Nil
			default:
				panic(r)
			}
		}
	}()
	return run(), nil
}

// EvalStringContext reads and evaluates all forms in source using ctx.
func (g *Interp) EvalStringContext(ctx context.Context, source string) (Value, error) {
	var result Value
	var err error
	result, err = g.withInvocation(ctx, func() (Value, error) {
		result, err = g.withExecution(func() Value {
			previousSource := g.source
			defer func() { g.source = previousSource }()
			forms, locations, readErr := g.readLocated(source, "<string>")
			if readErr != nil {
				panic(readErr)
			}
			for _, form := range forms {
				g.push(form)
			}
			result = Nil
			for i, form := range forms {
				g.source = locations[i]
				g.checkContext()
				result, err = g.EvalTop(form)
				if err != nil {
					panic(err)
				}
			}
			return result
		})
		return result, err
	})
	return result, err
}

// CallContext invokes fn with args using ctx.
func (g *Interp) CallContext(ctx context.Context, fn Value, args ...Value) (Value, error) {
	var result Value
	var err error
	result, err = g.withInvocation(ctx, func() (Value, error) {
		g.push(fn)
		for _, arg := range args {
			g.push(arg)
		}
		result, err = g.withExecution(func() Value {
			return g.applyValue(fn, args)
		})
		return result, err
	})
	return result, err
}

// Call invokes fn with args using the active invocation context. A call made
// outside an invocation uses a background context.
func (g *Interp) Call(fn Value, args ...Value) (Value, error) {
	return g.CallContext(g.Context(), fn, args...)
}
