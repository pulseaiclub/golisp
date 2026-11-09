package interp

// Conservative source-level constant propagation. This is the direct
// analogue of main.lisp's pre-optimize pass, applied after syntax expansion
// and before bytecode generation.
//
// Only immutable immediate values are propagated. Mutable objects, functions,
// and values in an environment after set! are left alone. That keeps the pass
// safe for the dynamic Lisp semantics while still eliminating generated let
// closures, constant branches, and arithmetic on literals.

func (c *compiler) preOptimize(x Value) Value {
	return c.preOpt(x, nil, false)
}

type constBinding struct {
	id    uint32
	value Value
	valid bool
}

func immediateConst(v Value) bool {
	return v == Nil || v == True || v == False || v == EOFVal ||
		IsFixnum(v) || IsDouble(v) || IsChar(v)
}

func (c *compiler) preLookup(id uint32, env []constBinding) (Value, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if env[i].id == id && env[i].valid {
			return env[i].value, true
		}
	}
	return 0, false
}

func (c *compiler) preOpt(x Value, env []constBinding, tail bool) Value {
	g := c.g
	if IsSymbol(x) {
		if v, ok := c.preLookup(SymID(x), env); ok {
			return v
		}
		return x
	}
	if !IsCons(x) {
		return x
	}

	head := carFast(x)
	if IsSymbol(head) {
		switch SymID(head) {
		case symQuote:
			return x
		case symIf:
			test := c.preOpt(cadr(x), env, false)
			then := c.preOpt(caddr(x), env, tail)
			els := Nil
			if IsCons(cdddr(x)) {
				els = c.preOpt(cadddr(x), env, tail)
			}
			if test == True {
				return then
			}
			if test == False {
				return els
			}
			return g.list4(head, test, then, els)
		case symBegin:
			return c.preOptBody(cdrFast(x), env, tail)
		case symSet:
			// Assignment invalidates this binding for the remainder of the
			// current lexical scope.
			id := SymID(cadr(x))
			env = c.invalidateConst(env, id)
			return g.list3(head, cadr(x), c.preOpt(caddr(x), env, false))
		case symLambda:
			return c.preOptLambda(x, env)
		case symLet:
			return c.preOptLet(x, env, tail)
		case symAnd, symOr:
			return c.preOptBool(x, env, tail)
		}
	}

	// Fold calls to the arithmetic/comparison primitives when all operands
	// are immediate constants. We use the existing builtin implementations so
	// overflow and numeric semantics stay identical to the VM. A module that
	// shadows a builtin name disables folding for that name.
	args := c.preOptList(cdrFast(x), env)
	id := SymID(head)
	if IsSymbol(head) && c.foldableBuiltin(id) &&
		(c.env == nil || c.env.get(id) == 0) {
		if vals, ok := constList(args); ok {
			if b := c.g.builtinOf(id); b != nil {
				return b.f(c.g, vals)
			}
		}
	}
	return g.Cons(c.preOpt(head, env, false), args)
}

func (c *compiler) preOptList(x Value, env []constBinding) Value {
	g := c.g
	if x == Nil {
		return Nil
	}
	// A bare dotted tail (e.g. the rest parameter of (lambda args ...) or a
	// define-macro formal like (name . args)) must pass through unchanged;
	// collapsing it to nil would rewrite the source form.
	if !IsCons(x) {
		return c.preOpt(x, env, false)
	}
	var vals []Value
	for p := x; IsCons(p); p = cdrFast(p) {
		vals = append(vals, c.preOpt(carFast(p), env, false))
		if cdrFast(p) != Nil && !IsCons(cdrFast(p)) {
			return buildImproperList(g, vals, c.preOpt(cdrFast(p), env, false))
		}
	}
	return g.reverseListFromSlice(vals)
}

func constList(x Value) ([]Value, bool) {
	var out []Value
	for IsCons(x) {
		v := carFast(x)
		if !immediateConst(v) {
			return nil, false
		}
		out = append(out, v)
		x = cdrFast(x)
	}
	return out, x == Nil
}

func (c *compiler) foldableBuiltin(id uint32) bool {
	name := SymName(SymbolValue(id))
	switch name {
	case "+", "-", "*", "/", "div0", "=", "<", "compare", "mod", "abs", "truncate", "not", "null?", "number?", "fixnum?":
		return true
	default:
		return false
	}
}

func (c *compiler) preOptBody(forms Value, env []constBinding, tail bool) Value {
	g := c.g
	var vals []Value
	for p := forms; IsCons(p); p = cdrFast(p) {
		vals = append(vals, c.preOpt(carFast(p), env, !IsCons(cdrFast(p)) && tail))
	}
	if len(vals) == 0 {
		return Nil
	}
	if len(vals) == 1 {
		return vals[0]
	}
	return g.Cons(SymbolValue(symBegin), g.reverseListFromSlice(vals))
}

func (c *compiler) preOptLambda(x Value, env []constBinding) Value {
	g := c.g
	args := cadr(x)
	body := cddr(x)
	return g.Cons(SymbolValue(symLambda), g.Cons(args, c.preOptList(body, nil)))
}

func (c *compiler) preOptLet(x Value, env []constBinding, tail bool) Value {
	g := c.g
	p := cdrFast(x)
	if IsSymbol(carFast(p)) {
		// Named let is expanded by the normal expansion pass; preserve it
		// here while optimizing its subexpressions.
		return g.Cons(carFast(x), c.preOptList(cdrFast(x), env))
	}
	bindings := carFast(p)
	body := cddr(p)
	newEnv := append([]constBinding(nil), env...)
	var out []Value
	for b := bindings; IsCons(b); b = cdrFast(b) {
		bind := carFast(b)
		id := SymID(carFast(bind))
		val := c.preOpt(cadr(bind), env, false)
		out = append(out, g.list2(SymbolValue(id), val))
		if immediateConst(val) {
			newEnv = append(newEnv, constBinding{id: id, value: val, valid: true})
		} else {
			newEnv = append(newEnv, constBinding{id: id, valid: false})
		}
	}
	bodyOpt := c.preOptBody(body, newEnv, tail)
	// Inline immutable literal bindings. This is the main closure-elimination
	// win for generated let forms; mutable bindings remain explicit.
	allConst := true
	for _, b := range out {
		if !immediateConst(cadr(b)) {
			allConst = false
			break
		}
	}
	if allConst {
		return bodyOpt
	}
	return g.list3(SymbolValue(symLet), g.reverseListFromSlice(out), bodyOpt)
}

func (c *compiler) preOptBool(x Value, env []constBinding, tail bool) Value {
	g := c.g
	forms := c.preOptList(cdrFast(x), env)
	return g.Cons(carFast(x), forms)
}

func (c *compiler) invalidateConst(env []constBinding, id uint32) []constBinding {
	out := append([]constBinding(nil), env...)
	for i := range out {
		if out[i].id == id {
			out[i].valid = false
		}
	}
	return out
}
