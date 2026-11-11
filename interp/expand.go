package interp

// Expansion pass: rewrite surface syntax into a small core language
// (quote, if, begin, lambda, set!, define, and the runtime primitives).
// Capture analysis then runs on fully expanded code, so variables that
// escape through generated lambdas (let, label, dotimes) are seen.

func (c *compiler) expand(x Value) Value {
	g := c.g
	if !IsCons(x) {
		return x
	}
	head := carFast(x)
	if !IsSymbol(head) {
		return c.expandList(x)
	}
	switch SymID(head) {
	case symQuote:
		return x
	case symPar:
		args := cdrFast(x)
		if !IsCons(args) || cdrFast(args) != Nil {
			raise(symArgError, "par: expected one expression")
		}
		// Introduce the lambda before capture analysis, so lexical variables
		// referenced only inside par are captured correctly.
		thunk := c.expand(g.list3(SymbolValue(symLambda), Nil, carFast(args)))
		spawn := g.box(boxBuiltin{&Builtin{name: "par", f: bPar, nargs: 1}})
		return g.list2(spawn, thunk)
	case symUnwindProtect:
		args := cdrFast(x)
		if !IsCons(args) {
			raise(symArgError, "unwind-protect: expected a protected expression")
		}
		cleanup := cdrFast(args)
		for p := cleanup; p != Nil; p = cdrFast(p) {
			if !IsCons(p) {
				raise(symArgError, "unwind-protect: expected a proper cleanup body")
			}
		}
		bodyFn := c.expand(g.list3(SymbolValue(symLambda), Nil, carFast(args)))
		cleanupFn := c.expand(g.cons3(SymbolValue(symLambda), Nil, cleanup))
		protect := g.box(boxBuiltin{&Builtin{name: "unwind-protect", f: bUnwindProtect, nargs: 2}})
		return g.list3(protect, bodyFn, cleanupFn)
	case symTaskScope:
		body := cdrFast(x)
		limit := FixnumValue(defaultTaskLimit)
		copyBytes := FixnumValue(0)
		seenLimit, seenBytes := false, false
		for IsCons(body) && (carFast(body) == Symbol(":limit") || carFast(body) == Symbol(":copy-bytes")) {
			option := carFast(body)
			if !IsCons(cdrFast(body)) {
				raise(symArgError, "task-scope: missing option value")
			}
			if option == Symbol(":limit") {
				if seenLimit {
					raise(symArgError, "task-scope: duplicate :limit")
				}
				limit, seenLimit = cadr(body), true
			} else {
				if seenBytes {
					raise(symArgError, "task-scope: duplicate :copy-bytes")
				}
				copyBytes, seenBytes = cadr(body), true
			}
			body = cddr(body)
		}
		for p := body; p != Nil; p = cdrFast(p) {
			if !IsCons(p) {
				raise(symArgError, "task-scope: expected a proper body")
			}
		}
		thunk := c.expand(g.cons3(SymbolValue(symLambda), Nil, body))
		scope := g.box(boxBuiltin{&Builtin{name: "task-scope", f: bTaskScope, nargs: 3}})
		return g.list4(scope, thunk, c.expand(limit), c.expand(copyBytes))
	case symBind:
		args := cdrFast(x)
		if !IsCons(args) || !IsSymbol(carFast(args)) ||
			!IsCons(cdrFast(args)) || cddr(args) != Nil {
			raise(symArgError, ": expected a symbol and a value")
		}
		return c.expand(g.Cons(SymbolValue(symDefine), args))
	case symLambda:
		// (lambda args body...) — expand body, keep the self-name tail
		args := cadr(x)
		body := cddr(x)
		if body == Nil {
			return g.Cons(head, g.Cons(args, Nil))
		}
		expanded := c.expandListTail(body)
		return g.Cons(head, g.Cons(args, expanded))
	case symIf:
		// (if a b) / (if a b c)
		p := cdrFast(x)
		test := c.expand(carFast(p))
		then := c.expand(cadr(p))
		elsev := Nil
		if IsCons(cddr(p)) {
			elsev = c.expand(caddr(p))
		}
		return g.list4(head, test, then, elsev)
	case symBegin:
		return g.Cons(head, c.expandList(cdrFast(x)))
	case symSet:
		return g.list3(head, cadr(x), c.expand(caddr(x)))
	case symDefine:
		return g.Cons(head, g.Cons(cadr(x), c.expandList(cddr(x))))
	case symAnd, symOr:
		return c.expandBool(head, cdrFast(x))
	case symCond:
		return c.expand(c.condToIfExpand(cdrFast(x)))
	case symLet:
		return c.expandLet(x)
	case symLetStar:
		return c.expandLetStar(x)
	case symLabel:
		return c.expandLabel(x)
	case symWhile:
		return c.expandWhile(x)
	case symDoTimes:
		return c.expandDotimes(x)
	case symProg1:
		return c.expandProg1(x)
	case symQuasiQuote:
		return c.expand(c.expandQQ(cadr(x)))
	case symDefineMac:
		return x // handled by the compiler, which registers the macro
	}
	if c.symIsMacro(SymID(head)) {
		// expand macros with the raw arguments (macros see source syntax)
		return c.expand(c.macroExpand(x))
	}
	return c.expandList(x)
}

// condToIfExpand rewrites a cond clause list into nested ifs.
func (c *compiler) condToIfExpand(clauses Value) Value {
	g := c.g
	if !IsCons(clauses) {
		return Nil
	}
	clause := carFast(clauses)
	test := carFast(clause)
	body := cdrFast(clause)
	rest := cdrFast(clauses)
	if IsSymbol(test) && SymID(test) == internString("else") {
		return g.cons2(SymbolValue(symBegin), body)
	}
	if test == True {
		return g.cons2(SymbolValue(symBegin), body)
	}
	// (cond (test => proc) ...)
	if IsCons(body) && carFast(body) == Symbol("=>") {
		tmp := Symbol("%cond")
		proc := cadr(body)
		return g.list3(SymbolValue(symLet),
			g.Cons(g.list2(tmp, test), Nil),
			g.list4(SymbolValue(symIf), tmp, g.list2(proc, tmp), c.condToIfExpand(rest)))
	}
	if !IsCons(body) {
		// (cond (test)) yields the test value when true
		tmp := Symbol("%cond")
		return g.list3(SymbolValue(symLet),
			g.Cons(g.list2(tmp, test), Nil),
			g.list4(SymbolValue(symIf), tmp, tmp, c.condToIfExpand(rest)))
	}
	return g.list4(SymbolValue(symIf), test,
		g.cons2(SymbolValue(symBegin), body), c.condToIfExpand(rest))
}

// expandList expands every element, preserving an improper tail.
func (c *compiler) expandList(x Value) Value {
	return c.expandListWithTail(x, false)
}

// expandListTail expands a body list but keeps its improper tail (used to
// carry a function's self-name).
func (c *compiler) expandListTail(x Value) Value {
	return c.expandListWithTail(x, true)
}

func (c *compiler) expandListWithTail(x Value, allowImproper bool) Value {
	g := c.g
	if x == Nil {
		return Nil
	}
	var values []Value
	for p := x; IsCons(p); p = cdrFast(p) {
		values = append(values, c.expand(carFast(p)))
		if rest := cdrFast(p); rest != Nil && !IsCons(rest) {
			if !allowImproper {
				return g.reverseListFromSlice(values)
			}
			return buildImproperList(g, values, rest)
		}
	}
	return g.reverseListFromSlice(values)
}

func buildImproperList(g *Interp, values []Value, tail Value) Value {
	if len(values) == 0 {
		return tail
	}
	out := g.Cons(values[len(values)-1], tail)
	for i := len(values) - 2; i >= 0; i-- {
		out = g.Cons(values[i], out)
	}
	return out
}

// (and a b c) => (if a (if b c #f) #f); zero forms => #t
func (c *compiler) expandBool(head Value, forms Value) Value {
	g := c.g
	isAnd := SymID(head) == symAnd
	if !IsCons(forms) {
		if isAnd {
			return True
		}
		return False
	}
	if !IsCons(cdrFast(forms)) {
		return c.expand(carFast(forms))
	}
	first := c.expand(carFast(forms))
	rest := c.expandBool(head, cdrFast(forms))
	// (if first rest <result>) keeps first's value for and/or
	if isAnd {
		tmp := Symbol("%bool")
		return g.list3(SymbolValue(symLet),
			g.Cons(g.list2(tmp, first), Nil),
			g.list4(SymbolValue(symIf), tmp, rest, tmp))
	}
	tmp := Symbol("%bool")
	return g.list3(SymbolValue(symLet),
		g.Cons(g.list2(tmp, first), Nil),
		g.list4(SymbolValue(symIf), tmp, tmp, rest))
}

func (c *compiler) expandLet(x Value) Value {
	g := c.g
	p := cdrFast(x)
	if IsSymbol(carFast(p)) {
		// named let: (let loop ((v e)...) body...)
		bindings := cadr(p)
		body := cddr(p)
		var vars, inits Value = Nil, Nil
		for b := bindings; IsCons(b); b = cdrFast(b) {
			bind := carFast(b)
			vars = g.Cons(carFast(bind), vars)
			inits = g.Cons(c.expand(cadr(bind)), inits)
		}
		vars = g.reverseList(vars)
		inits = g.reverseList(inits)
		expandedBody := c.expandListTail(body)
		lambda := g.Cons(SymbolValue(symLambda), g.Cons(vars, expandedBody))
		call := g.Cons(lambda, inits)
		return c.expand(call)
	}
	bindings := carFast(p)
	body := cdrFast(p)
	var vars, inits []Value
	for b := bindings; IsCons(b); b = cdrFast(b) {
		bind := carFast(b)
		if IsSymbol(bind) {
			vars = append(vars, bind)
			inits = append(inits, Nil)
		} else {
			vars = append(vars, carFast(bind))
			inits = append(inits, cadr(bind))
		}
	}
	varsV := Nil
	for i := len(vars) - 1; i >= 0; i-- {
		varsV = g.Cons(vars[i], varsV)
	}
	// expand inits in the outer scope, body inside the new lambda
	expandedInits := make([]Value, len(inits))
	for i, e := range inits {
		expandedInits[i] = c.expand(e)
	}
	expandedBody := c.expandBody(body)
	lambda := g.Cons(SymbolValue(symLambda), g.Cons(varsV, g.Cons(expandedBody, Nil)))
	return g.Cons(lambda, g.reverseListFromSlice(expandedInits))
}

// expandBody expands a lambda body, wrapping multiple forms in begin.
func (c *compiler) expandBody(body Value) Value {
	g := c.g
	if !IsCons(body) {
		return Nil
	}
	if !IsCons(cdrFast(body)) {
		return c.expand(carFast(body))
	}
	forms := c.expandList(body)
	return g.cons2(SymbolValue(symBegin), forms)
}

func (c *compiler) expandLetStar(x Value) Value {
	g := c.g
	bindings := cadr(x)
	body := cddr(x)
	if !IsCons(bindings) {
		return c.expandBody(body)
	}
	// (let* ((a 1) (b 2)) body) => (let ((a 1)) (let* ((b 2)) body))
	inner := g.Cons(SymbolValue(symLetStar), g.Cons(cdrFast(bindings), body))
	outer := g.list3(SymbolValue(symLet), g.list2(carFast(bindings), Nil), inner)
	return c.expand(outer)
}

func (c *compiler) expandLabel(x Value) Value {
	g := c.g
	name := cadr(x)
	fnForm := caddr(x)
	bindings := g.Cons(g.list2(name, Nil), Nil)
	setForm := g.list3(SymbolValue(symSet), name, c.expand(fnForm))
	return c.expand(g.list4(SymbolValue(symLet), bindings, setForm, name))
}

func (c *compiler) expandWhile(x Value) Value {
	g := c.g
	test := cadr(x)
	body := cddr(x)
	// Rebuild through the core while primitive so the compiler keeps its
	// dedicated bytecode; only the inner forms are expanded.
	inner := c.expandList(body)
	return g.Cons(SymbolValue(symWhile), g.Cons(c.expand(test), inner))
}

func (c *compiler) expandDotimes(x Value) Value {
	g := c.g
	spec := cadr(x)
	var_ := carFast(spec)
	cnt := cadr(spec)
	body := cddr(x)
	nSym := Symbol("%dotimes-n")
	loopVar := g.list2(var_, FixnumValue(0))
	nVar := g.list2(nSym, cnt)
	bindings := g.list2(loopVar, nVar)
	incr := g.list3(SymbolValue(symSet), var_,
		g.list3(Symbol("+"), var_, FixnumValue(1)))
	test := g.list3(Symbol("<"), var_, nSym)
	var whileBody Value = Nil
	var next Value
	for p := body; IsCons(p); p = cdrFast(p) {
		n := g.Cons(carFast(p), Nil)
		if whileBody == Nil {
			whileBody = n
		} else {
			SetCdr(next, n)
		}
		next = n
	}
	tail := g.Cons(incr, Nil)
	if whileBody == Nil {
		whileBody = tail
	} else {
		SetCdr(next, tail)
	}
	whileForm := g.cons3(SymbolValue(symWhile), test, whileBody)
	let := g.list3(SymbolValue(symLet), bindings, whileForm)
	return c.expand(let)
}

func (c *compiler) expandProg1(x Value) Value {
	g := c.g
	first := cadr(x)
	rest := cddr(x)
	tmp := Symbol("%prog1")
	let := g.list3(SymbolValue(symLet), g.Cons(g.list2(tmp, first), Nil),
		g.Cons(SymbolValue(symBegin),
			g.Cons(g.Cons(SymbolValue(symBegin), c.expandList(rest)), g.Cons(tmp, Nil))))
	return c.expand(let)
}

// expandLambdaForCompile expands a whole (lambda ...) form.
func (c *compiler) expandLambdaForCompile(lambda Value) Value {
	return c.expand(lambda)
}
